package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Real key shapes (the same formats redact_keyformats_test.go pins), built at
// runtime so this file does not itself trip a repository secret scanner.
var (
	logAnthropicKey = "sk-ant-api03-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"
	logAWSKeyID     = "AKIA" + "Q3EGRIXJ7PZ4WN2T"
	logGitHubPAT    = "ghp_" + "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	logStripeKey    = "sk_live_" + "51Hq7zR2mVx9LpQwErTy8uIo"
	logDSN          = "postgres://admin:" + "hunter2345xyz" + "@db.internal:5432/app"
)

func newRedactingTextLogger(buf *bytes.Buffer, redact func(string) string) *slog.Logger {
	return slog.New(NewRedactingHandler(slog.NewTextHandler(buf, nil), redact))
}

func assertNoneLeak(t *testing.T, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("secret %q reached the log:\n%s", s, out)
		}
	}
	if !strings.Contains(out, redacted) {
		t.Errorf("expected %s in the log, got:\n%s", redacted, out)
	}
}

// TestRedactingHandlerMessageAndAttrs covers every place a credential reaches
// a record: the message, a string attr, an error attr (the common case -- a
// wrapped provider error carrying a response body), a Stringer, an arbitrary
// value, and a []byte.
func TestRedactingHandlerMessageAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newRedactingTextLogger(&buf, nil)

	log.Warn("provider call failed with key "+logAnthropicKey,
		"body", "AWS key id "+logAWSKeyID+" in the response",
		"err", fmt.Errorf("upstream: %w", errors.New("401 for token "+logGitHubPAT)),
		"stringer", stringerFunc(func() string { return "dsn=" + logDSN }),
		"any", struct{ Key string }{Key: logStripeKey},
		"bytes", []byte("raw output "+logStripeKey),
	)
	assertNoneLeak(t, buf.String(), logAnthropicKey, logAWSKeyID, logGitHubPAT, logStripeKey, "hunter2345xyz")
}

// TestRedactingHandlerNestedGroupsAndWith covers attrs bound with With (the
// engine's per-project logger shape) and nested groups at any depth.
func TestRedactingHandlerNestedGroupsAndWith(t *testing.T) {
	var buf bytes.Buffer
	log := newRedactingTextLogger(&buf, nil).
		With("bound", "key "+logAnthropicKey).
		WithGroup("req")
	log.Info("call",
		slog.Group("outer",
			slog.String("plain", "ok"),
			slog.Group("inner", slog.Any("err", errors.New("token "+logGitHubPAT))),
		),
	)
	out := buf.String()
	assertNoneLeak(t, out, logAnthropicKey, logGitHubPAT)
	// Structure survives: the group path and the clean value are intact.
	if !strings.Contains(out, "req.outer.plain=ok") {
		t.Errorf("group structure lost:\n%s", out)
	}
}

// TestRedactingHandlerKeyedAssignment: the generic credential-assignment rule
// keys on the variable name, which in a log record is the attribute KEY, not
// part of the value. Redacting the value alone would let this through.
func TestRedactingHandlerKeyedAssignment(t *testing.T) {
	var buf bytes.Buffer
	log := newRedactingTextLogger(&buf, nil)
	log.Info("config", "db_password", "Zq8vLm2pXr7tKw4n", "api_key", "Hq7zR2mVx9LpQwEr3")
	assertNoneLeak(t, buf.String(), "Zq8vLm2pXr7tKw4n", "Hq7zR2mVx9LpQwEr3")
}

// TestRedactingHandlerKnownValues: a configured secret with no recognizable
// shape is masked when the redact func knows it (the daemon passes its
// Redactor's RedactString, which carries every project's resolved secrets).
func TestRedactingHandlerKnownValues(t *testing.T) {
	var buf bytes.Buffer
	r := NewRedactor("plain-looking-but-configured")
	log := newRedactingTextLogger(&buf, r.RedactString)
	log.Info("echo plain-looking-but-configured", "v", "x plain-looking-but-configured y")
	assertNoneLeak(t, buf.String(), "plain-looking-but-configured")
}

// TestRedactingHandlerLeavesCleanValuesNative: a value with nothing to redact
// keeps its kind, so a JSON inner handler still emits numbers as numbers and
// slices as arrays rather than everything as strings.
func TestRedactingHandlerLeavesCleanValuesNative(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewRedactingHandler(slog.NewJSONHandler(&buf, nil), nil))
	log.Info("clean", "n", 42, "d", 3*time.Second, "list", []string{"a", "b"}, "ok", true)
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got["n"] != float64(42) {
		t.Errorf("n = %#v, want the number 42", got["n"])
	}
	if l, ok := got["list"].([]any); !ok || len(l) != 2 {
		t.Errorf("list = %#v, want a two-element array", got["list"])
	}
	if got["ok"] != true {
		t.Errorf("ok = %#v, want true", got["ok"])
	}
}

// TestRedactingHandlerLogValuer: a LogValuer is resolved before redaction, so
// a type that renders itself lazily cannot carry a credential past the handler.
func TestRedactingHandlerLogValuer(t *testing.T) {
	var buf bytes.Buffer
	log := newRedactingTextLogger(&buf, nil)
	log.Info("lazy", "v", lazyValue{s: "key " + logAnthropicKey})
	assertNoneLeak(t, buf.String(), logAnthropicKey)
}

// TestRedactingHandlerRespectsLevel: Enabled is delegated, so wrapping does not
// turn on debug logging.
func TestRedactingHandlerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newRedactingTextLogger(&buf, nil)
	log.Debug("hidden " + logAnthropicKey)
	if buf.Len() != 0 {
		t.Fatalf("debug record written under an info-level handler:\n%s", buf.String())
	}
}

type stringerFunc func() string

func (f stringerFunc) String() string { return f() }

type lazyValue struct{ s string }

func (l lazyValue) LogValue() slog.Value { return slog.StringValue(l.s) }
