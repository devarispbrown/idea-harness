package workflow

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	ccontext "github.com/devarispbrown/idea-harness/internal/context"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/quality"
	"github.com/devarispbrown/idea-harness/internal/schema"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/textdiff"
)

// QualityReviewSchemaName is the structured-output name of the
// verification_engineer's quality review (schema.QualityReview). Its own key,
// so a scripted test can drive it without disturbing any other review.
const QualityReviewSchemaName = "quality_review"

// stageQualityReview is the run stage the quality review is recorded under.
const stageQualityReview = "quality_review"

const ergonomicsRubric = `Review the public API surface this change adds or alters -- HTTP operations, exported Go identifiers, CLI flags -- for usability by a client or SDK author. Judge against this rubric:
1. Naming consistency: new names follow the conventions already visible in the surrounding surface (casing, verb choice, plural/singular, prefixes).
2. Error shapes: errors are returned in one consistent, documented shape (an error object, typed Go errors), not ad-hoc strings or mixed shapes.
3. Pagination: list operations that can grow unbounded take a limit/cursor and say how to get the next page.
4. Idempotency: operations a client may retry (create, charge, send) can be retried safely, or say they cannot.
5. Examples: the new surface has a usage example (a doc example, an OpenAPI example, a Go Example func, a README snippet).
Only report a problem you can point at: every finding must name the file exactly as it appears in a diff header below and a line number in the NEW file that lies inside one of that file's hunks. A finding that cites no such line is discarded. Severity: high = a client will misuse or break on this; medium = friction every client pays; low = polish.`

const docsRubric = `Review whether the documentation covers what this change does: a reader of the README and docs alone should be able to install the project, use what was added, and configure it. Report a gap only where the diff adds behavior the docs do not explain. Every finding must name a file exactly as it appears in a diff header below and a line number in the NEW file inside one of its hunks -- point at the undocumented code or at the doc that should have changed. A finding with no such citation is discarded.`

const opsRubric = `Review whether this change is operable in production: health checking, graceful shutdown, structured logging, configuration via environment, safe database migrations, timeouts and retries on outbound calls, and resource limits. The deterministic checks have already looked for the common patterns; report only what they cannot see. Every finding must name a file exactly as it appears in a diff header below and a line number in the NEW file inside one of its hunks; a finding with no such citation is discarded.`

// reviewedFile is one file shown to the model, with the new-file line ranges
// its hunks cover (what a citation must land in).
type reviewedFile struct {
	path   string
	text   string
	ranges [][2]int // [start, end] inclusive, in new-file line numbers
}

var hunkHeaderRe = regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// qualityReviewMaxBytes caps the diff text one quality review sends.
const qualityReviewMaxBytes = 60_000

func (e *Engine) renderForReview(files []quality.ChangedFile) []reviewedFile {
	var out []reviewedFile
	total := 0
	for _, cf := range files {
		if !cf.AfterExists {
			continue
		}
		before := ""
		if cf.BeforeExists && cf.BeforeKnown {
			before = cf.Before
		}
		after := cf.After
		// Redact before diffing so the model never sees a credential value
		// and the line numbers stay those of the real file.
		before, after = e.redactForModel(before), e.redactForModel(after)
		var text string
		var ranges [][2]int
		if hunks, ok := textdiff.Hunks(before, after); ok && hunks != "" && cf.BeforeExists {
			text = hunks
			for _, m := range hunkHeaderRe.FindAllStringSubmatch(hunks, -1) {
				start, _ := strconv.Atoi(m[1])
				n := 1
				if m[2] != "" {
					n, _ = strconv.Atoi(m[2])
				}
				if n > 0 {
					ranges = append(ranges, [2]int{start, start + n - 1})
				}
			}
		} else {
			// A new file (or one the diff refuses): show it whole, numbered.
			lines := strings.Split(after, "\n")
			var b strings.Builder
			fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
			for _, l := range lines {
				b.WriteString("+" + l + "\n")
			}
			text = b.String()
			ranges = [][2]int{{1, len(lines)}}
		}
		if total+len(text) > qualityReviewMaxBytes {
			break
		}
		total += len(text)
		out = append(out, reviewedFile{path: cf.Path, text: text, ranges: ranges})
	}
	return out
}

func (e *Engine) redactForModel(s string) string {
	if s == "" {
		return s
	}
	e.cfgMu.RLock()
	r := e.redactor
	e.cfgMu.RUnlock()
	if r != nil {
		return r.RedactString(s)
	}
	return security.Redact(s)
}

// qualityModelReview asks the verification_engineer to review files against
// a rubric and turns its findings into checks.
//
// The citation rule is the convergence rule applied to lines: a finding
// counts only when it names a file it was shown and a line inside one of
// that file's hunks. Uncited findings are kept in the summary check's detail
// -- never silently dropped -- but cannot move the scorecard. A provider that
// says it did not review (the demo provider's unreviewed_reason), or a model
// call that fails, is not_measured.
func (e *Engine) qualityModelReview(ctx context.Context, initiativeID string, d domain.QualityDimension, rubric string, files []quality.ChangedFile) []domain.QualityCheck {
	model := domain.QualityMethodModel
	name := "model_review"
	shown := e.renderForReview(files)
	if len(shown) == 0 {
		return []domain.QualityCheck{{Name: name, Status: domain.QualityNotApplicable, Method: model, Detail: "no changed file to review"}}
	}
	var diff strings.Builder
	byPath := map[string]reviewedFile{}
	for _, f := range shown {
		fmt.Fprintf(&diff, "--- %s ---\n%s\n", f.path, f.text)
		byPath[f.path] = f
	}
	_, obj, err := e.runModel(ctx, initiativeID, stageQualityReview, RoleVerificationEngineer, QualityReviewSchemaName, schema.QualityReview,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: rubric},
			{Selector: "change_set", Content: diff.String()},
		},
		"Quality review ("+string(d)+") of the initiative's applied change.")
	if err != nil {
		return []domain.QualityCheck{{Name: name, Status: domain.QualityNotMeasured, Method: model, Detail: "the model review did not complete: " + err.Error()}}
	}
	if why, _ := obj["unreviewed_reason"].(string); strings.TrimSpace(why) != "" {
		return []domain.QualityCheck{{Name: name, Status: domain.QualityNotMeasured, Method: model, Detail: "not reviewed: " + strings.TrimSpace(why)}}
	}
	raw, _ := obj["findings"].([]any)
	var checks []domain.QualityCheck
	var uncited []string
	for _, item := range raw {
		f, _ := item.(map[string]any)
		if f == nil {
			continue
		}
		sev, _ := f["severity"].(string)
		cat, _ := f["category"].(string)
		text, _ := f["finding"].(string)
		rec, _ := f["recommendation"].(string)
		req, _ := f["requirement"].(string)
		p, _ := f["path"].(string)
		// The decoder may hand back float64, int64 or json.Number depending on
		// the provider path; the text form is the same for all of them.
		line, _ := strconv.Atoi(strings.TrimSuffix(fmt.Sprint(f["line"]), ".0"))
		rf, ok := byPath[strings.TrimPrefix(p, "./")]
		if !ok || !inRanges(line, rf.ranges) {
			uncited = append(uncited, fmt.Sprintf("[%s] %s (cited %s:%d)", sev, text, p, line))
			continue
		}
		status := domain.QualityPass
		switch domain.Severity(sev) {
		case domain.SeverityCritical, domain.SeverityHigh:
			status = domain.QualityFail
		case domain.SeverityMedium:
			status = domain.QualityWarn
		}
		detail := fmt.Sprintf("[%s] %s", sev, text)
		if rec != "" {
			detail += "\nrecommendation: " + rec
		}
		cite := domain.QualityCitation{Path: rf.path, Line: line, Requirement: req}
		checks = append(checks, domain.QualityCheck{Name: "review: " + strings.TrimSpace(cat), Status: status, Method: model,
			Detail: detail, Citations: []domain.QualityCitation{cite}})
	}
	summary := domain.QualityCheck{Name: name, Status: domain.QualityPass, Method: model,
		Detail: fmt.Sprintf("the verification_engineer reviewed %d file(s); %d cited finding(s)", len(shown), len(checks))}
	if len(uncited) > 0 {
		summary.Detail += fmt.Sprintf("\n%d finding(s) set aside because they cite no line in the diff (they cannot affect the scorecard):\n- %s",
			len(uncited), strings.Join(limitLines(uncited, 10), "\n- "))
	}
	return append([]domain.QualityCheck{summary}, checks...)
}

func inRanges(line int, ranges [][2]int) bool {
	for _, r := range ranges {
		if line >= r[0] && line <= r[1] {
			return true
		}
	}
	return false
}

// measureErgonomics is api_ergonomics: the model-reviewed rubric over the
// files that make up the change's public surface -- changed OpenAPI specs,
// changed importable Go files, and changed source files that add an HTTP
// route or CLI flag. Nothing public changed: not_applicable.
func (e *Engine) measureErgonomics(ctx context.Context, initiativeID string, snap *qualitySnapshot) []domain.QualityCheck {
	var files []quality.ChangedFile
	for _, cf := range snap.changed {
		if !cf.AfterExists {
			continue
		}
		switch {
		case quality.LooksLikeOpenAPI([]byte(cf.After)):
			files = append(files, cf)
		case quality.IsPublicGoPath(cf.Path):
			files = append(files, cf)
		case quality.IsSourcePath(cf.Path):
			var before []quality.SurfaceItem
			if cf.BeforeExists {
				before = quality.SurfaceItems(cf.Path, cf.Before)
			}
			for _, it := range quality.AddedSurface(before, quality.SurfaceItems(cf.Path, cf.After)) {
				if it.Kind == "route" || it.Kind == "flag" {
					files = append(files, cf)
					break
				}
			}
		}
	}
	if len(files) == 0 {
		return []domain.QualityCheck{{Name: "model_review", Status: domain.QualityNotApplicable, Method: domain.QualityMethodModel,
			Detail: "the change touched no public surface (OpenAPI spec, importable Go package, HTTP route, CLI flag)"}}
	}
	return e.qualityModelReview(ctx, initiativeID, domain.QualityAPIErgonomics, ergonomicsRubric, files)
}
