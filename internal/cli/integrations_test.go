package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestIntegrationsStatusRendersHealthAndJSON(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/p1/integrations", map[string]any{"integrations": []map[string]any{
		{"name": "slack", "configured": true, "active": true,
			"tokens":   map[string]bool{"SLACK_BOT_TOKEN": true, "SLACK_APP_TOKEN": false},
			"problems": []string{"SLACK_APP_TOKEN is not set: buttons and /idea are off (notifications still post)"},
			"outbox":   map[string]any{"pending": 1, "delivered": 7, "dead": 0}},
		{"name": "linear", "configured": false, "active": false, "problems": []string{}},
	}})

	stdout, _, err := run(t, "integrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slack", "7 delivered, 1 pending, 0 dropped", "SLACK_APP_TOKEN is not set", "SLACK_BOT_TOKEN:", "missing", "linear: not configured"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, stdout)
		}
	}
	stdout, _, err = run(t, "integrations", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v cliIntegrationsView
	if err := json.Unmarshal([]byte(stdout), &v); err != nil || len(v.Integrations) != 2 {
		t.Fatalf("--json output = %v\n%s", err, stdout)
	}
}

// doctor surfaces integration problems -- dropped deliveries, a missing
// token -- without failing: Slack being broken does not make the harness
// unhealthy, and a script gating on doctor must not start failing over it.
func TestDoctorReportsIntegrationProblemsWithoutFailing(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProject(t, f)
	f.json("GET /api/v1/projects/{projectId}/integrations", map[string]any{"integrations": []map[string]any{
		{"name": "slack", "configured": true, "active": true,
			"problems": []string{"2 event(s) could not be delivered and were dropped after retrying; last error: slack chat.postMessage: not_in_channel"},
			"outbox":   map[string]any{"pending": 0, "delivered": 3, "dead": 2}},
		{"name": "linear", "configured": false, "problems": []string{}},
	}})
	stdout, _, err := run(t, "doctor")
	if err != nil {
		t.Fatalf("an integration problem failed doctor: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "not_in_channel") || !strings.Contains(stdout, "2 dropped") {
		t.Fatalf("doctor hid the integration problem:\n%s", stdout)
	}
	if strings.Contains(stdout, "linear") {
		t.Fatalf("doctor's compact view lists unconfigured integrations:\n%s", stdout)
	}
}

func TestIntegrationsTestExitsNonZeroOnFailure(t *testing.T) {
	f := newFakeDaemon(t)
	f.mux.HandleFunc("POST /api/v1/projects/p1/integrations/{name}/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("name") == "slack" {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "slack", "ok": true, "result": map[string]any{"team": "Acme", "socket_mode": "ok"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "linear", "ok": false, "error": "linear: no team with key \"ENG\""})
	})
	stdout, _, err := run(t, "integrations", "test", "slack")
	if err != nil || !strings.Contains(stdout, "test passed") || !strings.Contains(stdout, "Acme") {
		t.Fatalf("slack test: %v\n%s", err, stdout)
	}
	if _, _, err := run(t, "integrations", "test", "linear"); err == nil || !strings.Contains(err.Error(), "no team") {
		t.Fatalf("a failed test must fail the command: %v", err)
	}
	if _, _, err := run(t, "integrations", "test", "jira"); err == nil || !strings.Contains(err.Error(), "slack|linear") {
		t.Fatalf("unknown integration: %v", err)
	}
}
