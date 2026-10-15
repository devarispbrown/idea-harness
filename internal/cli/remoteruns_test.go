package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

var remoteRunRows = []map[string]any{{
	"id": "r1", "initiative_id": "ini", "task_code": "TASK-2", "attempt": 2, "server_url": "https://oh.example",
	"conversation_id": "conv-9", "status": "failed", "event_count": 4, "last_event": "tool: npm test",
	"error": "server said 500", "updated_at": "2026-10-15T09:00:00.000Z",
}}

func TestRunsRemoteListsTheInitiativesRuns(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/remote-runs", remoteRunRows)
	stdout, _, err := run(t, "runs", "remote")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TASK-2", "failed", "attempt 2", "https://oh.example", "conv-9", "tool: npm test", "(4 events)", "server said 500"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if !f.asked("/remote-runs") {
		t.Errorf("requests = %v", f.requests)
	}
}

func TestRunsRemoteJSONAndAll(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/remote-runs", remoteRunRows)
	stdout, _, err := run(t, "runs", "remote", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if len(got) != 1 || got[0]["conversation_id"] != "conv-9" {
		t.Errorf("got %v", got)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/initiatives/") && strings.HasSuffix(r, "/remote-runs") {
			t.Errorf("--all asked the initiative listing: %v", f.requests)
		}
	}
}

func TestRunsNeedsRemote(t *testing.T) {
	newFakeDaemon(t)
	if _, _, err := run(t, "runs"); err == nil {
		t.Error("bare `idea runs` succeeded")
	}
}
