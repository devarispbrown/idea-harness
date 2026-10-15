package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func decodeInto(t *testing.T, resp *http.Response, want int, v any) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		var e map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&e)
		t.Fatalf("status %d, want %d: %v", resp.StatusCode, want, e)
	}
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOutcomesAPIRoundTrip walks the human path end to end over HTTP: add an
// objective, link a requirement, add a metric, record values, and read the
// drift back from /metrics, /outcomes and /guidance.
func TestOutcomesAPIRoundTrip(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: iniID, Statement: "onboarding checklist", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID

	var obj domain.Objective
	decodeInto(t, doReq(t, http.MethodPost, base+"/objectives", token, "", `{"statement":"New users succeed on day one","owner":"pm@example.com"}`), http.StatusCreated, &obj)
	if obj.Code != "OBJ-1" || obj.Status != domain.ObjectiveActive || obj.RequirementIDs == nil {
		t.Fatalf("created %+v", obj)
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/objectives", token, "", `{"statement":""}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/objectives", token, "", `{"statement":"x","status":"done"}`), http.StatusBadRequest, nil)

	decodeInto(t, doReq(t, http.MethodPost, base+"/objectives/OBJ-1/requirements", token, "", `{"requirement":"REQ-1"}`), http.StatusOK, &obj)
	if len(obj.RequirementIDs) != 1 || obj.RequirementIDs[0] != req.ID {
		t.Fatalf("after link %+v", obj)
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/objectives/OBJ-1/requirements", token, "", `{"requirement":"REQ-9"}`), http.StatusNotFound, nil)

	decodeInto(t, doReq(t, http.MethodPatch, base+"/objectives/obj-1", token, "", `{"rationale":"activation drives retention"}`), http.StatusOK, &obj)
	if obj.Rationale != "activation drives retention" || obj.Statement != "New users succeed on day one" || obj.Owner != "pm@example.com" {
		t.Fatalf("PATCH should change only what it names: %+v", obj)
	}

	var met domain.OutcomeMetric
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics", token, "",
		`{"objective":"OBJ-1","name":"activation","unit":"%","direction":"increase","baseline":20,"target":35,"cadence":"weekly","hypothesis":"We believe the checklist moves activation"}`),
		http.StatusCreated, &met)
	if met.Code != "MET-1" || met.ObjectiveCode != "OBJ-1" || met.Status == nil || met.Status.Drift != domain.DriftNoData || met.Source.Kind != domain.MetricSourceManual {
		t.Fatalf("created metric %+v", met)
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics", token, "", `{"objective":"OBJ-1","name":"x","direction":"up","target":1}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics", token, "", `{"objective":"OBJ-1","name":"x","direction":"increase"}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics", token, "", `{"objective":"OBJ-7","name":"x","direction":"increase","target":1}`), http.StatusNotFound, nil)

	var rec struct {
		Observation domain.MetricObservation `json:"observation"`
		Status      domain.MetricStatus      `json:"status"`
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics/MET-1/observations", token, "", `{"value":18,"note":"week 1"}`), http.StatusCreated, &rec)
	if rec.Status.Drift != domain.DriftOffTrack || rec.Observation.Note != "week 1" || rec.Observation.ChangeSetIDs == nil {
		t.Fatalf("record = %+v", rec)
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics/MET-1/observations", token, "", `{"note":"no value"}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics/MET-1/observations", token, "", `{"value":1,"observed_at":"yesterday"}`), http.StatusBadRequest, nil)

	var obs []domain.MetricObservation
	decodeInto(t, doReq(t, http.MethodGet, base+"/metrics/MET-1/observations", token, "", ""), http.StatusOK, &obs)
	if len(obs) != 1 || obs[0].Value != 18 {
		t.Fatalf("observations = %+v", obs)
	}

	// Measuring a manual metric by name is refused with a pointer to record.
	resp := doReq(t, http.MethodPost, base+"/metrics/measure", token, "", `{"metric":"MET-1"}`)
	var e map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(e["error"], "idea metric record") {
		t.Fatalf("measure manual: %d %v", resp.StatusCode, e)
	}
	var measured struct {
		Results []map[string]any `json:"results"`
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/metrics/measure", token, "", ""), http.StatusOK, &measured)
	if len(measured.Results) != 1 || measured.Results[0]["skipped"] == nil {
		t.Fatalf("bulk measure of a manual-only initiative: %+v", measured)
	}

	// PATCH: a cleared baseline falls back to the first observation, so a
	// single reading is no longer off track.
	met = domain.OutcomeMetric{} // fresh: an omitted baseline must not keep the old pointer
	decodeInto(t, doReq(t, http.MethodPatch, base+"/metrics/MET-1", token, "", `{"clear_baseline":true}`), http.StatusOK, &met)
	if met.Baseline != nil || met.Status.Drift != domain.DriftOnTrack || met.Status.BaselineSource != "first_observation" {
		t.Fatalf("after clearing the baseline: %+v / %+v", met, met.Status)
	}
	decodeInto(t, doReq(t, http.MethodPatch, base+"/metrics/MET-1", token, "", `{"baseline":20}`), http.StatusOK, &met)

	// Mark the requirement verified: the outcomes report and guidance now
	// say it works but fails its objective.
	if err := pr.db.ApproveRequirements(t.Context(), iniID, "human"); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.MarkRequirementVerified(t.Context(), req.ID, "human"); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Objectives       []domain.Objective               `json:"objectives"`
		Metrics          []domain.OutcomeMetric           `json:"metrics"`
		RequirementDrift []domain.RequirementOutcomeDrift `json:"requirement_drift"`
		Summary          map[string]int                   `json:"summary"`
	}
	decodeInto(t, doReq(t, http.MethodGet, base+"/outcomes", token, "", ""), http.StatusOK, &rep)
	if len(rep.RequirementDrift) != 1 || rep.Summary["off_track"] != 1 || rep.Summary["works_but_fails"] != 1 {
		t.Fatalf("outcomes = %+v", rep)
	}
	var g struct {
		Why      string         `json:"why"`
		Outcomes map[string]int `json:"outcomes"`
	}
	decodeInto(t, doReq(t, http.MethodGet, base+"/guidance", token, "", ""), http.StatusOK, &g)
	if !strings.Contains(g.Why, "fails its objective") || g.Outcomes["off_track"] != 1 {
		t.Fatalf("guidance = %+v", g)
	}

	// why/impact address the new node types.
	var why []map[string]any
	decodeInto(t, doReq(t, http.MethodGet, base+"/why?type=requirement&id="+req.ID, token, "", ""), http.StatusOK, &why)
	found := false
	for _, it := range why {
		if it["edge_type"] == "serves" {
			found = true
		}
	}
	if !found {
		t.Fatalf("why requirement should include serves: %+v", why)
	}
	decodeInto(t, doReq(t, http.MethodGet, base+"/impact?type=objective&id="+obj.ID, token, "", ""), http.StatusOK, nil)
}
