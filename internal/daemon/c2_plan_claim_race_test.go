package daemon

import (
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestLockPlanEditFreshObservesAConcurrentClaim is review C2's regression
// test for "re-read the initiative under the lock". Every Plan-editing
// handler used to do `pr, ini, err := s.initiative(r)` and THEN take
// lockPlanEdit, so taskEditableGuard ran against a snapshot fetched before
// the lock was held: whatever claimed the initiative while that snapshot was
// already in hand (RevisePlanInitiative/ReopenPhaseInitiative/
// AdvanceInitiative) was invisible to the guard no matter how long the lock
// serialized the rest of the handler. lockPlanEditFresh fixes this by
// re-reading the initiative AFTER acquiring the lock, so this test does not
// need real goroutine timing to be deterministic: it claims the initiative
// (exactly as those three entry points now do under this same lock) and then
// calls lockPlanEditFresh, asserting the initiative it returns reflects the
// claim -- proving the re-read is real, not a passthrough of whatever the
// caller already had.
func TestLockPlanEditFreshObservesAConcurrentClaim(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	s := NewServer(rt)

	// The stale value every pre-fix handler carried into taskEditableGuard.
	staleIni, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if staleIni.Status != domain.InitiativePlanApproved {
		t.Fatalf("fixture precondition: got status %s", staleIni.Status)
	}

	// Claim the initiative the way RevisePlanInitiative/ReopenPhaseInitiative/
	// AdvanceInitiative do, AFTER the snapshot above was taken.
	if err := pr.db.ClaimInitiativeRunning(t.Context(), iniID, domain.InitiativePlanApproved, domain.InitiativeNeedsHumanJudgment); err != nil {
		t.Fatalf("simulated claim: %v", err)
	}

	fresh, unlock, err := s.lockPlanEditFresh(t.Context(), pr, iniID)
	if err != nil {
		t.Fatalf("lockPlanEditFresh: %v", err)
	}
	defer unlock()

	if fresh.Status != domain.InitiativeRunning {
		t.Fatalf("lockPlanEditFresh returned status %s, want %s (the claim taken after the stale snapshot) -- "+
			"it must re-read the initiative under the lock, not reuse a value fetched before it",
			fresh.Status, domain.InitiativeRunning)
	}
	// Sanity check on the premise, not the fix: fed the stale, pre-claim
	// snapshot (as every pre-fix handler did), the guard wrongly considers
	// the initiative still editable -- no unfinished operation was created
	// in this test, so this is staleness alone, not the operations check,
	// passing it.
	if err := s.taskEditableGuard(t.Context(), pr, staleIni, nil); err != nil {
		t.Fatalf("sanity check failed: taskEditableGuard(staleIni) unexpectedly refused (%v) -- "+
			"this test's premise (the stale snapshot looks editable) no longer holds", err)
	}
	// The actual fix: fed the FRESH initiative lockPlanEditFresh returns, the
	// same guard must refuse.
	if err := s.taskEditableGuard(t.Context(), pr, fresh, nil); err == nil {
		t.Fatal("taskEditableGuard(fresh) should refuse once the initiative is running, " +
			"but passed -- review C2 is not fixed")
	}
}

// TestReopenPhaseClaimTakesThePlanEditLock is a narrower unit check that
// ReopenPhaseInitiative and RevisePlanInitiative actually acquire
// lockPlanEdit (as opposed to some other, ineffective serialization): with
// the lock already held externally, a concurrent RevisePlanInitiative call
// must block until it is released, rather than proceeding to claim the
// initiative underneath the held lock.
func TestRevisePlanInitiativeBlocksOnAConcurrentlyHeldPlanEditLock(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	unlock := pr.lockPlanEdit(iniID)

	type revision struct {
		opID string
		err  error
	}
	done := make(chan revision, 1)
	go func() {
		opID, err := rt.RevisePlanInitiative(t.Context(), projectID, iniID)
		done <- revision{opID, err}
	}()

	// Give a buggy (unlocked) RevisePlanInitiative every chance to run to
	// completion -- it needs only a couple of fast local DB calls -- before
	// checking that it has NOT. A correct one blocks on m.Lock() inside
	// lockPlanEdit regardless of how long we wait here, so this sleep can
	// only ever expose a regression, never mask one.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		unlock()
		t.Fatal("RevisePlanInitiative returned while the plan-edit lock was held externally -- it is not taking lockPlanEdit around its claim")
	default:
	}

	// Confirm the initiative was not claimed while the lock was held: the
	// claim and the lock acquisition must be the same critical section.
	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeRunning {
		unlock()
		t.Fatal("initiative was claimed running before RevisePlanInitiative's lockPlanEdit call was released -- claim is not inside the lock")
	}

	unlock()
	got := <-done
	if got.err != nil {
		t.Fatalf("RevisePlanInitiative after unlock: %v", got.err)
	}
	// RevisePlanInitiative returns as soon as it has launched its operation,
	// which keeps writing under the project directory -- itself under
	// t.TempDir() -- after this test returns. On a slower runner that races
	// t.TempDir()'s RemoveAll, which then fails the test with "directory not
	// empty" (seen on macOS amd64 in CI). Wait for the operation to finish.
	waitForOperation(t, ts, token, got.opID)
}
