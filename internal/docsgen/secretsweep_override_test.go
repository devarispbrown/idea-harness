package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretSweepOverrideDocMatchesCode guards review changes #1 (ICP round):
// docs/security-model.md documents secret_sweep as conditionally overridable
// by a human's not_a_secret disposition, mirroring how review_open_blocking is
// documented. This fails if either side drifts from the other -- the doc's
// claim is removed or weakened, or the code's disposition enum / its scoping
// to secret_sweep findings is removed -- so the two cannot silently disagree
// the way risk.go's un-overridable marker block and its promised "the task
// will be re-planned" did.
func TestSecretSweepOverrideDocMatchesCode(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}

	doc, err := os.ReadFile(filepath.Join(root, "docs", "security-model.md"))
	if err != nil {
		t.Fatal(err)
	}
	docText := string(doc)

	// The claim must sit with secret_sweep's own row/section, not just exist
	// somewhere in the file -- so grab the paragraph naming secret_sweep and
	// check the override claim is IN it, the same locality claimscheck.go
	// enforces for a canonical claim's restatements.
	idx := strings.Index(docText, "`secret_sweep`")
	if idx < 0 {
		t.Fatal("docs/security-model.md no longer documents secret_sweep at all")
	}
	// The table row is one line: cut there, not at a fixed window, or a
	// neighbouring row's own "Conditionally overridable" text (review_open_
	// blocking's) would satisfy this check without secret_sweep's own claim
	// saying anything at all.
	rest := docText[idx:]
	section := rest
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		section = rest[:nl]
	}
	if !strings.Contains(section, "Conditionally overridable") {
		t.Error("docs/security-model.md must document secret_sweep as \"Conditionally overridable\", " +
			"mirroring review_open_blocking -- the doc and the not_a_secret code path have drifted apart")
	}
	if !strings.Contains(section, "not_a_secret") {
		t.Error("docs/security-model.md's secret_sweep override claim must name the not_a_secret disposition")
	}
	if !strings.Contains(docText, "review_open_blocking") {
		t.Fatal("the mirroring claim references review_open_blocking, which must still be documented too")
	}

	// The code side: the enum value exists and is scoped to secret_sweep
	// findings in the store layer, not left as a declared-but-unenforced case.
	enums, err := os.ReadFile(filepath.Join(root, "internal", "domain", "enums.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enums), "DispositionNotASecret") {
		t.Error("internal/domain/enums.go no longer declares DispositionNotASecret; " +
			"docs/security-model.md still promises a not_a_secret override")
	}

	repo, err := os.ReadFile(filepath.Join(root, "internal", "store", "repo.go"))
	if err != nil {
		t.Fatal(err)
	}
	repoText := string(repo)
	if !strings.Contains(repoText, "DispositionNotASecret") || !strings.Contains(repoText, "secret_sweep") {
		t.Error("internal/store/repo.go's SetFindingDisposition no longer scopes DispositionNotASecret " +
			"to secret_sweep findings; the documented override would no longer be enforced")
	}
}
