package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
)

// TestConfirmPassingTestsMapsTaskDerivedEvidenceAcrossTwoStores is the
// end-to-end reproduction the ICP re-run itself hit (defect B): a founder-
// journey run reported evidence_tests null on all 4 requirements although 30
// tests passed on an applied change set implementing them. Root cause:
// ComputeEvidenceTests' task-derived tier (taskDerivedTests/decodePatch) was
// resolved against pr.blobs (the redacted store for model artifacts), but a
// change set's patch manifest and file content are ONLY ever written to
// pr.rawBlobs (the redaction-free store; see workflow.Engine.contentBlobs) --
// so decodePatch's Get always missed, silently, and no requirement was ever
// mapped by tier 3.
//
// Every earlier test for this tier (task_derived_tests_test.go) used a
// single blob.Store for both roles, which cannot catch a "wrong store"
// regression because there is only one store to pass. This test uses the
// real daemon (rt.EnsureProject), which is the only thing that actually
// constructs two independent stores the way production does, and drives the
// full path this bug was reported against: an applied change set with
// passing go tests (real go test -json evidence, WITH checkCommand's own
// "image: ..." preamble -- see evidence_test.go's sibling fix for why that
// preamble matters too) in a file it added for a task implementing REQ-1 →
// REQ-1's evidence_tests populated via GET .../requirements →
// confirm_passing_tests actually confirms it.
func TestConfirmPassingTestsMapsTaskDerivedEvidenceAcrossTwoStores(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	ctx := context.Background()
	pr, err := rt.EnsureProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.blobs == pr.rawBlobs {
		t.Fatal("setup: test requires two distinct stores, matching production (projectRuntime.blobs vs .rawBlobs)")
	}

	req := requirementWithEvidence(t, pr, iniID, "REQ-1", domain.RequirementApproved, true)
	task := &domain.Task{InitiativeID: iniID, Title: "Add per-key rate limiting", Statement: "s"}
	if err := pr.db.CreateTaskMulti(ctx, task, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}

	// The test file's content and the patch manifest naming it are written to
	// pr.rawBlobs -- exactly where the real apply pipeline puts them
	// (workflow.Engine.contentBlobs returns e.rawBlobs), never pr.blobs.
	const testFile = `package ratelimit

func TestRateLimiterAllowsWithinBudget(t *testing.T) {}
func TestRateLimiterRejectsOverBudget(t *testing.T) {}
`
	fileDigest, err := pr.rawBlobs.Put([]byte(testFile))
	if err != nil {
		t.Fatal(err)
	}
	patch := fsdiff.Patch{Files: []domain.FileChange{
		{Path: "internal/ratelimit/limiter_test.go", Digest: fileDigest, ChangeType: domain.FileAdded},
	}}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	patchDigest, err := pr.rawBlobs.Put(patchBytes)
	if err != nil {
		t.Fatal(err)
	}

	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: patchDigest, HumanID: "CHG-1"}
	if err := pr.db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	// Real go test -json evidence, including checkCommand's own imagePrefix
	// preamble (execute.go): a "tests" finding's Evidence is never a bare
	// JSON stream in production.
	evidence := "image: golang:1.22 (check.image not configured; derived golang:1.22 (go.mod))\n" +
		"test_cmd not configured; detected `go test ./...` from go.mod (set test_cmd in harness.yaml to override)\n" +
		`{"Action":"run","Package":"internal/ratelimit","Test":"TestRateLimiterAllowsWithinBudget"}` + "\n" +
		`{"Action":"pass","Package":"internal/ratelimit","Test":"TestRateLimiterAllowsWithinBudget"}` + "\n" +
		`{"Action":"run","Package":"internal/ratelimit","Test":"TestRateLimiterRejectsOverBudget"}` + "\n" +
		`{"Action":"pass","Package":"internal/ratelimit","Test":"TestRateLimiterRejectsOverBudget"}` + "\n"
	if err := pr.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass", Evidence: evidence,
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSetDaemon(t, pr, cs.ID)

	base := ts.URL + "/api/v1/projects/" + projectID

	// evidence_tests must be populated, never null, on the requirements list.
	resp := doReq(t, http.MethodGet, base+"/initiatives/"+iniID+"/requirements", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list requirements: status = %d", resp.StatusCode)
	}
	var reqs []struct {
		ID            string `json:"id"`
		Code          string `json:"code"`
		EvidenceTests []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"evidence_tests"`
	}
	decode(t, resp, &reqs)
	var got *struct {
		ID            string `json:"id"`
		Code          string `json:"code"`
		EvidenceTests []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"evidence_tests"`
	}
	for i := range reqs {
		if reqs[i].Code == "REQ-1" {
			got = &reqs[i]
		}
	}
	if got == nil {
		t.Fatalf("REQ-1 missing from requirements list: %+v", reqs)
	}
	if len(got.EvidenceTests) != 2 {
		t.Fatalf("REQ-1 evidence_tests = %+v, want 2 task-derived tests (bug: null/empty when the patch store is wrong or the JSON stream isn't detected)", got.EvidenceTests)
	}
	for _, e := range got.EvidenceTests {
		if e.Status != "passed" {
			t.Errorf("evidence test %s status = %q, want passed", e.Name, e.Status)
		}
	}

	// confirm_passing_tests must actually confirm REQ-1 end to end.
	resp = doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/verify", token, "", `{"confirm_passing_tests":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify with confirm_passing_tests: status = %d, body = %s", resp.StatusCode, readError(t, resp))
	}
	var out struct {
		Confirmed []string `json:"confirmed"`
		Skipped   []struct {
			Requirement string `json:"requirement"`
			Reason      string `json:"reason"`
		} `json:"skipped"`
	}
	decode(t, resp, &out)
	confirmed := false
	for _, c := range out.Confirmed {
		if c == "REQ-1" {
			confirmed = true
		}
	}
	if !confirmed {
		t.Fatalf("REQ-1 was not confirmed: %+v", out)
	}
	fresh, err := pr.db.GetRequirementByRef(ctx, iniID, "REQ-1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != domain.RequirementVerified {
		t.Fatalf("REQ-1 status = %s, want verified", fresh.Status)
	}
}
