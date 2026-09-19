package projection

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestResult is one parsed test outcome (C2, wave 3 contract). Package is a
// best-effort grouping key (a Go import path, from `go test -json`'s own
// Package field or a `-v` output's trailing "ok/FAIL <pkg>" summary line) --
// "" when the format carries no such notion (TAP, pytest, jest). It
// disambiguates two same-named tests in different packages; the exposed
// evidence (domain.EvidenceTest) never carries it, only Name.
type TestResult struct {
	Package string
	Name    string
	Status  string // "passed" | "failed"
}

var (
	tapLine = regexp.MustCompile(`^\s*(not ok|ok)\s+(\d+)(?:\s+(.*))?$`)
	// tapDirective recognizes a trailing TAP directive ("# SKIP ...", "# TODO
	// ..."), stripped from the test's name. Anything else after a '#' is part
	// of the name (review Low fix: "a TAP name containing # gets truncated").
	tapDirective = regexp.MustCompile(`(?i)\s*#\s*(SKIP|TODO)\b.*$`)
	goRunLine    = regexp.MustCompile(`^===\s+RUN\s+(\S+)`)
	goTestLine   = regexp.MustCompile(`^\s*--- (PASS|FAIL): (\S+)`)
	// goPkgSummary matches `go test`'s own per-package trailer ("ok
	// <pkg> <time>" / "FAIL <pkg> <time>"), used only to learn which package
	// the preceding --- PASS/FAIL lines belonged to; it is never itself
	// recorded as a result.
	goPkgSummary = regexp.MustCompile(`^(ok|FAIL)\s+(\S+)\s`)
	pytestLine   = regexp.MustCompile(`^(\S+)\s+(PASSED|FAILED|ERROR)\b`)
	jestLine     = regexp.MustCompile(`^\s*[✓✔✗✕✘]\s+(.+?)(?:\s*\(\d+(?:\.\d+)?\s*m?s\))?\s*$`)
	jestFailSet  = map[rune]bool{'✗': true, '✕': true, '✘': true}
)

// ParseTestOutput parses a test check's raw console output into per-test
// results, best-effort (C2, wave 3 contract). Two shapes are understood:
//
//   - `go test -json`'s newline-delimited JSON event stream (detected when
//     the first non-blank line looks like a JSON object) -- preferred
//     whenever the check command can produce it (see execute.go's
//     checkCommand), because it carries an exact Package/Test identity
//     instead of one scraped from text.
//   - Free text: node --test's TAP output, `go test -v`, `pytest -v`, and
//     jest-style ✓/✗ lines. Unrecognized lines are ignored.
//
// A test name that appears more than once (a genuine rerun, a name reused
// across packages, or an agent's own test printing a line that mimics the
// format) keeps its most severe outcome: any "failed" observation for a key
// is sticky and can never be overwritten back to "passed" by a later line,
// in either format (C2: "an agent-printed fake --- PASS cannot flip a real
// FAIL").
func ParseTestOutput(output string) []TestResult {
	if looksLikeGoTestJSON(output) {
		return parseGoTestJSON(output)
	}
	return parseTestOutputText(output)
}

// looksLikeGoTestJSON reports whether output is (or starts as) a `go test
// -json` event stream: its first non-blank line is a JSON object.
func looksLikeGoTestJSON(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "{")
	}
	return false
}

// goTestEvent is one line of `go test -json`'s test2json event stream. Only
// the fields ParseTestOutput needs are kept.
type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// parseGoTestJSON parses a `go test -json` stream: only "pass"/"fail" events
// naming a specific Test count as a result (a package-level pass/fail with no
// Test, or any "run"/"output"/"start"/"pause"/"cont" event, is ignored) --
// nothing here is inferred from an "output" event's free-text payload, so
// text an agent's own test prints to stdout cannot forge an event that never
// happened the way it could scraping `-v` text directly.
func parseGoTestJSON(output string) []TestResult {
	type key struct{ pkg, name string }
	byKey := map[key]string{}
	order := make([]key, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Test == "" {
			continue
		}
		var status string
		switch ev.Action {
		case "pass":
			status = "passed"
		case "fail":
			status = "failed"
		default:
			continue
		}
		k := key{ev.Package, ev.Test}
		prev, ok := byKey[k]
		if !ok {
			order = append(order, k)
			byKey[k] = status
			continue
		}
		if prev != "failed" && status == "failed" {
			byKey[k] = "failed"
		}
	}
	out := make([]TestResult, 0, len(order))
	for _, k := range order {
		out = append(out, TestResult{Package: k.pkg, Name: k.name, Status: byKey[k]})
	}
	return out
}

// parseTestOutputText parses the free-text formats (TAP, `go test -v`,
// pytest, jest). Go results are held in `pending` until a package boundary
// (a summary "ok"/"FAIL <pkg>" line) or end of input assigns them a package;
// a plain non-verbose `go test` run (no `=== RUN` markers, no per-package
// trailer visible in the captured slice) still works, just with an empty
// package.
func parseTestOutputText(output string) []TestResult {
	type key struct{ pkg, name string }
	byKey := map[key]string{}
	order := make([]key, 0)
	upsert := func(pkg, name, status string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		k := key{pkg, name}
		prev, ok := byKey[k]
		if !ok {
			order = append(order, k)
			byKey[k] = status
			return
		}
		if prev != "failed" && status == "failed" {
			byKey[k] = "failed" // sticky fail (C2 fix): never overwritten back to passed
		}
	}

	type pendingResult struct{ name, status string }
	pending := make([]pendingResult, 0)
	flushPending := func(pkg string) {
		for _, p := range pending {
			upsert(pkg, p.name, p.status)
		}
		pending = pending[:0]
	}

	ranTests := map[string]bool{}
	sawAnyRun := false

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case tapLine.MatchString(line):
			m := tapLine.FindStringSubmatch(line)
			desc := strings.TrimSpace(m[3])
			desc = strings.TrimPrefix(desc, "- ")
			if loc := tapDirective.FindStringIndex(desc); loc != nil {
				if strings.Contains(strings.ToUpper(desc[loc[0]:]), "SKIP") {
					continue // a SKIP directive is neither a pass nor a fail
				}
				desc = strings.TrimSpace(desc[:loc[0]])
			}
			status := "passed"
			if m[1] == "not ok" {
				status = "failed"
			}
			upsert("", desc, status)
		case goRunLine.MatchString(line):
			m := goRunLine.FindStringSubmatch(line)
			sawAnyRun = true
			ranTests[strings.TrimSpace(m[1])] = true
		case goPkgSummary.MatchString(trimmed):
			m := goPkgSummary.FindStringSubmatch(trimmed)
			flushPending(m[2])
		case goTestLine.MatchString(line):
			m := goTestLine.FindStringSubmatch(line)
			name := m[2]
			// Once real `=== RUN` markers are present (verbose mode), a
			// result for a name whose own start marker never appeared is
			// suspect -- e.g. a running test printing a fabricated
			// "--- PASS: SomeOtherTest" line of its own (C2 fix). A
			// non-verbose run (no markers at all, Go's own default for a
			// passing suite) is left alone: sawAnyRun stays false, so real
			// failures are never dropped for lacking a marker that
			// non-verbose output never prints for anyone.
			if sawAnyRun && !ranTests[name] {
				continue
			}
			status := "passed"
			if m[1] == "FAIL" {
				status = "failed"
			}
			pending = append(pending, pendingResult{name: name, status: status})
		case pytestLine.MatchString(line):
			m := pytestLine.FindStringSubmatch(line)
			status := "passed"
			if m[2] != "PASSED" {
				status = "failed" // FAILED or ERROR: never a pass (review Low fix)
			}
			upsert("", m[1], status)
		case jestLine.MatchString(line):
			r := []rune(trimmed)
			status := "passed"
			if len(r) > 0 && jestFailSet[r[0]] {
				status = "failed"
			}
			m := jestLine.FindStringSubmatch(line)
			upsert("", m[1], status)
		}
	}
	flushPending("") // any Go results with no trailing package summary in this slice

	out := make([]TestResult, 0, len(order))
	for _, k := range order {
		out = append(out, TestResult{Package: k.pkg, Name: k.name, Status: byKey[k]})
	}
	return out
}

// hookTestsLine recovers a verification hook's requirement ref and named
// tests from renderVerificationPlan's rendered markdown
// ("- **REQ-3** → check (evidence) [tests: a, b]") -- the artifact's only
// persisted form; the raw model JSON is not kept (see render.go). The tests
// capture is greedy and anchored to the end of the line, so a name
// containing "]" is not cut at its first bracket (review Low fix).
var hookTestsLine = regexp.MustCompile(`^-\s+\*\*(.+?)\*\*.*\[tests:\s*(.+)\]\s*$`)

// mappedTestsByRequirement parses the initiative's latest verification_plan
// artifact for each hook's named tests, keyed by normalized requirement ref.
func mappedTestsByRequirement(ctx context.Context, db *store.DB, blobs *blob.Store, initiativeID string) (map[string][]string, error) {
	out := map[string][]string{}
	if blobs == nil {
		return out, nil
	}
	versions, err := db.ListArtifacts(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	var latest *store.ArtifactView
	for i := range versions {
		if versions[i].Kind != domain.ArtifactVerificationPlan {
			continue
		}
		if latest == nil || versions[i].Version > latest.Version {
			latest = &versions[i]
		}
	}
	if latest == nil {
		return out, nil
	}
	body, err := blobs.Get(latest.BodyDigest)
	if err != nil {
		return out, nil // best-effort: a missing blob just means no mapped tests
	}
	for _, line := range strings.Split(string(body), "\n") {
		m := hookTestsLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ref := normalizeRequirementCode(m[1])
		var tests []string
		for _, t := range strings.Split(m[2], ",") {
			if t = strings.TrimSpace(t); t != "" {
				tests = append(tests, t)
			}
		}
		out[ref] = append(out[ref], tests...)
	}
	return out, nil
}

// normalizeRequirementCode is a local, dependency-free equivalent of
// workflow.NormalizeRequirementRef (trim + uppercase) -- internal/projection
// does not import internal/workflow, to avoid a cycle (workflow already
// imports store, and projection is a read-model layer both daemon and CLI use
// independently of the engine).
func normalizeRequirementCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// eligibleChangeSets returns the change set ids whose test-check evidence may
// count toward any requirement (C1, review fix): the latest change set of
// each task, and only when that change set is applied and not reverted (a
// status other than "applied" -- rejected, reverted, superseded, still
// running -- means the task's most current attempt has nothing landed, so an
// EARLIER applied attempt for the same task must not resurrect old evidence
// either). A change set with no task id (never expected, but never trusted)
// is its own singleton group rather than merged with every other taskless
// row.
func eligibleChangeSets(changeSets []domain.ChangeSet) map[string]bool {
	type latest struct {
		cs  domain.ChangeSet
		set bool
	}
	byTask := map[string]latest{}
	for _, cs := range changeSets {
		key := cs.TaskID
		if key == "" {
			key = "cs:" + cs.ID
		}
		// ListChangeSets orders by created_at ASC with no secondary key, so
		// two change sets created in the same millisecond tie on CreatedAt
		// alone; `!Before` rather than strict `After` makes the later one in
		// iteration order (the actual later attempt) win the tie.
		cur := byTask[key]
		if !cur.set || !cs.CreatedAt.Before(cur.cs.CreatedAt) {
			byTask[key] = latest{cs: cs, set: true}
		}
	}
	out := map[string]bool{}
	for _, l := range byTask {
		if l.cs.Status == domain.ChangeSetApplied {
			out[l.cs.ID] = true
		}
	}
	return out
}

// requirementCodePattern caches a per-code, word-boundary regexp for the
// fallback name match below (review C1 fix: a plain substring let REQ-1 pick
// up REQ-10..REQ-19's tests).
func requirementCodePattern(code string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(code) + `\b`)
}

// ComputeEvidenceTests maps each of an initiative's requirements to the
// test-run evidence that covers it (C2, wave 3 contract), tried in order
// until one produces names:
//
//  1. Tests named for its code in the verification plan's hooks
//     (mappedTestsByRequirement).
//  2. Any parsed test whose name contains the requirement's code on a word
//     boundary.
//  3. Test functions found in test/spec files touched by the change set
//     implementing a task that `implements` this requirement (taskDerivedTests,
//     ICP re-run defect #3 bullet (b)) -- because a real test's name almost
//     never contains the requirement's code ("TestRateLimiterAllowsWithinBudget"
//     for a per-key-rate-limiting requirement has no "REQ-5" in it anywhere),
//     so (2) alone left every requirement unmapped on a real run despite tests
//     passing. Only counted when that change set is applied and its OWN check
//     passed.
//
// Every tier is resolved against the most recent parsed result for that test
// name among the initiative's ELIGIBLE change sets (eligibleChangeSets) --
// never a rejected, reverted, or task-superseded attempt, and never a check
// that did not itself exit 0 (Disposition == DispositionRejected covers a
// non-zero exit, a timeout, and a sandbox that could not launch; only those
// three write a "tests" finding without an observation-level pass, so this
// predicate is exact, not heuristic). A test named by tier (1) or (3) that is
// missing from that finding's parsed output counts as failed, never as
// silently absent (C1 fix: a plan naming [TestA, TestB] where only TestA's
// line survived must not read as "both passed") -- tier (2) can never
// produce a "missing" name by construction (it only ever matches a name
// already known to have run).
func ComputeEvidenceTests(ctx context.Context, db *store.DB, blobs *blob.Store, initiativeID string) (map[string][]domain.EvidenceTest, error) {
	requirements, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	changeSets, err := db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	eligible := eligibleChangeSets(changeSets)

	// Most recent parsed result per test name, oldest eligible change set
	// first so a later one overwrites an earlier one for the same name --
	// except a sticky fail, which no later pass can overwrite (C2 fix).
	type resultRef struct {
		status    string
		changeSet string
	}
	byName := map[string]resultRef{}
	allNames := make([]string, 0)
	record := func(name, status, changeSet string) {
		if prev, ok := byName[name]; ok {
			if prev.status == "failed" {
				return // sticky fail
			}
		} else {
			allNames = append(allNames, name)
		}
		byName[name] = resultRef{status: status, changeSet: changeSet}
	}
	// checkPassed records which eligible change sets' OWN tests check itself
	// exited 0 (ICP re-run defect #3 bullet (b): "only when the check run
	// passed and the change set is applied" gates taskDerivedTests below).
	checkPassed := map[string]bool{}
	for _, cs := range changeSets {
		if !eligible[cs.ID] {
			continue
		}
		findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
		if err != nil {
			return nil, err
		}
		// The latest "tests" finding for this change set (execute.go appends
		// at most one per run, but "latest" is the documented rule -- C1).
		var latestTests *domain.Finding
		for i := range findings {
			f := &findings[i]
			if f.Category != "tests" {
				continue
			}
			if latestTests == nil || f.CreatedAt.After(latestTests.CreatedAt) {
				latestTests = f
			}
		}
		if latestTests == nil {
			continue
		}
		// Only a check that itself exited 0 contributes a pass (C1/C2: "a
		// check run that exited non-zero -- nothing counts as passed").
		// DispositionRejected is set exactly on a non-zero exit, a timeout,
		// or a sandbox that never launched; every other case (a genuine
		// pass, or "not applicable" when no test command is configured, at
		// which point there is no test output to parse anyway) is fine to
		// parse.
		if latestTests.Disposition == domain.DispositionRejected {
			continue
		}
		checkPassed[cs.ID] = true
		for _, r := range ParseTestOutput(latestTests.Evidence) {
			record(r.Name, r.Status, cs.HumanID)
		}
	}

	hookTests, err := mappedTestsByRequirement(ctx, db, blobs, initiativeID)
	if err != nil {
		return nil, err
	}

	out := map[string][]domain.EvidenceTest{}
	for _, req := range requirements {
		if req.SupersededBy != "" {
			continue
		}
		names := hookTests[normalizeRequirementCode(req.Code)]
		// sourced is true for a mapping this function can vouch actually
		// names real, intended tests for this requirement (the plan's own
		// hooks, or a task's own change set) -- as opposed to the loose
		// name-substring fallback below, which can only ever produce names
		// already known to have run (they come from allNames, itself built
		// from byName's keys) and so can never itself produce a "missing"
		// entry. A sourced mapping's test missing from byName counts as
		// failed, never silently dropped (C1 fix): a plan or a task naming
		// [TestA, TestB] where only TestA's line survived must not read as
		// "both passed".
		sourced := len(names) > 0
		if !sourced && req.Code != "" {
			pattern := requirementCodePattern(req.Code)
			for _, n := range allNames {
				if pattern.MatchString(n) {
					names = append(names, n)
				}
			}
		}
		if len(names) == 0 {
			// ICP re-run defect #3 bullet (b): a real test's NAME almost
			// never contains the requirement's code, so the fallback above
			// found nothing on 11 requirements even though 15 tests just
			// passed. Fall back to the task→requirement edge instead: tests
			// added or changed by the change set that implements the task
			// wired to this requirement, but ONLY when that change set is
			// applied and its own check passed -- never a guess at what the
			// task was FOR, always what it actually shipped and verified.
			taskNames, err := taskDerivedTests(ctx, db, blobs, initiativeID, changeSets, eligible, checkPassed, req.ID)
			if err != nil {
				return nil, err
			}
			if len(taskNames) == 0 {
				continue
			}
			names = taskNames
			sourced = true
		}
		seen := map[string]bool{}
		var evidence []domain.EvidenceTest
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			r, ok := byName[name]
			if !ok {
				if sourced {
					// The plan (or the implementing task's own change set)
					// names this test, but no eligible check run produced
					// it: missing is failing, never silently dropped (C1
					// fix).
					evidence = append(evidence, domain.EvidenceTest{Name: name, Status: "failed"})
				}
				continue
			}
			evidence = append(evidence, domain.EvidenceTest{Name: name, Status: r.status, ChangeSet: r.changeSet})
		}
		if len(evidence) > 0 {
			out[req.ID] = evidence
		}
	}
	return out, nil
}

// testFilePattern recognizes a test/spec file by name (Go, JS/TS, and the
// generic "test" infix), for taskDerivedTests below: a file this shape,
// touched (added or modified) by a change set, is assumed to carry the test
// functions that change set contributes.
var testFilePattern = regexp.MustCompile(`(?i)(_test\.go$|\.(spec|test)\.[jt]sx?$)`)

// goTestFuncPattern extracts a Go test function's name from a `func TestXxx(`
// declaration line.
var goTestFuncPattern = regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]*)\s*\(`)

// extractTestFuncNames returns every Go test function name declared in
// content. Only Go is understood today (the codebase this ships in is Go);
// JS/TS/pytest test files are still recognized by testFilePattern for
// "touched by this change set" purposes, but their individual test names are
// not extracted, since `it(...)`/`describe(...)` names are runtime strings,
// not declarations a static regex can safely pull out.
func extractTestFuncNames(content string) []string {
	var names []string
	for _, m := range goTestFuncPattern.FindAllStringSubmatch(content, -1) {
		names = append(names, m[1])
	}
	return names
}

// taskDerivedTests returns the names of test functions found in test/spec
// files that the change set implementing requirementID's task touched (ICP
// re-run defect #3 bullet (b)): a task→requirement `implements` edge, then
// that task's own eligible (applied, latest-attempt), check-passed change
// set's added/modified test files -- never a test name coincidentally
// containing the requirement's code, which real test names almost never do.
func taskDerivedTests(ctx context.Context, db *store.DB, blobs *blob.Store, initiativeID string,
	changeSets []domain.ChangeSet, eligible, checkPassed map[string]bool, requirementID string) ([]string, error) {
	if blobs == nil {
		return nil, nil
	}
	edges, err := db.ListGraphEdgesTo(ctx, initiativeID, "requirement", requirementID)
	if err != nil {
		return nil, err
	}
	taskIDs := map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements && e.FromType == "task" {
			taskIDs[e.FromID] = true
		}
	}
	if len(taskIDs) == 0 {
		return nil, nil
	}
	var names []string
	for _, cs := range changeSets {
		if !eligible[cs.ID] || !checkPassed[cs.ID] || !taskIDs[cs.TaskID] {
			continue
		}
		patch, err := decodePatch(blobs, cs.PatchDigest)
		if err != nil || patch == nil {
			continue // best-effort: an unreadable patch just contributes nothing
		}
		for _, fc := range patch.Files {
			if fc.IsDelete() || !testFilePattern.MatchString(fc.Path) {
				continue
			}
			content, err := blobs.Get(fc.Digest)
			if err != nil {
				continue
			}
			names = append(names, extractTestFuncNames(string(content))...)
		}
	}
	return names, nil
}

// decodePatch reads and decodes a change set's patch manifest from the blob
// store, best-effort (nil, nil when there is nothing to read). Mirrors
// workflow.Engine.loadPatch; duplicated rather than shared because
// internal/projection is a read-model layer independent of the engine (see
// normalizeRequirementCode's doc comment for the same constraint).
func decodePatch(blobs *blob.Store, patchDigest string) (*fsdiff.Patch, error) {
	if patchDigest == "" || blobs == nil {
		return nil, nil
	}
	data, err := blobs.Get(patchDigest)
	if err != nil {
		return nil, nil //nolint:nilerr // best-effort: unreadable patch contributes nothing
	}
	var p fsdiff.Patch
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, nil //nolint:nilerr // best-effort: malformed patch contributes nothing
	}
	return &p, nil
}

// AllTestsPassed reports whether ev is non-empty and every entry passed --
// the rule both GET .../requirements' display and confirm_passing_tests use
// (C2, wire contract).
func AllTestsPassed(ev []domain.EvidenceTest) bool {
	if len(ev) == 0 {
		return false
	}
	for _, e := range ev {
		if e.Status != "passed" {
			return false
		}
	}
	return true
}
