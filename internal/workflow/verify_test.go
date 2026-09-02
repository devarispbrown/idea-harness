package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func newVerifyFixture(t *testing.T) (*Engine, *store.DB, string) {
	t.Helper()
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID
}

func TestVerifyInitiativeGate(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID,
		Statement:    "Be fast",
		Priority:     domain.PriorityMust,
		Status:       domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "latency", Target: "< 200ms", Condition: "reference workload"},
		},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// No evidence yet: the gate must not complete the initiative.
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeComplete {
		t.Fatal("initiative must not be complete without evidence")
	}

	// Human pass clears the criterion, then the gate verifies + completes.
	if err := eng.RecordHumanPass(ctx, iniID, req.Code, "manual check", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}

	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementVerified {
		t.Fatalf("requirement status = %q, want verified", reqs[0].Status)
	}
	ini, err = db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("initiative status = %q, want complete", ini.Status)
	}
}

func TestPartialCoverageNotVerified(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID,
		Statement:    "Be fast and cheap",
		Priority:     domain.PriorityMust,
		Status:       domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "latency", Target: "< 200ms", Condition: "reference workload"},
			{Metric: "cost", Target: "< $1", Condition: "per 1k requests"},
		},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Pass only the first criterion directly (not via RecordHumanPass, which
	// passes all).
	if err := db.CreateVerificationEvidence(ctx, &domain.VerificationEvidence{
		InitiativeID:  iniID,
		RequirementID: req.ID,
		CriterionKey:  domain.CriterionKey(req.ID, 0),
		Kind:          domain.VerificationAcceptance,
		Status:        domain.VerificationPass,
		VerifiedBy:    "human:tester",
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementApproved {
		t.Fatalf("requirement status = %q, want still approved (partial coverage)", reqs[0].Status)
	}
}

func TestRecordAutomatedEvidenceOnApply(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}

	if err := eng.recordAutomatedEvidence(ctx, cs); err != nil {
		t.Fatal(err)
	}
	evs, err := db.ListEvidenceByRequirement(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(evs))
	}
	if evs[0].Status != domain.VerificationNeedsManual {
		t.Fatalf("status = %q, want needs_manual", evs[0].Status)
	}
	if evs[0].VerifiedBy != "verification_engineer" {
		t.Fatalf("verified_by = %q, want verification_engineer", evs[0].VerifiedBy)
	}
}

func TestCompletionSkipsRemovedRequirements(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req1 := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req1); err != nil {
		t.Fatal(err)
	}
	// A superseded requirement can never reach verified; the gate must skip it.
	req2 := &domain.Requirement{
		InitiativeID: iniID, Statement: "Old", Priority: domain.PriorityMust, Status: domain.RequirementRemoved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req2); err != nil {
		t.Fatal(err)
	}

	if err := eng.RecordHumanPass(ctx, iniID, req1.Code, "", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("expected complete (removed skipped), got %q", ini.Status)
	}
}
