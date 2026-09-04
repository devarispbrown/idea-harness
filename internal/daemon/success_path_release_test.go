package daemon

import (
	"net/http/httptest"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// appliedChangeSet drives a project to an applied change set through the same
// runtime the daemon uses.
//
// It exists because the branch that added the claim had no daemon-level
// fixture reaching a *successful* execute or apply. The existing
// TestExecuteReleasesTheInitiativeOnBothPaths is named for both paths but runs
// on an initiative with no tasks, so engine.Execute errors and only the error
// path is driven — deleting the success-path release left the whole suite
// green. A pre-merge review found that by mutation.
func appliedChangeSet(t *testing.T, rt *Runtime, ts *httptest.Server, token string) (projectID, iniID, csID string) {
	t.Helper()
	projectID, iniID = newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{
		InitiativeID: iniID, Title: "Implement core",
		Statement: "build it", Priority: domain.PriorityMust,
	}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	sets, err := pr.db.ListChangeSets(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) == 0 {
		t.Fatal("execute produced no change set; the fixture cannot reach the success path")
	}
	csID = sets[0].ID
	if err := pr.engine.ReviewChangeSet(t.Context(), iniID, csID, "approved"); err != nil {
		t.Fatal(err)
	}
	opID, err = rt.ApplyChangeSetInitiative(t.Context(), projectID, iniID, csID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	waitForOperation(t, ts, token, opID)
	return projectID, iniID, csID
}

func statusOf(t *testing.T, rt *Runtime, projectID, iniID string) domain.Initiative {
	t.Helper()
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	return ini
}

// TestExecuteSuccessReleasesTheInitiative is the audit-4 P0 itself, on the path
// that actually shipped broken. Deleting only the success-path release() in
// ExecuteInitiative left every other test in this package green.
func TestExecuteSuccessReleasesTheInitiative(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{
		InitiativeID: iniID, Title: "Implement core",
		Statement: "build it", Priority: domain.PriorityMust,
	}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, ts, token, opID)

	if got := statusOf(t, rt, projectID, iniID).Status; got == domain.InitiativeRunning {
		t.Fatal("the initiative is still running after a SUCCESSFUL execute; " +
			"`idea next` answers \"workflow running\" forever and every later command " +
			"answers \"already running\"")
	}
}

// TestApplySuccessReleasesTheInitiative covers C1's success path. Deleting all
// four release() calls in the apply and revert paths also left the suite green.
func TestApplySuccessReleasesTheInitiative(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, _ := appliedChangeSet(t, rt, ts, token)

	if got := statusOf(t, rt, projectID, iniID).Status; got == domain.InitiativeRunning {
		t.Fatal("the initiative is still running after a successful apply")
	}
}

// TestRevertFromCompleteLandsOnPlanApproved pins a regression this branch
// introduced.
//
// RevertChangeSet ends by setting the initiative to plan_approved — the
// requirement it implemented has just been unverified, so the initiative is no
// longer complete and the engine says so explicitly. The daemon's new
// success-path release() then wrote the status captured *before* the claim
// straight back over it. Reverting from `complete` left the files rolled back,
// the change set `reverted` and the requirement unverified, while the
// initiative still read `complete` and `idea next` reported it done.
func TestRevertFromCompleteLandsOnPlanApproved(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, csID := appliedChangeSet(t, rt, ts, token)

	// The state that exposes it: apply/revert deliberately claim from
	// `complete`, so priorStatus is `complete` and the release overwrites the
	// engine's own transition.
	setStatus(t, rt, projectID, iniID, domain.InitiativeComplete)

	opID, err := rt.RevertChangeSetInitiative(t.Context(), projectID, iniID, csID)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	ini := statusOf(t, rt, projectID, iniID)
	if ini.Status == domain.InitiativeRunning {
		t.Fatal("the initiative is still running after a successful revert")
	}
	if ini.Status != domain.InitiativePlanApproved {
		t.Fatalf("initiative status after reverting from complete = %q, want %q — "+
			"the engine unverified the requirement and left plan_approved; writing the "+
			"pre-claim status back over it reports a rolled-back initiative as finished",
			ini.Status, domain.InitiativePlanApproved)
	}
}
