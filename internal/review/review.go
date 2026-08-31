// Package review implements the review engine: deterministic PRD checks, the
// approval bar, and conversion of adversarial-review output into structured
// Findings (§11, §13–§16).
package review

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/idea-harness/idea-harness/internal/domain"
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
// approved while any critical/high finding is not dispositioned as fixed or
// (human) accepted_risk.
func EvaluateApproval(findings []domain.Finding) (blocked bool, reason string) {
	for _, f := range findings {
		if !f.Severity.Blocking() {
			continue
		}
		if f.Disposition != domain.DispositionFixed && f.Disposition != domain.DispositionAcceptedRisk {
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
