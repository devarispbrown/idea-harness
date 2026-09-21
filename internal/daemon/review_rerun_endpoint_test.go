package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// unreviewedChangeSetOverHTTP creates a second task/change set that reached
// review_required WITHOUT a completed adversarial review -- the persisted
// shape ICP re-run defect #1 left behind when Execute's own review call
// errored (e.g. the operation was cancelled mid-review) after the
// checks_running -> review_required transition already happened. It borrows
// an already-reviewed change set's patch/base digests so the borrowed one is
// still loadable (RerunChangeSetReview calls loadPatch).
func unreviewedChangeSetOverHTTP(t *testing.T, rt *Runtime, projectID, iniID, reviewedCSID string) string {
	t.Helper()
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := pr.db.GetChangeSet(t.Context(), reviewedCSID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Implement more", Statement: "build more", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: reviewed.PatchDigest, BaseSnapshotID: reviewed.BaseSnapshotID}
	if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	return cs.ID
}

// TestReviewApproveRefusesAndRerunFixesAnIncompleteReview is the end-to-end,
// HTTP-level version of ICP re-run defect #1: `POST .../review
// {"verdict":"approved"}` on a change set whose adversarial review never
// completed must refuse (naming rerun), and `POST .../review
// {"verdict":"rerun"}` must complete it, after which approve succeeds.
//
// rerun answers 202 + operation_id, not 200 (ICP re-run D2): it is a real
// model call, now run as an async operation rather than inside the request,
// so the CLI's own http.Client timeout can never again be mistaken for the
// review itself failing. The test waits on the operation the same way
// `idea review <id> rerun` does by default before checking the outcome.
func TestReviewApproveRefusesAndRerunFixesAnIncompleteReview(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, reviewedCSID := reviewRequiredChangeSet(t, rt, ts, token)
	csID := unreviewedChangeSetOverHTTP(t, rt, projectID, iniID, reviewedCSID)

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
		`{"verdict":"approved"}`)
	body := errorBody(t, resp)
	if !strings.Contains(body, "rerun") {
		t.Fatalf("approve refusal does not name rerun: %q", body)
	}

	resp = doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
		`{"verdict":"rerun"}`)
	var rerunOut map[string]string
	decode(t, resp, &rerunOut)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("rerun answered %d, want 202: %v", resp.StatusCode, rerunOut)
	}
	opID := rerunOut["operation_id"]
	if opID == "" {
		t.Fatal("rerun answered 202 with no operation_id")
	}
	waitForOperation(t, ts, token, opID)
	opResp := doReq(t, http.MethodGet, ts.URL+"/api/v1/operations/"+opID, token, "", "")
	var op struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	decode(t, opResp, &op)
	if op.Status != "succeeded" {
		t.Fatalf("review rerun operation ended %q, want succeeded: %s", op.Status, op.Error)
	}

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pr.db.GetChangeSet(t.Context(), csID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReviewCompleted() {
		t.Fatal("rerun did not record a completed review")
	}

	resp = doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
		`{"verdict":"approved"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve after rerun answered %d, want 200: %s", resp.StatusCode, errorBody(t, resp))
	}
	got, err = pr.db.GetChangeSet(t.Context(), csID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("change set is %q, want approved", got.Status)
	}
}

// TestReviewRerunAcceptFindingsRefused: accept_findings does not apply to
// rerun -- it is not a verdict the ChangeSet state machine records, and
// there is nothing to "sign off" before the review it runs has even
// produced findings.
func TestReviewRerunAcceptFindingsRefused(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, reviewedCSID := reviewRequiredChangeSet(t, rt, ts, token)
	csID := unreviewedChangeSetOverHTTP(t, rt, projectID, iniID, reviewedCSID)

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
		`{"verdict":"rerun","accept_findings":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rerun+accept_findings answered %d, want 400: %s", resp.StatusCode, errorBody(t, resp))
	}
}
