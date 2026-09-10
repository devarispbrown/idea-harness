package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestNoRequirementsIsNotComplete: the gate loop concludes allVerified vacuously
// when there is nothing to walk, and the write that followed set `complete` over
// anything. An initiative that died at framing has zero requirements, so a bare
// `idea verify` completed it and discarded its failure reason (audit 7 P0-2).
func TestNoRequirementsIsNotComplete(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x", Status: domain.InitiativeApproved}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.InitiativeComplete {
		t.Fatal("an initiative with zero requirements reached `complete`; nothing was ever built")
	}
}

// The all-removed case, which len(reqs) alone does not close. Engine.Revise
// supersedes every requirement before regenerating, so a model failure there
// leaves N removed requirements and zero active ones -- and the gate loop
// `continue`s all N, concluding allVerified exactly as the empty case does.
func TestAllRemovedRequirementsIsNotComplete(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x", Status: domain.InitiativeApproved}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	r := &domain.Requirement{InitiativeID: ini.ID, Statement: "s", Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "1", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeRequirements(ctx, ini.ID, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	if err := eng.VerifyInitiative(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.InitiativeComplete {
		t.Fatal("an initiative whose every requirement was superseded reached `complete`")
	}
}

// The write refuses over `failed` even when the caller does not. This is what
// covers the window between the Runtime's read and this write, and any future
// caller that does not go through the Runtime at all.
func TestCompleteInitiativeRefusesAFailedInitiative(t *testing.T) {
	fp := model.NewFakeProvider()
	_, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.FailInitiative(ctx, ini.ID, "provider 401"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteInitiative(ctx, ini.ID, store.ActorOrchestrator); err == nil {
		t.Fatal("CompleteInitiative wrote `complete` over a failed initiative")
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativeFailed || got.FailureReason != "provider 401" {
		t.Fatalf("the refused write still mutated the row: status=%q reason=%q", got.Status, got.FailureReason)
	}
}

// TestVerifyInitiativeDoesNotCompleteAFailedInitiative exercises the guarded
// write THROUGH the engine, which is what layer 2 exists for.
//
// Runtime.RunVerificationGate refuses a failed initiative synchronously, so a
// test that goes through the API can never reach this code -- and a mutation
// reverting the write to SetInitiativeStatus passed every other test in the
// suite. That is the shape of a vacuous guard: present, and unexercised on the
// path it protects. This calls the engine directly, as the window between the
// Runtime's read and this write does, and as any future caller would.
func TestVerifyInitiativeDoesNotCompleteAFailedInitiative(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x", Status: domain.InitiativeApproved}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	r := &domain.Requirement{InitiativeID: ini.ID, Statement: "s", Status: domain.RequirementWaived,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "1", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Waived, so the gate concludes allVerified and reaches the terminal write --
	// the same conclusion the all-waived flow reaches legitimately.
	if err := db.FailInitiative(ctx, ini.ID, "provider 401"); err != nil {
		t.Fatal(err)
	}

	err := eng.VerifyInitiative(ctx, ini.ID)
	got, gerr := db.GetInitiative(ctx, ini.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status == domain.InitiativeComplete {
		t.Fatalf("VerifyInitiative wrote `complete` over a failed initiative (err=%v)", err)
	}
	if got.FailureReason != "provider 401" {
		t.Errorf("the failure reason was discarded: %q", got.FailureReason)
	}
}
