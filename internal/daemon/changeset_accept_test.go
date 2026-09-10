package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// reviewRequiredChangeSet drives an initiative to a change set awaiting review
// -- the state `idea next` reports as `review CHG-1`.
func reviewRequiredChangeSet(t *testing.T, rt *Runtime, ts *httptest.Server, token string) (projectID, iniID, csID string) {
	t.Helper()
	projectID, iniID = newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	waitForOperation(t, ts, token, opID)
	sets, err := pr.db.ListChangeSets(t.Context(), iniID)
	if err != nil || len(sets) == 0 {
		t.Fatalf("execute produced no change set (err=%v)", err)
	}
	return projectID, iniID, sets[0].ID
}

// blockingChangeSetReview adds the finding a real reviewer persists on the first
// `idea review CHG-1 approve` -- after which every later attempt is refused by
// the deny gate before the review even runs.
func blockingChangeSetReview(t *testing.T, rt *Runtime, ts *httptest.Server, token string) (projectID, iniID, csID string) {
	t.Helper()
	projectID, iniID, csID = reviewRequiredChangeSet(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// What runChangeSetAdversarialReview persists: no Code, no disposition.
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: csID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "quality", Finding: "the placeholder file implements nothing",
		Recommendation: "write the real code",
	}); err != nil {
		t.Fatal(err)
	}
	return projectID, iniID, csID
}

// TestAcceptFindingsUnblocksAChangeSetTheReviewerWillNotPass is the acceptance
// test for the third live-lock.
//
// `idea next` names `idea review CHG-1 approve`; that runs a fresh adversarial
// review, and a model reviewer is not a fixed point. Worse, the first attempt
// persists its findings, so every later attempt is refused by
// denyReviewOpenBlocking BEFORE the review runs. `idea apply` answers "needs
// approval first". Every other command refuses. The keyed magic path hit this
// on every run and had no route out.
func TestAcceptFindingsUnblocksAChangeSetTheReviewerWillNotPass(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _, csID := blockingChangeSetReview(t, rt, ts, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)

	// Without the flag: still refused, and the refusal names the flag.
	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
		`{"verdict":"approved"}`)
	body := errorBody(t, resp)
	if !strings.Contains(body, "review_open_blocking") {
		t.Fatalf("setup: expected the deny, got %q", body)
	}
	if !strings.Contains(body, "--accept-findings") {
		t.Errorf("the refusal does not name the route out: %q", body)
	}

	// With it: approved.
	resp = doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
		`{"verdict":"approved","accept_findings":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("--accept-findings answered %d, want 200", resp.StatusCode)
	}
	cs, err := pr.db.GetChangeSet(t.Context(), csID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetApproved {
		t.Fatalf("change set is %q, want approved", cs.Status)
	}

	// The fourth gate: verify refuses `complete` while any initiative-wide
	// undisposed blocking finding exists, and the reviewer's change-set findings
	// carry the initiative id. A mis-scoped disposition approves the change set
	// and then wedges completion one gate further on.
	findings, err := pr.db.ListFindingsByTarget(t.Context(), "change_set", csID)
	if err != nil {
		t.Fatal(err)
	}
	// The reviewer's finding specifically. The fixture also carries the check
	// runner's `not_applicable` observations, which the predicate correctly
	// leaves alone -- a sweep over every finding would assert the wrong thing.
	found := false
	for _, f := range findings {
		if !strings.Contains(f.Finding, "placeholder file implements nothing") {
			continue
		}
		found = true
		if f.Disposition != domain.DispositionAcceptedRisk {
			t.Errorf("the reviewer's finding is %q, want accepted_risk", f.Disposition)
		}
		if f.DispositionActor != "human" {
			t.Errorf("the reviewer's finding was signed off by %q, not a human", f.DispositionActor)
		}
	}
	if !found {
		t.Fatal("the seeded reviewer finding is gone")
	}
}

// A system deny is not something a human can accept. The flag must leave it
// alone, and the gate must then still refuse.
func TestAcceptFindingsRefusesASystemDeny(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        domain.DenyCode
		disposition domain.Disposition
	}{
		{"a credential detection", domain.DenyCodeSecretSweep, ""},
		{"a sandbox failure", domain.DenyCodeHostExec, domain.DispositionRejected},
		{"a digest mismatch", domain.DenyCodeBlobDigestMismatch, ""},
		// No code, but already dispositioned: a failing build check. Indis-
		// tinguishable from a reviewer finding by code alone, which is why the
		// predicate is code AND disposition.
		{"a failing build check", "", domain.DispositionRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, iniID, csID := reviewRequiredChangeSet(t, rt, ts, token)
			pr, _ := rt.EnsureProject(t.Context(), projectID)
			if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
				InitiativeID: iniID, TargetType: "change_set", TargetID: csID,
				Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
				Category: "security", Code: tc.code, Disposition: tc.disposition,
				Finding: "system deny", Recommendation: "r",
			}); err != nil {
				t.Fatal(err)
			}

			resp := doReq(t, http.MethodPost,
				ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review", token, "",
				`{"verdict":"approved","accept_findings":true}`)
			body := errorBody(t, resp)
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("--accept-findings signed off %s", tc.name)
			}
			if !strings.Contains(body, "review_open_blocking") {
				t.Errorf("the refusal is not the deny gate: %q", body)
			}
			cs, _ := pr.db.GetChangeSet(t.Context(), csID)
			if cs.Status == domain.ChangeSetApproved {
				t.Fatalf("the change set was approved over %s", tc.name)
			}
			// The seeded finding specifically: the fixture carries other,
			// code-free findings that the accept legitimately signs off.
			findings, _ := pr.db.ListFindingsByTarget(t.Context(), "change_set", csID)
			for _, f := range findings {
				if f.Finding != "system deny" {
					continue
				}
				if f.Disposition == domain.DispositionAcceptedRisk {
					t.Errorf("%s was dispositioned accepted_risk", tc.name)
				}
			}
		})
	}
}

// The accept must make NO model call. That is what separates it from a retry,
// and it is the property the two previous rounds got wrong.
func TestAcceptFindingsMakesNoModelCall(t *testing.T) {
	rt, ts, token := newTestServer(t)
	// A reviewer that blocks 50 times, not 1. On a converging queue the accept
	// and a retry are indistinguishable -- the retry would simply find nothing
	// to object to -- so a count assertion against it asserts nothing. This is
	// the same reasoning the plan-accept test records for its own queue size.
	projectID, pr := newDemoSteeredProject(t, rt, ts, token,
		"\ndemo:\n  block_change_set_review_cycles: 50\n")

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives",
		token, "", `{"title":"billing","description":"usage-based billing"}`)
	var created struct {
		ID          string `json:"initiative_id"`
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &created)
	waitForOperation(t, ts, token, created.OperationID)

	task := &domain.Task{InitiativeID: created.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, created.ID, domain.InitiativePlanApproved)
	opID, err := rt.ExecuteInitiative(t.Context(), projectID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, ts, token, opID)
	sets, err := pr.db.ListChangeSets(t.Context(), created.ID)
	if err != nil || len(sets) == 0 {
		t.Fatalf("no change set (err=%v)", err)
	}
	csID := sets[0].ID
	base := ts.URL + "/api/v1/projects/" + projectID + "/change-sets/" + csID + "/review"

	// The real sequence: the first approve runs the reviewer, which blocks and
	// persists its finding.
	resp = doReq(t, http.MethodPost, base, token, "", `{"verdict":"approved"}`)
	if body := errorBody(t, resp); body == "" {
		t.Fatal("setup: the first approve was expected to be refused by the review")
	}
	before, err := pr.db.ListFindingsByTarget(t.Context(), "change_set", csID)
	if err != nil {
		t.Fatal(err)
	}

	resp = doReq(t, http.MethodPost, base, token, "", `{"verdict":"approved","accept_findings":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("--accept-findings answered %d: %s", resp.StatusCode, errorBody(t, resp))
	}

	// A fresh review PERSISTS its findings before evaluating them, so the count
	// is a direct observable of whether one ran -- no seam into the provider.
	after, err := pr.db.ListFindingsByTarget(t.Context(), "change_set", csID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the accept ran the reviewer (%d → %d findings on the change set); "+
			"that is a retry, not an accept", len(before), len(after))
	}
	cs, _ := pr.db.GetChangeSet(t.Context(), csID)
	if cs.Status != domain.ChangeSetApproved {
		t.Fatalf("change set is %q, want approved", cs.Status)
	}
	if cs.ReviewID != "" {
		t.Errorf("review_id was stamped (%q); no review ran, and the event log is the trail", cs.ReviewID)
	}
}

// errorBody returns the `error` field of a JSON error response.
func errorBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	return body.Error
}
