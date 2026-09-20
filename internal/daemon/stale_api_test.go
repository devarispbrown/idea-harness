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
	// This initiative has no execution plan yet (newTestProjectInitiative
	// creates it fresh, with no advance/plan_approved). ICP re-run defect: the
	// guidance used to name `idea revise-plan` here unconditionally -- a
	// command that would immediately refuse ("no plan to revise ... no
	// execution plan exists yet") -- since it never checked whether a plan
	// exists before recommending the one command that regenerates it.
	if strings.Contains(after.Why, "revise-plan") {
		t.Fatalf("expected why NOT to name revise-plan before any plan exists, got %q", after.Why)
	}
	if !strings.Contains(after.Why, "incorporated when the execution plan is generated") {
		t.Fatalf("expected why to explain the item will be incorporated once a plan exists, got %q", after.Why)
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

	// review M5: an unknown stale id is a 404, not a generic 400.
	unknown := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/stale/no-such-id/ack", token, "", "{}")
	defer unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("ack unknown stale id = %d, want 404 (got %q)", unknown.StatusCode, readError(t, unknown))
	}
}

// TestGuidanceNamesRevisePlanOnceAPlanExists is the positive case between the
// two ICP re-run fixes: once the initiative has an actual execution plan
// (plan_approved) and no change set holds work, `idea revise-plan` really is
// available, and guidance should say so -- unlike before any plan exists
// (TestGuidanceSurfacesStaleItemsAndAckClearsThem) or once a change set holds
// work (TestGuidanceAdvisesAckOrRerunWhenRevisePlanIsUnavailable).
func TestGuidanceNamesRevisePlanOnceAPlanExists(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

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
	if err := pr.db.ReopenDecision(t.Context(), dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	guidanceURL := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	g := getStaleGuidance(t, guidanceURL, token)
	if len(g.StaleItems) == 0 {
		t.Fatal("expected stale items after reopening the decision")
	}
	if !strings.Contains(g.Why, "Run `idea revise-plan`") {
		t.Fatalf("expected why to name revise-plan once a plan exists and nothing holds work, got %q", g.Why)
	}
}

// TestGuidanceAdvisesAckOrRerunWhenRevisePlanIsUnavailable is review M5: once
// a change set holds work, `idea revise-plan` refuses -- so the guidance
// "why" for stale items must not send the user at that guaranteed refusal.
// It should instead say to acknowledge the items or re-run the affected
// tasks.
func TestGuidanceAdvisesAckOrRerunWhenRevisePlanIsUnavailable(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, _ := reviewRequiredChangeSet(t, rt, ts, token)
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
	if err := pr.db.ReopenDecision(t.Context(), dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	guidanceURL := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	g := getStaleGuidance(t, guidanceURL, token)
	if len(g.StaleItems) == 0 {
		t.Fatal("expected stale items after reopening the decision")
	}
	if strings.Contains(g.Why, "Run `idea revise-plan`") {
		t.Fatalf("guidance still points at revise-plan although a change set holds work: %q", g.Why)
	}
	if !strings.Contains(g.Why, "acknowledge") || !strings.Contains(g.Why, "re-run") {
		t.Fatalf("guidance does not advise acking or re-running the affected tasks: %q", g.Why)
	}
}
