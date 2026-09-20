package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestUpdateRequirementResetsVerificationOnSubjectChange is review C5:
// ReviseInitiative matches requirements by code and blind-UpdateRequirement'd
// statement/acceptance_criteria, leaving status, verified_at and every
// verification_evidence row untouched -- even though revise is claimable
// from `complete`. A requirement could therefore report verified, with
// surviving human evidence, for a statement that no longer exists.
//
// Mutation-check: drop the reopenVerification branch (always take the
// "unchanged" UPDATE), and this fails -- status stays "verified",
// verified_at stays set, and the pass evidence survives.
func TestUpdateRequirementResetsVerificationOnSubjectChange(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Code: "REQ-1", Statement: "Retain audit logs for 90 days",
		Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "retention", Target: "90d", Condition: "audit log store"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Human pass, then verified -- exactly the state a completed initiative
	// leaves a requirement in.
	ev := &domain.VerificationEvidence{
		InitiativeID: ini.ID, RequirementID: req.ID, CriterionKey: domain.CriterionKey(req.ID, 0),
		Kind: domain.VerificationAcceptance, Status: domain.VerificationPass, VerifiedBy: HumanActor, Note: "looks good",
	}
	if err := db.CreateVerificationEvidence(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRequirementVerified(ctx, req.ID, HumanActor); err != nil {
		t.Fatal(err)
	}

	// A PRD revision re-points the SAME code (REQ-1) at a different subject.
	revised := &domain.Requirement{
		ID: req.ID, InitiativeID: ini.ID, Code: req.Code,
		Statement: "Retain audit logs for 365 days", // subject changed
		Priority:  domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "retention", Target: "365d", Condition: "audit log store"},
		},
	}
	if err := db.UpdateRequirement(ctx, revised); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetRequirementByRef(ctx, ini.ID, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RequirementApproved {
		t.Fatalf("status after subject-changing revise = %q, want approved (verification must be re-earned)", got.Status)
	}
	if got.VerifiedAt != nil {
		t.Fatalf("verified_at survived a subject-changing revise: %v", got.VerifiedAt)
	}
	passed, err := db.PassedCriterionKeys(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) != 0 {
		t.Fatalf("pass evidence for the OLD subject survived a subject-changing revise: %v", passed)
	}
}

// TestUpdateRequirementPreservesVerificationWhenSubjectUnchanged is the other
// half of C5's fix: a revision that does not touch statement or acceptance
// criteria (e.g. only priority moves) must not reopen a requirement that is
// already verified.
func TestUpdateRequirementPreservesVerificationWhenSubjectUnchanged(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	acs := []domain.AcceptanceCriterion{{Metric: "retention", Target: "90d", Condition: "audit log store"}}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Code: "REQ-1", Statement: "Retain audit logs for 90 days",
		Priority: domain.PriorityMust, Status: domain.RequirementApproved, AcceptanceCriteria: acs,
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	ev := &domain.VerificationEvidence{
		InitiativeID: ini.ID, RequirementID: req.ID, CriterionKey: domain.CriterionKey(req.ID, 0),
		Kind: domain.VerificationAcceptance, Status: domain.VerificationPass, VerifiedBy: HumanActor,
	}
	if err := db.CreateVerificationEvidence(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRequirementVerified(ctx, req.ID, HumanActor); err != nil {
		t.Fatal(err)
	}

	revised := &domain.Requirement{
		ID: req.ID, InitiativeID: ini.ID, Code: req.Code,
		Statement: req.Statement, Priority: domain.PriorityShould, AcceptanceCriteria: acs, // priority only
	}
	if err := db.UpdateRequirement(ctx, revised); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetRequirementByRef(ctx, ini.ID, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RequirementVerified {
		t.Fatalf("status after a priority-only revise = %q, want verified (subject unchanged)", got.Status)
	}
	if got.VerifiedAt == nil {
		t.Fatal("verified_at cleared by a revise that did not change the subject")
	}
	passed, err := db.PassedCriterionKeys(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) == 0 {
		t.Fatal("pass evidence cleared by a revise that did not change the subject")
	}
}

// TestUpdateRequirementLeavesDraftRequirementDraft: a still-draft requirement
// revised mid-PRD-review (before it has ever been approved) must not jump to
// approved just because UpdateRequirement's subject-changed branch fired --
// that would let a mid-review revision skip the review gate entirely.
func TestUpdateRequirementLeavesDraftRequirementDraft(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Code: "REQ-1", Statement: "Be fast",
		Priority: domain.PriorityMust, Status: domain.RequirementDraft,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "200ms", Condition: "p95"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	revised := &domain.Requirement{
		ID: req.ID, InitiativeID: ini.ID, Code: req.Code,
		Statement: "Be very fast", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "100ms", Condition: "p95"}},
	}
	if err := db.UpdateRequirement(ctx, revised); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetRequirementByRef(ctx, ini.ID, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RequirementDraft {
		t.Fatalf("status after revising a draft requirement = %q, want draft", got.Status)
	}
	if got.Statement != "Be very fast" {
		t.Fatalf("statement not updated: %q", got.Statement)
	}
}
