package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestAcceptRecommendationsResolvesEveryOpenDecision proves review B4's
// "accept-recommendations" endpoint resolves every open decision with its own
// recommendation, actor human, and leaves a decision with no recommendation
// open and named in `skipped`.
func TestAcceptRecommendationsResolvesEveryOpenDecision(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	withRec := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which DNS provider?",
		Options: []string{"eNom", "Route53"}, Recommendation: "eNom", Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), withRec); err != nil {
		t.Fatal(err)
	}
	noRec := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct, Question: "Which region?",
		Options: []string{"us", "eu"}, Status: domain.DecisionOpen}
	if err := pr.db.CreateDecision(t.Context(), noRec); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/decisions/accept-recommendations"
	resp := doReq(t, http.MethodPost, url, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept-recommendations: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var body struct {
		Resolved []string `json:"resolved"`
		Skipped  []string `json:"skipped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(body.Resolved) != 1 || body.Resolved[0] != withRec.Code {
		t.Fatalf("resolved = %v, want [%s]", body.Resolved, withRec.Code)
	}
	if len(body.Skipped) != 1 || body.Skipped[0] != noRec.Code {
		t.Fatalf("skipped = %v, want [%s]", body.Skipped, noRec.Code)
	}

	got, err := pr.db.GetDecision(t.Context(), withRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.DecisionDecided || got.Resolution != "eNom" || got.ResolvedBy != "human" {
		t.Fatalf("decision not resolved with its recommendation: %+v", got)
	}
	stillOpen, err := pr.db.GetDecision(t.Context(), noRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillOpen.Status != domain.DecisionOpen {
		t.Fatalf("no-recommendation decision should stay open, got %q", stillOpen.Status)
	}
}

// TestTriageRanksBlockingFirstThenTopThree proves review B4's triage ranking:
// blocking beats a plain open decision/question, and only the top 3 are
// "priority" -- the rest "optional".
func TestTriageRanksBlockingFirstThenTopThree(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// Four open decisions -- more than the top-3 cutoff.
	for i := 0; i < 4; i++ {
		d := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionProduct,
			Question: "Decision " + string(rune('A'+i)) + "?", Options: []string{"x", "y"}, Status: domain.DecisionOpen}
		if err := pr.db.CreateDecision(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	// One blocking finding, which must outrank every decision regardless of
	// creation order.
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, ReviewRunID: "r1", TargetType: "change_set", TargetID: "cs-1",
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh, Category: "secret_sweep",
		Finding: "a real credential was found", Recommendation: "rotate it",
	}); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/triage"
	resp := doReq(t, http.MethodGet, url, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("triage: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var body struct {
		Items []struct {
			Type     string `json:"type"`
			Blocking bool   `json:"blocking"`
			Rank     string `json:"rank"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(body.Items) != 5 {
		t.Fatalf("expected 5 items (1 finding + 4 decisions), got %d", len(body.Items))
	}
	if body.Items[0].Type != "finding" || !body.Items[0].Blocking || body.Items[0].Rank != "priority" {
		t.Fatalf("blocking finding must rank first: %+v", body.Items[0])
	}
	priority, optional := 0, 0
	for _, it := range body.Items {
		switch it.Rank {
		case "priority":
			priority++
		case "optional":
			optional++
		default:
			t.Fatalf("item has no rank: %+v", it)
		}
	}
	if priority != 3 || optional != 2 {
		t.Fatalf("priority=%d optional=%d, want 3 and 2", priority, optional)
	}
}
