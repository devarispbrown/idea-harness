// Package cli implements the idea command-line interface as a thin HTTP
// client to the daemon (Decision D1).
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
	"github.com/devarispbrown/idea-harness/internal/daemon"
)

const defaultAddr = "127.0.0.1:8787"

// daemonAddr returns the daemon address, overridable via IDEA_HARNESS_ADDR.
func daemonAddr() string {
	if v := os.Getenv("IDEA_HARNESS_ADDR"); v != "" {
		return v
	}
	return defaultAddr
}

// initiativeResource builds the URL of an initiative-scoped resource:
//
//	/api/v1/projects/{projectID}/initiatives/{initiativeID}/{resource}
//
// One builder rather than a template literal per call site, because the flat
// `?initiative_id=` form this replaces was hand-rolled at every site and drifted
// resource by resource. Both ids are path-escaped: in query position a stray
// slash or `..` was inert, and in path position it is not.
func initiativeResource(projectID, initiativeID, resource string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) +
		"/initiatives/" + url.PathEscape(initiativeID) + "/" + resource
}

// changeSetResource builds a change-set path with its segments escaped.
//
// `idea change 'CHG-1/diff'` used to build .../change-sets/CHG-1/diff -- a real,
// different endpoint -- and decode the patch response into a change set, so the
// user got a blank record printed with exit 0. A silent wrong answer, which is
// worse than the 404 the unescaped initiative paths produced.
func changeSetResource(projectID, changeSetID, action string) string {
	p := "/api/v1/projects/" + url.PathEscape(projectID) + "/change-sets/" + url.PathEscape(changeSetID)
	if action != "" {
		p += "/" + action
	}
	return p
}

// operationResource builds an operation path with its id escaped.
func operationResource(operationID, action string) string {
	p := "/api/v1/operations/" + url.PathEscape(operationID)
	if action != "" {
		p += "/" + action
	}
	return p
}

// client talks to the daemon API.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient() (*client, error) {
	token, err := os.ReadFile(filepath.Join(daemon.DefaultStateRoot(), "token"))
	if err != nil {
		return nil, fmt.Errorf("daemon not initialized; run `idea init` or `idea daemon` first")
	}
	return &client{
		baseURL: "http://" + daemonAddr(),
		token:   string(token),
		http:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// send executes a prepared request and decodes a JSON response into out (if
// non-nil). Shared by do and doMultipart so error handling stays in one place.
func (c *client) send(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var e map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&e)
		msg := e["error"]
		if msg == "" {
			msg = resp.Status
		}
		return &httpError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// do performs a JSON request and decodes the response into out (if non-nil).
func (c *client) do(method, path string, body any, out any) error {
	var buf io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, buf)
	if err != nil {
		return err
	}
	req.Header.Set("X-Idea-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	return c.send(req, out)
}

// doMultipart performs a multipart/form-data request with one file part plus
// optional form fields, decoding the JSON response into out (if non-nil). Used
// for context-doc file uploads (the daemon extracts text from the raw bytes).
func (c *client) doMultipart(method, path string, fields map[string]string, fileField, filename string, fileData []byte, out any) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if v != "" {
			if err := mw.WriteField(k, v); err != nil {
				return err
			}
		}
	}
	fw, err := mw.CreateFormFile(fileField, filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(fileData); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequest(method, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("X-Idea-Token", c.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.send(req, out)
}

// doRaw performs a request and returns the raw response body as a string.
func (c *client) doRaw(method, path string) (string, error) {
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Idea-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Decode the daemon's {"error": …} shape, the same way send() does, so
		// the user sees one error format regardless of which client path ran.
		data, _ := io.ReadAll(resp.Body)
		var e map[string]string
		msg := ""
		if json.Unmarshal(data, &e) == nil {
			msg = e["error"]
		}
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if msg == "" {
			// Same last resort as send(): never return a blank error, which the
			// CLI would print as a bare "idea: ".
			msg = resp.Status
		}
		return "", fmt.Errorf("%s", msg)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// daemonAnswersHealth probes /api/v1/health without a token.
//
// healthy() cannot answer "is a daemon running": it calls newClient(), which
// fails when the token file is absent, so it returns false before any HTTP
// happens. Remove the state directory while the daemon serves -- the natural
// "start fresh" move -- and `idea doctor` reported "daemon: not running" while
// curl got a 200 from that same daemon, and `idea daemon status` said running.
// Three answers to one question, and the designated diagnostic had the wrong
// one.
//
// /api/v1/health is the one unauthenticated route (auth short-circuits it), so
// this is the question asked directly.
func daemonAnswersHealth() bool {
	c := &http.Client{Timeout: healthProbeTimeout}
	resp, err := c.Get("http://" + daemonAddr() + "/api/v1/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// healthProbeTimeout bounds a liveness probe.
//
// healthy() used newClient()'s 60s timeout, which is right for a data call and
// catastrophic for a probe: ensureDaemon calls healthy() once and then 50 more
// times, so against a socket that accepts and never answers -- a non-daemon
// process holding the port -- `idea init` printed NOTHING for a code-bound
// 51 minutes. The same stall wedged `idea doctor`, `idea status` and
// `idea daemon status`, which answered wrongly after a full minute.
const healthProbeTimeout = 2 * time.Second

// healthy reports whether the daemon responds.
func healthy() bool {
	c, err := newClient()
	if err != nil {
		return false
	}
	c.http = &http.Client{Timeout: healthProbeTimeout}
	var out map[string]string
	if err := c.do(http.MethodGet, "/api/v1/health", nil, &out); err != nil {
		return false
	}
	return out["status"] == "ok"
}

// authAccepted reports whether a running daemon accepts this CLI's token.
//
// healthy() cannot answer that: /api/v1/health is the one unauthenticated
// route, so it returns true for a daemon that rejects every other request. If
// the state directory is removed while the daemon runs -- the natural "start
// fresh" move, and the docs say all state lives there -- the daemon keeps
// serving with the token it read at start, the CLI reads the new one, and every
// command answers a bare `idea: unauthorized` while `idea doctor` reports
// "all checks passed".
func authAccepted() bool {
	_, ok := daemonBuild()
	return ok
}

// daemonBuild asks the running daemon what build it is.
//
// Same request authAccepted already made; it threw the body away. /api/v1/version
// rather than /api/v1/health because health reports only the version string, and
// a daemon left running across an upgrade usually answers `dev` on both sides --
// the commit is the only thing that separates them. Using the authenticated
// route also means this is correctly unreachable when the token is wrong, where
// tokenRejected fires first and prescribes the same fix.
//
// The timeout is deliberately not newClient's 60 seconds. This runs from
// `idea version`, which has never touched the network and must not hang on a
// wedged daemon.
func daemonBuild() (buildinfo.Info, bool) {
	c, err := newClient()
	if err != nil {
		return buildinfo.Info{}, false
	}
	c.http = &http.Client{Timeout: 2 * time.Second}
	var out buildinfo.Info
	if err := c.do(http.MethodGet, "/api/v1/version", nil, &out); err != nil {
		return buildinfo.Info{}, false
	}
	return out, true
}

// daemonExecutable is the seam the tests need.
//
// ensureDaemon spawns os.Executable(), which under `go test` is the test binary
// -- it binds nothing, writes no bind error, exits 0, and re-runs the package's
// tests, forking again. So the failure this function exists to report could not
// be tested at all until the lookup was indirectable.
var daemonExecutable = os.Executable

// ensureDaemon starts a detached daemon if none is reachable.
//
// The daemon can fail to start for reasons the poll loop cannot distinguish from
// "not ready yet" -- a held port being the common one. Waiting on the child
// separates them: a daemon that exits is never going to answer, so the loop
// stops immediately instead of polling a corpse 50 times.
func ensureDaemon() error {
	if healthy() {
		return nil
	}
	exe, err := daemonExecutable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "daemon")
	cmd.SysProcAttr = sysProcAttrDetach
	// Route the detached daemon's output to the log file so its logs are
	// inspectable with `idea logs`.
	if f, err := os.OpenFile(filepath.Join(daemon.DefaultStateRoot(), "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
		defer f.Close()
	}
	// Where this run's output begins. daemon.log is opened O_APPEND and never
	// rotated, so without an offset the "last line" is whatever failed last --
	// possibly weeks ago, possibly a routine INFO line from a successful start.
	// Quoting that as the cause of this failure is worse than saying nothing.
	logStart := logSize()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Setsid makes the child a session leader, not someone else's child, so
	// Wait still reaps it here.
	//
	// The channel is buffered because on the success path this goroutine
	// outlives the call: the child is a daemon designed never to exit, so Wait
	// blocks until the CLI process ends. Buffering keeps the send from blocking
	// if that ever changes; it does not make the goroutine finish.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for i := 0; i < 50; i++ {
		// Checked before probing: a dead child cannot answer, and this saves a
		// probe timeout on the common failure.
		select {
		case werr := <-exited:
			return daemonStartFailed(werr, true, logStart)
		default:
		}
		if healthy() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return daemonStartFailed(nil, false, logStart)
}

// daemonStartFailed reports why the daemon did not come up, quoting the log.
//
// The reason was always in daemon.log and the message only named the command
// that would show it -- so the one diagnostic that answered was the one nobody
// was told to run, while doctor and status hung on the same stalled probe.
func daemonStartFailed(werr error, exited bool, logStart int64) error {
	what := "daemon did not become healthy"
	if exited {
		// cmd.Wait returns nil for exit status 0, so werr alone cannot
		// distinguish "exited cleanly" from "loop ran out".
		what = "the daemon exited immediately"
		if werr != nil {
			what = fmt.Sprintf("the daemon exited immediately (%v)", werr)
		}
	}
	if detail := lastLogLine(logStart); detail != "" {
		return fmt.Errorf("%s: %s; run `idea logs` for the full output", what, detail)
	}
	return fmt.Errorf("%s; run `idea logs` for details", what)
}

// logSize is daemon.log's length now, or 0 when it does not exist yet.
func logSize() int64 {
	fi, err := os.Stat(filepath.Join(daemon.DefaultStateRoot(), "daemon.log"))
	if err != nil {
		return 0
	}
	return fi.Size()
}

// lastLogLine returns the final non-blank line daemon.log gained after `from`,
// which is where this run's bind failure lands. Reading the whole file instead
// would quote a previous run's failure -- or a previous run's success -- as the
// reason this one did not start.
func lastLogLine(from int64) string {
	f, err := os.Open(filepath.Join(daemon.DefaultStateRoot(), "daemon.log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// projectIDForCwd returns the registered project id whose root_path matches cwd.
func (c *client) projectIDForCwd() (string, error) {
	var projects []struct {
		ProjectID string `json:"project_id"`
		RootPath  string `json:"root_path"`
	}
	if err := c.do(http.MethodGet, "/api/v1/projects", nil, &projects); err != nil {
		return "", err
	}
	cwd, _ := os.Getwd()
	for _, p := range projects {
		if samePath(p.RootPath, cwd) {
			return p.ProjectID, nil
		}
	}
	return "", fmt.Errorf("no project registered for %s; run `idea init` first", cwd)
}

func samePath(a, b string) bool {
	aa, errA := filepath.EvalSymlinks(a)
	bb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return aa == bb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// httpError carries the status alongside the daemon's message, so a caller can
// tell a mistake that will never succeed (404) from one that might (503 during
// a restart). watch needs that distinction to know when to stop polling.
type httpError struct {
	Status  int
	Message string
}

func (e *httpError) Error() string { return e.Message }

// isNotFound reports whether err is the daemon answering 404.
func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}
