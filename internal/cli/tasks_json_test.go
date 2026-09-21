package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestTasksJSONIncludesRequirementIDsAndDependsOn is an ICP re-run gap: the
// API's GET .../tasks always returns requirement_ids and depends_on
// (domain.Task's own fields, populated by AttachTaskEdges), but the CLI's
// cliTask DTO dropped both silently, so `idea tasks --json` -- the only
// surface a plan-editing script has, since there is no CLI verb for
// add/merge/split/depends-on/reorder -- could not see a task's requirement
// traceability or dependencies. The evaluator had to fall back to raw HTTP
// for exactly this.
func TestTasksJSONIncludesRequirementIDsAndDependsOn(t *testing.T) {
	f := newFakeDaemon(t)
	f.initiatives = []map[string]any{{"id": "ini-1", "title": "x", "status": "plan_approved"}}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.initiatives)
	})
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "task-1", "code": "TASK-1", "title": "Implement billing", "priority": "must",
				"requirement_ids": []string{"REQ-1", "REQ-2"},
				"depends_on":      []string{"TASK-0"},
			},
		})
	})

	stdout, _, err := run(t, "tasks", "--json")
	if err != nil {
		t.Fatalf("idea tasks --json: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 task, got %d", len(got))
	}
	reqIDs, _ := got[0]["requirement_ids"].([]any)
	if len(reqIDs) != 2 {
		t.Errorf("requirement_ids missing/wrong in `idea tasks --json` output: %v", got[0])
	}
	dependsOn, _ := got[0]["depends_on"].([]any)
	if len(dependsOn) != 1 {
		t.Errorf("depends_on missing/wrong in `idea tasks --json` output: %v", got[0])
	}
	if !strings.Contains(stdout, "REQ-1") || !strings.Contains(stdout, "TASK-0") {
		t.Errorf("raw output missing expected values: %s", stdout)
	}
}
