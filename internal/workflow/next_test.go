package workflow

import (
	"context"
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
