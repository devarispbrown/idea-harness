package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor/ohfake"
)

// Refusals that must happen before anything is sent anywhere: no URL, plain
// http to another machine, a missing --yes, flags of another kind.
func TestOpenHandsSetupRefusals(t *testing.T) {
	f := newFakeDaemon(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--kind", "openhands", "--yes"}, "--url is required"},
		{[]string{"--kind", "openhands", "--url", "http://agents.example.com", "--yes"}, "use https"},
		{[]string{"--kind", "openhands", "--url", "https://agents.example.com"}, "pass --yes"},
		{[]string{"--kind", "openhands", "--url", "https://a.example", "--image", "x", "--yes"}, "do not apply"},
		{[]string{"--kind", "openhands", "--url", "https://a.example", "--agent", "acp", "--yes"}, "needs --acp-command"},
		{[]string{"--kind", "openhands", "--url", "https://a.example", "--api-key-env", "sk-live", "--yes"}, "NAME of an environment variable"},
		{[]string{"--kind", "hermes", "--url", "https://a.example", "--yes"}, "only apply to --kind openhands"},
	} {
		_, _, err := run(t, append([]string{"executors", "setup"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if f.asked("/executors") {
		t.Fatal("a refused setup reached the daemon")
	}
}

// Setup probes the server with the session key from the environment, states
// what leaves the machine, and writes the openhands block through the
// daemon; a wrong key is refused before anything is written.
func TestOpenHandsSetupProbesAndConfigures(t *testing.T) {
	f := newFakeDaemon(t)
	srv := ohfake.New(t, "the-session-key-123456")
	var put map[string]any
	f.mux.HandleFunc("PUT /api/v1/projects/{projectId}/executors", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &put)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"updated"}`))
	})

	t.Setenv("OPENHANDS_API_KEY", "wrong-key-0000000")
	_, _, err := run(t, "executors", "setup", "--kind", "openhands", "--url", srv.URL(), "--yes")
	if err == nil || !strings.Contains(err.Error(), "refused the session API key") {
		t.Fatalf("wrong key: %v", err)
	}
	if put != nil {
		t.Fatal("configured despite a refused key")
	}

	t.Setenv("OPENHANDS_API_KEY", "the-session-key-123456")
	stdout, stderr, err := run(t, "executors", "setup", "--kind", "openhands", "--url", srv.URL(), "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "sends the work root") || !strings.Contains(stderr, "version 1.54.0") {
		t.Fatalf("stderr does not state the tradeoff and the probe:\n%s", stderr)
	}
	if !strings.Contains(stdout, "executor configured") {
		t.Fatalf("stdout:\n%s", stdout)
	}
	if put["kind"] != "openhands" || put["openhands_url"] != srv.URL() || put["openhands_agent"] != "openhands" {
		t.Fatalf("PUT body = %v", put)
	}
	if strings.Contains(stdout+stderr, "the-session-key-123456") {
		t.Fatal("the session key was printed")
	}
}

// doctor reports the configured Agent Server: reachable with the key, or a
// warning naming what is wrong.
func TestDoctorOpenHands(t *testing.T) {
	srv := ohfake.New(t, "the-session-key-123456")
	cfg := config.Config{Harness: config.HarnessConfig{Executor: config.ExecutorConfig{Kind: config.ExecutorOpenHands,
		OpenHands: config.OpenHandsConfig{URL: srv.URL()}}}}
	var out strings.Builder
	doctorOpenHands(cfg, map[string]string{"OPENHANDS_API_KEY": "the-session-key-123456"}, &out)
	if !strings.Contains(out.String(), "✓ openhands") {
		t.Fatalf("doctor = %q", out.String())
	}
	out.Reset()
	t.Setenv("OPENHANDS_API_KEY", "")
	doctorOpenHands(cfg, map[string]string{}, &out)
	if !strings.Contains(out.String(), "⚠ openhands") || !strings.Contains(out.String(), "session API key") {
		t.Fatalf("doctor without a key = %q", out.String())
	}
	out.Reset()
	doctorOpenHands(config.Config{}, nil, &out)
	if out.Len() != 0 {
		t.Fatal("doctor checked openhands for another kind")
	}
}
