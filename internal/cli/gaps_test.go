package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// seedGaps serves a two-gap report: one high, one low.
func (f *fakeDaemon) seedGaps() {
	f.seedInitiative()
	report := map[string]any{
		"initiative_id": "ini-1",
		"gaps": []map[string]any{
			{
				"kind": "unimplemented_requirement", "state": "not_applied", "severity": "high",
				"nodes":      []map[string]any{{"type": "requirement", "id": "req-1", "code": "REQ-1"}, {"type": "task", "id": "task-1", "code": "TASK-1"}},
				"why":        "REQ-1 is planned (TASK-1) but no change set implementing it has been applied",
				"next":       "idea review CHG-1 approve",
				"dependents": 0, "initiative_id": "ini-1",
			},
			{
				"kind": "untested_acceptance_criterion", "state": "no_evidence", "severity": "low",
				"nodes":      []map[string]any{{"type": "acceptance_criterion", "id": "req-1:0", "code": "REQ-1/AC-1"}},
				"why":        "REQ-1/AC-1 has no pass or fail evidence",
				"next":       "idea verify REQ-1 --pass|--fail|--waive",
				"dependents": 0, "initiative_id": "ini-1",
			},
		},
		"summary": map[string]any{
			"total":       2,
			"by_severity": map[string]int{"critical": 0, "high": 1, "medium": 0, "low": 1, "observation": 0},
			"by_kind":     map[string]int{"unimplemented_requirement": 1, "untested_acceptance_criterion": 1},
		},
	}
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/gaps", report)
	f.json("GET /api/v1/projects/{projectId}/gaps", report)
}

func TestGapsHumanOutputGroupsByKindWithNextCommand(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedGaps()
	stdout, _, err := run(t, "gaps")
	if err != nil {
		t.Fatalf("idea gaps with no --fail-on must exit 0 whatever it finds: %v", err)
	}
	for _, want := range []string{
		"Gaps · 2", "(1 high · 1 low)",
		"unimplemented_requirement · 1", "high   REQ-1", "→ idea review CHG-1 approve",
		"untested_acceptance_criterion · 1", "REQ-1/AC-1", "→ idea verify REQ-1 --pass|--fail|--waive",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Index(stdout, "unimplemented_requirement") > strings.Index(stdout, "untested_acceptance_criterion") {
		t.Errorf("groups out of the daemon's (severity) order:\n%s", stdout)
	}
}

func TestGapsJSONShape(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedGaps()
	stdout, _, err := run(t, "gaps", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		InitiativeID string `json:"initiative_id"`
		Gaps         []struct {
			Kind, State, Severity, Why, Next string
			Nodes                            []struct{ Type, ID, Code string }
		} `json:"gaps"`
		Summary struct {
			Total      int            `json:"total"`
			BySeverity map[string]int `json:"by_severity"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if got.InitiativeID != "ini-1" || len(got.Gaps) != 2 || got.Summary.Total != 2 || got.Summary.BySeverity["high"] != 1 {
		t.Errorf("report = %+v", got)
	}
	g := got.Gaps[0]
	if g.Kind != "unimplemented_requirement" || g.State != "not_applied" || g.Next == "" || g.Nodes[0].Code != "REQ-1" {
		t.Errorf("first gap = %+v", g)
	}
}

// --fail-on is the CI gate: exit 5 (gate_unmet) when any gap is at or above
// the severity, exit 0 when none is.
func TestGapsFailOnExitCode(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedGaps()
	colorEnabled = false
	for _, tc := range []struct {
		failOn string
		want   int
	}{
		{"high", 5}, {"low", 5}, {"critical", 0},
	} {
		code := Main([]string{"gaps", "--fail-on", tc.failOn}, io.Discard, io.Discard)
		if code != tc.want {
			t.Errorf("--fail-on %s exit = %d, want %d", tc.failOn, code, tc.want)
		}
	}

	// Under --json the report is still the one document on stdout, and the
	// error object goes to stderr.
	var out, errOut bytes.Buffer
	if code := Main([]string{"gaps", "--json", "--fail-on", "high"}, &out, &errOut); code != 5 {
		t.Fatalf("--json --fail-on high exit = %d, want 5", code)
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"gaps"`) {
		t.Errorf("stdout is not the report: %q", out.String())
	}
	if !strings.Contains(errOut.String(), `"gate_unmet"`) || !strings.Contains(errOut.String(), "1 gap(s) at or above high") {
		t.Errorf("stderr = %q", errOut.String())
	}

	_, _, err := run(t, "gaps", "--fail-on", "hgih")
	var ce *cliError
	if !errors.As(err, &ce) || ce.kind != kindUsage {
		t.Errorf("a mistyped severity must be a usage error, not a passed gate: %v", err)
	}
}

func TestGapsKindAndAllReachTheDaemon(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedGaps()
	if _, _, err := run(t, "gaps", "--kind", "orphan_task,stale_artifact"); err != nil {
		t.Fatal(err)
	}
	if !f.asked("/initiatives/ini-1/gaps?kind=orphan_task%2Cstale_artifact") {
		t.Errorf("--kind not forwarded; requests: %v", f.requests)
	}
	if _, _, err := run(t, "gaps", "--all"); err != nil {
		t.Fatal(err)
	}
	if !f.asked("GET /api/v1/projects/p1/gaps") {
		t.Errorf("--all did not use the project route; requests: %v", f.requests)
	}
	if _, _, err := run(t, "gaps", "ini-1", "--all"); err == nil {
		t.Error("an initiative with --all must be refused, not silently ignored")
	}
}

func TestGapsNoneSaysSo(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/gaps", map[string]any{"gaps": []any{}, "summary": map[string]any{"total": 0}})
	stdout, _, err := run(t, "gaps", "--fail-on", "observation")
	if err != nil {
		t.Fatalf("no gaps must pass any gate: %v", err)
	}
	if !strings.Contains(stdout, "No gaps") {
		t.Errorf("stdout = %q", stdout)
	}
}

// The guidance hook: `idea status` prints the gap counts guidance carries,
// and nothing when there are none.
func TestStatusPrintsGapSummary(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/guidance", map[string]any{
		"action": "Verify", "why": "every task is done",
		"gap_summary": map[string]any{"total": 3, "by_severity": map[string]int{"high": 1, "low": 2}, "by_kind": map[string]int{}},
	})
	stdout, _, _ := run(t, "status")
	if !strings.Contains(stdout, "gaps: 1 high · 2 low — idea gaps") {
		t.Errorf("status lacks the gap line:\n%s", stdout)
	}
}

func TestAssumptionCommandResolvesPrefixAndPostsStatus(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/assertions", []map[string]any{
		{"id": "abcd1234ffff", "type": "assumption", "statement": "users have one meter"},
		{"id": "abcd9999ffff", "type": "hypothesis", "statement": "usage billing lifts revenue"},
	})
	var body map[string]string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/assertions/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("id"), "status": body["status"]})
	})

	stdout, _, err := run(t, "assumption", "abcd1234", "retract")
	if err != nil {
		t.Fatal(err)
	}
	if body["status"] != "retracted" || !f.asked("/assertions/abcd1234ffff/status") || !strings.Contains(stdout, "retracted") {
		t.Errorf("body=%v stdout=%q requests=%v", body, stdout, f.requests)
	}

	if _, _, err := run(t, "assumption", "abcd", "accept"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("ambiguous prefix: %v", err)
	}
	if _, _, err := run(t, "assumption", "zzzz", "accept"); err == nil || !strings.Contains(err.Error(), "no assumption matching") {
		t.Errorf("unknown ref: %v", err)
	}
	if _, _, err := run(t, "assumption", "abcd1234", "approve"); err == nil || !strings.Contains(err.Error(), "accept|dispute|retract") {
		t.Errorf("bad verdict: %v", err)
	}
}
