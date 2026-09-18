package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// A revise cycle re-lists a decision it already asked. The list the UI reads
// shows the question once, and answering it answers every open copy -- or the
// older copy reappeared the moment the newest was answered.
func TestDuplicateDecisionsShowOnceAndAreAnsweredTogether(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, q := range []string{"Which database?", "[DEC-2] which database"} {
		d := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionType("product"), Question: q,
			Options: []string{"sqlite", "postgres"}, Status: domain.DecisionOpen}
		if err := pr.db.CreateDecision(t.Context(), d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/decisions"
	resp := doReq(t, http.MethodGet, url, token, "", "")
	var list []domain.Decision
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list) != 1 {
		t.Fatalf("the same question listed %d times", len(list))
	}

	resp = doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/decisions/"+list[0].ID+"/resolve", token, "", `{"resolution":"sqlite"}`)
	resp.Body.Close()
	for _, id := range ids {
		d, err := pr.db.GetDecision(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == domain.DecisionOpen {
			t.Errorf("%q is still open after its duplicate was answered", d.Question)
		}
	}
}
