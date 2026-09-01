package workflow

import (
	"context"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/store"
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
