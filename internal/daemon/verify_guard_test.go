package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestVerifyRefusesAFailedInitiative is the acceptance test for audit 7's P0-2.
//
// A bare `idea verify` -- no arguments, no flags -- on an initiative that failed
// at framing with a 401 printed `✓ verified status: complete`, exited 0, moved a
// terminal state to `complete` and discarded the recorded failure_reason. The
// mechanism: with zero requirements the gate loop in VerifyInitiative never
// runs, so allVerified stays true vacuously, and the terminal write was an
// unconditional SetInitiativeStatus.
//
// handleVerify was the one state-writing path that never went through the
// Runtime, which is why verifyInitiative declared no 409 and the conflict axis
// correctly agreed there was nothing to declare.
func TestVerifyRefusesAFailedInitiative(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	const reason = "workflow: framing: anthropic: status 401: API key is invalid."
	if err := pr.db.FailInitiative(t.Context(), iniID, reason); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/verify", token, "", "{}")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("verify on a failed initiative answered %d, want 409", resp.StatusCode)
	}

	got, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativeFailed {
		t.Fatalf("verify moved a failed initiative to %q", got.Status)
	}
	if got.FailureReason != reason {
		t.Errorf("verify discarded the failure reason:\n got  %q\n want %q", got.FailureReason, reason)
	}
}

// The apply-then-verify sequence both demo guides document, and which
// scripts/smoke.sh runs, verifies while the initiative still reads `running`:
// `idea apply` does not wait for its operation, and the release that restores
// the prior status happens after the change set is marked applied. Refusing
// `running` would have broken that flow, so it is asserted rather than assumed.
func TestVerifyIsAllowedWhileAnApplyIsInFlight(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	setStatus(t, rt, projectID, iniID, domain.InitiativeRunning)

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/verify", token, "", "{}")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify during an in-flight apply answered %d, want 200; the documented "+
			"apply-then-verify sequence runs inside that window", resp.StatusCode)
	}
}

// `idea verify REQ-99` used to answer about a different requirement entirely:
// the CLI sent an empty body on the read path, so the name never reached the
// daemon and the whole-initiative gate answered instead.
//
// 404, not 400. writeErr promotes a store NotFoundError regardless of the
// status argument the handler passes, and that is the right answer here: it is
// the same shape every other id-taking route gives for an id that does not
// resolve, and the same one `idea verify REQ-99 --pass` already produced on the
// write path. The two halves of the command now agree.
func TestVerifyNamesARequirementThatDoesNotExist(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/verify", token, "",
		`{"requirement":"REQ-99"}`)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("verify answered 200 for a requirement that does not exist; " +
			"it reported the whole-initiative gate and never mentioned REQ-99")
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("verify for an unknown requirement answered %d, want 404", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)
	if !strings.Contains(body.Error, "REQ-99") {
		t.Errorf("the error does not name the requirement that was asked for: %q", body.Error)
	}
}

// TestVerifyRefusesAFailedInitiativeClaimedRunning is the bypass a commit review
// found in the first version of this guard.
//
// apply, revert, revise, retry and reopen-phase all claim `running` FROM
// `failed`, and ClaimInitiativeRunning deliberately leaves failure_reason intact
// so the release can restore it. For the length of any of those operations the
// row is a failed initiative wearing a running status -- so a status-only guard
// passes, verify completes it, and CompleteInitiative clears the reason. The
// release then writes only the status back, so the recorded failure reason is
// gone for good. That is the exact symptom P0-2 is named after, reachable
// through the documented apply-then-verify sequence.
func TestVerifyRefusesAFailedInitiativeClaimedRunning(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	const reason = "workflow: framing: anthropic: status 401: API key is invalid."
	if err := pr.db.FailInitiative(t.Context(), iniID, reason); err != nil {
		t.Fatal(err)
	}
	// Exactly what `idea apply` does to a failed initiative.
	if err := pr.db.ClaimInitiativeRunning(t.Context(), iniID, domain.InitiativeFailed); err != nil {
		t.Fatal(err)
	}
	claimed, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != domain.InitiativeRunning || claimed.FailureReason != reason {
		t.Fatalf("setup: want running with the reason intact, got status=%q reason=%q",
			claimed.Status, claimed.FailureReason)
	}

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/verify", token, "", "{}")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("verify answered %d for a failed initiative claimed running, want 409", resp.StatusCode)
	}

	got, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureReason != reason {
		t.Fatalf("the recorded failure reason was destroyed: %q\n"+
			"the release restores the status, not the reason, so this is permanent", got.FailureReason)
	}
}

// Recording a human's judgment about a requirement is not the gate, and is not
// refused because a later stage failed. `idea retry` -- the escape the refusal
// names -- supersedes every requirement before re-running framing, so evidence
// that could not be recorded here can never be recorded at all.
func TestVerifyStillRecordsEvidenceOnAFailedInitiative(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: iniID, Statement: "s", Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "1", Condition: "c"}}}
	if err := pr.db.CreateRequirement(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.FailInitiative(t.Context(), iniID, "provider 401"); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/verify", token, "",
		`{"requirement":"`+req.Code+`","pass":true}`)
	resp.Body.Close()

	// The evidence rows, not the requirement's status: a `pass` is recorded as
	// evidence and the requirement is marked verified by the GATE, which is the
	// half a failed initiative may not run. So this asserts the half that must
	// still work.
	passed, err := pr.db.PassedCriterionKeys(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) != len(req.AcceptanceCriteria) {
		t.Fatalf("the human's pass was discarded because a later stage failed "+
			"(%d of %d criteria recorded); `idea retry` supersedes every requirement, "+
			"so it could never be recorded again", len(passed), len(req.AcceptanceCriteria))
	}
	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeFailed {
		t.Errorf("recording evidence completed a failed initiative: %q", ini.Status)
	}
}
