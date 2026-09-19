package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestGuidanceIsHonestAfterADeniedChangeSet is review C6, end to end: a
// secret-sweep denial rejects the change set (see rerun_after_denial_test.go
// for why this, not a scripted adversarial-review block, is what still denies
// post-ICP-fix), and `/guidance` must say what happened and the next step --
// never the unconditional "ready to build" a plan_approved initiative with a
// rejected change set used to get.
func TestGuidanceIsHonestAfterADeniedChangeSet(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
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
	if err != nil || len(sets) != 1 || sets[0].Status != domain.ChangeSetRejected {
		t.Fatalf("setup: expected 1 rejected change set, got %+v (err=%v)", sets, err)
	}

	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/guidance", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("guidance: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var g struct {
		Action string `json:"action"`
		Why    string `json:"why"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if strings.Contains(g.Action, "Execution plan approved") || strings.Contains(g.Why, "ready to build") {
		t.Fatalf("guidance still said the plan is ready to build over a fresh denial: %+v", g)
	}
	if !strings.Contains(g.Why, sets[0].HumanID) {
		t.Fatalf("guidance did not name the rejected change set: %+v", g)
	}
}
