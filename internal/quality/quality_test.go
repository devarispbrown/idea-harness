package quality

import (
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func chk(s domain.QualityStatus) domain.QualityCheck { return domain.QualityCheck{Name: string(s), Status: s} }

func TestRollupOrder(t *testing.T) {
	cases := []struct {
		in   []domain.QualityStatus
		want domain.QualityStatus
	}{
		{nil, domain.QualityNotMeasured},
		{[]domain.QualityStatus{domain.QualityNotApplicable}, domain.QualityNotApplicable},
		{[]domain.QualityStatus{domain.QualityNotApplicable, domain.QualityPass}, domain.QualityPass},
		{[]domain.QualityStatus{domain.QualityPass, domain.QualityWarn}, domain.QualityWarn},
		// Unmeasured outranks warn and pass: a partly-measured dimension has
		// not passed.
		{[]domain.QualityStatus{domain.QualityPass, domain.QualityWarn, domain.QualityNotMeasured}, domain.QualityNotMeasured},
		{[]domain.QualityStatus{domain.QualityNotMeasured, domain.QualityFail}, domain.QualityFail},
	}
	for _, tc := range cases {
		var checks []domain.QualityCheck
		for _, s := range tc.in {
			checks = append(checks, chk(s))
		}
		if got := Rollup(checks); got != tc.want {
			t.Errorf("Rollup(%v)=%s want %s", tc.in, got, tc.want)
		}
	}
}

func TestDefaultModesPerRigor(t *testing.T) {
	quick := DefaultModes("quick")
	for _, d := range domain.QualityDimensions {
		if quick[d] != domain.QualityModeOff {
			t.Errorf("quick: %s should be off", d)
		}
	}
	std := DefaultModes("standard")
	thr := DefaultModes("thorough")
	core := map[domain.QualityDimension]bool{domain.QualitySetupReproducibility: true, domain.QualityDocsCompleteness: true, domain.QualityAPICompatibility: true}
	for _, d := range domain.QualityDimensions {
		wantStd, wantThr := domain.QualityModeOff, domain.QualityModeAdvisory
		if core[d] {
			wantStd, wantThr = domain.QualityModeAdvisory, domain.QualityModeGate
		}
		if std[d] != wantStd {
			t.Errorf("standard: %s = %s want %s", d, std[d], wantStd)
		}
		if thr[d] != wantThr {
			t.Errorf("thorough: %s = %s want %s", d, thr[d], wantThr)
		}
	}
	if DefaultModes("bogus")[domain.QualityDocsCompleteness] != domain.QualityModeAdvisory {
		t.Error("an unknown rigor falls back to standard")
	}
}

func TestResolvePolicyOverrides(t *testing.T) {
	p := ResolvePolicy("quick", map[string]string{"security": "gate", "docs_completeness": "advisory", "nonsense": "gate", "performance": "bogus"})
	if p.Modes[domain.QualitySecurity] != domain.QualityModeGate || p.Modes[domain.QualityDocsCompleteness] != domain.QualityModeAdvisory {
		t.Fatalf("overrides not applied: %+v", p.Modes)
	}
	if p.Modes[domain.QualityPerformance] != domain.QualityModeOff {
		t.Error("an invalid mode must leave the default")
	}
	if len(p.Overridden) != 2 || !p.Enabled() {
		t.Errorf("overridden=%v enabled=%v", p.Overridden, p.Enabled())
	}
	if ResolvePolicy("quick", nil).Enabled() {
		t.Error("quick with no overrides measures nothing")
	}
}

func TestGateUnmetConsidersOnlyGatedDimensions(t *testing.T) {
	p := ResolvePolicy("quick", map[string]string{"api_compatibility": "gate", "docs_completeness": "gate", "security": "advisory", "setup_reproducibility": "gate"})
	results := map[domain.QualityDimension]domain.QualityResult{
		domain.QualityAPICompatibility:     {Status: domain.QualityWarn, InputDigest: "d1"},
		domain.QualityDocsCompleteness:     {Status: domain.QualityNotMeasured, InputDigest: "d1"},
		domain.QualitySecurity:             {Status: domain.QualityFail, InputDigest: "d1"}, // advisory: ignored
		domain.QualitySetupReproducibility: {Status: domain.QualityPass, InputDigest: "old"},
	}
	unmet := GateUnmet(p, results, "d1")
	joined := strings.Join(unmet, "; ")
	if len(unmet) != 2 || !strings.Contains(joined, "docs_completeness not measured") || !strings.Contains(joined, "setup_reproducibility not measured since the last change") {
		t.Fatalf("unmet = %v", unmet)
	}
	delete(results, domain.QualityDocsCompleteness)
	if u := GateUnmet(p, results, "d1"); !strings.Contains(strings.Join(u, ";"), "docs_completeness not measured") {
		t.Fatalf("a missing gated result is not measured: %v", u)
	}
}

func TestBudgetVerdict(t *testing.T) {
	max := 100.0
	base := 50.0
	if s, d := BudgetVerdict("b", "ns/op", 90, &max, 0, nil); s != domain.QualityPass || !strings.Contains(d, "within") {
		t.Errorf("under max: %s %s", s, d)
	}
	if s, _ := BudgetVerdict("b", "", 101, &max, 0, nil); s != domain.QualityFail {
		t.Error("over max must fail")
	}
	if s, d := BudgetVerdict("b", "", 70, nil, 20, &base); s != domain.QualityFail || !strings.Contains(d, "+40.0%") {
		t.Errorf("40%% regression over a 20%% allowance must fail: %s %s", s, d)
	}
	if s, d := BudgetVerdict("b", "", 70, nil, 20, nil); s != domain.QualityPass || !strings.Contains(d, "no baseline yet") {
		t.Errorf("no baseline: %s %s", s, d)
	}
	if v, err := ExtractMetric(`BenchmarkX\S*\s+\d+\s+([0-9.,]+) ns/op`, "BenchmarkX-8   1000   1,234.5 ns/op\n"); err != nil || v != 1234.5 {
		t.Errorf("ExtractMetric = %v %v", v, err)
	}
	if _, err := ExtractMetric(`(\d+) B`, "nothing"); err == nil {
		t.Error("no match must be an error, not zero")
	}
}
