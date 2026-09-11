package cli

import (
	"strings"
	"testing"
)

// `idea status` printed `● failed` with the provider's full error and returned
// 0, so a script running it as a health check could not tell. Audit 8 counted
// 18 commands exiting 0 over a reported or concealed failure; this is the one
// that reports the failure in full and is most often scripted.
func TestStatusExitsNonZeroOnAFailedInitiative(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]string{
		{"id": "i1", "title": "Recipe app", "status": "failed",
			"failure_reason": "workflow: framing: openai: status 401"},
	})

	stdout, _, err := run(t, "status")
	if err == nil {
		t.Errorf("status exited 0 having printed a failed initiative:\n%s", stdout)
	}
	if err != nil && !strings.Contains(err.Error(), "401") {
		t.Errorf("the error does not carry the reason: %v", err)
	}
}

// The exclusion, and it is load-bearing: `needs_human_judgment` is the loop
// working — it is waiting on a person, not broken. Treating it as a failure
// would make `idea next`, `idea reviews` and the whole judgment path exit
// non-zero on their happy path, and would break scripts/smoke.sh in four
// places, which runs under `set -euo pipefail`.
func TestStatusExitsZeroOnNeedsHumanJudgment(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]string{
		{"id": "i1", "title": "Recipe app", "status": "needs_human_judgment"},
	})

	if _, _, err := run(t, "status"); err != nil {
		t.Errorf("status exited non-zero on an initiative merely waiting on a person: %v", err)
	}
}

// The machine path was strictly less informative than the human one about the
// only thing that matters. The daemon always sent failure_reason and the CLI
// struct dropped it.
func TestStatusJSONCarriesTheFailureReason(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]string{
		{"id": "i1", "title": "Recipe app", "status": "failed",
			"failure_reason": "workflow: framing: openai: status 401"},
	})

	stdout, _, _ := run(t, "status", "--json")
	if !strings.Contains(stdout, "failure_reason") {
		t.Errorf("--json drops the failure reason, so a script can see that something failed "+
			"but never why:\n%s", stdout)
	}
	if !strings.Contains(stdout, "401") {
		t.Errorf("--json carries the key but not the reason:\n%s", stdout)
	}
	// The shape is pinned by docs/cli.md and ledger row G4: still an array, and
	// omitempty means a non-failed initiative is byte-identical to before.
	if !strings.HasPrefix(strings.TrimSpace(stdout), "[") {
		t.Errorf("--json is no longer an array; `jq '.[0]'` breaks:\n%s", stdout)
	}
}

func TestStatusJSONOmitsTheReasonWhenThereIsNone(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]string{
		{"id": "i1", "title": "Recipe app", "status": "approved"},
	})

	stdout, _, err := run(t, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if strings.Contains(stdout, "failure_reason") {
		t.Errorf("a healthy initiative gained a key, so the documented shape changed:\n%s", stdout)
	}
}

// The exit code must not be sticky. Scanning every initiative for a failure
// made one abandoned experiment gate the project's overview command forever,
// with nothing a user could do to clear it — `idea status` unscoped lists every
// initiative, and an overview of ten has no single status.
//
// The newest is what every other command means by "the initiative":
// ListInitiatives is ORDER BY created_at ASC and resolveInitiative takes the
// last row.
func TestStatusExitIsNotStickyAcrossInitiatives(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]string{
		{"id": "old", "title": "Abandoned experiment", "status": "failed",
			"failure_reason": "openai: status 401"},
		{"id": "new", "title": "Current work", "status": "complete"},
	})

	if _, _, err := run(t, "status"); err != nil {
		t.Errorf("an older failed initiative gates the overview command: %v", err)
	}
}

func TestStatusExitsNonZeroWhenTheNewestFailed(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]string{
		{"id": "old", "title": "Finished work", "status": "complete"},
		{"id": "new", "title": "Current work", "status": "failed",
			"failure_reason": "openai: status 401"},
	})

	_, _, err := run(t, "status")
	if err == nil {
		t.Error("status exited 0 although the initiative the next command will act on has failed")
	}
	if err != nil && !strings.Contains(err.Error(), "Current work") {
		t.Errorf("the error names the wrong initiative: %v", err)
	}
}
