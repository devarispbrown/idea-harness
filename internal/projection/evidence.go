package projection

import (
	"context"
	"regexp"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestResult is one parsed test outcome, keyed by name (C2, wave 3 contract).
type TestResult struct {
	Name   string
	Status string // "passed" | "failed"
}

var (
	tapLine     = regexp.MustCompile(`^\s*(not ok|ok)\s+\d+\s*(?:-\s*)?(.+?)\s*(#.*)?$`)
	goTestLine  = regexp.MustCompile(`^\s*--- (PASS|FAIL): (\S+)`)
	pytestLine  = regexp.MustCompile(`^(\S+)\s+(PASSED|FAILED)\b`)
	jestLine    = regexp.MustCompile(`^\s*[✓✔✗✕✘]\s+(.+?)(?:\s*\(\d+(?:\.\d+)?\s*m?s\))?\s*$`)
	jestFailSet = map[rune]bool{'✗': true, '✕': true, '✘': true}
)

// ParseTestOutput parses a test check's raw console output into per-test
// results, best-effort (C2, wave 3 contract): node --test's TAP output, `go
// test -v`, `pytest -v`, and jest-style ✓/✗ lines. Unrecognized lines are
// ignored. A test name that appears more than once keeps its LAST result (a
// rerun or a later summary line wins over an earlier one).
func ParseTestOutput(output string) []TestResult {
	byName := map[string]string{}
	order := make([]string, 0)
	record := func(name, status string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := byName[name]; !ok {
			order = append(order, name)
		}
		byName[name] = status
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case tapLine.MatchString(line):
			m := tapLine.FindStringSubmatch(line)
			if strings.Contains(strings.ToUpper(m[3]), "SKIP") {
				continue
			}
			status := "passed"
			if m[1] == "not ok" {
				status = "failed"
			}
			record(m[2], status)
		case goTestLine.MatchString(line):
			m := goTestLine.FindStringSubmatch(line)
			status := "passed"
			if m[1] == "FAIL" {
				status = "failed"
			}
			record(m[2], status)
		case pytestLine.MatchString(line):
			m := pytestLine.FindStringSubmatch(line)
			status := "passed"
			if m[2] == "FAILED" {
				status = "failed"
			}
			record(m[1], status)
		case jestLine.MatchString(line):
			r := []rune(trimmed)
			status := "passed"
			if len(r) > 0 && jestFailSet[r[0]] {
				status = "failed"
			}
			m := jestLine.FindStringSubmatch(line)
			record(m[1], status)
		}
	}
	out := make([]TestResult, 0, len(order))
	for _, name := range order {
		out = append(out, TestResult{Name: name, Status: byName[name]})
	}
	return out
}

// hookTestsLine recovers a verification hook's requirement ref and named
// tests from renderVerificationPlan's rendered markdown
// ("- **REQ-3** → check (evidence) [tests: a, b]") -- the artifact's only
// persisted form; the raw model JSON is not kept (see render.go).
var hookTestsLine = regexp.MustCompile(`^-\s+\*\*(.+?)\*\*.*\[tests:\s*(.+?)\]`)

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

// ComputeEvidenceTests maps each of an initiative's requirements to the
// test-run evidence that covers it (C2, wave 3 contract): tests named for its
// code in the verification plan's hooks, or else any parsed test whose name
// contains the requirement's code, resolved against the most recent parsed
// result for that test name across the initiative's change sets.
func ComputeEvidenceTests(ctx context.Context, db *store.DB, blobs *blob.Store, initiativeID string) (map[string][]domain.EvidenceTest, error) {
	requirements, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	changeSets, err := db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return nil, err
	}

	// Most recent parsed result per test name, oldest change set first so a
	// later one overwrites an earlier one for the same name.
	type resultRef struct {
		status    string
		changeSet string
	}
	byName := map[string]resultRef{}
	allNames := make([]string, 0)
	for _, cs := range changeSets {
		findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range findings {
			if f.Category != "tests" {
				continue
			}
			for _, r := range ParseTestOutput(f.Evidence) {
				if _, ok := byName[r.Name]; !ok {
					allNames = append(allNames, r.Name)
				}
				byName[r.Name] = resultRef{status: r.Status, changeSet: cs.HumanID}
			}
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
		if len(names) == 0 {
			for _, n := range allNames {
				if req.Code != "" && strings.Contains(n, req.Code) {
					names = append(names, n)
				}
			}
		}
		if len(names) == 0 {
			continue
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
