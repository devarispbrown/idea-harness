package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ledgerGlob finds the defect ledgers. A ledger records what an audit found and
// what happened to it; the guard exists because a ledger nobody re-checks
// quietly becomes a list of claims, which is the failure it was created to
// prevent one level down.
//
// Globbed rather than listed: a hardcoded list meant the next round's ledger was
// unguarded until somebody remembered to edit Go source, and nothing would have
// said so.
const ledgerGlob = "docs/plans/dx-audit-*-doc-defects.md"

// ledgerRow is one row of a defect table.
type ledgerRow struct {
	file      string // the ledger the row is in
	line      int
	id        string // U1, A6, …
	cells     string // the Claim and Truth cells, which R2 reads for cited paths
	predicate string
	status    string // open | fixed <commit> | deferred <reason>
}

// A row is `| id | claim | truth | `predicate` | status |`. The predicate is
// matched inside backticks because an unquoted one would be reformatted by any
// Markdown tool that touches the file, and because a backtick is the one
// character a predicate may then not contain — which keeps the capture
// unambiguous. Pipes are a separate matter: the first two cells must be
// pipe-free, and a row that breaks that is reported by ledgerRowStartRe rather
// than skipped.
// ledgerRowStartRe recognises the shape of a row without validating it, so a
// malformed row is reported rather than silently dropped.
//
// Deliberately permissive about the id: the first version required
// `[A-Z]+[0-9]+` with no indent and no decoration, so `D19a`, `  | D19 |`,
// `| **D19** |` and “ | `D19` | “ all slipped past both regexes and vanished
// with no error and no change to the row count. Letter-suffixed ids are the
// house convention -- expandIDs supports them, deferred.md uses `C4c` -- so the
// shape this guard was least able to see was the one most likely to be written.
var ledgerRowStartRe = regexp.MustCompile("^\\s*\\|\\s*[*`_]*[A-Za-z][A-Za-z0-9-]*[0-9][a-z]?[*`_]*\\s*\\|")

var ledgerRowRe = regexp.MustCompile("^\\s*\\|\\s*[*`_]*([A-Za-z][A-Za-z0-9-]*[0-9][a-z]?)[*`_]*\\s*\\|((?:[^|]*\\|){2})\\s*`([^`]+)`\\s*\\|\\s*([^|]+?)\\s*\\|\\s*$")

// runLedgerCheck evaluates every row's predicate and reports both directions of
// drift: a row marked open whose defect is gone (stale-open), and a row marked
// fixed whose defect returned (regression).
//
// The asymmetry matters. An earlier specification of this guard was "fail when
// a row marked open names a file:line that no longer contains the claimed
// text" — which the ledgers could not satisfy, because no row carried a
// file:line. It would have passed on both files while certifying the exact
// stale state it existed to catch. That is why the predicate is a required
// column rather than something inferred from prose.
func runLedgerCheck() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	found, err := filepath.Glob(filepath.Join(root, ledgerGlob))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("no ledger matched %s; this check is not reading anything", ledgerGlob)
	}
	sort.Strings(found)

	var rows []ledgerRow
	for _, path := range found {
		lf, _ := filepath.Rel(root, path)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			m := ledgerRowRe.FindStringSubmatch(line)
			if m == nil {
				// A row that opens like a ledger row but does not parse is an
				// error, not something to skip. Skipping in silence is how a
				// guard goes blind: an escaped pipe or a backtick inside the
				// predicate dropped a row from the count and nothing said so.
				if ledgerRowStartRe.MatchString(line) {
					return fmt.Errorf("%s:%d looks like a ledger row but does not parse — "+
						"a predicate may not contain a backtick, and a cell may not contain an escaped pipe:\n    %s",
						lf, i+1, strings.TrimSpace(line))
				}
				continue
			}
			rows = append(rows, ledgerRow{file: lf, line: i + 1, id: m[1], cells: m[2], predicate: m[3], status: m[4]})
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("no ledger rows parsed — the table format changed and this check went blind")
	}

	var problems []string
	manual := 0
	for _, r := range rows {
		preds := splitPredicates(r.predicate)
		problems = append(problems, checkRowAssertions(r, preds)...)

		wantDefect, err := statusExpectsDefect(r.status)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s:%d %s: %v", r.file, r.line, r.id, err))
			continue
		}
		// EVERY predicate must agree with the status. A row is one claim; if its
		// predicates disagree with each other the row asserts nothing, and
		// evaluating only the first would make the rest decorative.
		anyCheckable := false
		for _, pred := range preds {
			defectPresent, checkable, err := evalPredicate(root, pred)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s:%d %s: %v", r.file, r.line, r.id, err))
				continue
			}
			if !checkable {
				continue
			}
			anyCheckable = true
			if defectPresent == wantDefect {
				continue
			}
			if wantDefect {
				problems = append(problems, fmt.Sprintf(
					"%s:%d %s is marked %q but its predicate no longer holds — the defect is gone and the row was not updated\n    predicate: %s",
					r.file, r.line, r.id, r.status, pred))
			} else {
				problems = append(problems, fmt.Sprintf(
					"%s:%d %s is marked %q but its predicate holds — the defect is present\n    predicate: %s",
					r.file, r.line, r.id, r.status, pred))
			}
		}
		if !anyCheckable {
			manual++
		}
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		return fmt.Errorf("%d ledger row(s) disagree with the tree:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
	// The second axis of the same bookkeeping problem: a ledger can be accurate
	// while the plan that produced it claims work it did not do.
	if err := runCoverageCheck(root); err != nil {
		return err
	}

	fmt.Printf("ledger-check: %d rows checked, %d mechanically (%d manual); plan coverage claims agree with deferred.md\n",
		len(rows), len(rows)-manual, manual)
	return nil
}

// evalPredicate reports whether the defect a predicate describes is present.
// The second return is false for `manual:` rows, which assert nothing.
func evalPredicate(root, pred string) (defectPresent, checkable bool, err error) {
	form, rest, ok := strings.Cut(pred, ":")
	if !ok {
		return false, false, fmt.Errorf("predicate %q has no form prefix (absent:, present: or manual:)", pred)
	}
	if form == "manual" {
		if strings.TrimSpace(rest) == "" {
			return false, false, fmt.Errorf("manual: predicate needs a reason")
		}
		return false, false, nil
	}

	// Only the first two colons are separators; the literal may contain any
	// number of its own, which docs full of `http://` and `file:line` do.
	relPath, literal, ok := strings.Cut(rest, ":")

	// An escape sequence in the LITERAL is always a mistake, and a silent one.
	// A row wrote `cannot be cleared by anything the CLI\ncan do` meaning "the
	// source wraps here"; normalizeWrapping collapses REAL newlines, so those
	// two characters could never match. The `present:` clause was permanently
	// false, the row passed as fixed, and the false claim it pinned stayed in
	// the tree for a round.
	//
	// The literal only, not the path: a predicate pinning a real source line
	// that contains `\n` -- `fmt.Fprintf(stdout, "%s\n", ...)` is everywhere in
	// this codebase -- is legitimate and must not be rejected. Checking `rest`
	// banned those outright.
	//
	// This closes one way to be unmatchable, not the class: a typo'd literal is
	// equally unmatchable and equally green. Recorded in deferred.md rather
	// than claimed closed.
	if strings.Contains(literal, `\n`) || strings.Contains(literal, `\t`) {
		return false, false, fmt.Errorf(
			"predicate literal %q contains an escape sequence.\n"+
				"    Source wrapping is normalised already, so write it on one line; as written it\n"+
				"    can never match, and an unmatchable `present:` is indistinguishable from a\n"+
				"    fixed defect", literal)
	}
	if !ok || strings.TrimSpace(literal) == "" {
		return false, false, fmt.Errorf("predicate %q needs <path>:<literal>", pred)
	}
	// Confined to the repository, and never at a ledger. An unconfined path
	// (`../../../go.mod`) resolved outside the tree and was permanently
	// satisfied, and a predicate naming its own ledger is satisfied by the
	// predicate cell's own text -- a row that can never go stale and never
	// regress, which is a row that asserts nothing.
	full := filepath.Join(root, filepath.Clean(relPath))
	if !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return false, false, fmt.Errorf("predicate path %q escapes the repository", relPath)
	}
	if matched, _ := filepath.Match(filepath.Join(root, ledgerGlob), full); matched {
		return false, false, fmt.Errorf("predicate names a defect ledger (%s); "+
			"the predicate cell's own text would satisfy it", relPath)
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return false, false, fmt.Errorf("predicate names %s: %w", relPath, err)
	}
	found := strings.Contains(normalizeWrapping(string(raw)), normalizeWrapping(literal))

	switch form {
	case "absent":
		// The defect is the *absence* of the correction: present means fixed.
		return !found, true, nil
	case "present":
		// The defect is the presence of the false claim.
		return found, true, nil
	default:
		return false, false, fmt.Errorf("unknown predicate form %q (want absent:, present: or manual:)", form)
	}
}

// ---------- coverage claims vs. deferred.md ----------

// deferredPath is the append-only record of what each round decided not to do.
const deferredPath = "docs/plans/deferred.md"

// coverageClaimRe matches a plan's coverage sentence. The ids after it are what
// that round claims it mechanically verified.
//
// (?s) and a non-greedy run to the first period, because the sentence wraps:
// the anchored single-line version silently matched nothing the moment a claim
// list grew past the file's ~78-column wrap, which turned the check off exactly
// when the claim got long enough to be worth checking. The plan file this reads
// wraps its Partially: line already.
var coverageClaimRe = regexp.MustCompile(`(?s)(?m)^Mechanically checked:\s*(.+?)\.\s`)

// coveragePrefixRe finds the sentence's opening independently, so a claim that
// is present but unparseable is an error rather than a silent skip.
var coveragePrefixRe = regexp.MustCompile(`(?m)^Mechanically checked:`)

// partialClaimRe matches the escape hatch: an item done in part, which may
// legitimately also appear in deferred.md.
var partialClaimRe = regexp.MustCompile(`(?s)(?m)^Partially:\s*(.+?)\.\s`)

// partialEntryRe requires each Partially: entry to name its carve-out in
// parentheses: `C4 (a and b; c deferred)`.
//
// Without it the line was a bare suppression list with no substantiation, so a
// single line -- `Partially: C1-C7, G2, S6` -- turned every contradiction in a
// plan green. Ranges are refused for the same reason: "partially" is a claim
// about one item, and a range is a claim about seven.
var partialEntryRe = regexp.MustCompile(`^([A-Za-z]{1,3}[0-9]+[a-z]?)\s*\([^)]+\)$`)

// planAuditRe pulls the round number out of a plan filename, so a claim in
// audit 3's plan is checked against deferred rows whose origin is audit 3.
var planAuditRe = regexp.MustCompile(`dx-audit-(\d+)-fix-plan\.md$`)

// deferredOriginRe reads the Origin column: `audit 3 / C5`, `audit 4 / S2`.
var deferredOriginRe = regexp.MustCompile(`^\|\s*audit (\d+)\s*(?:/\s*([A-Za-z0-9, ]+?)\s*)?\|(.*)$`)

// deferredRowStartRe recognises a deferred row without indexing it, so one this
// check cannot read is reported rather than dropped.
var deferredRowStartRe = regexp.MustCompile(`^\|\s*audit\s`)

// idRangeRe expands `C1-C7` and `D1–D3` (both dash characters, because a
// Markdown file written by a human contains both).
var idRangeRe = regexp.MustCompile(`^([A-Za-z]+)(\d+)\s*[-–]\s*([A-Za-z]+)?(\d+)$`)

// runCoverageCheck fails when a plan claims it mechanically checked an item that
// deferred.md records as not attempted.
//
// This is B3, and it is a guard rather than a reconciliation because the
// contradiction it catches was written by hand into a paragraph that sat
// directly below another paragraph correcting the same mistake. Audit 3's plan
// claimed "C1-C7, G2, S6" while deferred.md recorded C4's third part, C5, C6,
// G2 and three S6 sub-items as not attempted; two rounds read past it.
func runCoverageCheck(root string) error {
	deferred, err := deferredByAudit(filepath.Join(root, deferredPath))
	if err != nil {
		return err
	}

	planGlob := filepath.Join(root, "docs", "plans", "dx-audit-*-fix-plan.md")
	plans, err := filepath.Glob(planGlob)
	if err != nil {
		return err
	}
	sort.Strings(plans)

	var problems []string
	for _, plan := range plans {
		m := planAuditRe.FindStringSubmatch(plan)
		if m == nil {
			continue
		}
		round := m[1]
		raw, err := os.ReadFile(plan)
		if err != nil {
			return err
		}
		text := string(raw)

		rel, _ := filepath.Rel(root, plan)

		partial := map[string]bool{}
		if pm := partialClaimRe.FindStringSubmatch(text); pm != nil {
			for _, entry := range strings.Split(pm[1], ",") {
				entry = strings.TrimSpace(strings.ReplaceAll(entry, "\n", " "))
				entry = strings.Join(strings.Fields(entry), " ")
				if entry == "" {
					continue
				}
				em := partialEntryRe.FindStringSubmatch(entry)
				if em == nil {
					problems = append(problems, fmt.Sprintf(
						"%s: Partially: entry %q must name one id and its carve-out, as `C4 (a and b; c deferred)`.\n"+
							"    A bare id list here suppresses the coverage check for every id in it.", rel, entry))
					continue
				}
				id := em[1]
				if _, deferredHere := deferred[round+"/"+id]; !deferredHere {
					problems = append(problems, fmt.Sprintf(
						"%s: Partially: names %s, which deferred.md does not record for audit %s.\n"+
							"    Nothing was carved out, so claim it outright or record the carve-out first.", rel, id, round))
					continue
				}
				partial[id] = true
			}
		}
		cm := coverageClaimRe.FindStringSubmatch(text)
		if cm == nil {
			if coveragePrefixRe.MatchString(text) {
				return fmt.Errorf("%s has a `Mechanically checked:` line this check cannot parse; "+
					"it must end in a period", rel)
			}
			continue // a plan need not make a coverage claim; it just may not lie in one
		}
		for _, id := range expandIDs(cm[1]) {
			if partial[id] {
				continue
			}
			if why, ok := deferred[round+"/"+id]; ok {
				problems = append(problems, fmt.Sprintf(
					"%s claims %s was mechanically checked, but deferred.md records it for audit %s:\n    %s",
					rel, id, round, strings.TrimSpace(why)))
			}
		}
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		return fmt.Errorf("%d plan coverage claim(s) contradict %s:\n  %s",
			len(problems), deferredPath, strings.Join(problems, "\n  "))
	}
	return nil
}

// deferredByAudit indexes deferred.md by "<round>/<id>". A row marked "No longer
// deferred" is skipped: it records history, not an open omission, so a plan may
// legitimately claim it.
func deferredByAudit(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("coverage-check: %w", err)
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		m := deferredOriginRe.FindStringSubmatch(line)
		if m == nil {
			// A row that opens `| audit N ...` and does not yield an origin is
			// reported, not skipped. Seven of twenty-five real rows were being
			// dropped in silence -- comma lists (`audit 3 / D7, D8`), a named
			// group (`audit 3 / Track A`, five items), and the round-level rows
			// with no id -- so a plan could claim any of them and stay green.
			if deferredRowStartRe.MatchString(line) {
				return nil, fmt.Errorf("%s:%d is a deferred row this check cannot index:\n    %s\n"+
					"    Use `audit <n> / <id>` or `audit <n>` with no slash.",
					deferredPath, i+1, strings.TrimSpace(line))
			}
			continue
		}
		if strings.Contains(m[3], "No longer deferred") {
			continue
		}
		for _, id := range strings.Split(m[2], ",") {
			if id = strings.TrimSpace(id); id != "" {
				out[m[1]+"/"+id] = m[3]
			}
		}
	}
	return out, nil
}

// expandIDs turns a claim list into individual ids, expanding ranges. Anything
// that is not an id — "S1's isolation table" — is ignored rather than guessed
// at, so the check is conservative: it never invents a claim to fail on.
func expandIDs(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(strings.Trim(strings.TrimSpace(part), "`"))
		if part == "" {
			continue
		}
		// A parenthesised aside after an id ("C4 (a and b; c deferred)") names
		// the id and then qualifies it; take the id.
		if i := strings.IndexAny(part, " ("); i > 0 {
			candidate := part[:i]
			if simpleIDRe.MatchString(candidate) {
				out = append(out, candidate)
				continue
			}
		}
		if m := idRangeRe.FindStringSubmatch(part); m != nil {
			prefix, endPrefix := m[1], m[3]
			if endPrefix != "" && endPrefix != prefix {
				continue // a range across two prefixes is not one this understands
			}
			var lo, hi int
			fmt.Sscanf(m[2], "%d", &lo)
			fmt.Sscanf(m[4], "%d", &hi)
			for i := lo; i <= hi && hi-lo < 100; i++ {
				out = append(out, fmt.Sprintf("%s%d", prefix, i))
			}
			continue
		}
		if simpleIDRe.MatchString(part) {
			out = append(out, part)
		}
	}
	return out
}

var simpleIDRe = regexp.MustCompile(`^[A-Za-z]{1,3}[0-9]+[a-z]?$`)

// statusExpectsDefect maps a row's status to what its predicate must say, and
// rejects anything outside the vocabulary.
//
// The status used to be free text compared against the literal "open" and the
// prefix "fixed", so every other value -- "Open", "deferred later",
// "not reproduced", "FIXED abc123" -- asserted nothing while still being
// counted in the "checked mechanically" total. Two of those are documented
// statuses, so the guard was already reporting a larger number than it was
// actually checking.
func statusExpectsDefect(status string) (bool, error) {
	switch {
	case status == "open":
		// Still broken: the predicate must hold.
		return true, nil
	case strings.HasPrefix(status, "deferred "):
		// Decided against doing, so the defect is by definition still there.
		return true, nil
	case strings.HasPrefix(status, "fixed "):
		return false, nil
	case status == "not reproduced":
		// The defect was never real; the predicate records what was found
		// instead, and it must keep holding in the "gone" direction.
		return false, nil
	default:
		return false, fmt.Errorf("unknown status %q (want `open`, `fixed <ref>`, "+
			"`deferred <reason>` or `not reproduced`)", status)
	}
}

// wrapped collapses line breaks -- including a Go comment continuation -- so a
// literal that is one sentence in a document matches text the author wrapped
// across lines.
//
// Without it a predicate could be permanently unsatisfiable: I1 quoted
// "Both forms are live during the migration" while routes.go wrapped that
// sentence between "Both" and "forms", so restoring the entire defect left the
// check green. That is the same class of blind guard the ledger exists to
// catch.
var wrapped = regexp.MustCompile(`\s*\n\s*(?://+\s*|#\s*|\*\s*)?`)

func normalizeWrapping(s string) string { return wrapped.ReplaceAllString(s, " ") }

// ---------- what a row must assert, not merely what it says ----------
//
// DX audit 7's method finding: a green row is not evidence of a correct fix.
// Three of audit 6's ~19 `fixed audit-6` rows were green over replacement text
// that was itself false, and one (D8d) carried a predicate whose file was not
// the file its claim cited, so it passed on an import line.
//
// A `present:` predicate proves a specific false sentence is GONE. It cannot
// prove the sentence that replaced it is TRUE. Two rules close that, and a
// third makes them expressible.

// predicateForms are the prefixes a predicate may start with. Used to split a
// multi-predicate cell: Go's RE2 has no lookahead, so the split is done by
// rejoining fragments that do not begin a new predicate, which is exactly what
// a lookahead on these prefixes would express.
var predicateForms = []string{"absent:", "present:", "manual:"}

var predicateSepRe = regexp.MustCompile(`\s+\+\s+`)

// splitPredicates splits a predicate cell into its predicates.
//
// The separator is ` + `, and a literal may contain that sequence -- the docs
// are full of `pass + fail`-shaped prose -- so a naive split would cut a
// predicate in half. A fragment that does not begin with a form prefix is not a
// new predicate; it is the continuation of the one before it, and is rejoined
// with the separator it was split on.
func splitPredicates(cell string) []string {
	parts := predicateSepRe.Split(cell, -1)
	var out []string
	for _, p := range parts {
		starts := false
		for _, form := range predicateForms {
			if strings.HasPrefix(p, form) {
				starts = true
				break
			}
		}
		if starts || len(out) == 0 {
			out = append(out, p)
			continue
		}
		out[len(out)-1] += " + " + p
	}
	return out
}

// ledgerLegacyFiles are the ledgers written before the two rules below existed.
// They are exempt, and the exemption is a literal set rather than a date or a
// glob so a new file cannot join it by accident.
//
// Retrofitting was measured, not estimated: of 110 rows, **50** non-`manual`
// rows cite no path at all in their Claim or Truth cells -- audit 3 puts the
// file in the section HEADING, so ten of its rows read `| U1 | Top bar with a
// project selector | No top bar … |` -- and **18** rows marked `fixed` carry
// only a `present:`. Retrofitting means ~68 hand-edits, most of them
// re-deriving the corrected text for a defect fixed up to four rounds ago and
// finding a literal that still holds. That is worth doing when someone touches
// a row, and is not worth blocking this round's rows on.
//
// The list shrinks. Removing a file from it is the way a round adopts the rules
// for its own ledger -- audit 7 removed its own when it wrote D9b, which is the
// first row written under them.
var ledgerLegacyFiles = map[string]bool{
	"docs/plans/dx-audit-3-doc-defects.md": true,
	"docs/plans/dx-audit-4-doc-defects.md": true,
	"docs/plans/dx-audit-5-doc-defects.md": true,
	"docs/plans/dx-audit-6-doc-defects.md": true,
}

// citedPathRe finds a path-shaped backtick span in a row's Claim or Truth cell.
// Longest extension first: Go's regexp alternation is leftmost-FIRST, not
// leftmost-longest, so `md|go|ts|tsx` captured `HealthPage.ts` out of
// `HealthPage.tsx` and the row read as citing a file that does not exist.
var citedPathRe = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:tsx|ts|yaml|yml|md|go|sh|py|json|html))")

// checkRowAssertions applies the two rules to one row. Legacy files are exempt.
func checkRowAssertions(r ledgerRow, preds []string) []string {
	if ledgerLegacyFiles[r.file] {
		return nil
	}
	var out []string

	// R1: a row claiming the defect is gone must assert what is TRUE now, not
	// only that the false text is absent. Keyed on the row rather than on what
	// else it carries, so it covers a `present:`-only row AND a `manual:`-only
	// one -- a row marked fixed that asserts nothing mechanically is the same
	// "green over unverified" failure, one form over.
	if wantDefect, err := statusExpectsDefect(r.status); err == nil && !wantDefect {
		hasAbsent := false
		for _, p := range preds {
			if strings.HasPrefix(p, "absent:") {
				hasAbsent = true
			}
		}
		if !hasAbsent {
			out = append(out, fmt.Sprintf(
				"%s:%d %s is marked %q but carries no `absent:` predicate.\n"+
					"    A `present:` proves the false sentence is gone; it cannot prove its replacement is true,\n"+
					"    and three of audit 6's rows were green over replacement text that was itself false.\n"+
					"    Add an `absent:` naming what the corrected text says, joined with ` + `.",
				r.file, r.line, r.id, r.status))
		}
	}

	// R2: the predicate must name a file the row cites. D8d pointed at
	// web/src/Settings.tsx for a claim about docs/security.md and passed on an
	// import line -- a row that can never fail for the reason it describes.
	cited := map[string]bool{}
	for _, m := range citedPathRe.FindAllStringSubmatch(r.cells, -1) {
		cited[m[1]] = true
	}
	for _, p := range preds {
		if strings.HasPrefix(p, "manual:") {
			continue // no path to check
		}
		_, rest, ok := strings.Cut(p, ":")
		if !ok {
			continue // reported by evalPredicate
		}
		relPath, _, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		if !cited[relPath] {
			out = append(out, fmt.Sprintf(
				"%s:%d %s has a predicate on %s, which its Claim and Truth cells never name.\n"+
					"    A predicate pointing somewhere the row does not describe cannot fail for the reason\n"+
					"    the row gives. Cite the file in the row, or point the predicate at the file it is about.",
				r.file, r.line, r.id, relPath))
		}
	}
	return out
}
