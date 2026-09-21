package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRecordHumanEvidenceIsOneTransaction is the MEDIUM regression test for
// RecordHumanEvidence atomicity.
//
// Before this, workflow.Engine.RecordHumanEvidence ran ClearVerificationEvidence
// + one CreateVerificationEvidence per acceptance criterion + an optional
// MarkRequirementFailed/MarkRequirementWaived as N+2 separate transactions
// (store/verify.go). A crash between any two of them destroyed the prior
// evidence without recording the new judgment, leaving the requirement's
// stored status (most visibly `verified`) standing over zero evidence rows.
//
// This proves the fix's actual, durable property rather than simulating a
// crash: RecordHumanEvidence is now ONE store call wrapping ONE transaction,
// so there is no longer an external boundary between "clear" and "write new
// evidence and status" for a crash (or a Stop) to land in at all. It checks
// this the way that matters for a caller -- one failing precondition (an
// invalid target status) leaves EVERY part of the operation untouched,
// including the parts (the clear) that used to be a separate, already-committed
// step before the failure could even be detected.
func TestRecordHumanEvidenceIsOneTransaction(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Statement: "s", Status: domain.RequirementVerified,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "m1", Target: "1", Condition: "c1"},
			{Metric: "m2", Target: "1", Condition: "c2"},
		},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	// A prior human pass, exactly what a later call must supersede.
	if err := db.RecordHumanEvidence(ctx, ini.ID, req.ID, len(req.AcceptanceCriteria), domain.VerificationPass, "prior pass", HumanActor); err != nil {
		t.Fatal(err)
	}
	before, err := db.ListEvidenceByRequirement(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatalf("fixture precondition: expected 2 evidence rows, got %d", len(before))
	}

	// A call this store layer must refuse outright: a requirement with a
	// non-approvable status (removed). If the old N+2-transaction sequence
	// were still in play, ClearVerificationEvidence would have ALREADY
	// committed by the time this precondition could even be checked -- there
	// was no shared transaction to roll it back with. Here, one bad target
	// state must leave everything, including the clear, untouched.
	if err := db.SupersedeRequirements(ctx, ini.ID, ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	err = db.RecordHumanEvidence(ctx, ini.ID, req.ID, len(req.AcceptanceCriteria), domain.VerificationFail, "should not land", HumanActor)
	if err == nil {
		t.Fatal("RecordHumanEvidence on a removed (non-approvable) requirement should have been refused")
	}

	after, err := db.ListEvidenceByRequirement(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("a refused RecordHumanEvidence call destroyed the prior evidence anyway: %d rows remain, want 2 "+
			"(the clear and the refusal are no longer in the same transaction)", len(after))
	}
	for _, e := range after {
		if e.Status != domain.VerificationPass || e.Note != "prior pass" {
			t.Fatalf("prior evidence was mutated by a refused call: %+v", e)
		}
	}
}
