package main

import (
	"strings"
	"testing"
)

// The separator has to survive a literal that contains it. The docs are full of
// `pass + fail`-shaped prose, and a naive split on " + " would cut a predicate
// in half and then report a parse error on the halves -- so the split rejoins
// any fragment that does not begin a new predicate, which is exactly what a
// lookahead on the form prefixes would express. Go's RE2 has no lookahead.
func TestSplitPredicates(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"single", "absent:docs/a.md:hello", []string{"absent:docs/a.md:hello"}},
		{"paired", "present:docs/a.md:old + absent:docs/a.md:new",
			[]string{"present:docs/a.md:old", "absent:docs/a.md:new"}},
		{"literal containing the separator", "absent:docs/a.md:pass + fail are verdicts",
			[]string{"absent:docs/a.md:pass + fail are verdicts"}},
		{"paired, first literal containing it", "present:docs/a.md:pass + fail + absent:docs/a.md:new",
			[]string{"present:docs/a.md:pass + fail", "absent:docs/a.md:new"}},
		{"manual alongside", "manual:some reason + absent:docs/a.md:new",
			[]string{"manual:some reason", "absent:docs/a.md:new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitPredicates(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d predicates %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// R1: a row claiming the defect is gone must assert what is true now.
func TestRowAssertionsRequireAnAbsentPredicate(t *testing.T) {
	for _, tc := range []struct {
		name, status, predicate string
		wantProblem             bool
	}{
		{"fixed with only present", "fixed audit-8", "present:docs/a.md:old", true},
		{"fixed with only manual", "fixed audit-8", "manual:no literal to match", true},
		{"fixed with a pair", "fixed audit-8", "present:docs/a.md:old + absent:docs/a.md:new", false},
		{"fixed with only absent", "fixed audit-8", "absent:docs/a.md:new", false},
		{"open with only present", "open", "present:docs/a.md:old", false},
		{"deferred with only present", "deferred cost", "present:docs/a.md:old", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ledgerRow{
				file: "docs/plans/dx-audit-8-doc-defects.md", line: 1, id: "X1",
				cells: "| a claim about `docs/a.md` | the truth |", predicate: tc.predicate, status: tc.status,
			}
			got := checkRowAssertions(r, splitPredicates(tc.predicate))
			hasR1 := false
			for _, p := range got {
				if strings.Contains(p, "no `absent:` predicate") {
					hasR1 = true
				}
			}
			if hasR1 != tc.wantProblem {
				t.Fatalf("R1 fired = %v, want %v (problems: %q)", hasR1, tc.wantProblem, got)
			}
		})
	}
}

// R2: a predicate must name a file the row cites. D8d pointed at
// web/src/Settings.tsx for a claim about docs/security.md and passed on an
// import line -- a row that could never fail for the reason it gave.
func TestRowAssertionsRequireACitedPath(t *testing.T) {
	for _, tc := range []struct {
		name, cells, predicate string
		wantProblem            bool
	}{
		{"cited in the claim", "| `docs/a.md` says x | it does not |", "absent:docs/a.md:new", false},
		{"cited in the truth", "| a claim | `docs/a.md` actually says y |", "absent:docs/a.md:new", false},
		{"not cited anywhere", "| a claim about the UI | it is wrong |", "absent:web/src/Settings.tsx:x", true},
		{"cited, different file", "| `docs/security.md` says x | wrong |", "absent:web/src/Settings.tsx:x", true},
		{"manual has no path to check", "| a claim | truth |", "manual:a reason", false},
		{"a code path is fine when cited", "| `internal/cli/next.go` says x | wrong |", "absent:internal/cli/next.go:y", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ledgerRow{
				file: "docs/plans/dx-audit-8-doc-defects.md", line: 1, id: "X1",
				cells: tc.cells, predicate: tc.predicate, status: "open",
			}
			got := checkRowAssertions(r, splitPredicates(tc.predicate))
			hasR2 := false
			for _, p := range got {
				if strings.Contains(p, "never name") {
					hasR2 = true
				}
			}
			if hasR2 != tc.wantProblem {
				t.Fatalf("R2 fired = %v, want %v (problems: %q)", hasR2, tc.wantProblem, got)
			}
		})
	}
}

// The grandfather list is a literal set so a new ledger cannot join it by
// accident. Retrofitting the five files in it is ~68 hand-edits; adopting the
// rules for a round's own ledger is removing its file from the map.
func TestLegacyLedgersAreExemptButNewOnesAreNot(t *testing.T) {
	legacy := ledgerRow{
		file: "docs/plans/dx-audit-6-doc-defects.md", line: 1, id: "X1",
		cells: "| a claim | truth |", predicate: "present:docs/a.md:old", status: "fixed audit-6",
	}
	if got := checkRowAssertions(legacy, splitPredicates(legacy.predicate)); len(got) != 0 {
		t.Errorf("a legacy ledger row was checked: %q", got)
	}
	fresh := legacy
	fresh.file = "docs/plans/dx-audit-9-doc-defects.md"
	if got := checkRowAssertions(fresh, splitPredicates(fresh.predicate)); len(got) == 0 {
		t.Error("a new ledger's row was exempted; the grandfather list must be a literal set")
	}
}
