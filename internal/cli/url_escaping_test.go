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
func TestNoRawResourcePathConcatenation(t *testing.T) {
	// Every resource whose segment is a user-supplied id, not just initiatives.
	// The first version of this guard watched "/initiatives/" alone, so the
	// identical bug survived for change sets and operations -- where it is worse,
	// because `idea change 'CHG-1/diff'` reaches a real, different endpoint and
	// prints a blank record with exit 0 rather than failing.
	raw := regexp.MustCompile(`\+\s*"/(initiatives|change-sets|operations)/"\s*\+`)
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
			// The helpers themselves concatenate, but they escape each segment
			// first, which is the whole point of them.
			if strings.Contains(line, "url.PathEscape") {
				continue
			}
			if raw.MatchString(line) {
				t.Errorf(`%s:%d builds a resource path by concatenation:
  %s
Use initiativeResource / changeSetResource / operationResource, which escape their segments.`,
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
	if got := changeSetResource("proj", "CHG-1/diff", ""); strings.Contains(got, "/diff") {
		t.Errorf("changeSetResource let an id reach a different endpoint: %s", got)
	}
	if got := operationResource("op/cancel", ""); strings.Contains(got, "/cancel") {
		t.Errorf("operationResource let an id reach a different endpoint: %s", got)
	}
	got := initiativeResource("proj", "../../health", "tasks")
	if strings.Contains(got, "../") {
		t.Errorf("initiativeResource left a traversal in the path: %s", got)
	}
	if want := "/api/v1/projects/proj/initiatives/..%2F..%2Fhealth/tasks"; got != want {
		t.Errorf("initiativeResource(...) = %q, want %q", got, want)
	}
}
