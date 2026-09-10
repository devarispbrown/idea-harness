package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// newDemoSteeredProject creates a project whose harness.yaml carries `demo`
// settings, and reloads so buildRegistry re-primes the demo provider's queues
// from them.
//
// The write has to happen after CreateProject -- project.InitConfig writes
// harness.yaml unconditionally (internal/project/project.go:45) -- and the
// reload has to happen after the write, because a project's config is cached
// for the runtime's lifetime (EnsureProject, runtime.go:119).
func newDemoSteeredProject(t *testing.T, rt *Runtime, ts *httptest.Server, token, demoYAML string) (string, *projectRuntime) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "", `{"root_path":"`+dir+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	var p map[string]string
	json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()

	f, err := os.OpenFile(filepath.Join(dir, ".idea-harness", "harness.yaml"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(demoYAML); err != nil {
		t.Fatal(err)
	}
	f.Close()

	pr, err := rt.ReloadProject(t.Context(), p["project_id"])
	if err != nil {
		t.Fatal(err)
	}
	return p["project_id"], pr
}

// TestApprovePlanAcceptsAPlanTheReviewerWillNotPass is the acceptance test for
// audit 7's P0-1: `idea approve-plan` was a retry, not an accept.
//
// Engine.ApprovePlan dispositioned the open findings and then re-ran
// runExecutionPlanReview, which runs a fresh adversarial review. A model
// reviewer is not a fixed point, so on a real provider the plan review blocked
// again, and again -- observed 11 consecutive times, with no route to
// plan_approved from any surface. `idea revise-plan` re-enters the same
// function; the web UI posts to the same endpoint.
//
// This test goes through Runtime.ApprovePlanInitiative rather than calling the
// engine directly, and that is the whole point. ApprovePlanInitiative claims
// the initiative to `running` (runtime.go:1166) BEFORE launchOperation, so the
// engine observes `running`, not `needs_human_judgment`. A fix that CASed from
// {needs_human_judgment, plan_approved} -- which is what Engine.Approve does,
// and what the first draft of this fix proposed -- would match zero rows and
// send failOperation(..., failInitiative: true) down the error path, marking
// every initiative FAILED. A workflow-level test cannot see that: that path
// never claims, so the broken CAS would match and the test would pass while
// production failed 100% of the time. TestApprovePlanDispositionsWhatItApproves
// (judgment_test.go) has exactly that blind spot.
//
// block_plan_review_cycles is 50, not 1. The queue is finite either way, but
// the distinction matters: a queue of 1 CONVERGES, and on a converging queue
// `approve-plan` succeeding is indistinguishable from the queue draining --
// which is why scripts/smoke.sh's plan-judgment pass has always passed over
// this defect and says so in its own comment. Fifty entries against a test that
// makes at most two plan_review calls cannot drain, so a pass here is
// attributable to the accept and to nothing else.
func TestApprovePlanAcceptsAPlanTheReviewerWillNotPass(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, pr := newDemoSteeredProject(t, rt, ts, token,
		"\ndemo:\n  block_plan_review_cycles: 50\n")

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives",
		token, "", `{"title":"billing","description":"usage-based billing"}`)
	var created struct {
		ID          string `json:"initiative_id"`
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("could not create an initiative")
	}
	waitForOperation(t, ts, token, created.OperationID)

	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + created.ID
	waitForOperation(t, ts, token, startOp(t, ts, token, base+"/advance"))

	got, err := pr.db.GetInitiative(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativeNeedsHumanJudgment || got.JudgmentReason != domain.JudgmentPlanReview {
		t.Fatalf("setup did not reach the plan-review park: status=%q reason=%q",
			got.Status, got.JudgmentReason)
	}

	opID, err := rt.ApprovePlanInitiative(t.Context(), projectID, created.ID)
	if err != nil {
		t.Fatalf("approve-plan was refused on the state it exists to accept: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	got, err = pr.db.GetInitiative(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	switch got.Status {
	case domain.InitiativePlanApproved:
	case domain.InitiativeFailed:
		t.Fatalf("approve-plan FAILED the initiative (%q). A CAS in Engine.ApprovePlan "+
			"cannot match: the runtime claims the initiative to `running` before "+
			"launching the operation.", got.FailureReason)
	default:
		t.Fatalf("approve-plan left the initiative at %q; a human accepted this plan "+
			"and there is no other command that can", got.Status)
	}

	findings, err := pr.db.ListFindingsByInitiative(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Disposition == "" {
			t.Errorf("approve-plan left finding %q undisposed; hasOpenBlockingFindings "+
				"will refuse `complete` forever and nothing in the CLI can clear it", f.Finding)
		}
	}
}

// startOp posts to an operation-returning endpoint and returns its id.
func startOp(t *testing.T, ts *httptest.Server, token, url string) string {
	t.Helper()
	resp := doReq(t, http.MethodPost, url, token, "", "{}")
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("%s answered %d: %s", url, resp.StatusCode, body)
	}
	var out struct {
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &out)
	if out.OperationID == "" {
		t.Fatalf("no operation_id from %s", url)
	}
	return out.OperationID
}

// TestApprovePlanRefusesAStructuralDefect and its sibling below are a pair, and
// the pair is the point.
//
// Making approve-plan a real accept removed the only thing keeping a plan with
// a task-dependency CYCLE out of Build: it used to re-run the deterministic
// checks, find the cycle again, and park again. detectCycle's finding says "the
// plan cannot be scheduled", and it is right -- nextReadyTask requires every
// depends_on predecessor to have an applied change set, which no task in a
// cycle can ever satisfy. Accepting one would answer `idea next` with "verify"
// forever, recoverable only through revise-plan, which `idea next` does not
// name from plan_approved. That is the audit-7 live-lock again in a new place.
func TestApprovePlanRefusesAStructuralDefect(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, TargetType: "initiative", TargetID: iniID,
		ReviewRunID: workflow.DeterministicPlanCheckRun,
		Severity:    domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "plan", Finding: "task dependency cycle detected: TASK-1",
		Impact: "the plan cannot be scheduled", Recommendation: "break the cycle",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.NeedsHumanJudgment(t.Context(), iniID, domain.JudgmentPlanReview, "test"); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.ApprovePlanInitiative(t.Context(), projectID, iniID); err == nil {
		t.Fatal("approve-plan accepted a plan with a dependency cycle; Build can never schedule it")
	}

	got, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativeNeedsHumanJudgment {
		t.Errorf("a refused approve-plan moved the status to %q; a precondition must "+
			"decline the request, not fail the initiative", got.Status)
	}
	findings, err := pr.db.ListFindingsByInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Disposition != "" {
			t.Errorf("a refused approve-plan dispositioned the cycle finding as %q", f.Disposition)
		}
	}
}

// TestApprovePlanAcceptsAnUntracedTask is the guard that keeps the gate above
// from widening back into a live-lock.
//
// deterministicPlanChecks emits its traceability finding at `high`, once per
// untraced task, and a task is untraced whenever the model's `requirement`
// string does not resolve to a live requirement code: reqByCode returns "" and
// createTask skips the edge with no error. That field is a bare string with no
// enum, so this is an ordinary outcome on a real model, not an exceptional one.
// A gate on "critical or high" -- which is what the first version of this fix
// proposed -- would therefore refuse approve-plan on the common case and send
// the user to revise-plan, which re-runs the same unconstrained call.
//
// It cannot be written against the demo path: demoResponse's execution plan
// emits "requirement":"REQ-1", which always resolves, so the finding never
// occurs there. The finding is seeded directly.
func TestApprovePlanAcceptsAnUntracedTask(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, TargetType: "initiative", TargetID: iniID,
		ReviewRunID: workflow.DeterministicPlanCheckRun,
		Severity:    domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "traceability", Finding: "TASK-1 has no implements edge to a requirement",
		Impact:         "untraced work cannot be verified against the PRD",
		Recommendation: "trace the task to a requirement",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.NeedsHumanJudgment(t.Context(), iniID, domain.JudgmentPlanReview, "test"); err != nil {
		t.Fatal(err)
	}

	opID, err := rt.ApprovePlanInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("approve-plan refused a plan whose only defect is an untraced task. "+
			"That is a judgment call a human may accept, and refusing it recreates "+
			"the live-lock this fix removes: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	got, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativePlanApproved {
		t.Fatalf("approve-plan left the initiative at %q", got.Status)
	}
}

// An already-approved plan is refused rather than silently re-accepted: the web
// UI renders its commit bar in every status, and after this round's change an
// accept would disposition every open finding as a human's accepted_risk with
// no review behind it.
func TestApprovePlanRefusesAnAlreadyApprovedPlan(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, TargetType: "initiative", TargetID: iniID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "plan", Finding: "f", Recommendation: "r",
	}); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	// The message, not just the refusal. Deleting the dedicated check and
	// widening the status gate back still fails the request -- the narrowed
	// ClaimInitiativeRunning refuses it -- but with "already running or not ready
	// to approve-plan", which tells the user something untrue. Asserting only
	// `err != nil` left that swap invisible.
	_, err = rt.ApprovePlanInitiative(t.Context(), projectID, iniID)
	if err == nil {
		t.Fatal("approve-plan re-accepted an already-approved plan")
	}
	if !strings.Contains(err.Error(), "already approved") {
		t.Errorf("the refusal does not say the plan is already approved: %v", err)
	}
	findings, err := pr.db.ListFindingsByInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Disposition != "" {
			t.Errorf("a refused approve-plan dispositioned %q as %q", f.Finding, f.Disposition)
		}
	}
}

// TestRevisePlanClearsTheStructuralGate closes the loop on the refusal above.
//
// The gate's message names `idea revise-plan` as the way out, and a named
// escape that does not work is how this project produced audit 7's P0-1 in the
// first place -- `approve-plan` was offered as the escape from a blocked plan
// review and could not deliver it. So the escape is asserted, not assumed.
//
// It works because RevisePlan dispositions every open finding NotApplicable
// before regenerating (advance.go), so a cycle finding cannot outlive the plan
// it described. Were that disposition removed, the stale critical would block
// approve-plan forever and the gate would become a new live-lock.
func TestRevisePlanClearsTheStructuralGate(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, TargetType: "initiative", TargetID: iniID,
		ReviewRunID: workflow.DeterministicPlanCheckRun,
		Severity:    domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "plan", Finding: "task dependency cycle detected: TASK-1",
		Impact: "the plan cannot be scheduled", Recommendation: "break the cycle",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.NeedsHumanJudgment(t.Context(), iniID, domain.JudgmentPlanReview, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ApprovePlanInitiative(t.Context(), projectID, iniID); err == nil {
		t.Fatal("setup: the structural gate did not refuse")
	}

	opID, err := rt.RevisePlanInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("revise-plan is the escape the refusal names, and it was refused: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	findings, err := pr.db.ListFindingsByInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Disposition == "" && f.ReviewRunID == workflow.DeterministicPlanCheckRun &&
			f.Severity == domain.SeverityCritical {
			t.Fatalf("revise-plan left a stale cycle finding undisposed (%q); "+
				"approve-plan would refuse forever and the gate would be a new live-lock", f.Finding)
		}
	}
}
