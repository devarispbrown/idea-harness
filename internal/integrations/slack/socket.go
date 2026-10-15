package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/devarispbrown/idea-harness/internal/integrations"
)

// Envelope is one Socket Mode frame.
type Envelope struct {
	Type                   string          `json:"type"`
	EnvelopeID             string          `json:"envelope_id,omitempty"`
	Payload                json.RawMessage `json:"payload,omitempty"`
	AcceptsResponsePayload bool            `json:"accepts_response_payload,omitempty"`
	// Reason is set on a "disconnect" frame: refresh_requested, warning,
	// link_disabled.
	Reason string `json:"reason,omitempty"`
}

// ack is the reply Socket Mode requires for every enveloped frame, within
// three seconds, or Slack retries it and eventually shows the user an error.
type ack struct {
	EnvelopeID string `json:"envelope_id"`
	Payload    any    `json:"payload,omitempty"`
}

// EnvelopeHandler handles one enveloped frame and returns the ack payload
// (nil for an empty ack). It must return quickly: slow work belongs on a
// goroutine, answered later through the interaction's response_url.
type EnvelopeHandler func(ctx context.Context, env Envelope) any

// SocketState is a connection's health, for `idea integrations status`.
type SocketState struct {
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	Reconnects  int        `json:"reconnects"`
}

// SocketClient holds one Socket Mode connection open, reconnecting on
// disconnects, refresh requests and errors.
//
// The websocket library is github.com/coder/websocket (formerly
// nhooyr.io/websocket): zero dependencies of its own, context-aware reads and
// writes -- which is what lets shutdown and the per-frame deadlines here be a
// context cancel rather than a goroutine juggling SetDeadline -- and safe
// concurrent writes, so an ack from the read loop and an ack from a slower
// handler cannot interleave frames. gorilla/websocket is the common
// alternative; it needs a write mutex everywhere and has no context support.
type SocketClient struct {
	API     *Client
	Handler EnvelopeHandler
	Log     *slog.Logger
	// MinBackoff/MaxBackoff bound reconnect delays (defaults 1s, 2m).
	MinBackoff, MaxBackoff time.Duration

	mu    sync.Mutex
	state SocketState
}

// State returns a snapshot of the connection's health.
func (c *SocketClient) State() SocketState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *SocketClient) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Connected = false
	if err != nil {
		c.state.LastError = err.Error()
	}
}

func (c *SocketClient) log() *slog.Logger {
	if c.Log == nil {
		return slog.Default()
	}
	return c.Log
}

// Run connects and serves until ctx is cancelled.
func (c *SocketClient) Run(ctx context.Context) {
	minB, maxB := c.MinBackoff, c.MaxBackoff
	if minB <= 0 {
		minB = time.Second
	}
	if maxB <= 0 {
		maxB = 2 * time.Minute
	}
	backoff := minB
	for ctx.Err() == nil {
		helloed, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		c.mu.Lock()
		c.state.Reconnects++
		c.mu.Unlock()
		if helloed {
			backoff = minB // a healthy connection that ended resets the backoff
		}
		wait := backoff
		if err != nil {
			c.setErr(err)
			c.log().Warn("slack socket mode: connection ended; reconnecting", "err", err, "in", wait)
			if integrations.IsPermanent(err) {
				// A revoked or wrong app token: hammering apps.connections.open
				// will not fix it. Retry slowly so a corrected .env still
				// recovers without a daemon restart.
				wait = maxB
			}
		} else {
			c.setErr(nil)
			wait = 0 // a requested refresh: reconnect at once
		}
		backoff = min(backoff*2, maxB)
		if wait > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
		}
	}
	c.setErr(nil)
}

// connectOnce opens one connection and reads until it ends. It reports
// whether Slack said hello, and a nil error for a disconnect Slack asked for.
func (c *SocketClient) connectOnce(ctx context.Context) (bool, error) {
	wsURL, err := c.API.OpenConnection(ctx)
	if err != nil {
		return false, err
	}
	if err := checkSocketURL(wsURL); err != nil {
		return false, integrations.Permanent(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := websocket.Dial(dctx, wsURL, nil)
	cancel()
	if err != nil {
		return false, fmt.Errorf("slack socket mode: dial: %w", err)
	}
	defer conn.CloseNow()
	// Interactive payloads embed the whole message that was clicked; the
	// library's 32 KiB default is too small for a message with blocks.
	conn.SetReadLimit(4 << 20)

	helloed := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				conn.Close(websocket.StatusNormalClosure, "shutting down")
				return helloed, nil
			}
			return helloed, fmt.Errorf("slack socket mode: read: %w", err)
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.log().Warn("slack socket mode: undecodable frame", "err", err)
			continue
		}
		switch env.Type {
		case "hello":
			helloed = true
			now := time.Now().UTC()
			c.mu.Lock()
			c.state.Connected, c.state.ConnectedAt, c.state.LastError = true, &now, ""
			c.mu.Unlock()
			continue
		case "disconnect":
			conn.Close(websocket.StatusNormalClosure, "disconnect requested")
			if env.Reason == "link_disabled" {
				return helloed, integrations.Permanent(errors.New("slack socket mode: link_disabled (Socket Mode was turned off for the app)"))
			}
			return helloed, nil
		}
		if env.EnvelopeID == "" {
			continue
		}
		var payload any
		if c.Handler != nil {
			payload = c.safeHandle(ctx, env)
		}
		frame, err := json.Marshal(ack{EnvelopeID: env.EnvelopeID, Payload: payload})
		if err != nil {
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = conn.Write(wctx, websocket.MessageText, frame)
		cancel()
		if err != nil {
			return helloed, fmt.Errorf("slack socket mode: ack: %w", err)
		}
	}
}

func (c *SocketClient) safeHandle(ctx context.Context, env Envelope) (out any) {
	defer func() {
		if r := recover(); r != nil {
			c.log().Error("slack socket mode: handler panicked", "type", env.Type, "panic", r)
			out = nil
		}
	}()
	return c.Handler(ctx, env)
}

// checkSocketURL refuses anything but wss:// -- except ws:// to a loopback
// host, which is what the in-process fake server in the tests serves.
func checkSocketURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("slack socket mode: bad url: %w", err)
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	return fmt.Errorf("slack socket mode: refusing non-TLS websocket url %q", u.Redacted())
}
