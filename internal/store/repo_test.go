package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestRepoRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "Usage-based billing", Description: "build it"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if ini.ID == "" {
		t.Fatal("expected initiative id to be assigned")
	}

	a := &domain.Assertion{InitiativeID: ini.ID, Type: domain.AssertionFact, Status: domain.AssertionAccepted,
		Statement: "Users need usage-based billing", Confidence: domain.ConfidenceHigh, Source: "interview"}
	if err := db.CreateAssertion(ctx, a); err != nil {
		t.Fatal(err)
	}

	r1 := &domain.Requirement{InitiativeID: ini.ID, Statement: "Usage events are metered",
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "events", Target: ">= 1", Condition: "per request"}},
		Priority:           domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r1); err != nil {
		t.Fatal(err)
	}
	if r1.Code != "REQ-1" {
		t.Fatalf("expected REQ-1, got %q", r1.Code)
	}
	r2 := &domain.Requirement{InitiativeID: ini.ID, Statement: "Invoices are generated"}
	if err := db.CreateRequirement(ctx, r2); err != nil {
		t.Fatal(err)
	}
	if r2.Code != "REQ-2" {
		t.Fatalf("expected REQ-2, got %q", r2.Code)
	}

	d := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "Should sharing links expire?", Options: []string{"30 days", "never"}, Recommendation: "30 days default"}
	if err := db.CreateDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	if d.Code != "DEC-1" {
		t.Fatalf("expected DEC-1, got %q", d.Code)
	}
	if err := db.CreateDissent(ctx, &domain.DecisionDissent{DecisionID: d.ID, ProponentRole: "security", Position: "default expiration"}); err != nil {
		t.Fatal(err)
	}

	q := &domain.Question{InitiativeID: ini.ID, Question: "Conflict resolution semantics?"}
	if err := db.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	// Read back.
	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil || len(reqs) != 2 {
		t.Fatalf("expected 2 requirements, got %d (err=%v)", len(reqs), err)
	}
	decisions, err := db.ListDecisions(ctx, ini.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d (err=%v)", len(decisions), err)
	}
	questions, err := db.ListQuestions(ctx, ini.ID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("expected 1 question, got %d (err=%v)", len(questions), err)
	}
}

func TestDecisionResolveAndImmutability(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	d := &domain.Decision{InitiativeID: ini.ID, Question: "q"}
	if err := db.CreateDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, d.ID, "30 days", HumanActor); err != nil {
		t.Fatal(err)
	}
	// Resolving again must fail (immutable once decided).
	if err := db.ResolveDecision(ctx, d.ID, "never", HumanActor); err == nil {
		t.Fatal("expected second resolution to fail")
	}
	// Reopen undoes the resolution; it can then be resolved again.
	if err := db.ReopenDecision(ctx, d.ID, HumanActor); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListDecisions(ctx, ini.ID)
	if err != nil || len(got) != 1 || got[0].Status != domain.DecisionOpen || got[0].Resolution != "" {
		t.Fatalf("reopen did not clear decision: %+v (err=%v)", got, err)
	}
	if err := db.ResolveDecision(ctx, d.ID, "90 days", HumanActor); err != nil {
		t.Fatalf("re-resolve after reopen: %v", err)
	}
	// Reopening an already-open decision must fail.
	if err := db.ReopenDecision(ctx, d.ID, HumanActor); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, d.ID, HumanActor); err == nil {
		t.Fatal("expected reopening an open decision to fail")
	}
}

func TestFindingDispositionAndAcceptedRisk(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	runID, err := db.CreateRun(ctx, domain.RunReview, "", "adversarial_reviewer")
	if err != nil {
		t.Fatal(err)
	}
	f := &domain.Finding{ReviewRunID: runID, TargetType: "prd", TargetID: "x",
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: "scope", Finding: "unclear"}
	if err := db.CreateFinding(ctx, f); err != nil {
		t.Fatal(err)
	}
	// accepted_risk by a non-human must be rejected.
	if err := db.SetFindingDisposition(ctx, f.ID, domain.DispositionAcceptedRisk, ActorOrchestrator); err == nil {
		t.Fatal("expected accepted_risk by non-human to be rejected")
	}
	if err := db.SetFindingDisposition(ctx, f.ID, domain.DispositionAcceptedRisk, HumanActor); err != nil {
		t.Fatal(err)
	}
}
