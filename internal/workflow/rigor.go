package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// RigorLevel is review.rigor's calibrated value once "auto" is resolved
// (review B1). Only critical/high findings ever block approval (§11,
// Severity.Blocking); rigor governs how MUCH review a change gets, not
// whether a system-deny check (secret sweep, host exec, blob digest
// mismatch) can be skipped -- those are absolute and never rigor-gated.
type RigorLevel string

const (
	RigorAuto     RigorLevel = "auto"
	RigorQuick    RigorLevel = "quick"
	RigorStandard RigorLevel = "standard"
	RigorThorough RigorLevel = "thorough"
)

// Valid reports whether r is one of the four configured values (§B1). Auto is
// a valid CONFIGURED value (it is what calibrateRigor resolves away), but is
// never itself an "effective" level a reviewer runs at.
func (r RigorLevel) Valid() bool {
	switch r {
	case RigorAuto, RigorQuick, RigorStandard, RigorThorough, "":
		return true
	}
	return false
}

// findingCap is the per-review-cycle findings cap for an effective rigor
// level (review B1). An unrecognized or empty level falls back to standard's
// cap -- the safe middle, not the most permissive one.
func findingCap(r RigorLevel) int {
	switch r {
	case RigorQuick:
		return 5
	case RigorThorough:
		return 20
	default:
		return 10
	}
}

// severityRank orders Severity from most (5) to least (1) severe, for
// capFindings's "keep the highest severity" rule.
var severityRank = map[domain.Severity]int{
	domain.SeverityCritical:    5,
	domain.SeverityHigh:        4,
	domain.SeverityMedium:      3,
	domain.SeverityLow:         2,
	domain.SeverityObservation: 1,
}

// RigorCapNote is the category stamped on the synthetic observation finding
// capFindings adds when it drops findings, so the UI and tests can find it
// without string-matching the whole findings list.
const RigorCapCategory = "review_rigor"

// capFindings keeps at most max findings from the cycle, preferring the
// highest severity (stable within a severity band, so ties keep the
// reviewer's own order) (review B1: "keep the highest severity. Record the
// dropped count in a note"). When anything is dropped, an extra observation
// finding is appended recording how many were dropped and at what rigor
// level, rather than silently discarding review output.
func capFindings(findings []domain.Finding, max int, rigor RigorLevel) []domain.Finding {
	if max <= 0 || len(findings) <= max {
		return findings
	}
	sorted := make([]domain.Finding, len(findings))
	copy(sorted, findings)
	sort.SliceStable(sorted, func(i, j int) bool {
		return severityRank[sorted[i].Severity] > severityRank[sorted[j].Severity]
	})
	kept := make([]domain.Finding, max)
	copy(kept, sorted[:max])
	dropped := len(findings) - max
	kept = append(kept, domain.Finding{
		Severity:   domain.SeverityObservation,
		Confidence: domain.ConfidenceHigh,
		Category:   RigorCapCategory,
		Finding:    fmt.Sprintf("%d lower-severity finding(s) not shown", dropped),
		Note:       fmt.Sprintf("review.rigor=%s caps this cycle at %d findings; the highest-severity ones are kept", rigor, max),
		Advisory:   true,
	})
	return kept
}

// projectScale is a cheap summary of the workspace's size (review B1, B5):
// file count and a line-of-code estimate, gathered from the same ignore
// rules the seed copy uses (git ls-files --exclude-standard, or a plain walk
// skipping dotdirs/build output outside a git repo) so a vendored or
// generated tree never inflates it.
type projectScale struct {
	Files int
	LOC   int
}

// scaleWalkMaxBytes bounds how much file content projectScaleOf reads, so the
// walk that calibrates rigor stays cheap even on a large repository: past
// this the file count is still exact, but LOC becomes a lower bound.
const scaleWalkMaxBytes = 4 << 20 // 4 MiB

// projectScaleOf walks workspace (the seed's own ignore rules via
// workspaceFiles) and returns a cheap size summary. Errors are swallowed to a
// zero scale -- calibrateRigor treats a zero (unmeasured/no-workspace) scale
// as UNKNOWN, not small: it falls through to `standard` rather than `quick`,
// since "we could not measure it" is not evidence the project is tiny.
func projectScaleOf(ctx context.Context, workspace string) projectScale {
	if workspace == "" {
		return projectScale{}
	}
	paths, err := workspaceFiles(ctx, workspace)
	if err != nil {
		return projectScale{}
	}
	scale := projectScale{Files: len(paths)}
	var read int64
	for _, rel := range paths {
		if seedExcluded(rel) || isEnvFile(rel) {
			continue
		}
		if read >= scaleWalkMaxBytes {
			break
		}
		data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		read += int64(len(data))
		if len(data) == 0 {
			continue
		}
		scale.LOC += bytes.Count(data, []byte("\n")) + 1
	}
	return scale
}

// changeSize is the size of the specific change under review (review B1): a
// requirement count for the PRD/plan review, or a file count for the
// change-set review -- whichever the caller has in hand.
type changeSize struct {
	Requirements int
	Files        int
}

// calibrateRigor derives review.rigor: auto's effective level from project and
// change size (review B1). A configured (non-auto, non-empty) level always
// wins outright -- calibration only fills in for "auto"/unset.
//
// Thresholds are deliberately simple and documented rather than tuned: the
// ICP this wave answers is a toy app whose review loop demanded ABNF grammars
// and audit schemas, so erring toward `quick` on anything small is the
// correction that matters. A large project OR a large change set pushes
// toward `thorough` even if the other axis is small (touching one file of a
// 500-file codebase still benefits from more scrutiny than a greenfield
// throwaway does).
func calibrateRigor(configured RigorLevel, scale projectScale, chg changeSize) RigorLevel {
	if configured != "" && configured != RigorAuto {
		return configured
	}
	big := scale.Files > 200 || scale.LOC > 20000 || chg.Requirements > 15 || chg.Files > 15
	// scale.Files > 0 is required: a zero scale means the workspace could not
	// be measured (no workspace yet, a brand-new idea, a read error), which is
	// unknown, not small, and must not default to the LEAST scrutiny.
	small := scale.Files > 0 && scale.Files <= 20 && scale.LOC <= 1500 && chg.Requirements <= 3 && chg.Files <= 3
	switch {
	case big:
		return RigorThorough
	case small:
		return RigorQuick
	default:
		return RigorStandard
	}
}

// conventionsMaxBytes caps how much of a conventions doc is fed to the
// reviewer/PRD/planner prompts, so a huge CONTRIBUTING.md cannot blow the
// stage's output/context budget on its own.
const conventionsMaxBytes = 4000

// projectConventionsFiles is the order project convention docs are looked for
// in, first match wins (review B1, B5).
var projectConventionsFiles = []string{"CONVENTIONS.md", "CONTRIBUTING.md", "docs/CONTRIBUTING.md", ".github/CONTRIBUTING.md"}

// projectConventions reads the first convention doc it finds in workspace,
// capped to conventionsMaxBytes, or "" when none exists. Errors are
// swallowed: a missing/unreadable conventions doc is not a reason to fail a
// review.
func projectConventions(workspace string) string {
	if workspace == "" {
		return ""
	}
	for _, name := range projectConventionsFiles {
		data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		s := string(data)
		if len(s) > conventionsMaxBytes {
			s = s[:conventionsMaxBytes] + "\n…(truncated)"
		}
		return strings.TrimSpace(s)
	}
	return ""
}

// stackMarkers maps a file that names it, present at the workspace root, to
// the stack it indicates (review B5: "the PRD/explore prompts get the
// workspace scale, stack and conventions"). Deliberately simple -- one marker
// file per stack, root-level only -- since this is a hint for "don't invent
// infrastructure the project doesn't have", not a build-system detector.
var stackMarkers = []struct {
	file  string
	stack string
}{
	{"go.mod", "Go"},
	{"package.json", "Node.js/JavaScript"},
	{"Cargo.toml", "Rust"},
	{"pyproject.toml", "Python"},
	{"requirements.txt", "Python"},
	{"Gemfile", "Ruby"},
	{"pom.xml", "Java (Maven)"},
	{"build.gradle", "Java/Kotlin (Gradle)"},
	{"composer.json", "PHP"},
}

// detectStack returns the stack(s) a workspace's root-level marker files
// indicate, or nil for an empty/unrecognized workspace (a brand-new idea with
// no code yet, where there is nothing to be proportionate ABOUT).
func detectStack(workspace string) []string {
	if workspace == "" {
		return nil
	}
	var out []string
	for _, m := range stackMarkers {
		if _, err := os.Stat(filepath.Join(workspace, m.file)); err == nil {
			out = append(out, m.stack)
		}
	}
	return out
}

// scaleAndConventions renders the "judge proportionately" context block given
// to the PRD author, planner and adversarial reviewer prompts (review B1,
// B5): the project's measured scale, the change under review, the effective
// rigor, and (when present) the project's own conventions doc. It is a single
// selector so every stage attaches it the same way.
func scaleAndConventions(scale projectScale, chg changeSize, rigor RigorLevel, conventions string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project scale: %d files, ~%d lines. This change: ", scale.Files, scale.LOC)
	var parts []string
	if chg.Requirements > 0 {
		parts = append(parts, fmt.Sprintf("%d requirement(s)", chg.Requirements))
	}
	if chg.Files > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s)", chg.Files))
	}
	if len(parts) == 0 {
		parts = append(parts, "not yet sized")
	}
	fmt.Fprintf(&b, "%s. Effective review rigor: %s.\n", strings.Join(parts, ", "), rigor)
	b.WriteString("Judge proportionately to this scale: do not demand infrastructure, formal grammars, or " +
		"exhaustive audit schemas the project does not already have or the idea did not ask for, and do not " +
		"treat a small project or a small change as if it were an enterprise system.\n")
	if conventions != "" {
		b.WriteString("Project conventions:\n")
		b.WriteString(conventions)
		b.WriteString("\n")
	}
	return b.String()
}

// proportionateContext renders the workspace-scale/stack/conventions block
// given to the PRD-authoring and framing ("explore") prompts (review B5), so
// the model does not turn a small idea into an enterprise spec: the ICP round
// this wave answers saw a 60-line toy app's PRD grow Postgres, Redis and
// scrypt requirements nobody asked for.
func proportionateContext(scale projectScale, stack []string, conventions string) string {
	var b strings.Builder
	if scale.Files == 0 {
		b.WriteString("This is a new project with no existing code yet.\n")
	} else {
		fmt.Fprintf(&b, "Existing project scale: %d files, ~%d lines.", scale.Files, scale.LOC)
		if len(stack) > 0 {
			fmt.Fprintf(&b, " Stack: %s.", strings.Join(stack, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("Respect the existing project's scale and stack. Do not add infrastructure the project does not " +
		"already have -- a database, a queue, a cache, a new service -- unless the idea itself asks for it.\n")
	if conventions != "" {
		b.WriteString("Project conventions:\n")
		b.WriteString(conventions)
		b.WriteString("\n")
	}
	return b.String()
}
