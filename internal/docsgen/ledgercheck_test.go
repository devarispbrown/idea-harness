package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvalPredicate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.md"),
		[]byte("There is no project switcher.\nSee http://example.com/a:b for more.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name      string
		pred      string
		defect    bool
		checkable bool
		wantErr   bool
	}{
		{name: "absent: correction present means fixed", pred: "absent:doc.md:no project switcher"},
		{name: "absent: correction gone means the defect is back",
			pred: "absent:doc.md:a sentence nobody wrote", defect: true, checkable: true},
		{name: "present: false claim gone means fixed", pred: "present:doc.md:a sentence nobody wrote"},
		{name: "present: false claim still there", pred: "present:doc.md:project switcher", defect: true, checkable: true},
		// The literal may contain colons; only the first two are separators.
		// Documentation is full of `http://` and `file:line`, so a naive
		// three-way split would mangle exactly the predicates worth writing.
		{name: "a literal containing colons", pred: "absent:doc.md:http://example.com/a:b"},
		{name: "manual asserts nothing", pred: "manual:not mechanically checkable"},
		{name: "manual needs a reason", pred: "manual:", wantErr: true},
		{name: "unknown form", pred: "maybe:doc.md:x", wantErr: true},
		{name: "no form prefix", pred: "doc.md", wantErr: true},
		{name: "no literal", pred: "absent:doc.md", wantErr: true},
		{name: "missing file", pred: "absent:nope.md:x", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			defect, checkable, err := evalPredicate(root, c.pred)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			// checkable defaults false only for manual; every other case above
			// that is not an error is checkable.
			wantCheckable := c.checkable || c.pred[:6] != "manual"
			if checkable != wantCheckable {
				t.Errorf("checkable = %v, want %v", checkable, wantCheckable)
			}
			if defect != c.defect {
				t.Errorf("defectPresent = %v, want %v", defect, c.defect)
			}
		})
	}
}

func TestExpandIDs(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"C1, C2, C3", []string{"C1", "C2", "C3"}},
		{"C1-C7", []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}},
		// An en dash, because a Markdown file written by a human has both.
		{"D1–D3", []string{"D1", "D2", "D3"}},
		{"C4 (a and b; c deferred)", []string{"C4"}},
		// Prose that names no id must not be guessed at: a check that invents a
		// claim invents a failure.
		{"S1's isolation table", nil},
		{"G1a, G1b", []string{"G1a", "G1b"}},
		{"", nil},
	} {
		got := expandIDs(c.in)
		if len(got) != len(c.want) {
			t.Errorf("expandIDs(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("expandIDs(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// TestLedgerCheckOnTheRealLedgers is the guard's own smoke test: it runs against
// the checked-in ledgers, so a predicate that stops parsing fails here as well
// as in CI.
func TestLedgerCheckOnTheRealLedgers(t *testing.T) {
	if err := runLedgerCheck(); err != nil {
		t.Fatalf("the checked-in ledgers do not pass their own check:\n%v", err)
	}
}
