package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// readError pulls the message out of an error response.
func readError(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	if e.Error == "" {
		return string(b)
	}
	return e.Error
}

// TestRevertRejectsANonAppliedChangeSet pins audit finding b. handleApply grew a
// synchronous status guard -- with a comment explaining that without it
// `idea apply CHG-1` on an already-applied change set answered "✓ apply started"
// -- and handleRevert never got the same treatment. Measured against the shipped
// v0.1.0 binary: `idea revert CHG-1` on a reverted change set printed
// "✓ revert started" and exited 0 while `idea apply CHG-1` correctly exited 1.
func TestRevertRejectsANonAppliedChangeSet(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	base := ts.URL + "/api/v1/projects/" + projectID

	// Drive a change set through the real state machine to reverted, rather
	// than hoping framing happens to produce one -- the first version of this
	// test skipped, which is no test at all.
	cs := &domain.ChangeSet{InitiativeID: iniID}
	if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, step.from, step.to, "test"); err != nil {
			t.Fatalf("transition %s -> %s: %v", step.from, step.to, err)
		}
	}
	if err := pr.db.MarkChangeSetApplied(t.Context(), cs.ID, "digest", "test"); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.MarkChangeSetReverted(t.Context(), cs.ID, "test"); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost, base+"/change-sets/"+cs.ID+"/revert", token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("revert on a reverted change set = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
	if msg := readError(t, resp); !strings.Contains(msg, "already reverted") {
		t.Errorf("409 message %q does not say the change set is already reverted", msg)
	}
}

// TestAdvanceTwiceIsAConflict pins audit finding l: handleAdvance mapped every
// error to 500, including "initiative is already running" and "advance requires
// an approved PRD", while retry/approve-plan/reopen-phase/revise-plan returned
// 409 for the identical class.
//
// The handler still passes StatusInternalServerError. It gets 409 anyway,
// because the classification now travels with the error -- which is the point:
// the seventh handler cannot get it wrong.
func TestAdvanceTwiceIsAConflict(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, t.TempDir())

	// A freshly framed initiative is not in the approved state advance requires.
	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/advance", token, "", "")
	if resp.StatusCode == http.StatusAccepted {
		var out struct {
			OperationID string `json:"operation_id"`
		}
		decode(t, resp, &out)
		waitForOperation(t, ts, token, out.OperationID)
		resp = doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/advance", token, "", "")
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("advance from a non-advanceable state = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
}

// TestExecuteRequiresAnApprovedPlan pins the execute half of finding b.
func TestExecuteRequiresAnApprovedPlan(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, t.TempDir())

	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/execute", token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("execute without an approved plan = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
}

// TestPreconditionErrorMapsToConflict tests the invariant rather than one
// handler: writeErr answers 409 for a PreconditionError regardless of the status
// the handler passed. Testing only `advance` would test the sixth handler.
func TestPreconditionErrorMapsToConflict(t *testing.T) {
	s := &Server{}
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadRequest,
		http.StatusConflict,
	} {
		w := httptest.NewRecorder()
		s.writeErr(w, status, preconditionf("initiative is already running"))
		if w.Code != http.StatusConflict {
			t.Errorf("writeErr(%d, PreconditionError) = %d, want 409", status, w.Code)
		}
		if got := w.Body.String(); !strings.Contains(got, "already running") {
			t.Errorf("writeErr(%d) lost the reason: %q", status, got)
		}
	}
}

// TestNonPreconditionErrorsKeepTheirStatus is the other half, and the one that
// matters for regressions: fourteen handler call sites pass StatusBadRequest for
// engine errors (ReviewChangeSet and VerifyInitiative among them). If an error
// those paths surface were ever given the precondition type, the response would
// silently flip 400 -> 409.
func TestNonPreconditionErrorsKeepTheirStatus(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.writeErr(w, http.StatusBadRequest, errPlain("requirement REQ-9 is not in this initiative"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a plain domain error passed at 400 became %d", w.Code)
	}

	// A store miss still wins over everything: 404, not the handler's status.
	w = httptest.NewRecorder()
	s.writeErr(w, http.StatusBadRequest, &store.NotFoundError{Kind: "requirement", Ref: "REQ-9"})
	if w.Code != http.StatusNotFound {
		t.Errorf("a store miss passed at 400 became %d, want 404", w.Code)
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }
