package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every document that walks a reader through approving a change set has to name
// the escape from a refused approval.
//
// `--accept-findings` was added to the code and to both demo guides, and to
// neither README.md nor docs/quickstart.md — the two documents that define the
// canonical magic path. A reader following the front page with a real model
// could hit a blocking finding and find no documented way past it, which is the
// live-lock the flag was added to remove, surviving in the only place most
// people read.
func TestReviewEscapeIsDocumentedWhereReviewIsDocumented(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	// Discovered, not listed. A hardcoded set of four omitted docs/ui.md, which
	// walks a reader through change-set approval and is squarely in the class
	// this check describes — the same failure the check exists to prevent,
	// committed by the check itself.
	docs, err := lintedDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("no documents to check — lintedDocs returned nothing, so this proves nothing")
	}
	checked := 0
	for _, rel := range docs {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		doc := string(b)
		if !strings.Contains(doc, "idea review") {
			continue // this document does not walk through a review
		}
		checked++
		// Either spelling: `--accept-findings` in CLI-facing docs, `accept_findings`
		// in the API reference, which documents the wire field rather than the flag.
		if !strings.Contains(doc, "--accept-findings") && !strings.Contains(doc, "accept_findings") {
			t.Errorf("%s walks a reader through `idea review ... approve` but never names\n"+
				"    `--accept-findings`. With a real model the reviewer can refuse, and this\n"+
				"    document leaves no way past it.", rel)
		}
	}
	if checked == 0 {
		t.Fatal("no document mentions `idea review` — the matcher is broken, so this proves nothing")
	}
	t.Logf("checked %d document(s) that walk through a review", checked)
}
