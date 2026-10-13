package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real key shapes, assembled at runtime so this file is not itself a finding
// for a repository secret scanner.
var (
	testAnthropicKey = "sk-ant-api03-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"
	testGitHubPAT    = "ghp_" + "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	testAWSKeyID     = "AKIA" + "Q3EGRIXJ7PZ4WN2T"
)

// TestDaemonLogIsRedactedAtWrite: everything the runtime logs goes through the
// redacting handler -- pattern-shaped keys, the daemon's own API token, and a
// project's own configured secret (a .env value with no recognizable shape),
// which only becomes known once the project loads.
func TestDaemonLogIsRedactedAtWrite(t *testing.T) {
	var buf bytes.Buffer
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "state"), slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	const projectSecret = "plainButConfigured-77Qx"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("BILLING_SERVICE_SECRET="+projectSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := rt.CreateProject(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.EnsureProject(ctx, id); err != nil {
		t.Fatal(err)
	}

	rt.log.Warn("provider call failed: key "+testAnthropicKey,
		"err", fmt.Errorf("wrapped: %w", errors.New("401 for "+testGitHubPAT)),
		"token", rt.Token(),
		"detail", "echoed "+projectSecret,
	)
	out := buf.String()
	for _, s := range []string{testAnthropicKey, testGitHubPAT, rt.Token(), projectSecret} {
		if strings.Contains(out, s) {
			t.Errorf("%q reached the daemon log:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction markers in the log:\n%s", out)
	}
}

// TestLogsEndpointRedactsOnRead: GET /api/v1/logs masks what the slog handler
// never saw -- lines written before it existed, or raw stderr in daemon.log.
func TestLogsEndpointRedactsOnRead(t *testing.T) {
	rt, ts, token := newTestServer(t)
	lines := []string{
		"time=2026-01-01T00:00:00Z level=INFO msg=ok",
		"panic: dial failed for key " + testAnthropicKey,
		"level=WARN msg=x aws=" + testAWSKeyID,
		"level=WARN msg=y token=" + token,
	}
	if err := os.WriteFile(filepath.Join(rt.StateRoot(), "daemon.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/logs?n=10", token, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /logs: %d", resp.StatusCode)
	}
	var body struct {
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(body.Lines, "\n")
	for _, s := range []string{testAnthropicKey, testAWSKeyID, token} {
		if strings.Contains(joined, s) {
			t.Errorf("%q served by /api/v1/logs:\n%s", s, joined)
		}
	}
	if len(body.Lines) != len(lines) || body.Lines[0] != lines[0] {
		t.Errorf("clean lines must pass through unchanged, got %q", body.Lines)
	}
}

// TestLogRedactorNilIsPatternOnly: a Runtime built as a bare struct literal has
// no logRedactor; the logs endpoint must still apply the pattern catalog rather
// than serve the file verbatim.
func TestLogRedactorNilIsPatternOnly(t *testing.T) {
	var l *logRedactor
	l.add("ignored")
	if got := l.RedactString("k " + testAnthropicKey); strings.Contains(got, testAnthropicKey) {
		t.Fatalf("nil logRedactor failed open: %q", got)
	}
}
