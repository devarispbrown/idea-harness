package daemon

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestConfirmPassingTestsSkipReasonNamesUnmappedRequirement reproduces the
// ICP re-run's own defect #3, bullet (a): a founder-journey run reported
// evidence_tests null on all 11 requirements even though its checks ran
// `go test ./...` and passed 15 tests, and confirm_passing_tests skipped
// every one of them with "not every mapped test passed" -- wrong, since
// nothing was mapped to any of them at all. The skip reason must say THAT,
// and name the requirement code, instead of reading as "some test failed".
func TestConfirmPassingTestsSkipReasonNamesUnmappedRequirement(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	ctx := context.Background()
	pr, err := rt.EnsureProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}

	// Approved, has acceptance criteria, so it IS a confirm_passing_tests
	// candidate -- but nothing (no plan hook, no test-name substring, no
	// implementing task) ever maps a test to it.
	unmapped := requirementWithEvidence(t, pr, iniID, "REQ-9", domain.RequirementApproved, true)

	task := &domain.Task{InitiativeID: iniID, Title: "unrelated task", Statement: "s"}
	if err := pr.db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := pr.db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	// 15 tests ran and passed, none of them naming REQ-9 in any way.
	if err := pr.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass",
		Evidence: "=== RUN   TestSomethingElse\n--- PASS: TestSomethingElse (0.00s)\n" +
			"=== RUN   TestAnotherThing\n--- PASS: TestAnotherThing (0.00s)\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSetDaemon(t, pr, cs.ID)

	base := ts.URL + "/api/v1/projects/" + projectID
	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/verify", token, "", `{"confirm_passing_tests":true}`)
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

	var reason string
	found := false
	for _, s := range out.Skipped {
		if s.Requirement == unmapped.Code {
			found = true
			reason = s.Reason
		}
	}
	if !found {
		t.Fatalf("expected %s to be reported as skipped: %+v", unmapped.Code, out.Skipped)
	}
	if reason == "not every mapped test passed" {
		t.Errorf("skip reason for an UNMAPPED requirement must not read as if a mapped test failed, got %q", reason)
	}
	if !strings.Contains(reason, "no tests are mapped") || !strings.Contains(reason, unmapped.Code) {
		t.Errorf("skip reason = %q, want it to say no tests are mapped and name %s", reason, unmapped.Code)
	}
}
