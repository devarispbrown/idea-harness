package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestGuidanceNamesSecretSweepCauseNotJustGenericProblem reproduces the ICP
// re-run's own defect #5: guidance for a change set the secret sweep denied
// said only "a check or the review found a problem it would not pass" --
// true, but it does not name the actual cause (which the finding already
// knows: the file and kind of secret, and the not_a_secret disposition route
// for an overridable match). `/guidance` composes this detail from the
// change set's own findings (handleGuidance has store access; BuildGuidance,
// the pure function, does not).
func TestGuidanceNamesSecretSweepCauseNotJustGenericProblem(t *testing.T) {
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

	if !strings.Contains(g.Why, "secret sweep") {
		t.Errorf("guidance did not name the secret sweep as the cause: %q", g.Why)
	}
	if !strings.Contains(g.Why, "AWS access key ID") {
		t.Errorf("guidance did not name the kind of secret: %q", g.Why)
	}
	if !strings.Contains(g.Why, "hello.txt") {
		t.Errorf("guidance did not name the file: %q", g.Why)
	}
	if !strings.Contains(g.Why, "not a secret") {
		t.Errorf("guidance did not name the not_a_secret disposition route: %q", g.Why)
	}
	// Regression guard for the defect this detail used to carry: it named
	// `idea reviews`, which only ever listed findings and had no way to
	// disposition one -- a user hitting a secret denial was told to use a
	// capability that did not exist. `idea disposition` (internal/cli/
	// disposition.go) is the real verb.
	if !strings.Contains(g.Why, "idea disposition") {
		t.Errorf("guidance does not name a real CLI verb: %q", g.Why)
	}
	if strings.Contains(g.Why, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("guidance must never echo the credential value: %q", g.Why)
	}
}
