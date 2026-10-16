package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// seedOutcomes registers the objectives/metrics/outcomes reads and captures
// every write body by "METHOD path".
func (f *fakeDaemon) seedOutcomes() map[string]map[string]any {
	f.seedInitiative()
	bodies := map[string]map[string]any{}
	capture := func(pattern string, status int, reply any) {
		f.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			bodies[r.Method+" "+r.URL.Path] = b
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(reply)
		})
	}
	obj := map[string]any{"id": "o1", "code": "OBJ-1", "statement": "New users succeed on day one", "status": "active",
		"requirement_ids": []string{"req-1"}, "metric_ids": []string{}}
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/objectives", []map[string]any{obj})
	metric := map[string]any{"id": "m1", "code": "MET-1", "objective_code": "OBJ-1", "name": "activation", "unit": "%",
		"direction": "increase", "baseline": 20, "target": 35, "cadence": "weekly", "source": map[string]any{"kind": "manual"},
		"status": map[string]any{"drift": "off_track", "observations": 1, "latest": 18, "reasons": []string{"latest 18 is worse than the declared baseline 20"}}}
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/metrics", []map[string]any{metric})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/outcomes", map[string]any{
		"metrics":           []map[string]any{metric},
		"requirement_drift": []map[string]any{{"detail": "REQ-1 is verified, but OBJ-1's MET-1 is off track"}},
		"gaps":              map[string]any{"objectives_without_metrics": []string{}},
		"summary":           map[string]any{"metrics": 1, "off_track": 1},
	})
	capture("POST /api/v1/projects/{projectId}/initiatives/{id}/objectives", http.StatusCreated, obj)
	capture("PATCH /api/v1/projects/{projectId}/initiatives/{id}/objectives/{objectiveId}", http.StatusOK, obj)
	capture("POST /api/v1/projects/{projectId}/initiatives/{id}/objectives/{objectiveId}/requirements", http.StatusOK, obj)
	capture("POST /api/v1/projects/{projectId}/initiatives/{id}/metrics", http.StatusCreated, metric)
	capture("PATCH /api/v1/projects/{projectId}/initiatives/{id}/metrics/{metricId}", http.StatusOK, metric)
	capture("POST /api/v1/projects/{projectId}/initiatives/{id}/metrics/{metricId}/observations", http.StatusCreated, map[string]any{
		"observation": map[string]any{"change_set_ids": []string{"cs-1"}},
		"status":      map[string]any{"drift": "regressed", "reasons": []string{"moved from 20 to 18 against its direction (increase) after 1 change set(s) landed"}},
	})
	capture("POST /api/v1/projects/{projectId}/initiatives/{id}/metrics/measure", http.StatusOK, map[string]any{"results": []map[string]any{
		{"metric_code": "MET-1", "skipped": "manual source"},
		{"metric_code": "MET-2", "observation": map[string]any{"value": 42}, "status": map[string]any{"drift": "on_track"}},
		{"metric_code": "MET-3", "error": "MET-3: sandbox unavailable (is Docker running?)"},
	}})
	return bodies
}

func TestObjectivesAndMetricsLists(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedOutcomes()
	out, _, err := run(t, "objectives")
	if err != nil || !strings.Contains(out, "OBJ-1 New users succeed on day one") || !strings.Contains(out, "no metric") {
		t.Fatalf("objectives: %v\n%s", err, out)
	}
	out, _, err = run(t, "metrics")
	if err != nil || !strings.Contains(out, "MET-1 activation (OBJ-1)  off track") || !strings.Contains(out, "worse than the declared baseline") ||
		!strings.Contains(out, "increase from 20 to 35%") {
		t.Fatalf("metrics: %v\n%s", err, out)
	}
	out, _, err = run(t, "metrics", "--json")
	var ms []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &ms) != nil || len(ms) != 1 {
		t.Fatalf("metrics --json: %v\n%s", err, out)
	}
}

func TestObjectiveSubcommands(t *testing.T) {
	f := newFakeDaemon(t)
	bodies := f.seedOutcomes()
	if _, _, err := run(t, "objective", "add", "New users succeed on day one", "--owner", "pm"); err != nil {
		t.Fatal(err)
	}
	if b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/objectives"]; b["statement"] != "New users succeed on day one" || b["owner"] != "pm" {
		t.Fatalf("add body = %v", b)
	}
	if _, _, err := run(t, "objective", "edit", "OBJ-1", "--status", "achieved"); err != nil {
		t.Fatal(err)
	}
	b := bodies["PATCH /api/v1/projects/p1/initiatives/ini-1/objectives/OBJ-1"]
	if b["status"] != "achieved" || len(b) != 1 {
		t.Fatalf("edit must send only what was named: %v", b)
	}
	if _, _, err := run(t, "objective", "edit", "OBJ-1"); err == nil || classify(err).ExitCode != 2 {
		t.Fatalf("edit with nothing to change should be a usage error, got %v", err)
	}
	out, _, err := run(t, "objective", "link", "REQ-1", "OBJ-1", "--unlink")
	if err != nil || !strings.Contains(out, "REQ-1 no longer serves OBJ-1") {
		t.Fatalf("link --unlink: %v %s", err, out)
	}
	if b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/objectives/OBJ-1/requirements"]; b["requirement"] != "REQ-1" || b["unlink"] != true {
		t.Fatalf("link body = %v", b)
	}
	if _, _, err := run(t, "objective", "frobnicate"); err == nil || classify(err).ExitCode != 2 {
		t.Fatalf("an unknown subcommand is a usage error, got %v", err)
	}
}

func TestMetricSubcommands(t *testing.T) {
	f := newFakeDaemon(t)
	bodies := f.seedOutcomes()
	if _, _, err := run(t, "metric", "add", "OBJ-1", "activation", "--target", "35", "--baseline", "20", "--command", "wc -l < USERS"); err != nil {
		t.Fatal(err)
	}
	b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/metrics"]
	src, _ := b["source"].(map[string]any)
	if b["objective"] != "OBJ-1" || b["target"] != 35.0 || b["baseline"] != 20.0 || b["direction"] != "increase" || src["kind"] != "command" {
		t.Fatalf("add body = %v", b)
	}
	if _, _, err := run(t, "metric", "add", "OBJ-1", "x", "--target", "lots"); err == nil || classify(err).ExitCode != 2 {
		t.Fatalf("a non-numeric target is a usage error, got %v", err)
	}
	if _, _, err := run(t, "metric", "add", "OBJ-1", "x"); err == nil || classify(err).ExitCode != 2 {
		t.Fatalf("--target is required, got %v", err)
	}
	if _, _, err := run(t, "metric", "add", "OBJ-1", "x", "--target", "1", "--command", "a", "--url", "https://x"); err == nil {
		t.Fatal("--command and --url together must be refused")
	}

	if _, _, err := run(t, "metric", "edit", "MET-1", "--clear-baseline"); err != nil {
		t.Fatal(err)
	}
	if b := bodies["PATCH /api/v1/projects/p1/initiatives/ini-1/metrics/MET-1"]; b["clear_baseline"] != true || len(b) != 1 {
		t.Fatalf("edit body = %v", b)
	}

	out, _, err := run(t, "metric", "record", "MET-1", "18", "--note", "week 2")
	if err != nil || !strings.Contains(out, "recorded MET-1 = 18  regressed") || !strings.Contains(out, "1 change set(s) landed") || !strings.Contains(out, "against its direction") {
		t.Fatalf("record: %v\n%s", err, out)
	}
	if b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/metrics/MET-1/observations"]; b["value"] != 18.0 || b["note"] != "week 2" {
		t.Fatalf("record body = %v", b)
	}
	if _, _, err := run(t, "metric", "record", "MET-1", "eighteen"); err == nil || classify(err).ExitCode != 2 {
		t.Fatalf("a non-numeric value is a usage error, got %v", err)
	}

	// measure: one failure makes the command fail (run_failed), with every
	// result still printed.
	out, _, err = run(t, "metric", "measure")
	if err == nil || classify(err).ExitCode != 9 {
		t.Fatalf("a failed measurement should exit run_failed (9), got %v", err)
	}
	for _, want := range []string{"MET-1 skipped: manual source", "MET-2 = 42  on track", "MET-3 MET-3: sandbox unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("measure output lacks %q:\n%s", want, out)
		}
	}
	if b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/metrics/measure"]; b != nil {
		t.Errorf("bulk measure sends no body, got %v", b)
	}
	if _, _, err := run(t, "metric", "measure", "MET-2"); err == nil {
		// the canned reply still includes MET-3's failure
		t.Fatal("expected the canned failure")
	}
	if b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/metrics/measure"]; b["metric"] != "MET-2" {
		t.Fatalf("measure MET-2 body = %v", b)
	}
}

func TestStatusShowsOutcomeDrift(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedOutcomes()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/guidance", map[string]any{"action": "Verify", "why": "x"})
	out, _, err := run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "outcomes: 1 metric(s) · 1 off track") || !strings.Contains(out, "works, but fails its objective: REQ-1 is verified") {
		t.Fatalf("status should carry the outcome line:\n%s", out)
	}
}

func TestMCPOutcomeTools(t *testing.T) {
	f := newFakeDaemon(t)
	bodies := f.seedOutcomes()
	// seedMCP would re-register seedInitiative's routes; only its extra.
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}", map[string]any{"id": "ini-1", "title": "Build usage-based billing", "status": "complete"})
	s := startMCP(t)
	names := s.toolNames()
	for _, want := range []string{"list_objectives", "list_metrics", "record_metric"} {
		if !contains(names, want) {
			t.Fatalf("tools lack %s: %v", want, names)
		}
	}
	if contains(startMCP(t, "--read-only").toolNames(), "record_metric") {
		t.Fatal("record_metric is a judgment tool; --read-only must not expose it")
	}

	text, st, isErr := s.callTool("list_metrics", map[string]any{})
	if isErr || !strings.Contains(text, "MET-1 activation (OBJ-1) [off_track]") || !strings.Contains(text, "works, but fails its objective") {
		t.Fatalf("list_metrics = %q", text)
	}
	if len(st["metrics"].([]any)) != 1 || len(st["requirement_drift"].([]any)) != 1 {
		t.Fatalf("list_metrics structured = %v", st)
	}
	text, _, isErr = s.callTool("list_objectives", map[string]any{})
	if isErr || !strings.Contains(text, "OBJ-1 [active] New users succeed on day one (1 requirement(s), 0 metric(s))") {
		t.Fatalf("list_objectives = %q", text)
	}

	text, _, isErr = s.callTool("record_metric", map[string]any{"metric": "MET-1", "value": 0, "note": "dashboard"})
	if isErr || !strings.Contains(text, "recorded MET-1 = 0; drift: regressed") {
		t.Fatalf("record_metric = %q", text)
	}
	if b := bodies["POST /api/v1/projects/p1/initiatives/ini-1/metrics/MET-1/observations"]; b["value"] != 0.0 || b["note"] != "dashboard" {
		t.Fatalf("record_metric must send 0 as a value, not drop it: %v", b)
	}
	if _, _, isErr = s.callTool("record_metric", map[string]any{"metric": "MET-1"}); !isErr {
		t.Fatal("record_metric without a value must fail")
	}
}
