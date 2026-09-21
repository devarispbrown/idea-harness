package daemon

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestFailOperationAfterPanicDoesNotStealAnUnrelatedLiveClaim is review
// H7(b)'s regression test.
//
// SuggestAnswersInitiative creates an operation row but never calls
// ClaimInitiativeRunning -- it only reads the initiative to refuse a
// synchronous "already running" request; it claims nothing. Before this,
// failOperationAfterPanic called FailInitiative unconditionally whenever the
// panicked operation's row carried an initiative id, with no regard for
// whether that operation type ever took a claim. Proven: if the initiative
// happened to be genuinely `running` at that moment because some OTHER,
// unrelated operation (e.g. a live advance) legitimately held it, a panic in
// suggest_answers overwrote that live claim with `failed` -- the user then
// ran `idea retry`, and a second pipeline started concurrently with the
// first, which kept running to completion none the wiser.
//
// This is deterministic, no goroutines needed: it sets up exactly that
// state (an initiative genuinely `running`, an unrelated `suggest_answers`
// operation row pointing at it) and calls failOperationAfterPanic directly,
// asserting the live claim survives.
func TestFailOperationAfterPanicDoesNotStealAnUnrelatedLiveClaim(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a live, unrelated advance: claims the initiative running.
	setStatus(t, rt, projectID, iniID, domain.InitiativeApproved)
	if err := pr.db.ClaimInitiativeRunning(t.Context(), iniID, domain.InitiativeApproved); err != nil {
		t.Fatal(err)
	}

	// suggest_answers's own operation row -- created, per production, with
	// NO preceding ClaimInitiativeRunning call of its own.
	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "suggest_answers")
	if err != nil {
		t.Fatal(err)
	}

	rt.failOperationAfterPanic(opID, "boom from suggest_answers")

	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeRunning {
		t.Fatalf("failOperationAfterPanic for a non-claiming operation type changed the initiative "+
			"status to %s -- it stole/failed a live claim that belongs to a different, unrelated "+
			"operation (review H7)", ini.Status)
	}

	op, err := pr.db.GetOperation(t.Context(), opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != domain.OperationFailed {
		t.Fatalf("the panicked operation itself should still be recorded failed: got %s", op.Status)
	}
}

// TestFailOperationAfterPanicReleasesItsOwnClaim is the positive control: a
// panic in an operation type that DOES claim (e.g. advance) must still
// release the initiative, exactly as before.
func TestFailOperationAfterPanicReleasesItsOwnClaim(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativeApproved)
	if err := pr.db.ClaimInitiativeRunning(t.Context(), iniID, domain.InitiativeApproved); err != nil {
		t.Fatal(err)
	}
	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "advance_initiative")
	if err != nil {
		t.Fatal(err)
	}

	rt.failOperationAfterPanic(opID, "boom from advance")

	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeFailed {
		t.Fatalf("a panic in a claiming operation type must release the initiative to failed, got %s", ini.Status)
	}
}
