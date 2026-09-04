package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The defect ledgers. A ledger records what an audit found and what happened to
// it; the guard exists because a ledger nobody re-checks quietly becomes a list
// of claims, which is the failure it was created to prevent one level down.
var ledgerFiles = []string{
	"docs/plans/dx-audit-3-doc-defects.md",
	"docs/plans/dx-audit-4-doc-defects.md",
}

// ledgerRow is one row of a defect table.
type ledgerRow struct {
	file      string // the ledger the row is in
	line      int
	id        string // U1, A6, …
	predicate string
	status    string // open | fixed <commit> | deferred <reason>
}

// A row is `| id | claim | truth | `predicate` | status |`. The predicate is
// matched inside backticks so a claim containing a pipe cannot be mistaken for
// a column boundary — and because an unquoted predicate would be reformatted by
// any Markdown tool that touches the file.
// ledgerRowStartRe recognises the shape of a row without validating it, so a
// malformed row can be reported rather than silently dropped.
var ledgerRowStartRe = regexp.MustCompile(`^\|\s*[A-Z]+[0-9]+\s*\|`)

var ledgerRowRe = regexp.MustCompile("^\\|\\s*([A-Z]+[0-9]+)\\s*\\|(?:[^|]*\\|){2}\\s*`([^`]+)`\\s*\\|\\s*([^|]+?)\\s*\\|\\s*$")

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

	var rows []ledgerRow
	for _, lf := range ledgerFiles {
		path := filepath.Join(root, lf)
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// A ledger listed here but not yet written is a drift of its own:
				// the list is edited when a ledger is created, so a missing file
				// means one was deleted or never landed.
				return fmt.Errorf("ledger %s is listed in ledgerFiles but does not exist", lf)
			}
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
			rows = append(rows, ledgerRow{file: lf, line: i + 1, id: m[1], predicate: m[2], status: m[3]})
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("no ledger rows parsed — the table format changed and this check went blind")
	}

	var problems []string
	manual := 0
	for _, r := range rows {
		defectPresent, checkable, err := evalPredicate(root, r.predicate)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s:%d %s: %v", r.file, r.line, r.id, err))
			continue
		}
		if !checkable {
			manual++
			continue
		}
		open := r.status == "open"
		switch {
		case open && !defectPresent:
			problems = append(problems, fmt.Sprintf(
				"%s:%d %s is marked open but its predicate no longer holds — the defect is fixed and the row was not updated\n    predicate: %s",
				r.file, r.line, r.id, r.predicate))
		case !open && defectPresent && strings.HasPrefix(r.status, "fixed"):
			problems = append(problems, fmt.Sprintf(
				"%s:%d %s is marked %q but its predicate holds again — the defect regressed\n    predicate: %s",
				r.file, r.line, r.id, r.status, r.predicate))
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
	if !ok || strings.TrimSpace(literal) == "" {
		return false, false, fmt.Errorf("predicate %q needs <path>:<literal>", pred)
	}
	raw, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		return false, false, fmt.Errorf("predicate names %s: %w", relPath, err)
	}
	found := strings.Contains(string(raw), literal)

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
var coverageClaimRe = regexp.MustCompile(`(?m)^Mechanically checked:\s*(.+?)\.\s*$`)

// partialClaimRe matches the escape hatch: an item done in part, which may
// legitimately also appear in deferred.md.
var partialClaimRe = regexp.MustCompile(`(?m)^Partially:\s*(.+?)\.\s*$`)

// planAuditRe pulls the round number out of a plan filename, so a claim in
// audit 3's plan is checked against deferred rows whose origin is audit 3.
var planAuditRe = regexp.MustCompile(`dx-audit-(\d+)-fix-plan\.md$`)

// deferredOriginRe reads the Origin column: `audit 3 / C5`, `audit 4 / S2`.
var deferredOriginRe = regexp.MustCompile(`^\|\s*audit (\d+)\s*/\s*([A-Za-z0-9]+)\s*\|(.*)$`)

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

		partial := map[string]bool{}
		if pm := partialClaimRe.FindStringSubmatch(text); pm != nil {
			for _, id := range expandIDs(pm[1]) {
				partial[id] = true
			}
		}
		cm := coverageClaimRe.FindStringSubmatch(text)
		if cm == nil {
			continue // a plan need not make a coverage claim; it just may not lie in one
		}
		rel, _ := filepath.Rel(root, plan)
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
	for _, line := range strings.Split(string(raw), "\n") {
		m := deferredOriginRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.Contains(m[3], "No longer deferred") {
			continue
		}
		out[m[1]+"/"+m[2]] = m[3]
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
