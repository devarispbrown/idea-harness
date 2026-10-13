// Package slack is the Slack integration: an outbound notifier (a bus
// Subscriber posting Block Kit messages via chat.postMessage, threaded per
// initiative) and an inbound Socket Mode client (buttons and the /idea slash
// command) that routes every action through the daemon's own HTTP handlers,
// gated by a per-project allowlist of Slack user IDs.
//
// Socket Mode, rather than an HTTP Request URL, because the daemon is
// loopback-only by design (docs/security-model.md): Socket Mode is an
// OUTBOUND websocket, so interactivity works with no public URL, no tunnel
// and no inbound port.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/integrations"
)

// DefaultAPIBase is Slack's Web API root.
const DefaultAPIBase = "https://slack.com/api/"

// Client is a minimal Slack Web API client: the five methods this
// integration uses, nothing else.
type Client struct {
	// APIBase is the Web API root (tests point it at an httptest server).
	APIBase  string
	BotToken string
	AppToken string
	HTTP     *http.Client
}

func (c *Client) base() string {
	if c.APIBase == "" {
		return DefaultAPIBase
	}
	if !strings.HasSuffix(c.APIBase, "/") {
		return c.APIBase + "/"
	}
	return c.APIBase
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// APIError is Slack's `{"ok": false, "error": "..."}`.
type APIError struct {
	Method string
	Code   string
}

func (e *APIError) Error() string { return fmt.Sprintf("slack %s: %s", e.Method, e.Code) }

// permanentCodes are Slack error codes no retry can fix: the token, the
// channel membership, or the request itself is wrong, and only a human
// changing configuration helps. Everything else (internal_error,
// fatal_error, service_unavailable, request_timeout, ...) is retried.
var permanentCodes = map[string]bool{
	"invalid_auth": true, "not_authed": true, "account_inactive": true, "token_revoked": true,
	"token_expired": true, "no_permission": true, "missing_scope": true, "not_allowed_token_type": true,
	"channel_not_found": true, "not_in_channel": true, "is_archived": true, "invalid_blocks": true,
	"invalid_blocks_format": true, "msg_too_long": true, "no_text": true, "invalid_arguments": true,
	"invalid_trigger_id": true, "expired_trigger_id": true, "ekm_access_denied": true,
	"restricted_action": true,
}

// call POSTs a JSON body to a Web API method with token and decodes into out.
// A 429 becomes a RetryAfterError carrying Slack's Retry-After; a permanent
// error code becomes an integrations.PermanentError.
func (c *Client) call(ctx context.Context, method, token string, body any, out any) error {
	if token == "" {
		return integrations.Permanent(fmt.Errorf("slack %s: no token configured", method))
	}
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+method, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("slack %s: read: %w", method, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if after <= 0 {
			after = 30
		}
		return &integrations.RetryAfterError{Err: fmt.Errorf("slack %s: rate limited", method), After: time.Duration(after) * time.Second}
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("slack %s: HTTP %d", method, resp.StatusCode)
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("slack %s: HTTP %d: undecodable response", method, resp.StatusCode)
	}
	if !envelope.OK {
		apiErr := &APIError{Method: method, Code: envelope.Error}
		if envelope.Error == "ratelimited" {
			return &integrations.RetryAfterError{Err: apiErr, After: 30 * time.Second}
		}
		if permanentCodes[envelope.Error] {
			return integrations.Permanent(apiErr)
		}
		return apiErr
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("slack %s: decode: %w", method, err)
		}
	}
	return nil
}

// Message is a chat.postMessage request.
type Message struct {
	Channel        string  `json:"channel"`
	Text           string  `json:"text"`
	Blocks         []Block `json:"blocks,omitempty"`
	ThreadTS       string  `json:"thread_ts,omitempty"`
	ReplyBroadcast bool    `json:"reply_broadcast,omitempty"`
	// UnfurlLinks is off: the only links are to the loopback UI, which
	// Slack's servers cannot reach anyway.
	UnfurlLinks bool `json:"unfurl_links"`
	UnfurlMedia bool `json:"unfurl_media"`
}

// PostMessage sends a message and returns its ts.
func (c *Client) PostMessage(ctx context.Context, m Message) (string, error) {
	var out struct {
		TS      string `json:"ts"`
		Channel string `json:"channel"`
	}
	if err := c.call(ctx, "chat.postMessage", c.BotToken, m, &out); err != nil {
		return "", err
	}
	return out.TS, nil
}

// AuthIdentity is auth.test's answer.
type AuthIdentity struct {
	Team   string `json:"team"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

// AuthTest validates the bot token.
func (c *Client) AuthTest(ctx context.Context) (AuthIdentity, error) {
	var out AuthIdentity
	err := c.call(ctx, "auth.test", c.BotToken, nil, &out)
	return out, err
}

// OpenConnection asks for a Socket Mode websocket URL (app-level token).
func (c *Client) OpenConnection(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.call(ctx, "apps.connections.open", c.AppToken, nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("slack apps.connections.open: no url in response")
	}
	return out.URL, nil
}

// OpenView opens a modal in response to an interaction's trigger_id.
func (c *Client) OpenView(ctx context.Context, triggerID string, view any) error {
	return c.call(ctx, "views.open", c.BotToken, map[string]any{"trigger_id": triggerID, "view": view}, nil)
}
