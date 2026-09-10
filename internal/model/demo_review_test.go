package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// The demo reviewer returned `{"findings":[]}` unconditionally, so the review
// loop always converged and `needs_human_judgment` was structurally unreachable
// on the only path CI, scripts/smoke.sh and six DX audits ever ran.
//
// This pins the shape of the blocking finding that fixes that. schema.Review
// requires five fields per item; a response missing one fails DecodeStructured,
// and runModel retries three times and then fails the initiative -- which looks
// like a blocked review from a distance and is a different thing entirely.
func TestDemoBlockingReviewHasEveryRequiredField(t *testing.T) {
	var out struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(DemoBlockingReviewJSON), &out); err != nil {
		t.Fatalf("blocking review JSON does not parse: %v", err)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("got %d findings, want exactly 1", len(out.Findings))
	}
	f := out.Findings[0]
	// The five schema.Review marks required. evidence and impact are optional.
	for _, field := range []string{"severity", "confidence", "category", "finding", "recommendation"} {
		v, ok := f[field]
		if !ok {
			t.Errorf("missing required field %q; the run would fail rather than the review blocking", field)
			continue
		}
		if s, isStr := v.(string); !isStr || s == "" {
			t.Errorf("field %q = %v, want a non-empty string", field, v)
		}
	}
	// `high` is what makes EvaluateApproval block: it gates on
	// Severity.Blocking(), which is critical-or-high.
	if f["severity"] != "high" && f["severity"] != "critical" {
		t.Errorf("severity = %v, which does not block the review", f["severity"])
	}
}

// The queue is drained by the first `review` calls a run makes, which are M1's
// review cycles. This pins that it is a queue and not a standing response --
// all three adversarial reviews share the schema name `review`, and a standing
// response would also block the change-set review, where a blocking finding
// could not be cleared by anything the CLI could do (until
// `--accept-findings`, which clears the reviewer's own findings, not a deny code).
func TestBlockingReviewIsQueuedNotStanding(t *testing.T) {
	f := NewFakeProvider()
	f.EnqueueResponse("review", DemoBlockingReviewJSON)

	req := Request{JSONSchemaName: "review", Messages: []Message{{Role: "user", Content: "x"}}}
	first, err := f.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.RawJSON != DemoBlockingReviewJSON {
		t.Fatalf("first review call did not get the blocking finding: %s", first.RawJSON)
	}
	second, err := f.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.RawJSON == DemoBlockingReviewJSON {
		t.Fatal("the blocking finding is standing, not queued: it would also block the plan " +
			"and change-set reviews, and a blocking change-set finding is unrecoverable")
	}
}

// The dangling plan is only useful if it parses. A response missing a field
// schema.ExecutionPlan requires fails DecodeStructured, and runModel retries
// three times and then FAILS the initiative — which looks like the block this
// exists to produce and is a different thing entirely.
func TestDemoDanglingPlanHasEveryRequiredField(t *testing.T) {
	var out struct {
		Summary string           `json:"summary"`
		Tasks   []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(DemoDanglingPlanJSON), &out); err != nil {
		t.Fatalf("dangling plan JSON does not parse: %v", err)
	}
	if out.Summary == "" {
		t.Error("summary is required and empty")
	}
	if len(out.Tasks) != 1 {
		t.Fatalf("got %d tasks, want exactly 1", len(out.Tasks))
	}
	for _, field := range []string{"title", "statement", "requirement", "priority"} {
		v, ok := out.Tasks[0][field]
		if !ok {
			t.Errorf("missing required task field %q; the run would fail rather than park", field)
			continue
		}
		if s, isStr := v.(string); !isStr || s == "" {
			t.Errorf("task field %q = %v, want a non-empty string", field, v)
		}
	}
}

// And it must actually dangle. If the dependency normalises to the task's own
// title the plan resolves cleanly, the reason is never recorded, and the smoke
// pass built on it asserts nothing.
func TestDemoDanglingPlanActuallyDangles(t *testing.T) {
	var out struct {
		Tasks []struct {
			Title     string   `json:"title"`
			DependsOn []string `json:"depends_on"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(DemoDanglingPlanJSON), &out); err != nil {
		t.Fatal(err)
	}
	// The same normaliser the engine uses to match a depends_on title to a task.
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	titles := map[string]bool{}
	for _, task := range out.Tasks {
		titles[norm(task.Title)] = true
	}
	dangling := 0
	for _, task := range out.Tasks {
		for _, dep := range task.DependsOn {
			if !titles[norm(dep)] {
				dangling++
			}
		}
	}
	if dangling == 0 {
		t.Fatal("every depends_on resolves to a task, so this plan is approved rather than " +
			"parked on plan_dependencies")
	}
}

// The plan review has its own queue key so a surplus cannot outlive it and land
// on the change-set review, where a blocking finding cannot be cleared by
// anything the CLI can do. Without the fake knowing the key, the response falls
// to `default` — `{}` — and the run fails instead of the review blocking.
func TestPlanReviewSchemaNameStillDecodes(t *testing.T) {
	f := NewFakeProvider()
	req := Request{JSONSchemaName: "plan_review", Messages: []Message{{Role: "user", Content: "x"}}}
	resp, err := f.Complete(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.RawJSON), &out); err != nil {
		t.Fatalf("plan_review default response does not parse: %q", resp.RawJSON)
	}
	if _, ok := out["findings"]; !ok {
		t.Fatalf("plan_review default response has no findings key: %q; ParseFindings errors and "+
			"runModel fails the initiative instead of the review passing", resp.RawJSON)
	}
}
