package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

func TestNextAction(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	setStatus := func(s domain.InitiativeStatus) {
		t.Helper()
		if err := db.SetInitiativeStatus(ctx, iniID, s, ""); err != nil {
			t.Fatal(err)
		}
	}

	setStatus(domain.InitiativeApproved)
	if a, _ := eng.NextAction(ctx, iniID); a != "advance" {
		t.Errorf("approved -> %q, want advance", a)
	}

	// plan_approved with nothing runnable must not propose execute. NextAction
	// used to return "execute" here unconditionally, so an initiative whose
	// tasks were all done -- or blocked on unsatisfied dependencies -- was told
	// to execute and then failed in the background. Once ExecuteInitiative
	// gained a precondition (DX audit finding b) that background failure would
	// have become a hard error out of `idea next --until-human`, which drives
	// itself from this string.
	setStatus(domain.InitiativePlanApproved)
	if a, _ := eng.NextAction(ctx, iniID); a != "verify" {
		t.Errorf("plan_approved with no ready task -> %q, want verify", a)
	}

	// With a ready task it is execute again.
	task := &domain.Task{InitiativeID: iniID, Title: "T", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskReady}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	if a, _ := eng.NextAction(ctx, iniID); a != "execute" {
		t.Errorf("plan_approved with a ready task -> %q, want execute", a)
	}

	cs := &domain.ChangeSet{InitiativeID: iniID}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	_ = db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, "")
	_ = db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, "")
	if a, _ := eng.NextAction(ctx, iniID); a != "review "+cs.HumanID {
		t.Errorf("review_required -> %q, want %q", a, "review "+cs.HumanID)
	}

	setStatus(domain.InitiativeComplete)
	if a, _ := eng.NextAction(ctx, iniID); a != "done" {
		t.Errorf("complete -> %q, want done", a)
	}

	// failed and running were one token ("wait") until audit 6. That told a
	// failed initiative the workflow was still running, so `idea next` printed
	// "workflow running -- try `idea watch`" and exited 0 forever while
	// `idea execute` pointed back at `idea next`. Both arms are asserted: the
	// split is only correct if `running` still answers wait.
	setStatus(domain.InitiativeFailed)
	if a, _ := eng.NextAction(ctx, iniID); a != "failed" {
		t.Errorf("failed -> %q, want failed", a)
	}

	setStatus(domain.InitiativeRunning)
	if a, _ := eng.NextAction(ctx, iniID); a != "wait" {
		t.Errorf("running -> %q, want wait", a)
	}
}

// TestNextReadyTaskReportsTheTaskExecuteWouldRun is the Build contract's
// `next_task` (B1/backend-2): the UI names the task a "Run next task" button
// would start, without a second round trip, and gets nil when there is none.
func TestNextReadyTaskReportsTheTaskExecuteWouldRun(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()
	if err := db.SetInitiativeStatus(ctx, iniID, domain.InitiativePlanApproved, ""); err != nil {
		t.Fatal(err)
	}

	if got, err := eng.NextReadyTask(ctx, iniID); err != nil || got != nil {
		t.Fatalf("NextReadyTask with no tasks = (%v, %v), want (nil, nil)", got, err)
	}

	task := &domain.Task{InitiativeID: iniID, Title: "Add /health endpoint", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskReady}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := eng.NextReadyTask(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("NextReadyTask = nil, want the ready task")
	}
	if got.ID != task.ID || got.HumanID != task.Code || got.Title != task.Title {
		t.Errorf("NextReadyTask = %+v, want id=%s human_id=%s title=%s", got, task.ID, task.Code, task.Title)
	}
}

// TestRunBlockedReasonNamesEachRefusal is the Build contract's
// `run_blocked_reason`: it must name the SAME reasons Execute itself refuses
// for, read-only, so the UI can grey out the button and say why instead of
// letting the click land on an error.
func TestRunBlockedReasonNamesEachRefusal(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	// Plan not approved.
	if err := db.SetInitiativeStatus(ctx, iniID, domain.InitiativeApproved, ""); err != nil {
		t.Fatal(err)
	}
	reason, err := eng.RunBlockedReason(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "not approved") {
		t.Errorf("approved status: reason = %q, want it to name the plan not being approved", reason)
	}

	// An operation already running.
	if err := db.SetInitiativeStatus(ctx, iniID, domain.InitiativeRunning, ""); err != nil {
		t.Fatal(err)
	}
	reason, err = eng.RunBlockedReason(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "already running") {
		t.Errorf("running status: reason = %q, want it to name an operation already running", reason)
	}

	// Ready, but nothing to run: not a blocked run.
	if err := db.SetInitiativeStatus(ctx, iniID, domain.InitiativePlanApproved, ""); err != nil {
		t.Fatal(err)
	}
	reason, err = eng.RunBlockedReason(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("no ready task: reason = %q, want empty (next_task=nil already conveys this)", reason)
	}

	// A ready task, but another task's change set is still open (A5).
	blocked := &domain.Task{InitiativeID: iniID, Title: "blocked task", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskReady}
	if err := db.CreateTask(ctx, blocked, "", nil); err != nil {
		t.Fatal(err)
	}
	other := &domain.Task{InitiativeID: iniID, Title: "other task", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskDone}
	if err := db.CreateTask(ctx, other, "", nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: other.ID}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	reason, err = eng.RunBlockedReason(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, cs.HumanID) {
		t.Errorf("open change set: reason = %q, want it to name %s", reason, cs.HumanID)
	}
	if strings.HasPrefix(reason, "workflow: execute:") {
		t.Errorf("reason leaks the internal error prefix: %q", reason)
	}
}

// TestNextPrescribesARunnableChangeSetRef closes the other half of the
// ambiguous-CHG live-lock.
//
// Human ids are per initiative; apply/review/change resolve project-wide. With
// two initiatives each holding a CHG-1, `idea next` printed `idea apply CHG-1`,
// that command refused as ambiguous, and next printed it again — exit 0, no
// progress, and the id that would work appeared in no human-readable output.
func TestNextPrescribesARunnableChangeSetRef(t *testing.T) {
	ctx := context.Background()
	e, db := newTestEngine(t, model.NewFakeProvider(), config.HarnessConfig{Network: config.NetworkDisabled})

	one := &domain.Initiative{Title: "first", Status: domain.InitiativePlanApproved}
	two := &domain.Initiative{Title: "second", Status: domain.InitiativePlanApproved}
	for _, ini := range []*domain.Initiative{one, two} {
		if err := db.CreateInitiative(ctx, ini); err != nil {
			t.Fatal(err)
		}
	}

	csA := &domain.ChangeSet{InitiativeID: one.ID, TaskID: "t1", PatchDigest: "d1"}
	if err := db.CreateChangeSet(ctx, csA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`UPDATE change_sets SET status = ? WHERE id = ?`, domain.ChangeSetApproved, csA.ID); err != nil {
		t.Fatal(err)
	}

	// Unique so far: the readable id is correct and is what should be printed.
	got, err := e.NextAction(ctx, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "apply "+csA.HumanID {
		t.Fatalf("got %q, want %q — the human id is right while it is unambiguous", got, "apply "+csA.HumanID)
	}

	// A second initiative mints its own CHG-1, and `idea apply CHG-1` now refuses.
	csB := &domain.ChangeSet{InitiativeID: two.ID, TaskID: "t2", PatchDigest: "d2"}
	if err := db.CreateChangeSet(ctx, csB); err != nil {
		t.Fatal(err)
	}
	if csB.HumanID != csA.HumanID {
		t.Fatalf("fixture assumption broken: %q vs %q", csA.HumanID, csB.HumanID)
	}

	got, err = e.NextAction(ctx, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "apply "+csA.ID {
		t.Errorf("got %q, want %q — next must prescribe a command that runs, and `idea apply %s` "+
			"now refuses as ambiguous", got, "apply "+csA.ID, csA.HumanID)
	}
}
