package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The OpenHands Agent Server API this client speaks.
//
// Pinned to openhands-agent-server v1.54.0 (OpenHands/software-agent-sdk,
// released 2026-10-09; image ghcr.io/openhands/agent-server:1.54.0-python),
// read from the server's own routers rather than from prose docs:
//
//	https://github.com/OpenHands/software-agent-sdk/tree/v1.54.0/openhands-agent-server/openhands/agent_server
//	  conversation_router.py   POST/GET /api/conversations[/{id}], /interrupt, /pause
//	  event_router.py          GET /api/conversations/{id}/events/search
//	  file_router.py           POST /api/file/upload, GET /api/file/download, GET /api/file/archive
//	  bash_router.py           POST /api/bash/execute_bash_command
//	  server_details_router.py GET /server_info
//	  sockets.py               /sockets/events/{id} (first-frame auth)
//	  dependencies.py          X-Session-API-Key
//	https://docs.openhands.dev/sdk (the SDK's RemoteConversation/RemoteWorkspace
//	clients, openhands-sdk/openhands/sdk/conversation/impl/remote_conversation.py
//	and .../workspace/remote/remote_workspace_mixin.py, call these same routes)
//
// What is confirmed against that source, and what is assumed, is listed in
// docs/agents.md ("OpenHands Agent Server"). The assumed parts are each
// confined to one function here, so a server that differs breaks in one
// place: the paging semantics of events/search (searchEventsAfter), the
// layout of the tar.gz the archive route returns (stripArchivePrefix in
// openhands_workspace.go), and the JSON shape of a conversation secret
// (acpSecrets in openhands.go).
const (
	// OpenHandsAPIVersion is the server release this client was written
	// against. GET /server_info reports the server's own; a different one is
	// reported by setup and doctor, not refused -- the routes used here have
	// been stable across the 1.x releases checked.
	OpenHandsAPIVersion = "1.54.0"

	ohSessionHeader = "X-Session-API-Key"

	// ohMaxJSONBytes bounds one JSON response. Conversation info and event
	// pages are small; this refuses a response that is not one.
	ohMaxJSONBytes = 16 << 20
	// ohMaxArchiveBytes bounds the downloaded tar.gz (compressed).
	ohMaxArchiveBytes = 512 << 20
)

// ohHTTPError is a non-2xx answer from the server.
type ohHTTPError struct {
	Op     string
	Status int
	Body   string
}

func (e *ohHTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	if body == "" {
		return fmt.Sprintf("openhands: %s: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("openhands: %s: HTTP %d: %s", e.Op, e.Status, body)
}

// ErrOpenHandsAuth is a 401/403: the session API key is missing or wrong.
// Never retried -- the same key will be refused the same way.
var ErrOpenHandsAuth = errors.New("openhands: the Agent Server refused the session API key")

// ErrOpenHandsNotFound is a 404 on a conversation: it does not exist (any
// more) on that server.
var ErrOpenHandsNotFound = errors.New("openhands: not found on the Agent Server")

// isTransient reports whether err is worth retrying: a network error, a
// timeout, or a 408/429/5xx from the server. A 4xx is an answer, not an
// outage, and is returned at once.
func isTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var he *ohHTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusRequestTimeout || he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	if errors.Is(err, ErrOpenHandsAuth) || errors.Is(err, ErrOpenHandsNotFound) {
		return false
	}
	// Anything else from the transport: connection refused, reset, DNS, a
	// per-request deadline.
	return true
}

// ohRetry is the backoff policy for transient errors. Fields so tests can
// make it instant.
type ohRetry struct {
	attempts int
	base     time.Duration
	max      time.Duration
}

var defaultOHRetry = ohRetry{attempts: 5, base: 500 * time.Millisecond, max: 8 * time.Second}

// ohClient is a minimal Agent Server client: only the routes the executor
// uses. Every call is idempotent on the server side as the executor uses it
// (create carries a client-chosen conversation_id; upload overwrites; the
// unpack command starts with rm -rf), which is what makes retrying any of
// them safe.
type ohClient struct {
	base   *url.URL
	key    string
	http   *http.Client
	retry  ohRetry
	sleep  func(context.Context, time.Duration) error
	header http.Header // extra headers (tests)
}

func newOHClient(baseURL, sessionKey string) (*ohClient, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("openhands: url: %w", err)
	}
	return &ohClient{
		base: u,
		key:  sessionKey,
		// No client-wide timeout: an archive download can legitimately take
		// minutes. Each call bounds itself with its context.
		http:  &http.Client{},
		retry: defaultOHRetry,
		sleep: sleepCtx,
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *ohClient) endpoint(p string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + p
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// request is one HTTP call. body is a byte slice, not a reader, so a retry
// can send it again.
type ohRequest struct {
	op          string
	method      string
	path        string
	query       url.Values
	body        []byte
	contentType string
	timeout     time.Duration
	// notFound maps a 404 to ErrOpenHandsNotFound (conversation routes).
	notFound bool
}

// do performs req with retries for transient failures and returns the
// response body (bounded by limit). The caller's ctx bounds the whole thing,
// retries included.
func (c *ohClient) do(ctx context.Context, req ohRequest, limit int64) ([]byte, error) {
	var lastErr error
	delay := c.retry.base
	attempts := c.retry.attempts
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := c.sleep(ctx, delay); err != nil {
				return nil, lastErr
			}
			delay *= 2
			if delay > c.retry.max {
				delay = c.retry.max
			}
		}
		body, err := c.once(ctx, req, limit)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if ctx.Err() != nil || !isTransient(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w (after %d attempts)", lastErr, attempts)
}

func (c *ohClient) once(ctx context.Context, req ohRequest, limit int64) ([]byte, error) {
	timeout := req.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
	}
	hr, err := http.NewRequestWithContext(rctx, req.method, c.endpoint(req.path, req.query), body)
	if err != nil {
		return nil, err
	}
	if req.contentType != "" {
		hr.Header.Set("Content-Type", req.contentType)
	}
	hr.Header.Set("Accept", "application/json")
	if c.key != "" {
		hr.Header.Set(ohSessionHeader, c.key)
	}
	for k, vs := range c.header {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("openhands: %s: %w", req.op, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("openhands: %s: read response: %w", req.op, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (%s: HTTP %d)", ErrOpenHandsAuth, req.op, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound && req.notFound:
		return nil, fmt.Errorf("%w (%s)", ErrOpenHandsNotFound, req.op)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &ohHTTPError{Op: req.op, Status: resp.StatusCode, Body: string(data)}
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("openhands: %s: response larger than %d bytes", req.op, limit)
	}
	return data, nil
}

func (c *ohClient) doJSON(ctx context.Context, req ohRequest, in, out any) error {
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		req.body = b
		req.contentType = "application/json"
	}
	data, err := c.do(ctx, req, ohMaxJSONBytes)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("openhands: %s: decode response: %w", req.op, err)
	}
	return nil
}

// ---------- server ----------

// ohServerInfo is GET /server_info (server_details_router.py). It needs no
// session key.
type ohServerInfo struct {
	Title        string   `json:"title"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	UsableTools  []string `json:"usable_tools"`
}

func (c *ohClient) serverInfo(ctx context.Context) (ohServerInfo, error) {
	var info ohServerInfo
	err := c.doJSON(ctx, ohRequest{op: "server info", method: http.MethodGet, path: "/server_info", timeout: 15 * time.Second}, nil, &info)
	return info, err
}

// checkAuth makes one cheap authenticated call, so a wrong session key is
// found by setup and doctor rather than by the first run.
// GET /api/conversations/count (conversation_router.py).
func (c *ohClient) checkAuth(ctx context.Context) error {
	return c.doJSON(ctx, ohRequest{op: "check session key", method: http.MethodGet, path: "/api/conversations/count", timeout: 15 * time.Second}, nil, nil)
}

// ---------- conversations ----------

// ohConversation is the part of ConversationInfo (agent_server/models.py)
// the executor reads.
type ohConversation struct {
	ID              string `json:"id"`
	ExecutionStatus string `json:"execution_status"`
}

// Execution statuses (openhands-sdk conversation/state.py,
// ConversationExecutionStatus). finished, error and stuck are terminal;
// idle is where a conversation starts.
const (
	ohIdle                   = "idle"
	ohRunning                = "running"
	ohPaused                 = "paused"
	ohWaitingForConfirmation = "waiting_for_confirmation"
	ohFinished               = "finished"
	ohError                  = "error"
	ohStuck                  = "stuck"
	ohDeleting               = "deleting"
)

func (c *ohClient) getConversation(ctx context.Context, id string) (ohConversation, error) {
	var conv ohConversation
	err := c.doJSON(ctx, ohRequest{op: "get conversation", method: http.MethodGet,
		path: "/api/conversations/" + url.PathEscape(id), notFound: true}, nil, &conv)
	return conv, err
}

// startConversation is POST /api/conversations. With conversation_id set the
// call is idempotent on the server (capability
// idempotent_conversation_create_v1): 201 creates, 200 returns the existing
// conversation. With initial_message.run=true the server starts the agent
// itself.
func (c *ohClient) startConversation(ctx context.Context, body map[string]any) (ohConversation, error) {
	var conv ohConversation
	err := c.doJSON(ctx, ohRequest{op: "start conversation", method: http.MethodPost, path: "/api/conversations"}, body, &conv)
	return conv, err
}

// interrupt cancels the in-flight model call at once; the conversation goes
// to paused. pause waits for the current call to finish. Both answer 400 for
// a conversation they cannot act on, which is not worth retrying.
func (c *ohClient) interrupt(ctx context.Context, id string) error {
	return c.doJSON(ctx, ohRequest{op: "interrupt conversation", method: http.MethodPost,
		path: "/api/conversations/" + url.PathEscape(id) + "/interrupt", notFound: true}, nil, nil)
}

func (c *ohClient) pause(ctx context.Context, id string) error {
	return c.doJSON(ctx, ohRequest{op: "pause conversation", method: http.MethodPost,
		path: "/api/conversations/" + url.PathEscape(id) + "/pause", notFound: true}, nil, nil)
}

// ---------- events ----------

type ohEventPage struct {
	Items      []json.RawMessage `json:"items"`
	NextPageID *string           `json:"next_page_id"`
}

// ohEventHeader is the part of every event (openhands-sdk event/base.py) the
// cursor needs.
type ohEventHeader struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Timestamp string `json:"timestamp"`
}

// searchEventsAfter returns the events after cursor, oldest first, at most
// one page (limit <= 100). next reports whether more remain.
//
// ASSUMED (not confirmed against the server's source): page_id names the
// event a page starts AT, inclusively, so passing the last event already
// seen returns it again first, and it is dropped here. If the server instead
// starts AFTER page_id, nothing is dropped and nothing is lost either way --
// the cursor event is only removed when it is actually present.
func (c *ohClient) searchEventsAfter(ctx context.Context, convID, cursor string, limit int) (events []json.RawMessage, headers []ohEventHeader, next bool, err error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "sort_order": {"TIMESTAMP"}}
	if cursor != "" {
		q.Set("page_id", cursor)
	}
	var page ohEventPage
	if err := c.doJSON(ctx, ohRequest{op: "search events", method: http.MethodGet,
		path: "/api/conversations/" + url.PathEscape(convID) + "/events/search", query: q, notFound: true}, nil, &page); err != nil {
		return nil, nil, false, err
	}
	for _, raw := range page.Items {
		var h ohEventHeader
		if err := json.Unmarshal(raw, &h); err != nil {
			continue
		}
		if cursor != "" && h.ID == cursor {
			// Drop the cursor and anything a server returned before it.
			events, headers = events[:0], headers[:0]
			continue
		}
		events = append(events, raw)
		headers = append(headers, h)
	}
	next = page.NextPageID != nil && *page.NextPageID != "" && *page.NextPageID != cursor
	return events, headers, next, nil
}

// subscribe opens the event websocket, /sockets/events/{id} (sockets.py), and
// calls onFrame for every frame until ctx ends or the socket closes. The
// session key goes in the first frame -- the server's recommended form; the
// query-parameter and header forms are deprecated -- never in the URL, where
// proxies log it.
func (c *ohClient) subscribe(ctx context.Context, convID string, onFrame func([]byte)) error {
	u := *c.base
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/sockets/events/" + url.PathEscape(convID)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPClient: c.http})
	cancel()
	if err != nil {
		return fmt.Errorf("openhands: event socket: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(ohMaxJSONBytes)
	if c.key != "" {
		auth, _ := json.Marshal(map[string]string{"type": "auth", "session_api_key": c.key})
		if err := conn.Write(ctx, websocket.MessageText, auth); err != nil {
			return fmt.Errorf("openhands: event socket auth: %w", err)
		}
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == 4001 {
				return fmt.Errorf("%w (event socket)", ErrOpenHandsAuth)
			}
			if websocket.CloseStatus(err) == 4004 {
				return fmt.Errorf("%w (event socket)", ErrOpenHandsNotFound)
			}
			return err
		}
		onFrame(data)
	}
}

// ---------- files and commands ----------

// upload is POST /api/file/upload?path=<absolute path>, multipart field
// "file" (file_router.py). It overwrites.
func (c *ohClient) upload(ctx context.Context, remotePath string, data []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "upload.tar.gz")
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	_, err = c.do(ctx, ohRequest{op: "upload work root", method: http.MethodPost, path: "/api/file/upload",
		query: url.Values{"path": {remotePath}}, body: buf.Bytes(), contentType: mw.FormDataContentType(),
		timeout: 10 * time.Minute}, ohMaxJSONBytes)
	return err
}

// ohBashOutput is the BashOutput execute_bash_command returns (bash_router.py).
// Only the LAST 1 MiB chunk of output comes back, which is why the executor
// only runs commands whose output is small.
type ohBashOutput struct {
	ExitCode *int   `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func (c *ohClient) execute(ctx context.Context, command, cwd string, timeoutSeconds int) (ohBashOutput, error) {
	var out ohBashOutput
	err := c.doJSON(ctx, ohRequest{op: "run command", method: http.MethodPost, path: "/api/bash/execute_bash_command",
		timeout: time.Duration(timeoutSeconds+30) * time.Second},
		map[string]any{"command": command, "cwd": cwd, "timeout": timeoutSeconds}, &out)
	return out, err
}

// archive is GET /api/file/archive?path=<dir>&format=tar.gz (file_router.py):
// the directory as one tarball. use_default_excludes leaves out dependency
// and cache directories (node_modules, .venv, ...), which the engine's own
// snapshot ignores anyway; a file the server leaves out that WAS uploaded is
// fetched on its own (openhands_workspace.go) rather than read as deleted.
func (c *ohClient) archive(ctx context.Context, dir string) ([]byte, error) {
	return c.do(ctx, ohRequest{op: "download work root", method: http.MethodGet, path: "/api/file/archive",
		query:   url.Values{"path": {dir}, "format": {"tar.gz"}, "use_default_excludes": {"true"}},
		timeout: 10 * time.Minute, notFound: true}, ohMaxArchiveBytes)
}

// download is GET /api/file/download?path=<absolute path>: one file's bytes.
// found is false for a file that does not exist.
func (c *ohClient) download(ctx context.Context, remotePath string, limit int64) (data []byte, found bool, err error) {
	data, err = c.do(ctx, ohRequest{op: "download file", method: http.MethodGet, path: "/api/file/download",
		query: url.Values{"path": {remotePath}}, timeout: 5 * time.Minute, notFound: true}, limit)
	if errors.Is(err, ErrOpenHandsNotFound) {
		return nil, false, nil
	}
	return data, err == nil, err
}
