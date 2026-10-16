package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// seedQuality registers the quality endpoints on the fake daemon: the first
// scorecard read reports needsFirst, a POST starts op-q (which succeeds), and
// POST /verify reports coverage met plus the given quality scorecard.
func seedQuality(f *fakeDaemon, needsFirst bool, sc map[string]any) (posts *int) {
	posts = new(int)
	reads := 0
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/quality", func(w http.ResponseWriter, _ *http.Request) {
		reads++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"needs_measurement": needsFirst && reads == 1, "dimensions": []any{}, "gate_unmet": []string{}})
	})
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/quality", func(w http.ResponseWriter, _ *http.Request) {
		*posts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"op-q"}`))
	})
	f.json("GET /api/v1/operations/op-q", map[string]any{"status": "succeeded", "type": "measure_quality"})
	f.verifyCoverage = map[string]any{
		"status":   "approved",
		"coverage": []map[string]any{{"code": "REQ-1", "pass": 1, "total": 1, "needs_manual": 0}},
		"quality":  sc,
	}
	return posts
}

func gatedFailScorecard() map[string]any {
	return map[string]any{
		"rigor": "thorough",
		"dimensions": []map[string]any{
			{"dimension": "docs_completeness", "mode": "gate", "stale": false, "result": map[string]any{
				"id": "q1", "status": "fail", "summary": "1 fail — routes_documented: 1 of 1 added HTTP routes are not mentioned",
				"checks": []map[string]any{{"name": "routes_documented", "status": "fail", "method": "deterministic",
					"detail": "1 of 1 added HTTP routes are not mentioned in any doc: /v1/x", "citations": []map[string]any{{"path": "main.go", "line": 9}}}},
			}},
			{"dimension": "security", "mode": "advisory", "stale": false, "result": map[string]any{
				"id": "q2", "status": "not_measured", "summary": "1 not measured", "checks": []map[string]any{}}},
			{"dimension": "performance", "mode": "off"},
		},
		"gate_unmet": []string{"docs_completeness fail"},
	}
}

func TestVerifyMeasuresStaleQualityAndFailsOnAGatedDimension(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	posts := seedQuality(f, true, gatedFailScorecard())

	stdout, _, err := run(t, "verify")
	if *posts != 1 {
		t.Fatalf("a stale scorecard must be measured once before the gate, got %d POSTs", *posts)
	}
	if err == nil || !strings.Contains(err.Error(), "quality: docs_completeness fail") {
		t.Fatalf("a failing gated dimension must fail verify: %v", err)
	}
	for _, want := range []string{"Quality", "docs_completeness", "routes_documented", "main.go:9", "[deterministic]", "off: performance"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("scorecard missing %q:\n%s", want, stdout)
		}
	}
}

func TestVerifyAdvisoryOnlyQualityDoesNotFailAndNoMeasureSkips(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	sc := gatedFailScorecard()
	sc["gate_unmet"] = []string{} // docs is advisory in this policy
	posts := seedQuality(f, true, sc)

	stdout, _, err := run(t, "verify", "--no-measure", "--json")
	if err != nil {
		t.Fatalf("advisory quality must not fail the gate: %v", err)
	}
	if *posts != 0 {
		t.Fatal("--no-measure must not start a measurement")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if _, ok := payload["quality"]; !ok {
		t.Fatalf("--json must include the scorecard: %v", payload)
	}
}

func TestVerifyToleratesADaemonWithoutTheQualityEndpoint(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	if _, _, err := run(t, "verify"); err != nil {
		t.Fatalf("an older daemon (no /quality) must verify as before: %v", err)
	}
}
