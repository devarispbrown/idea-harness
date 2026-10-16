package eval

import "fmt"

// Regression is one way a run is worse than its baseline.
type Regression struct {
	Case     string `json:"case"`
	Metric   string `json:"metric"`
	Baseline string `json:"baseline"`
	Current  string `json:"current"`
}

// CostRegressionRatio is how much more a case may cost than its baseline
// before it counts as a regression. Real-model spend varies run to run, so
// only a clear jump is flagged; a scripted run costs nothing either way.
const CostRegressionRatio = 1.25

// Compare returns every way cur is worse than base, case by case (matched by
// name; a case only one side has is skipped, so a filtered run can still be
// compared). Only outcomes are compared -- how far a case got, whether each
// review converged and in how many cycles, how many times a human was needed,
// whether verification passed, whether it errored -- plus cost when both runs
// were billed. Wall time is reported but never compared: it measures the
// machine and the provider's queue as much as the harness.
//
// Comparing a scripted run with a real one is refused: neither predicts the
// other, and a "regression" between them would be noise.
func Compare(base, cur *Report) ([]Regression, error) {
	if base.Provider != cur.Provider {
		return nil, fmt.Errorf("eval: the baseline is a %s run and this is a %s run; compare like with like", base.Provider, cur.Provider)
	}
	byName := make(map[string]CaseResult, len(base.Cases))
	for _, c := range base.Cases {
		byName[c.Name] = c
	}
	var out []Regression
	add := func(c, metric string, b, n any) {
		out = append(out, Regression{Case: c, Metric: metric, Baseline: fmt.Sprint(b), Current: fmt.Sprint(n)})
	}
	for _, c := range cur.Cases {
		b, ok := byName[c.Name]
		if !ok {
			continue
		}
		if stageRank(c.Reached) < stageRank(b.Reached) {
			add(c.Name, "reached", b.Reached, c.Reached)
		}
		if b.PRDConverged && !c.PRDConverged {
			add(c.Name, "prd_converged", true, false)
		}
		if c.PRDCycles > b.PRDCycles {
			add(c.Name, "prd_cycles", b.PRDCycles, c.PRDCycles)
		}
		if b.PlanConverged && !c.PlanConverged {
			add(c.Name, "plan_converged", true, false)
		}
		if len(c.HumanStops) > len(b.HumanStops) {
			add(c.Name, "human_stops", len(b.HumanStops), len(c.HumanStops))
		}
		if b.VerifyPassed && !c.VerifyPassed {
			add(c.Name, "verify_passed", true, false)
		}
		if b.Error == "" && c.Error != "" {
			add(c.Name, "error", "none", c.Error)
		}
		if b.CostUSD > 0 && c.CostUSD > b.CostUSD*CostRegressionRatio {
			add(c.Name, "cost_usd", fmt.Sprintf("%.4f", b.CostUSD), fmt.Sprintf("%.4f", c.CostUSD))
		}
	}
	return out, nil
}
