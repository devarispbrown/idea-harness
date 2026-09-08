package model

import (
	"encoding/json"
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
// cannot be cleared by anything the CLI can do.
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
