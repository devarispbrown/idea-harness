package projection

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// applyChangeSet advances cs through the full legal transition chain to
// "applied", the only status ComputeEvidenceTests trusts (C1, review fix).
func applyChangeSet(t *testing.T, db *store.DB, csID string) {
	t.Helper()
	ctx := context.Background()
	steps := []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	}
	for _, s := range steps {
		if err := db.TransitionChangeSet(ctx, csID, s.from, s.to, ""); err != nil {
			t.Fatalf("transition %s->%s: %v", s.from, s.to, err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, csID, "digest", ""); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
}

// TestComputeEvidenceTests_MatchByRequirementCode pins the fallback mapping
// (C2): with no verification-plan hook naming a test, a test whose name
// contains the requirement's code is still mapped -- only once the change
// set carrying it is actually applied (C1, review fix).
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
	applyChangeSet(t, db, cs.ID)

	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
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

// TestComputeEvidenceTests_WordBoundary pins the C1 fix: REQ-1's fallback
// match must not pick up REQ-10's test (the reviewer's own probe: "REQ-1
// evidence=[{REQ-10 limit works passed}]").
func TestComputeEvidenceTests_WordBoundary(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req1 := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-1", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req1); err != nil {
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
		Evidence: "ok 1 - REQ-10 limit works\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSet(t, db, cs.ID)

	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := evidence[req1.ID]; ok {
		t.Errorf("REQ-1 must not match REQ-10's test on a plain substring, got %+v", got)
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
	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
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
	applyChangeSet(t, db, cs.ID)
	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := evidence[req.ID]; ok {
		t.Error("a superseded requirement must never be mapped to test evidence")
	}
}

// TestComputeEvidenceTests_RejectedChangeSetIgnored pins C1: a rejected (or
// otherwise non-applied) change set's "tests" finding must never count as
// evidence, even when its own check exited 0 for something else recorded in
// the same finding text -- the reviewer's own probe: "a change set rejected
// with exit code 1 still made both requirements ready to confirm".
func TestComputeEvidenceTests_RejectedChangeSetIgnored(t *testing.T) {
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
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	// A "tests" finding that LOOKS like it passed (no Disposition set), but
	// the change set itself never reaches applied -- it is rejected instead
	// (e.g. a LATER check, build, denied the change set as a whole).
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass",
		Evidence: "ok 1 - REQ-1 rate limit works\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := evidence[req.ID]; ok {
		t.Errorf("a rejected change set must never contribute evidence, got %+v", got)
	}
}

// TestComputeEvidenceTests_FailedCheckNeverCountsAsPassed pins C1/C2: a
// "tests" finding whose own check exited non-zero (Disposition ==
// DispositionRejected, exactly what execute.go's checkCommand sets) must
// never be parsed for a pass, even if its change set later happens to be
// applied by some other path.
func TestComputeEvidenceTests_FailedCheckNeverCountsAsPassed(t *testing.T) {
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
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass — FAIL", Disposition: domain.DispositionRejected,
		// A forged pass line inside evidence the check itself exited non-zero
		// for -- must still never count (C1/C2).
		Evidence: "exit code 1\nok 1 - REQ-1 rate limit works\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSet(t, db, cs.ID)

	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := evidence[req.ID]; ok {
		t.Errorf("a finding from a non-zero-exit check must never contribute evidence, got %+v", got)
	}
}

// TestComputeEvidenceTests_HookNamedTestMissingCountsAsFailed pins C1: the
// verification plan names REQ-1's tests as TestA and TestB; only TestA
// appears in the (untruncated) check output. TestB must read as failed, not
// be silently dropped -- a plan naming two tests where only one is seen must
// never look like "both passed".
func TestComputeEvidenceTests_HookNamedTestMissingCountsAsFailed(t *testing.T) {
	db := openProjTestDB(t)
	blobs, err := blob.New(filepath.Join(t.TempDir(), "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
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
		Evidence: "=== RUN   TestA\n--- PASS: TestA (0.00s)\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSet(t, db, cs.ID)

	digest, err := blobs.Put([]byte("- **REQ-1** → run tests (evidence) [tests: TestA, TestB]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateArtifact(ctx,
		&domain.Artifact{InitiativeID: ini.ID, Kind: domain.ArtifactVerificationPlan},
		&domain.ArtifactVersion{BodyDigest: digest},
	); err != nil {
		t.Fatal(err)
	}

	evidence, err := ComputeEvidenceTests(ctx, db, blobs, blobs, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := evidence[req.ID]
	byName := map[string]string{}
	for _, e := range got {
		byName[e.Name] = e.Status
	}
	if byName["TestA"] != "passed" {
		t.Errorf("TestA = %q, want passed", byName["TestA"])
	}
	if byName["TestB"] != "failed" {
		t.Errorf("TestB (named by the plan, absent from the check output) = %q, want failed", byName["TestB"])
	}
	if AllTestsPassed(got) {
		t.Error("a requirement with one missing named test must never read as all-passed")
	}
}
