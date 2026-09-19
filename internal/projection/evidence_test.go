package projection

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestParseTestOutput_TAP pins node --test's TAP format (C2, wave 3
// contract): `ok`/`not ok` lines, and a SKIP directive ignored rather than
// reported as passed or failed.
func TestParseTestOutput_TAP(t *testing.T) {
	out := `TAP version 13
ok 1 - rate limiter blocks after N requests
not ok 2 - rate limiter resets after window
ok 3 - skipped case # SKIP not implemented yet
`
	got := ParseTestOutput(out)
	want := map[string]string{
		"rate limiter blocks after N requests": "passed",
		"rate limiter resets after window":     "failed",
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2 (SKIP must be ignored): %+v", len(got), got)
	}
	for _, r := range got {
		if want[r.Name] != r.Status {
			t.Errorf("%s: got %s, want %s", r.Name, r.Status, want[r.Name])
		}
	}
}

// TestParseTestOutput_GoTest pins `go test -v` output.
func TestParseTestOutput_GoTest(t *testing.T) {
	out := `=== RUN   TestRateLimiter
--- PASS: TestRateLimiter (0.00s)
=== RUN   TestRateLimiterResets
--- FAIL: TestRateLimiterResets (0.01s)
    rate_limiter_test.go:42: window did not reset
FAIL
`
	got := ParseTestOutput(out)
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	if byName["TestRateLimiter"] != "passed" {
		t.Errorf("TestRateLimiter = %s, want passed", byName["TestRateLimiter"])
	}
	if byName["TestRateLimiterResets"] != "failed" {
		t.Errorf("TestRateLimiterResets = %s, want failed", byName["TestRateLimiterResets"])
	}
}

// TestParseTestOutput_Pytest pins `pytest -v` output.
func TestParseTestOutput_Pytest(t *testing.T) {
	out := `tests/test_rate_limit.py::test_blocks_after_n PASSED [ 50%]
tests/test_rate_limit.py::test_resets_after_window FAILED [100%]
`
	got := ParseTestOutput(out)
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	if byName["tests/test_rate_limit.py::test_blocks_after_n"] != "passed" {
		t.Errorf("got %v", byName)
	}
	if byName["tests/test_rate_limit.py::test_resets_after_window"] != "failed" {
		t.Errorf("got %v", byName)
	}
}

// TestParseTestOutput_Jest pins jest-style ✓/✗ lines.
func TestParseTestOutput_Jest(t *testing.T) {
	out := `  ✓ blocks after N requests (12 ms)
  ✗ resets after window (3 ms)
`
	got := ParseTestOutput(out)
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	if byName["blocks after N requests"] != "passed" {
		t.Errorf("got %v", byName)
	}
	if byName["resets after window"] != "failed" {
		t.Errorf("got %v", byName)
	}
}

// TestParseTestOutput_LastResultWins: a name that appears twice (a rerun)
// keeps its LAST outcome, not its first.
func TestParseTestOutput_LastResultWins(t *testing.T) {
	out := `--- FAIL: TestFlaky (0.00s)
--- PASS: TestFlaky (0.00s)
`
	got := ParseTestOutput(out)
	if len(got) != 1 || got[0].Status != "passed" {
		t.Fatalf("got %+v, want a single passed result", got)
	}
}

// TestParseTestOutput_Unrecognized: lines matching none of the formats are
// silently ignored rather than misparsed.
func TestParseTestOutput_Unrecognized(t *testing.T) {
	got := ParseTestOutput("some random build log line\nanother one\n")
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

// TestAllTestsPassed pins the confirm_passing_tests rule (C2, wire contract):
// non-empty AND every entry passed. Mutation-check: an implementation that
// returns true for an empty slice would let confirm_passing_tests confirm a
// requirement with NO test evidence at all.
func TestAllTestsPassed(t *testing.T) {
	if AllTestsPassed(nil) {
		t.Error("nil evidence must not count as all-passed")
	}
	if AllTestsPassed([]domain.EvidenceTest{}) {
		t.Error("empty evidence must not count as all-passed")
	}
	if !AllTestsPassed([]domain.EvidenceTest{{Name: "a", Status: "passed"}, {Name: "b", Status: "passed"}}) {
		t.Error("all passed must count as all-passed")
	}
	if AllTestsPassed([]domain.EvidenceTest{{Name: "a", Status: "passed"}, {Name: "b", Status: "failed"}}) {
		t.Error("one failed entry must not count as all-passed")
	}
}
