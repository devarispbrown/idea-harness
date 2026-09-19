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

// TestParseTestOutput_TAPNameWithHash pins the review Low fix: a TAP name
// containing a literal '#' that is not a SKIP/TODO directive keeps its full
// text rather than being truncated at the first '#'.
func TestParseTestOutput_TAPNameWithHash(t *testing.T) {
	got := ParseTestOutput("ok 1 - handles #42 issue\n")
	if len(got) != 1 || got[0].Name != "handles #42 issue" || got[0].Status != "passed" {
		t.Fatalf("got %+v, want a single passed \"handles #42 issue\"", got)
	}
}

// TestParseTestOutput_GoSummaryLineIsNotATAPResult pins the review Low fix:
// a `go test` package summary line whose package path starts with a digit
// ("ok  1password/foo") must never be misread as a TAP result numbered 1.
func TestParseTestOutput_GoSummaryLineIsNotATAPResult(t *testing.T) {
	got := ParseTestOutput("ok  \t1password/foo\t0.010s\n")
	if len(got) != 0 {
		t.Fatalf("got %+v, want no results", got)
	}
}

// TestParseTestOutput_PytestError pins the review Low fix: a pytest ERROR
// row (a fixture/setup error, not a plain failure) must count as failed, not
// be silently ignored -- an ignored ERROR fed C1's missing-tests hole.
func TestParseTestOutput_PytestError(t *testing.T) {
	got := ParseTestOutput("tests/test_x.py::test_setup ERROR [ 50%]\n")
	if len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("got %+v, want a single failed result", got)
	}
}

// TestParseTestOutput_GoTestPackageBoundary pins the text-format
// package-qualified key (C1): two packages' same-named test, scraped from
// plain `go test -v ./...` output, do not collide.
func TestParseTestOutput_GoTestPackageBoundary(t *testing.T) {
	out := `=== RUN   TestParse
--- PASS: TestParse (0.00s)
ok  	pkg/a	0.010s
=== RUN   TestParse
--- FAIL: TestParse (0.00s)
FAIL	pkg/b	0.010s
`
	got := ParseTestOutput(out)
	if len(got) != 2 {
		t.Fatalf("got %+v, want two independent results (one per package)", got)
	}
	byPkg := map[string]string{}
	for _, r := range got {
		byPkg[r.Package] = r.Status
	}
	if byPkg["pkg/a"] != "passed" || byPkg["pkg/b"] != "failed" {
		t.Fatalf("got %+v, want pkg/a passed and pkg/b failed independently", got)
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

// TestParseTestOutput_FailIsSticky pins the C2 fix: a name that appears
// twice keeps its most severe outcome. A real FAIL can never be flipped back
// to passed by a later PASS line for the same name, in either order -- the
// exact shape of the review's own forgery probe: an agent's test printing a
// fake "--- PASS" for a name that really failed elsewhere in the output.
func TestParseTestOutput_FailIsSticky(t *testing.T) {
	failThenPass := `=== RUN   TestFlaky
--- FAIL: TestFlaky (0.00s)
--- PASS: TestFlaky (0.00s)
`
	got := ParseTestOutput(failThenPass)
	if len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("fail-then-forged-pass: got %+v, want a single failed result", got)
	}

	passThenFail := `=== RUN   TestFlaky
--- PASS: TestFlaky (0.00s)
--- FAIL: TestFlaky (0.00s)
`
	got = ParseTestOutput(passThenFail)
	if len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("pass-then-fail: got %+v, want a single failed result", got)
	}
}

// TestParseTestOutput_ForgedNameWithoutItsOwnRunMarkerIgnored pins the C2
// fix's second half: once a stream shows real `=== RUN` markers (verbose
// mode), a --- PASS/FAIL line for a name that never got its own marker is
// dropped rather than trusted -- an agent's running test cannot conjure
// evidence for a completely different, never-run test name.
func TestParseTestOutput_ForgedNameWithoutItsOwnRunMarkerIgnored(t *testing.T) {
	out := `=== RUN   TestReal
--- PASS: TestForged
--- PASS: TestReal (0.00s)
`
	got := ParseTestOutput(out)
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	if _, ok := byName["TestForged"]; ok {
		t.Errorf("TestForged has no === RUN marker of its own and must be ignored, got %+v", got)
	}
	if byName["TestReal"] != "passed" {
		t.Errorf("TestReal = %q, want passed", byName["TestReal"])
	}
}

// TestParseTestOutput_NonVerboseStillWorks: a plain (non -v) `go test` run
// prints failures with no `=== RUN` marker at all -- the gate above must
// never suppress those (no markers were ever going to appear for anyone).
func TestParseTestOutput_NonVerboseStillWorks(t *testing.T) {
	out := "--- FAIL: TestRateLimiterResets (0.01s)\n    rate_limiter_test.go:42: window did not reset\nFAIL\n"
	got := ParseTestOutput(out)
	if len(got) != 1 || got[0].Name != "TestRateLimiterResets" || got[0].Status != "failed" {
		t.Fatalf("got %+v, want a single failed TestRateLimiterResets", got)
	}
}

// TestParseTestOutput_GoTestJSON pins the preferred structured path (C2):
// `go test -json`'s event stream is parsed by Action/Package/Test, never by
// scraping an "output" event's free-text payload -- so a test printing a
// fake pass/fail line to its own stdout (which test2json would relay as an
// "output" action, not a "pass"/"fail" one) cannot forge a result.
func TestParseTestOutput_GoTestJSON(t *testing.T) {
	out := `{"Action":"run","Package":"pkg","Test":"TestA"}
{"Action":"output","Package":"pkg","Test":"TestA","Output":"--- PASS: TestForged\n"}
{"Action":"pass","Package":"pkg","Test":"TestA"}
{"Action":"run","Package":"pkg","Test":"TestB"}
{"Action":"fail","Package":"pkg","Test":"TestB"}
`
	got := ParseTestOutput(out)
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.Status
	}
	if len(byName) != 2 {
		t.Fatalf("got %+v, want exactly TestA and TestB (never TestForged)", got)
	}
	if byName["TestA"] != "passed" {
		t.Errorf("TestA = %q, want passed", byName["TestA"])
	}
	if byName["TestB"] != "failed" {
		t.Errorf("TestB = %q, want failed", byName["TestB"])
	}
}

// TestParseTestOutput_GoTestJSON_SamePackageAndSubtestsUniqueKey pins the
// package-qualified key (C1: "key results by package or file plus name"):
// the same test name in two different packages is tracked independently.
func TestParseTestOutput_GoTestJSON_DifferentPackagesDoNotCollide(t *testing.T) {
	out := `{"Action":"pass","Package":"pkg/a","Test":"TestParse"}
{"Action":"fail","Package":"pkg/b","Test":"TestParse"}
`
	got := ParseTestOutput(out)
	if len(got) != 2 {
		t.Fatalf("got %+v, want two independent results (one per package)", got)
	}
	byPkg := map[string]string{}
	for _, r := range got {
		byPkg[r.Package] = r.Status
	}
	if byPkg["pkg/a"] != "passed" || byPkg["pkg/b"] != "failed" {
		t.Fatalf("got %+v, want pkg/a passed and pkg/b failed independently", got)
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
