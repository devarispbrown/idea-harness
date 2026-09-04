package daemon

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestCancellingAnOperationReleasesTheInitiative pins a P0 that this round's own
// claim mechanism introduced.
//
// ClaimInitiativeRunning marks an initiative running before async work starts,
// and only a terminal transition releases it. failOperation released it on
// OperationFailed but not on OperationCancelled -- so `idea cancel` left the
// operation correctly cancelled and the initiative running forever. Every later
// advance/execute/revise answered "already running";
// reconcileStaleOperations could not see it, because a cancelled operation is
// terminal and that sweep only scans pending/running; and no CLI command could
// recover it.
//
// Worse than the double-click race the claim closes, created by closing it.
func TestCancellingAnOperationReleasesTheInitiative(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	if err := pr.db.ClaimInitiativeRunning(ctx, iniID, domain.InitiativePlanApproved); err != nil {
		t.Fatal(err)
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, iniID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}

	// The operation ends cancelled, as it does when a user runs `idea cancel`.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	rt.failOperation(opID, iniID, "execute cancelled", cancelled, pr, context.Canceled, true)

	ini, err := pr.db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeRunning {
		t.Fatal("the initiative is still running after its operation was cancelled; " +
			"every later request answers \"already running\" with no way back")
	}
}

// TestOrphanedClaimsAreSwept is the backstop for the case failOperation cannot
// reach: a daemon killed between claiming an initiative and recording anything.
func TestOrphanedClaimsAreSwept(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	if err := pr.db.ClaimInitiativeRunning(ctx, iniID, domain.InitiativePlanApproved); err != nil {
		t.Fatal(err)
	}
	// No operation at all: the process died before CreateOperation committed.

	rt.releaseOrphanedClaims(ctx, pr)

	ini, err := pr.db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeRunning {
		t.Error("an initiative claimed with no operation was not swept; it is stuck forever")
	}
}

// TestExecuteReleasesTheInitiativeOnBothPaths pins the P0 that the claim
// mechanism introduced for the most common command in the tool.
//
// Execute is the one claiming entry point whose engine call never sets the
// initiative status itself — the other six reach a terminal status inside
// internal/workflow, so their claim is released as a side effect. Execute's
// closure wrote only the operation status on success and passed
// failInitiative=false on error, so after the claim was added a *successful*
// execute left the initiative running forever: `idea next` answered "workflow
// running" indefinitely and every later command answered "already running".
//
// scripts/smoke.sh and the audit journey both missed it because they revert the
// change set afterwards and RevertChangeSet happens to reset the status. This
// test deliberately never reverts.
func TestExecuteReleasesTheInitiativeOnBothPaths(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("execute was refused from plan_approved: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeRunning {
		t.Fatal("the initiative is still running after execute finished; " +
			"`idea next` answers \"workflow running\" forever and every command " +
			"answers \"already running\"")
	}
}
