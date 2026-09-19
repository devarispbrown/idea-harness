package projection

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestComputeEvidenceTests_MatchByRequirementCode pins the fallback mapping
// (C2): with no verification-plan hook naming a test, a test whose name
// contains the requirement's code is still mapped.
func TestComputeEvidenceTests_MatchByRequirementCode(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-1", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass",
		Evidence: "ok 1 - REQ-1 rate limit works\n",
	}); err != nil {
		t.Fatal(err)
	}

	evidence, err := ComputeEvidenceTests(ctx, db, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := evidence[req.ID]
	if len(got) != 1 {
		t.Fatalf("got %d evidence entries, want 1: %+v", len(got), got)
	}
	if got[0].Status != "passed" {
		t.Errorf("status = %q, want passed", got[0].Status)
	}
	if got[0].ChangeSet != cs.HumanID {
		t.Errorf("change_set = %q, want %q", got[0].ChangeSet, cs.HumanID)
	}
}

// TestComputeEvidenceTests_NoMappedTests: a requirement whose code appears in
// no test name and has no verification hook gets no evidence_tests entry at
// all (not an empty slice masquerading as "confirmed nothing").
func TestComputeEvidenceTests_NoMappedTests(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-9", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	evidence, err := ComputeEvidenceTests(ctx, db, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := evidence[req.ID]; ok {
		t.Errorf("expected no entry for a requirement with no mapped tests, got %+v", evidence[req.ID])
	}
}

// TestComputeEvidenceTests_SupersededExcluded: a superseded requirement is
// never mapped, even if its old code still appears in a test name.
func TestComputeEvidenceTests_SupersededExcluded(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-1", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeRequirements(ctx, ini.ID, "test"); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass",
		Evidence: "ok 1 - REQ-1 rate limit works\n",
	}); err != nil {
		t.Fatal(err)
	}
	evidence, err := ComputeEvidenceTests(ctx, db, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := evidence[req.ID]; ok {
		t.Error("a superseded requirement must never be mapped to test evidence")
	}
}
