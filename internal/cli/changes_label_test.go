package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestChangeSetReviewStatusLabelMatchesStatusWording is ICP re-run D4:
// `idea changes` labelled a change set whose adversarial review never
// completed at all "needs your approval" -- the same status
// (review_required) `idea status`'s guidance already, correctly, describes
// as "never finished its adversarial review" (see
// domain.ChangeSet.ReviewCompleted/ReviewNeverCompletedCause). There is
// nothing to approve yet in that state: `idea review <id> approve` refuses
// it and names `rerun`. The two commands must not disagree about what to
// call it.
func TestChangeSetReviewStatusLabelMatchesStatusWording(t *testing.T) {
	completed := "2026-01-01T00:00:00Z"
	cases := []struct {
		name string
		cs   cliChangeSet
		want string
	}{
		{"review_required_never_reviewed",
			cliChangeSet{Status: "review_required", ReviewCompletedAt: nil},
			"never finished its adversarial review"},
		{"review_required_awaiting_approval",
			cliChangeSet{Status: "review_required", ReviewCompletedAt: &completed},
			"needs your approval"},
		{"applied_unaffected",
			cliChangeSet{Status: "applied", ReviewCompletedAt: &completed},
			"applied"},
		{"unknown_status_falls_back_to_raw",
			cliChangeSet{Status: "some_future_status"},
			"some_future_status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := changeSetReviewStatusLabel(c.cs); got != c.want {
				t.Errorf("changeSetReviewStatusLabel(%+v) = %q, want %q", c.cs, got, c.want)
			}
		})
	}
}

// TestIdeaChangesReportsUnreviewedConsistentlyWithStatus drives the real
// `idea changes` command end to end against a change set in exactly the
// persisted shape ICP re-run defect #1 leaves behind (review_required, no
// review_completed_at), and pins the printed line.
func TestIdeaChangesReportsUnreviewedConsistentlyWithStatus(t *testing.T) {
	f := newFakeDaemon(t)
	f.initiatives = []map[string]any{{"id": "ini-1", "title": "x", "status": "plan_approved"}}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.initiatives)
	})
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/change-sets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "cs-1", "human_id": "CHG-1", "status": "review_required"},
		})
	})

	stdout, _, err := run(t, "changes")
	if err != nil {
		t.Fatalf("idea changes: %v", err)
	}
	if !strings.Contains(stdout, "never finished its adversarial review") {
		t.Errorf("idea changes did not report the unreviewed change set consistently with `idea status`: %q", stdout)
	}
	if strings.Contains(stdout, "needs your approval") {
		t.Errorf("idea changes still claims a never-reviewed change set needs approval: %q", stdout)
	}
}
