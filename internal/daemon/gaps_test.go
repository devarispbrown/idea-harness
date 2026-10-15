package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// The gap routes end to end: the initiative route, the project-wide one, the
// kind filter and its refusal, the guidance hook, and the assertion judgment
// that closes an unvalidated_assumption gap.
func TestGapRoutes(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: iniID, Code: "REQ-1", Statement: "bill by usage", Priority: domain.PriorityMust,
		Status: domain.RequirementApproved, AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := pr.db.CreateRequirement(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	a := &domain.Assertion{InitiativeID: iniID, Type: domain.AssertionAssumption, Statement: "one meter per customer", Confidence: domain.ConfidenceLow}
	if err := pr.db.CreateAssertion(t.Context(), a); err != nil {
		t.Fatal(err)
	}

	get := func(path string) (int, domain.GapReport, string) {
		t.Helper()
		resp := doReq(t, http.MethodGet, ts.URL+path, token, "", "")
		defer resp.Body.Close()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		var rep domain.GapReport
		_ = json.Unmarshal(raw, &rep)
		return resp.StatusCode, rep, string(raw)
	}
	base := "/api/v1/projects/" + projectID

	code, rep, raw := get(base + "/initiatives/" + iniID + "/gaps")
	if code != http.StatusOK || rep.InitiativeID != iniID || rep.Summary.Total != len(rep.Gaps) || rep.Summary.Total == 0 {
		t.Fatalf("GET gaps = %d %s", code, raw)
	}
	kinds := map[domain.GapKind]bool{}
	for _, g := range rep.Gaps {
		kinds[g.Kind] = true
	}
	for _, want := range []domain.GapKind{domain.GapUnimplementedRequirement, domain.GapUntestedAcceptanceCriterion, domain.GapUnvalidatedAssumption} {
		if !kinds[want] {
			t.Errorf("missing %s in %s", want, raw)
		}
	}

	code, rep, raw = get(base + "/initiatives/" + iniID + "/gaps?kind=unvalidated_assumption")
	if code != http.StatusOK || len(rep.Gaps) != 1 || rep.Gaps[0].Kind != domain.GapUnvalidatedAssumption {
		t.Fatalf("kind filter = %d %s", code, raw)
	}

	code, _, raw = get(base + "/initiatives/" + iniID + "/gaps?kind=bogus")
	if code != http.StatusBadRequest || !strings.Contains(raw, "known kinds") {
		t.Errorf("bogus kind = %d %s", code, raw)
	}

	code, rep, raw = get(base + "/gaps")
	if code != http.StatusOK || rep.Summary.Total == 0 || rep.Gaps[0].InitiativeID != iniID {
		t.Errorf("project gaps = %d %s", code, raw)
	}

	// The guidance hook.
	resp := doReq(t, http.MethodGet, ts.URL+base+"/initiatives/"+iniID+"/guidance", token, "", "")
	var g struct {
		GapSummary *domain.GapSummary `json:"gap_summary"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	if g.GapSummary == nil || g.GapSummary.Total == 0 {
		t.Errorf("guidance gap_summary = %+v", g.GapSummary)
	}

	// Closing the assumption gap.
	post := func(id, body string) (int, string) {
		t.Helper()
		resp := doReq(t, http.MethodPost, ts.URL+base+"/assertions/"+id+"/status", token, "", body)
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		b, _ := json.Marshal(m)
		return resp.StatusCode, string(b)
	}
	if code, body := post(a.ID, `{"status":"candidate"}`); code != http.StatusBadRequest {
		t.Errorf("candidate = %d %s", code, body)
	}
	if code, body := post(a.ID, `{"status":"accepted"}`); code != http.StatusOK || !strings.Contains(body, "accepted") {
		t.Errorf("accept = %d %s", code, body)
	}
	if code, _, raw := get(base + "/initiatives/" + iniID + "/gaps?kind=unvalidated_assumption"); code != http.StatusOK || !strings.Contains(raw, `"gaps":[]`) {
		t.Errorf("after accept = %d %s", code, raw)
	}
	if err := pr.db.SupersedeAssertions(t.Context(), iniID, "retry"); err != nil {
		t.Fatal(err)
	}
	if code, body := post(a.ID, `{"status":"retracted"}`); code != http.StatusConflict {
		t.Errorf("superseded = %d %s; want 409", code, body)
	}
}
