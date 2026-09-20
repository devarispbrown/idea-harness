package daemon

import (
	"net/http"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestAddTaskRefusedWhenARevisePlanClaimIsConcurrent is review C2's
// regression test.
//
// Before the fix, lockPlanEdit was taken only in the daemon's Plan-editing
// HTTP handlers; RevisePlanInitiative/ReopenPhaseInitiative/AdvanceInitiative
// -- which claim the initiative `running` and then rewrite (or, for Advance,
// build) its tasks via DeleteTasksForInitiative -- took no lock at all. Each
// handler also read the initiative BEFORE acquiring its own lock and then
// ran taskEditableGuard against that pre-lock snapshot, so even the lock it
// did take served no purpose. The proven repro: while a revise-plan/
// reopen-phase claim is in flight for an initiative, `add-task` reads the
// initiative as still `plan_approved`, the guard passes against that stale
// read, and the handler answers 200 OK for a task that a concurrent
// DeleteTasksForInitiative removes moments later.
//
// This test forces the exact interleaving deterministically -- not by
// timing or the race detector, which cannot observe a business-logic
// mistake like "checked the wrong snapshot" -- by holding the same
// per-initiative lock RevisePlanInitiative now takes around its own
// claim-then-CreateOperation sequence (runtime.go), and performing that
// exact sequence while an add-task request is blocked waiting for the lock.
// Whenever the add-task goroutine is scheduled, the lock guarantees it
// cannot observe anything from before the claim: it must see `running` and
// answer 409, never 200.
func TestAddTaskRefusedWhenARevisePlanClaimIsConcurrent(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	_ = mustCreateTask(t, pr, iniID, "Existing task", req.ID, nil)

	// Hold the lock exactly as RevisePlanInitiative does around its own
	// read-check-claim-CreateOperation sequence (runtime.go), simulating a
	// revise-plan request that reached the claim first.
	unlock := pr.lockPlanEdit(iniID)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks"
	body := `{"title":"Human task 0","statement":"a new task","requirement_ids":["` + req.Code + `"]}`

	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, err := concurrentGetOrPost(http.MethodPost, url, token, body)
		done <- result{status, err}
	}()

	// Whenever the goroutine above gets scheduled, decodes its body and
	// calls lockPlanEditFresh, it blocks here -- the lock is still held --
	// no matter how the runtime schedules it. Perform exactly what
	// RevisePlanInitiative performs under this same lock before we release
	// it, so there is no window in which the goroutine could observe a
	// state other than "claimed".
	if err := pr.db.ClaimInitiativeRunning(t.Context(), iniID, domain.InitiativePlanApproved, domain.InitiativeNeedsHumanJudgment); err != nil {
		unlock()
		t.Fatalf("simulated revise-plan claim: %v", err)
	}
	if _, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "revise_plan"); err != nil {
		unlock()
		t.Fatalf("simulated revise-plan CreateOperation: %v", err)
	}
	unlock()

	res := <-done
	if res.err != nil {
		t.Fatalf("add-task request: %v", res.err)
	}
	if res.status == http.StatusOK {
		t.Fatal("add-task returned 200 OK while a revise-plan claim was concurrently taken " +
			"(review C2): the guard observed a snapshot from before the claim instead of the " +
			"claim itself, so the caller was told the task exists when a concurrent " +
			"DeleteTasksForInitiative was about to remove it")
	}
	if res.status != http.StatusConflict {
		t.Fatalf("expected 409 once the claim is visible under the lock, got %d", res.status)
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

	done := make(chan error, 1)
	go func() {
		_, err := rt.RevisePlanInitiative(t.Context(), projectID, iniID)
		done <- err
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
	if err := <-done; err != nil {
		t.Fatalf("RevisePlanInitiative after unlock: %v", err)
	}
}
