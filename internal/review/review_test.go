package review

import (
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
)

func TestVagueTarget(t *testing.T) {
	if !VagueTarget("fast") {
		t.Error("'fast' should be vague")
	}
	if !VagueTarget("secure") {
		t.Error("'secure' should be vague")
	}
	if VagueTarget("p95 < 200ms") {
		t.Error("'p95 < 200ms' should NOT be vague")
	}
	if VagueTarget(">= 99.9%") {
		t.Error("'>= 99.9%' should NOT be vague")
	}
}

func TestDeterministicChecks(t *testing.T) {
	reqs := []domain.Requirement{
		{ID: "r1", Code: "REQ-1", Statement: "meter usage",
			AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "events", Target: ">= 1", Condition: "per request"}}},
		{ID: "r2", Code: "REQ-2", Statement: "be fast",
			AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "fast", Condition: ""}}},
		{ID: "r3", Code: "REQ-3", Statement: "no criteria"},
	}
	findings := DeterministicChecks("i-1", reqs)

	// REQ-2 (vague) and REQ-3 (no criteria) must be flagged; REQ-1 must not.
	var hasVague, hasEmpty bool
	for _, f := range findings {
		if f.TargetID == "r2" && f.Category == "acceptance" {
			hasVague = true
		}
		if f.TargetID == "r3" {
			hasEmpty = true
		}
		if f.TargetID == "r1" {
			t.Errorf("REQ-1 should not produce a finding")
		}
	}
	if !hasVague || !hasEmpty {
		t.Fatalf("expected vague + empty findings, got %+v", findings)
	}
}

func TestEvaluateApproval(t *testing.T) {
	// Open critical/high blocks.
	blocked, _ := EvaluateApproval([]domain.Finding{{Severity: domain.SeverityHigh}})
	if !blocked {
		t.Error("open high finding should block")
	}
	// Fixed critical/high unblocks.
	blocked, _ = EvaluateApproval([]domain.Finding{{Severity: domain.SeverityHigh, Disposition: domain.DispositionFixed}})
	if blocked {
		t.Error("fixed high finding should not block")
	}
	// accepted_risk unblocks.
	blocked, _ = EvaluateApproval([]domain.Finding{{Severity: domain.SeverityCritical, Disposition: domain.DispositionAcceptedRisk}})
	if blocked {
		t.Error("accepted_risk critical should not block")
	}
	// Medium never blocks.
	blocked, _ = EvaluateApproval([]domain.Finding{{Severity: domain.SeverityMedium}})
	if blocked {
		t.Error("medium finding should not block")
	}
}

func TestParseFindings(t *testing.T) {
	obj := map[string]any{
		"findings": []any{
			map[string]any{"severity": "high", "confidence": "high", "category": "scope", "finding": "unclear", "recommendation": "clarify"},
		},
	}
	findings, err := ParseFindings("run-1", "prd", "prd-1", obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Severity != domain.SeverityHigh {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}
