package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
)

// putPatch stores fc's content in blobs and writes a one-file patch manifest
// naming it, returning the patch's own digest (for ChangeSet.PatchDigest).
func putPatch(t *testing.T, blobs *blob.Store, path, content string) string {
	t.Helper()
	digest, err := blobs.Put([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	patch := fsdiff.Patch{Files: []domain.FileChange{
		{Path: path, Digest: digest, ChangeType: domain.FileAdded},
	}}
	data, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	patchDigest, err := blobs.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	return patchDigest
}

// TestComputeEvidenceTests_TaskDerivedFromChangeSetDiff reproduces the ICP
// re-run's own defect #3, bullet (b): a real test's function name almost
// never contains the requirement's code ("TestRateLimiterAllowsWithinBudget"
// has no "REQ-5" in it anywhere), so neither the verification plan's hooks
// nor the name-substring fallback ever map it -- 11 requirements, 15 passing
// tests, `evidence_tests` null on all of them. The fix: a task→requirement
// `implements` edge plus that task's own applied, check-passed change set's
// test files map the requirement to the tests it actually shipped with.
func TestComputeEvidenceTests_TaskDerivedFromChangeSetDiff(t *testing.T) {
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
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-5", Statement: "API-key auth with per-key rate limiting", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	// The task IMPLEMENTS REQ-5 (the task->requirement edge); its own test
	// function names never mention "REQ-5" anywhere, as in the reported run.
	task := &domain.Task{InitiativeID: ini.ID, Title: "Add per-key rate limiting", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}

	patchDigest := putPatch(t, blobs, "internal/ratelimit/limiter_test.go", `package ratelimit

func TestRateLimiterAllowsWithinBudget(t *testing.T) {}
func TestRateLimiterRejectsOverBudget(t *testing.T) {}
`)
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: patchDigest, HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass",
		Evidence: "=== RUN   TestRateLimiterAllowsWithinBudget\n--- PASS: TestRateLimiterAllowsWithinBudget (0.00s)\n" +
			"=== RUN   TestRateLimiterRejectsOverBudget\n--- PASS: TestRateLimiterRejectsOverBudget (0.00s)\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSet(t, db, cs.ID)

	evidence, err := ComputeEvidenceTests(ctx, db, blobs, blobs, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := evidence[req.ID]
	if len(got) != 2 {
		t.Fatalf("got %d evidence entries, want 2 (task-derived from the change set's own test file): %+v", len(got), got)
	}
	byName := map[string]string{}
	for _, e := range got {
		byName[e.Name] = e.Status
	}
	for _, name := range []string{"TestRateLimiterAllowsWithinBudget", "TestRateLimiterRejectsOverBudget"} {
		if byName[name] != "passed" {
			t.Errorf("%s status = %q, want passed", name, byName[name])
		}
	}
}

// TestComputeEvidenceTests_TaskDerivedRequiresCheckPassed pins the "only when
// the check run passed and the change set is applied" gate (ICP re-run defect
// #3 bullet (b)): a change set whose OWN tests check failed must not
// contribute task-derived evidence, even though its task implements the
// requirement and its diff carries a test file.
func TestComputeEvidenceTests_TaskDerivedRequiresCheckPassed(t *testing.T) {
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
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-9", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}
	patchDigest := putPatch(t, blobs, "pkg_test.go", "package pkg\n\nfunc TestSomething(t *testing.T) {}\n")
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: patchDigest, HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	// The check itself failed (non-zero exit): Disposition = rejected.
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests fail", Disposition: domain.DispositionRejected,
		Evidence: "exit code 1\n=== RUN   TestSomething\n--- FAIL: TestSomething (0.00s)\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSet(t, db, cs.ID)

	evidence, err := ComputeEvidenceTests(ctx, db, blobs, blobs, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := evidence[req.ID]; ok {
		t.Errorf("a failed check's change set must not contribute task-derived evidence, got %+v", got)
	}
}

// TestComputeEvidenceTests_UnmappedRequirementYieldsEmptyEvidence documents
// the precondition the daemon-level skip-reason fix relies on (ICP re-run
// defect #3 bullet (a), tested end to end in
// internal/daemon/confirm_skip_reason_test.go): a requirement with no plan
// hook, no test-name match, and no implementing task gets NO evidence at all
// -- an empty slice, not entries for a failing subset -- so the caller can
// tell "nothing is mapped" apart from "something failed".
func TestComputeEvidenceTests_UnmappedRequirementYieldsEmptyEvidence(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-3", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	evidence, err := ComputeEvidenceTests(ctx, db, nil, nil, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence[req.ID]) != 0 {
		t.Fatalf("setup: expected no evidence at all for %s, got %+v", req.Code, evidence[req.ID])
	}
}
