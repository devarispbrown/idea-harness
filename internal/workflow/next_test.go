package workflow

import (
	"context"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
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

	setStatus(domain.InitiativePlanApproved)
	if a, _ := eng.NextAction(ctx, iniID); a != "execute" {
		t.Errorf("plan_approved (no change sets) -> %q, want execute", a)
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
