package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoRawInitiativePathConcatenation is a source guard, and it exists because
// the fix it protects was already made once.
//
// Audit 1 added initiativeResource() precisely so initiative-scoped URLs would
// have their segments escaped. Audit 2 found sixteen call sites still building
// the same path by concatenation -- the helper existed, was correct, and was
// simply bypassed. That is the propagation failure this whole round is about,
// and a unit test on the helper would not have caught it, because the helper
// was never wrong.
func TestNoRawInitiativePathConcatenation(t *testing.T) {
	raw := regexp.MustCompile(`\+\s*"/initiatives/"\s*\+`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for i, line := range strings.Split(string(src), "\n") {
			if raw.MatchString(line) {
				t.Errorf(`%s:%d builds an initiative path by concatenation:
  %s
Use initiativeResource(projectID, initiativeID, resource), which escapes its segments.`,
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no files; this test is not reading the package")
	}
}

// TestInitiativeResourceEscapesItsSegments pins the behaviour the call sites now
// depend on.
func TestInitiativeResourceEscapesItsSegments(t *testing.T) {
	got := initiativeResource("proj", "../../health", "tasks")
	if strings.Contains(got, "../") {
		t.Errorf("initiativeResource left a traversal in the path: %s", got)
	}
	if want := "/api/v1/projects/proj/initiatives/..%2F..%2Fhealth/tasks"; got != want {
		t.Errorf("initiativeResource(...) = %q, want %q", got, want)
	}
}
