package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// staleGuidanceView is the subset of GET .../guidance review A4 #5 added.
type staleGuidanceView struct {
	Why        string          `json:"why"`
	StaleItems []staleItemView `json:"stale_items"`
}

type staleItemView struct {
	StaleID string `json:"stale_id"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
}

func getStaleGuidance(t *testing.T, url, token string) staleGuidanceView {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Idea-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET guidance: %d: %s", resp.StatusCode, body)
	}
	var g staleGuidanceView
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestGuidanceSurfacesStaleItemsAndAckClearsThem is the review A4 #5
// end-to-end test at the API layer: reopening a decided decision surfaces
// stale_items and a "why" sentence naming the decision on /guidance, and
// acknowledging one via the ack endpoint clears it.
func TestGuidanceSurfacesStaleItemsAndAckClearsThem(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	dec := &domain.Decision{InitiativeID: iniID, Type: domain.DecisionArchitecture, Question: "Which database?", Options: []string{"Postgres", "MongoDB"}}
	if err := pr.db.CreateDecision(t.Context(), dec); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.ResolveDecision(t.Context(), dec.ID, "Postgres", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: iniID, Statement: "Persist data", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.WireRequirementDerivedFromDecision(t.Context(), iniID, req.ID, dec.ID); err != nil {
		t.Fatal(err)
	}

	guidanceURL := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"

	// Before the decision is reopened, guidance carries no stale items.
	before := getStaleGuidance(t, guidanceURL, token)
	if len(before.StaleItems) != 0 {
		t.Fatalf("expected no stale items before reopen, got %+v", before.StaleItems)
	}

	// Reopen the decision (i.e. "change" it).
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/decisions/"+dec.ID+"/reopen", token, "", "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reopen decision: %d", resp.StatusCode)
	}
	resp.Body.Close()

	after := getStaleGuidance(t, guidanceURL, token)
	if len(after.StaleItems) == 0 {
		t.Fatal("expected stale_items after the decision was reopened")
	}
	if !strings.Contains(after.Why, dec.Code) {
		t.Fatalf("expected why to name %s, got %q", dec.Code, after.Why)
	}
	if !strings.Contains(after.Why, "revise-plan") {
		t.Fatalf("expected why to name the re-plan path, got %q", after.Why)
	}
	var reqItem *staleItemView
	for i := range after.StaleItems {
		if after.StaleItems[i].Type == "requirement" && after.StaleItems[i].ID == req.ID {
			reqItem = &after.StaleItems[i]
		}
	}
	if reqItem == nil {
		t.Fatalf("expected the requirement to appear in stale_items, got %+v", after.StaleItems)
	}
	if reqItem.Label != req.Code {
		t.Fatalf("expected the stale item's label to be %s, got %q", req.Code, reqItem.Label)
	}

	// Acknowledge it.
	ackResp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/stale/"+reqItem.StaleID+"/ack", token, "", "{}")
	if ackResp.StatusCode != http.StatusOK {
		t.Fatalf("ack stale item: %d", ackResp.StatusCode)
	}
	ackResp.Body.Close()

	cleared := getStaleGuidance(t, guidanceURL, token)
	if len(cleared.StaleItems) != 0 {
		t.Fatalf("expected the acknowledged item to be cleared, got %+v", cleared.StaleItems)
	}

	// Acknowledging it again is refused, not a silent no-op.
	ackAgain := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/stale/"+reqItem.StaleID+"/ack", token, "", "{}")
	if ackAgain.StatusCode == http.StatusOK {
		t.Fatal("expected acknowledging an already-cleared item to fail")
	}
	ackAgain.Body.Close()
}
