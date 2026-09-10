// Guards on the CI configuration itself.
//
// DX audit 7 found that .github/workflows/release.yml gated a tag on `go vet`
// and `go test` on one OS: none of the eight guards, no smoke, no macOS, no web
// tests, and no `needs:` on anything. A red enum-check or check-embed shipped.
// It had been open since audit 6 and was, with the spec's frozen info.version,
// one of the two longest-lived findings in the project.
//
// The fix moves the release job into ci.yml so `needs:` can reach the other
// jobs -- `needs:` resolves job ids within one workflow file and cannot
// reference another, which is why the report's own suggested two-line fix would
// not have worked. These tests are what stop it drifting apart again.
package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	// Under -trimpath runtime.Caller reports a relative path and this silently
	// produces garbage, failing later with a confusing message about ci.yml.
	// Nothing builds with -trimpath today; say so rather than debug it twice.
	if !filepath.IsAbs(root) {
		t.Skipf("runtime.Caller reported a relative path (%s); these guards read files "+
			"from the repository and cannot locate it under -trimpath", file)
	}
	return root
}

// releaseJobID is the job that publishes a tag.
const releaseJobID = "release"

// releaseIf is the exact condition under which a tag publishes. Compared whole:
// see TestReleaseGatesOnEveryCIJob for the two mutations a substring check
// let through.
const releaseIf = "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')"

// gateExemptions are the ci.yml jobs that deliberately do NOT gate a release,
// each with the reason. A job absent from both this list and the release job's
// `needs` fails the test below, which forces a decision rather than letting one
// be made by omission.
var gateExemptions = map[string]string{
	"agent": "workflow_dispatch-only, so it is skipped on a tag push -- and a " +
		"skipped job in `needs` would skip the release with it",
}

type workflow struct {
	Jobs map[string]struct {
		Needs           yaml.Node `yaml:"needs"`
		If              string    `yaml:"if"`
		ContinueOnError bool      `yaml:"continue-on-error"`
	} `yaml:"jobs"`
}

// TestReleaseGatesOnEveryCIJob is a completeness rule, not a membership rule,
// and the distinction is the whole value.
//
// A test asserting "needs names compile-no-node, macos, backend and web" pins
// today's four names and says nothing about the fifth job someone adds next
// year -- which is exactly how release.yml came to gate on almost nothing while
// ci.yml grew eight guards beside it. Here a new job matches none of the three
// branches and this fails, so the choice has to be made explicitly.
func TestReleaseGatesOnEveryCIJob(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	release, ok := wf.Jobs[releaseJobID]
	if !ok {
		t.Fatalf("ci.yml has no %q job; the release must live here so `needs` can "+
			"reach the other jobs -- `needs` cannot cross workflow files", releaseJobID)
	}

	needs := map[string]bool{}
	switch release.Needs.Kind {
	case yaml.ScalarNode:
		needs[release.Needs.Value] = true
	case yaml.SequenceNode:
		for _, n := range release.Needs.Content {
			needs[n.Value] = true
		}
	default:
		t.Fatalf("the %q job declares no `needs`; it would publish a tag without "+
			"waiting for any guard", releaseJobID)
	}

	for id := range wf.Jobs {
		switch {
		case id == releaseJobID:
		case needs[id]:
		case gateExemptions[id] != "":
		default:
			t.Errorf("ci.yml job %q neither gates the release nor is exempt.\n"+
				"Add it to the %q job's `needs`, or add it to gateExemptions with a reason.\n"+
				"A job that does neither publishes tags without being consulted, which is "+
				"the defect this test exists for.", id, releaseJobID)
		}
	}
	for id := range needs {
		if _, ok := wf.Jobs[id]; !ok {
			t.Errorf("the %q job needs %q, which is not a job in ci.yml", releaseJobID, id)
		}
	}

	// The WHOLE condition, not a substring of it. A `strings.Contains` check for
	// the push clause passed both of these:
	//
	//   if: github.event_name == 'push'                       -- no tag filter,
	//       so the job runs on every push to every branch holding contents:write
	//   if: (github.event_name == 'push' || github.event_name ==
	//       'workflow_dispatch') && startsWith(...)           -- reopening the
	//       dispatch-selects-a-tag hole this test exists to close
	//
	// Any change to the publishing condition should require touching this line,
	// which is what an equality check buys and a substring check does not.
	if got := strings.TrimSpace(release.If); got != releaseIf {
		t.Errorf("the %q job's publishing condition changed.\n got  %s\n want %s\n"+
			"Both halves are load-bearing: the tag filter, and the push requirement "+
			"(ci.yml accepts workflow_dispatch, and a dispatch can select a tag ref).",
			releaseJobID, got, releaseIf)
	}

	// A job in `needs` that cannot run on a tag push is worse than one missing
	// from it: a skipped dependency skips the dependent, so every release would
	// be silently skipped. Same for a gating job that swallows its own failure --
	// `needs` reads a continue-on-error job as successful.
	for id := range needs {
		job, ok := wf.Jobs[id]
		if !ok {
			continue // reported below
		}
		if strings.Contains(job.If, "workflow_dispatch") || strings.Contains(job.If, "schedule") {
			t.Errorf("the %q job gates on %q, which does not run on a tag push (%q).\n"+
				"A skipped dependency skips the dependent, so every release would be "+
				"skipped in silence.", releaseJobID, id, job.If)
		}
		if job.ContinueOnError {
			t.Errorf("the %q job gates on %q, which sets continue-on-error.\n"+
				"`needs` reads that job as successful however it ends, so it is not a gate.",
				releaseJobID, id)
		}
	}
}

// TestCITestSummaryCatchesEveryFailureShape pins scripts/ci-test-summary.py
// against the four ways a `go test` run can die, plus a clean run.
//
// The script used to fail only on an `action == "fail"` carrying a `Test`, and
// three of the four shapes emit no such event -- so the macOS job could go green
// while printing "All tests passed (0 package(s) with tests)". Gating a release
// on that job made it worth fixing before trusting it.
//
// The fixtures are real `go test -json` output, captured from programs written
// to produce each shape, not hand-authored: the whole defect was a wrong belief
// about which events go1.25 emits, and a hand-written fixture would have encoded
// the same belief.
func TestCITestSummaryCatchesEveryFailureShape(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH; ci-test-summary.py runs in the ubuntu and macOS jobs, " +
			"not in the container job that has no Python")
	}
	for _, tc := range []struct {
		fixture string
		shape   string
		wantErr bool
	}{
		{"gotest-a.json", "a package that does not compile", true},
		{"gotest-b.json", "a panic in TestMain", true},
		{"gotest-c.json", "a panic inside a test", true},
		{"gotest-d.json", "a panic in a background goroutine, which takes the binary down mid-test", true},
		{"gotest-e.json", "a clean run with one skipped test", false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			cmd := exec.Command("python3",
				filepath.Join(root, "scripts", "ci-test-summary.py"),
				filepath.Join(root, "scripts", "testdata", tc.fixture))
			out, err := cmd.CombinedOutput()
			if tc.wantErr && err == nil {
				t.Fatalf("ci-test-summary.py reported success for %s.\n"+
					"A job whose verdict comes from this script would be green over it.\n%s",
					tc.shape, out)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ci-test-summary.py failed on %s: %v\n%s", tc.shape, err, out)
			}
		})
	}
}

// The exit status is the second signal: no event in the stream can describe a
// JSON file that was truncated or never written.
func TestCITestSummaryHonoursTheExitStatus(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
	script := filepath.Join(root, "scripts", "ci-test-summary.py")
	clean := filepath.Join(root, "scripts", "testdata", "gotest-e.json")

	if out, err := exec.Command("python3", script, clean, "0").CombinedOutput(); err != nil {
		t.Fatalf("a clean run with status 0 must pass: %v\n%s", err, out)
	}
	if _, err := exec.Command("python3", script, clean, "1").CombinedOutput(); err == nil {
		t.Fatal("a non-zero `go test` status with no failure in the stream was reported as success; " +
			"that is what a truncated output file looks like")
	}

	// The three shapes that emit no terminal event at all. Each answered
	// "All tests passed (0 package(s) with tests)" before the status was wired
	// through -- on the job that gates a release.
	for _, tc := range []struct{ name, content string }{
		{"empty file", ""},
		{"not JSON", "go: cannot load module github.com/x\n"},
		{"truncated mid-line", `{"Time":"2026-01-01T00:00:00Z","Action":"run","Pack`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "gotest.json")
			if err := os.WriteFile(f, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.Command("python3", script, f, "1").CombinedOutput(); err == nil {
				t.Errorf("%s with a non-zero `go test` status was reported as success", tc.name)
			}
			if _, err := exec.Command("python3", script, f, "0").CombinedOutput(); err != nil {
				t.Errorf("%s with a zero status must not fail: %v", tc.name, err)
			}
		})
	}
}

// TestMacOSJobPassesTheExitStatus is the other half: the script grew the
// argument, and its only caller has to actually pass it. Without this, the
// unit test above passes while the release still gates on a job that swallows
// `go test`'s status -- which is exactly what a commit review found.
func TestMacOSJobPassesTheExitStatus(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name            string `yaml:"name"`
				Run             string `yaml:"run"`
				ContinueOnError bool   `yaml:"continue-on-error"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	var summary, test string
	var testContinues bool
	for _, st := range wf.Jobs["macos"].Steps {
		if strings.Contains(st.Run, "ci-test-summary.py") {
			summary = st.Run
		}
		if strings.Contains(st.Run, "go test -json") {
			test, testContinues = st.Run, st.ContinueOnError
		}
	}
	if summary == "" || test == "" {
		t.Fatal("the macos job no longer runs `go test -json` into ci-test-summary.py")
	}
	if !strings.Contains(test, "|| status=") {
		t.Errorf("the macos job's test step does not capture `go test`'s exit status:\n%s\n"+
			"Without it the summary cannot tell a clean run from an unwritten output file.", test)
	}
	if testContinues {
		t.Errorf("the macos job's test step sets continue-on-error, which swallows the status " +
			"the summary step needs")
	}
	if !strings.Contains(summary, "gotest.status") {
		t.Errorf("ci-test-summary.py is called without the exit status:\n%s", summary)
	}
}
