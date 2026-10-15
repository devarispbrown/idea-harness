package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConflictSourcesStayNarrow pins the propagation rule in BOTH directions.
//
// `updateProviders` reached 409 through writeReloadErr and declared none, while
// its sibling `updateExecutors` reached 409 by the identical call and did
// declare it. The axis saw neither, because it seeded only on functions
// containing a literal `preconditionf`.
//
// The obvious widening — "follow any callee that writes StatusConflict" — is
// wrong, and wrong in the direction that destroys the guard rather than leaving
// it blind: `writeErr` itself contains a literal
// writeError(w, http.StatusConflict, …) in its *PreconditionError branch, and
// 75 functions call it, every read-only GET included. Measured, that rule takes
// conflict-capable operations from 15 to 78 and newly demands a spurious 409 on
// 57 of them.
//
// So this asserts both halves: the handler that needed finding is found, and
// the read-only handlers are not.
func TestConflictSourcesStayNarrow(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := conflictSources(filepath.Join(root, "internal", "daemon"))
	if err != nil {
		t.Fatal(err)
	}

	// The one this round exists for.
	if !sources["handleUpdateProviders"] {
		t.Error("handleUpdateProviders is not classified as 409-capable; it reaches " +
			"writeReloadErr, whose every branch writes 409")
	}
	// Its sibling, which was already declared and must stay found.
	if !sources["handleUpdateExecutors"] {
		t.Error("handleUpdateExecutors is not classified as 409-capable")
	}

	// The half that keeps the rule from eating the spec. These are read-only and
	// answer 404 or 500 through writeErr; if any is flagged, the rule has been
	// widened to follow writeErr and every GET now demands a 409.
	for _, h := range []string{
		"handleListProjects", "handleGetProject", "handleListInitiatives",
		"handleGetInitiative", "handleListTasks", "handleListFindings",
		"handleNext", "handleActivity", "handleListChangeSets", "handleGuidance",
	} {
		if sources[h] {
			t.Errorf("%s is classified as 409-capable. It is a read-only handler that reaches "+
				"409 only through writeErr's precondition branch, which every handler shares — "+
				"so the propagation rule has been widened past `every status this helper writes "+
				"is 409` and the spec will now demand a 409 on essentially every route.", h)
		}
	}

	// A blunt bound on the same property: the narrow rule classifies a handful of
	// handlers, not most of them. (35, not 30, since writeHarness -- the shared
	// settings write behind the integration, budget and automation editors --
	// reaches writeReloadErr exactly as handleUpdateExecutors does.)
	if len(sources) > 35 {
		var names []string
		for n := range sources {
			names = append(names, n)
		}
		t.Errorf("%d handlers are classified as 409-capable, which is far more than the "+
			"mutating set. The propagation rule is too wide: %s",
			len(sources), strings.Join(names, " "))
	}
}
