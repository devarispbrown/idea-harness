package daemon

import (
	"context"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// applyChangeSetDaemon drives cs through the real state machine to applied
// (C1/C3, review fix: confirm_passing_tests only trusts an applied change
// set's evidence, so tests exercising it must produce a genuinely applied
// one rather than hoping framing happens to).
func applyChangeSetDaemon(t *testing.T, pr *projectRuntime, csID string) {
	t.Helper()
	ctx := context.Background()
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := pr.db.TransitionChangeSet(ctx, csID, step.from, step.to, "test"); err != nil {
			t.Fatalf("transition %s -> %s: %v", step.from, step.to, err)
		}
	}
	if err := pr.db.MarkChangeSetApplied(ctx, csID, "digest", "test"); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateProvidersGitAndRigorFields pins the wave 3 contract additions to
// PUT/GET .../providers: git_allow_default_branch, git_branch_per_initiative
// and review_rigor round-trip, and an invalid review_rigor is a 400.
func TestUpdateProvidersGitAndRigorFields(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	// Default view: review_rigor reads "auto" even though harness.yaml never
	// wrote it (config.applyDefaults' own default).
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "", "")
	var view struct {
		GitAllowDefaultBranch  bool   `json:"git_allow_default_branch"`
		GitBranchPerInitiative bool   `json:"git_branch_per_initiative"`
		ReviewRigor            string `json:"review_rigor"`
	}
	decode(t, resp, &view)
	if view.ReviewRigor != "auto" {
		t.Fatalf("default review_rigor = %q, want auto", view.ReviewRigor)
	}
	if view.GitAllowDefaultBranch || view.GitBranchPerInitiative {
		t.Fatalf("git settings should default to false: %+v", view)
	}

	// Invalid review_rigor is refused, and refused BEFORE anything is written.
	resp = doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "",
		`{"review_rigor":"bogus"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid review_rigor: status = %d, want 400", resp.StatusCode)
	}

	// A valid write round-trips through GET.
	resp = doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "",
		`{"git_allow_default_branch":true,"git_branch_per_initiative":true,"review_rigor":"thorough"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid write: status = %d", resp.StatusCode)
	}
	resp = doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "", "")
	decode(t, resp, &view)
	if !view.GitAllowDefaultBranch || !view.GitBranchPerInitiative {
		t.Fatalf("git settings did not persist: %+v", view)
	}
	if view.ReviewRigor != "thorough" {
		t.Fatalf("review_rigor = %q, want thorough", view.ReviewRigor)
	}
}

// TestTimelineCostsQueueEndpointsAnswer is a smoke test for the three new
// wave 3 routes: they must exist, answer 200, and return the documented top-
// level shape (array for timeline/queue, object for costs) even for a
// brand-new initiative with no activity yet.
func TestTimelineCostsQueueEndpointsAnswer(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, t.TempDir())

	resp := doReq(t, http.MethodGet, base+"/initiatives/"+iniID+"/timeline", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("timeline: status = %d", resp.StatusCode)
	}
	var timeline []map[string]any
	decode(t, resp, &timeline)
	if timeline == nil {
		t.Error("timeline decoded to nil, want an array (possibly empty)")
	}

	resp = doReq(t, http.MethodGet, base+"/initiatives/"+iniID+"/timeline?since=not-a-date", token, "", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("timeline with a bad since: status = %d, want 400", resp.StatusCode)
	}

	resp = doReq(t, http.MethodGet, base+"/initiatives/"+iniID+"/costs", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("costs: status = %d", resp.StatusCode)
	}
	var costs struct {
		TotalUSD float64 `json:"total_usd"`
		ByPhase  []any   `json:"by_phase"`
		Agent    struct {
			Runs int `json:"runs"`
		} `json:"agent"`
	}
	decode(t, resp, &costs)
	if costs.ByPhase == nil {
		t.Error("by_phase decoded to nil, want an array (possibly empty)")
	}

	// Queue is project-scoped, not initiative-scoped: base is
	// ".../projects/{projectId}" (newSeededProject appends "/initiatives"
	// itself for the create call above).
	resp = doReq(t, http.MethodGet, base+"/queue", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue: status = %d", resp.StatusCode)
	}
	var queue []map[string]any
	decode(t, resp, &queue)
	if queue == nil {
		t.Error("queue decoded to nil, want an array (possibly empty)")
	}
}

// requirementWithEvidence seeds an approved-and-ready-to-verify requirement
// (acceptance criteria included, matching CreateRequirement's own
// validation) at the given status, for confirm_passing_tests eligibility
// tests below.
func requirementWithEvidence(t *testing.T, pr *projectRuntime, iniID, code string, status domain.RequirementStatus, withCriteria bool) *domain.Requirement {
	t.Helper()
	req := &domain.Requirement{
		InitiativeID: iniID, Code: code, Statement: "s", Priority: domain.PriorityMust, Status: status,
	}
	if withCriteria {
		req.AcceptanceCriteria = []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}
	}
	if err := pr.db.CreateRequirement(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return req
}

// TestVerifyConfirmPassingTestsNeverAutoConfirmsWithoutTheFlag pins the C2
// safety rule: a bare POST .../verify (no confirm_passing_tests) must never
// verify a requirement, however clean its test evidence looks -- exercised
// against a requirement that WOULD be confirmable if the flag were sent, so
// this actually proves the flag is load-bearing rather than merely that the
// endpoint accepts an empty body.
func TestVerifyConfirmPassingTestsNeverAutoConfirmsWithoutTheFlag(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	ctx := context.Background()
	pr, err := rt.EnsureProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	req := requirementWithEvidence(t, pr, iniID, "REQ-1", domain.RequirementApproved, true)
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s"}
	if err := pr.db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := pr.db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass", Evidence: "ok 1 - REQ-1 works\n",
	}); err != nil {
		t.Fatal(err)
	}
	applyChangeSetDaemon(t, pr, cs.ID)

	base := ts.URL + "/api/v1/projects/" + projectID
	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/verify", token, "", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bare verify: status = %d", resp.StatusCode)
	}
	var out map[string]any
	decode(t, resp, &out)
	if _, ok := out["status"]; !ok {
		t.Fatalf("expected a status field in the response: %+v", out)
	}
	if _, ok := out["confirmed"]; ok {
		t.Fatalf("a bare verify must never report a confirmed list: %+v", out)
	}
	got, err := pr.db.GetRequirementByRef(ctx, iniID, req.Code)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.RequirementVerified {
		t.Fatal("REQ-1 was verified by a call that never set confirm_passing_tests")
	}
}

// TestConfirmPassingTestsEligibilityAndReporting is the end-to-end case the
// review's "tests that prove little" note asked for: confirm_passing_tests
// actually confirms a genuinely eligible requirement, reports each
// ineligible one by name and reason (C3/H2/H4), and never touches a
// requirement outside the `approved` state -- not a human's already-recorded
// pass/fail/waive, not a draft, not one with no acceptance criteria at all.
func TestConfirmPassingTestsEligibilityAndReporting(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	ctx := context.Background()
	pr, err := rt.EnsureProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}

	passing := requirementWithEvidence(t, pr, iniID, "REQ-1", domain.RequirementApproved, true)
	failing := requirementWithEvidence(t, pr, iniID, "REQ-2", domain.RequirementApproved, true)
	noCriteria := requirementWithEvidence(t, pr, iniID, "REQ-3", domain.RequirementApproved, false)
	alreadyVerified := requirementWithEvidence(t, pr, iniID, "REQ-4", domain.RequirementVerified, true)
	alreadyFailed := requirementWithEvidence(t, pr, iniID, "REQ-5", domain.RequirementFailed, true)
	waived := requirementWithEvidence(t, pr, iniID, "REQ-6", domain.RequirementWaived, true)
	draft := requirementWithEvidence(t, pr, iniID, "REQ-7", domain.RequirementDraft, true)

	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s"}
	if err := pr.db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := pr.db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass",
		Evidence: "ok 1 - REQ-1 works\nnot ok 2 - REQ-2 broken\n",
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

	if len(out.Confirmed) != 1 || out.Confirmed[0] != passing.Code {
		t.Fatalf("confirmed = %+v, want exactly [%s]", out.Confirmed, passing.Code)
	}
	skippedReasons := map[string]string{}
	for _, s := range out.Skipped {
		skippedReasons[s.Requirement] = s.Reason
	}
	if _, ok := skippedReasons[failing.Code]; !ok {
		t.Errorf("REQ-2 (a failing mapped test) must be reported as skipped: %+v", out.Skipped)
	}
	if _, ok := skippedReasons[noCriteria.Code]; !ok {
		t.Errorf("REQ-3 (no acceptance criteria) must be reported as skipped: %+v", out.Skipped)
	}
	for _, code := range []string{alreadyVerified.Code, alreadyFailed.Code, waived.Code, draft.Code} {
		if _, ok := skippedReasons[code]; ok {
			t.Errorf("%s is not `approved` and must not even be reported as a skip candidate: %+v", code, out.Skipped)
		}
	}

	// The database, not just the response, must show it: REQ-1 verified, and
	// every other requirement's status untouched -- in particular, a human's
	// prior fail/waive judgment on REQ-5/REQ-6 must survive byte for byte
	// (C3: "Confirm overwrites a human's explicit fail or waive").
	for _, want := range []*domain.Requirement{
		{Code: passing.Code, Status: domain.RequirementVerified},
		{Code: failing.Code, Status: domain.RequirementApproved},
		{Code: noCriteria.Code, Status: domain.RequirementApproved},
		{Code: alreadyVerified.Code, Status: domain.RequirementVerified},
		{Code: alreadyFailed.Code, Status: domain.RequirementFailed},
		{Code: waived.Code, Status: domain.RequirementWaived},
		{Code: draft.Code, Status: domain.RequirementDraft},
	} {
		got, err := pr.db.GetRequirementByRef(ctx, iniID, want.Code)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != want.Status {
			t.Errorf("%s status = %q, want %q", want.Code, got.Status, want.Status)
		}
	}
}

// TestRetryAcceptsNoteWithoutModel pins M4, review fix: `{"note":"x"}` with
// no `model` field must be accepted (falling back to the initiative's
// configured default model), not rejected with 400 "model is required" --
// the endpoint's own doc comment already promised this leniency, matching
// /execute's `fresh`/`note` decode, but the code enforced the opposite.
func TestRetryAcceptsNoteWithoutModel(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	setStatus(t, rt, projectID, iniID, domain.InitiativeFailed)

	base := ts.URL + "/api/v1/projects/" + projectID
	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/retry", token, "", `{"note":"try again with more context"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry with note, no model: status = %d, body = %s", resp.StatusCode, readError(t, resp))
	}
}

// TestConfirmPassingTestsRequiresAnAppliedChangeSet pins C3's explicit
// precondition: with no applied change set at all, confirm_passing_tests
// confirms nothing and says why, rather than reading a stray finding as
// evidence for code that never landed.
func TestConfirmPassingTestsRequiresAnAppliedChangeSet(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	ctx := context.Background()
	pr, err := rt.EnsureProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	req := requirementWithEvidence(t, pr, iniID, "REQ-1", domain.RequirementApproved, true)

	base := ts.URL + "/api/v1/projects/" + projectID
	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/verify", token, "", `{"confirm_passing_tests":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readError(t, resp))
	}
	var out struct {
		Confirmed []string `json:"confirmed"`
		Skipped   []struct {
			Requirement string `json:"requirement"`
			Reason      string `json:"reason"`
		} `json:"skipped"`
	}
	decode(t, resp, &out)
	if len(out.Confirmed) != 0 {
		t.Fatalf("confirmed = %+v, want none (no applied change set exists)", out.Confirmed)
	}
	found := false
	for _, s := range out.Skipped {
		if s.Requirement == req.Code {
			found = true
		}
	}
	if !found {
		t.Errorf("REQ-1 must be reported as skipped when there is no applied change set: %+v", out.Skipped)
	}
}
