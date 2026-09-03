package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
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
}
