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
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// releaseJobID is the job that publishes a tag.
const releaseJobID = "release"

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
		Needs yaml.Node `yaml:"needs"`
		If    string    `yaml:"if"`
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

	// The release must not be reachable by a manual dispatch against a tag ref:
	// ci.yml carries workflow_dispatch, and a dispatch can select a tag, which
	// would be a publishing path release.yml never had.
	if !strings.Contains(release.If, "github.event_name == 'push'") {
		t.Errorf("the %q job's `if` does not require a push (%q).\n"+
			"ci.yml accepts workflow_dispatch and a dispatch can select a tag ref, "+
			"so a ref test alone opens a release path that did not exist before.",
			releaseJobID, release.If)
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
}
