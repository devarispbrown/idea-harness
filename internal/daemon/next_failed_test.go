package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestNextReportsAFailedInitiative covers the audit-5/audit-6 A3 defect at the
// wire: GET /next collapsed running and failed to "wait", so the CLI told a
// failed initiative the workflow was still running and exited 0.
//
// FailInitiative rather than setStatus, deliberately. SetInitiativeStatus is the
// one transition that does not write failure_reason (store/repo.go:195), so
// seeding through setStatus would produce an empty reason and quietly weaken the
// assertion this test exists to make.
func TestNextReportsAFailedInitiative(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	const reason = "framing: openai: status 401: invalid api key"
	if err := pr.db.FailInitiative(t.Context(), iniID, reason); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Action        string `json:"action"`
		FailureReason string `json:"failure_reason"`
	}
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/next", token, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET next: %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Action != "failed" {
		t.Errorf("action = %q, want failed", out.Action)
	}
	// The reason is the whole point: "it failed" without it sends the reader to
	// `idea logs` for something the daemon already has in hand.
	if out.FailureReason != reason {
		t.Errorf("failure_reason = %q, want %q", out.FailureReason, reason)
	}

	// A running initiative must still answer wait, and must not carry a reason.
	setStatus(t, rt, projectID, iniID, domain.InitiativeRunning)
	resp2 := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/next", token, "", "")
	defer resp2.Body.Close()
	out.Action, out.FailureReason = "", ""
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Action != "wait" {
		t.Errorf("running action = %q, want wait", out.Action)
	}
	if out.FailureReason != "" {
		t.Errorf("running carried failure_reason %q, want none", out.FailureReason)
	}
}

// TestReconcileRecordsWhyItFailed covers the other half of A3: a token that says
// "failed" is only useful if something recorded why.
//
// reconcileStaleOperations failed the initiative through SetInitiativeStatus,
// which is the one transition that deliberately leaves failure_reason alone
// (store/repo.go:195). So every initiative failed by a daemon restart
// mid-operation -- the most common way one fails without an explicit
// FailInitiative -- ended up `failed` with an empty reason, and `idea status`
// reported a failure it could not explain. releaseOrphanedClaims a few lines
// below already used FailInitiative; this loop did not.
func TestReconcileRecordsWhyItFailed(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	// An operation left unfinished, exactly as a daemon killed mid-run leaves it.
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	if err := pr.db.ClaimInitiativeRunning(ctx, iniID, domain.InitiativePlanApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.db.CreateOperation(ctx, projectID, iniID, "execute_initiative"); err != nil {
		t.Fatal(err)
	}

	rt.reconcileStaleOperations(ctx, pr)

	ini, err := pr.db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeFailed {
		t.Fatalf("status = %q, want failed", ini.Status)
	}
	if ini.FailureReason == "" {
		t.Fatal("the initiative failed with no recorded reason; `idea next` and `idea status` have nothing to name")
	}
	// Not just non-empty: the reason has to name the cause, or the assertion
	// above passes for any placeholder string.
	if !strings.Contains(ini.FailureReason, "daemon restarted") {
		t.Errorf("failure reason %q does not name the cause", ini.FailureReason)
	}
}

// TestReconcileLeavesANonRunningInitiativeAlone is the guard on the write above.
//
// Not every operation claims its initiative: SuggestAnswersInitiative creates a
// row and runs against a `failed` or `complete` initiative without claiming it.
// Failing unconditionally therefore reached initiatives that were never running
// -- and because the write now carries a reason, it would overwrite a real one.
// An initiative that failed on a provider 401 and picked up a stale
// suggest_answers row would come back from a restart blaming the restart, and
// `idea next` would report that as the cause.
func TestReconcileLeavesANonRunningInitiativeAlone(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	const realReason = "framing: openai: status 401: invalid api key"
	if err := pr.db.FailInitiative(ctx, iniID, realReason); err != nil {
		t.Fatal(err)
	}
	// An operation that never claimed the initiative, left unfinished.
	if _, err := pr.db.CreateOperation(ctx, projectID, iniID, "suggest_answers"); err != nil {
		t.Fatal(err)
	}

	rt.reconcileStaleOperations(ctx, pr)

	ini, err := pr.db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.FailureReason != realReason {
		t.Fatalf("reconcile overwrote a real failure reason:\n got  %q\n want %q", ini.FailureReason, realReason)
	}

	// And a complete initiative must not be dragged to failed.
	setStatus(t, rt, projectID, iniID, domain.InitiativeComplete)
	if _, err := pr.db.CreateOperation(ctx, projectID, iniID, "suggest_answers"); err != nil {
		t.Fatal(err)
	}
	rt.reconcileStaleOperations(ctx, pr)
	ini, err = pr.db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("a complete initiative was moved to %q by a stale operation row", ini.Status)
	}
}
