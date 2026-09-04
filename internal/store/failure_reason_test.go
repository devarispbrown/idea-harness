package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func newReasonDB(t *testing.T) (*DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, context.Background()
}

// TestFailureReasonSurvivesAClaimAndRelease pins the loss a pre-merge review
// found by probing the store directly.
//
// Apply and revert claim the initiative from every non-running state, `failed`
// included, and release it back to where it was. Both the claim and the release
// cleared `failure_reason` unconditionally, so applying an already-approved
// change set on a failed initiative left it `failed` with no reason at all —
// `idea status` showed a failure nobody could explain. Before the claim
// existed, apply never touched the initiative row and the reason survived.
func TestFailureReasonSurvivesAClaimAndRelease(t *testing.T) {
	db, ctx := newReasonDB(t)
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	const reason = "the model provider returned 401"
	if err := db.FailInitiative(ctx, ini.ID, reason); err != nil {
		t.Fatal(err)
	}

	// What apply does: claim from failed, run, release back to failed.
	if err := db.ClaimInitiativeRunning(ctx, ini.ID,
		domain.InitiativeApproved, domain.InitiativePlanApproved,
		domain.InitiativeNeedsHumanJudgment, domain.InitiativeComplete,
		domain.InitiativeFailed); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeFailed, ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativeFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.FailureReason != reason {
		t.Fatalf("failure_reason = %q, want %q — an initiative reported as failed "+
			"with no reason is worse than one that was never claimed", got.FailureReason, reason)
	}
}

// TestTransitionOutOfFailedStillClearsTheReason is the other half: a stale
// reason must not follow an initiative into a state where it did not fail.
// Preserving the reason on a release to `failed` must not preserve it forever.
func TestTransitionOutOfFailedStillClearsTheReason(t *testing.T) {
	db, ctx := newReasonDB(t)
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.FailInitiative(ctx, ini.ID, "transient boom"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativePlanApproved, ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureReason != "" {
		t.Fatalf("failure_reason = %q after moving to plan_approved, want it cleared", got.FailureReason)
	}
}
