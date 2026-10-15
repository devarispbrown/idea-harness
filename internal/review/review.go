// Package review implements the review engine: deterministic PRD checks, the
// approval bar, and conversion of adversarial-review output into structured
// Findings (§11, §13–§16).
package review

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// vagueWords are bare evaluative adjectives that must not be used as an
// acceptance-criterion target without a quantified predicate (§11).
var vagueWords = []string{
	"fast", "secure", "easy", "intuitive", "scalable", "reliable", "robust",
	"performant", "good", "nice", "quick", "responsive", "efficient", "simple",
}

var quantifierRe = regexp.MustCompile(`[0-9]|<|>|<=|>=|%|\bms\b|\bs\b|second|minute|hour|day|p[0-9]`)

// VagueTarget reports whether a target is a bare evaluative adjective with no
// quantified predicate.
func VagueTarget(target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	if t == "" {
		return true
	}
	if quantifierRe.MatchString(t) {
		return false
	}
	for _, w := range vagueWords {
		if t == w {
			return true
		}
	}
	return false
}

// DeterministicChecks runs the cheap, deterministic PRD checks (§11, §16):
// (1) no empty acceptance criteria, (2) no vague-only criteria, (3) unique REQ
// codes. These run before the adversarial reviewer.
func DeterministicChecks(initiativeID string, reqs []domain.Requirement) []domain.Finding {
	var out []domain.Finding
	seen := map[string]bool{}

	for _, r := range reqs {
		if seen[r.Code] {
			out = append(out, domain.Finding{
				Severity:   domain.SeverityCritical,
				Confidence: domain.ConfidenceHigh,
				Category:   "traceability",
				Finding:    fmt.Sprintf("duplicate requirement code %s", r.Code),
				Evidence:   r.Code,
				Impact:     "requirement codes must be unique for traceability",
				Recommendation: "assign a unique code",
				TargetType: "requirement",
				TargetID:   r.ID,
			})
		}
		seen[r.Code] = true

		if len(r.AcceptanceCriteria) == 0 {
			out = append(out, domain.Finding{
				Severity:       domain.SeverityHigh,
				Confidence:     domain.ConfidenceHigh,
				Category:       "acceptance",
				Finding:        fmt.Sprintf("%s has no acceptance criteria", r.Code),
				Evidence:       r.Statement,
				Impact:         "unverifiable requirement",
				Recommendation: "add a measurable acceptance criterion",
				TargetType:     "requirement",
				TargetID:       r.ID,
			})
			continue
		}

		for _, ac := range r.AcceptanceCriteria {
			if VagueTarget(ac.Target) {
				out = append(out, domain.Finding{
					Severity:       domain.SeverityHigh,
					Confidence:     domain.ConfidenceHigh,
					Category:       "acceptance",
					Finding:        fmt.Sprintf("%s has a vague acceptance criterion (%q)", r.Code, ac.Target),
					Evidence:       fmt.Sprintf("metric=%q target=%q", ac.Metric, ac.Target),
					Impact:         "criterion is not verifiable",
					Recommendation: "quantify the target (metric + number + condition)",
					TargetType:     "requirement",
					TargetID:       r.ID,
				})
			}
		}
	}
	return out
}

// EvaluateApproval implements the approval bar (§11): an initiative may not be
// approved while any critical/high finding is not resolved (Finding.
// ResolvesBlocking -- fixed, accepted_risk, or a human's rejected on a
// reviewer's own judgment call; see its doc comment, ICP re-run defect #2).
// An Advisory finding never blocks, whatever its severity (review B1's
// rigor-cap note, and review B2's "about code this change set did not
// touch").
func EvaluateApproval(findings []domain.Finding) (blocked bool, reason string) {
	for _, f := range findings {
		if f.Advisory {
			continue
		}
		if !f.Severity.Blocking() {
			continue
		}
		if !f.ResolvesBlocking() {
			return true, fmt.Sprintf("open %s finding: %s", f.Severity, f.Finding)
		}
	}
	return false, ""
}

// AllDispositioned reports whether every finding has a disposition (§11, §16).
func AllDispositioned(findings []domain.Finding) bool {
	for _, f := range findings {
		if f.Disposition == "" {
			return false
		}
	}
	return true
}

// ParseFindings converts a validated adversarial-review object into Findings.
func ParseFindings(reviewRunID, targetType, targetID string, obj map[string]any) ([]domain.Finding, error) {
	raw, ok := obj["findings"].([]any)
	if !ok {
		return nil, fmt.Errorf("review: findings array missing")
	}
	out := make([]domain.Finding, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("review: finding %d is not an object", i)
		}
		f := domain.Finding{
			ReviewRunID:    reviewRunID,
			TargetType:     targetType,
			TargetID:       targetID,
			Severity:       domain.Severity(str(m["severity"])),
			Confidence:     domain.Confidence(str(m["confidence"])),
			Category:       str(m["category"]),
			Finding:        str(m["finding"]),
			Evidence:       str(m["evidence"]),
			Impact:         str(m["impact"]),
			Recommendation: str(m["recommendation"]),
			Path:           str(m["path"]),
			// The convergence fields (parse-time only; see domain.Finding.Cites).
			// Whitespace-trimmed because a model pads them as freely as any
			// other string, and an untrimmed " F1" would fail to name F1.
			Cites:         strings.TrimSpace(str(m["requirement"])),
			ReRaises:      strings.TrimSpace(str(m["reraises"])),
			Justification: strings.TrimSpace(str(m["justification"])),
			// InDiff defaults to true (assume in-diff) when the reviewer omits
			// it entirely: B2's self-report only demotes a finding when the
			// reviewer actively says it is NOT about the diff. A reviewer that
			// does not participate in this field at all (an older prompt, a
			// scripted/demo response) must not have every finding it raises
			// silently downgraded to advisory.
			InDiff: true,
		}
		if b, ok := m["in_diff"].(bool); ok {
			f.InDiff = b
		}
		if !f.Severity.Valid() {
			return nil, fmt.Errorf("review: invalid severity %q", f.Severity)
		}
		if !f.Confidence.Valid() {
			return nil, fmt.Errorf("review: invalid confidence %q", f.Confidence)
		}
		out = append(out, f)
	}
	return out, nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// OutcomeCategory is the Finding.Category of every outcome-chain finding.
const OutcomeCategory = "outcome"

// OutcomeChecks are the deterministic outcome-chain checks on a PRD:
//
//   - with at least one active objective, every active requirement must serve
//     one (high, targeted at -- and so citing -- the requirement): work with
//     no stated outcome is exactly what verification cannot catch, since it
//     can pass every test and still not matter;
//   - every active objective must have at least one live metric (high,
//     targeted at the objective): an objective nobody can measure is a
//     promise nobody can check;
//   - a PRD that defines no active objective at all gets one medium finding,
//     not a block per requirement. It is a gap a human should see, and a
//     PRD written before objectives existed (or by a model that ignored the
//     field) must not wedge in review over it.
//
// They run with the other deterministic checks, before the adversarial
// reviewer, so -- like a missing acceptance criterion -- they gate approval
// at every rigor and the reviser is told about them by label (D1, D2, ...).
// objectives and metrics are the LIVE (non-superseded) ones; requirements
// carry ObjectiveIDs from the store.
func OutcomeChecks(initiativeID string, reqs []domain.Requirement, objectives []domain.Objective, metrics []domain.OutcomeMetric) []domain.Finding {
	active := map[string]domain.Objective{}
	for _, o := range objectives {
		if o.Active() {
			active[o.ID] = o
		}
	}
	if len(active) == 0 {
		if len(reqs) == 0 {
			return nil
		}
		return []domain.Finding{{
			Severity: domain.SeverityMedium, Confidence: domain.ConfidenceHigh, Category: OutcomeCategory,
			Finding:        "the PRD defines no active objective, so no requirement can be traced to an outcome",
			Evidence:       fmt.Sprintf("%d requirement(s), 0 active objectives", len(reqs)),
			Impact:         "requirements can be verified as built but never checked against what they were for",
			Recommendation: "state the objective(s) this initiative exists to move, each with a measurable metric, and link every requirement to one",
			TargetType:     "prd", TargetID: initiativeID,
		}}
	}
	var out []domain.Finding
	for _, r := range reqs {
		serves := false
		for _, oid := range r.ObjectiveIDs {
			if _, ok := active[oid]; ok {
				serves = true
				break
			}
		}
		if serves {
			continue
		}
		out = append(out, domain.Finding{
			Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: OutcomeCategory,
			Finding:        fmt.Sprintf("%s serves no objective", r.Code),
			Evidence:       r.Statement,
			Impact:         "the requirement can pass verification without anyone being able to say what outcome it was for",
			Recommendation: "list the objective(s) it serves in its objectives field, or remove it if it serves none",
			TargetType:     "requirement", TargetID: r.ID,
		})
	}
	hasMetric := map[string]bool{}
	for _, m := range metrics {
		if m.SupersededBy == "" && m.Measurable() {
			hasMetric[m.ObjectiveID] = true
		}
	}
	codes := make([]string, 0, len(active))
	byCode := map[string]domain.Objective{}
	for _, o := range active {
		codes = append(codes, o.Code)
		byCode[o.Code] = o
	}
	sort.Strings(codes)
	for _, c := range codes {
		o := byCode[c]
		if hasMetric[o.ID] {
			continue
		}
		out = append(out, domain.Finding{
			Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: OutcomeCategory,
			Finding:        fmt.Sprintf("%s has no measurable metric", o.Code),
			Evidence:       o.Statement,
			Impact:         "nobody can tell whether the objective is being met, so drift cannot be detected",
			Recommendation: "add a metric with a direction and a target (and a baseline, when known)",
			TargetType:     "objective", TargetID: o.ID,
		})
	}
	return out
}
