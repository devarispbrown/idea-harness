package eval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// IDEA_EVAL_UPDATE_BASELINE=1 rewrites the saved scripted baseline from this
// run (an environment variable rather than a flag, so it can be set on a
// `go test ./...` that also builds packages which do not define the flag):
//
//	IDEA_EVAL_UPDATE_BASELINE=1 go test ./internal/eval/ -run TestDefaultSuiteScripted
func updateBaseline() bool { return os.Getenv("IDEA_EVAL_UPDATE_BASELINE") != "" }

const baselinePath = "testdata/baseline-scripted.json"

// TestDefaultSuiteScripted is the CI gate: the whole default suite, end to
// end through a real in-process daemon, against the deterministic scripted
// provider. It fails when a case misses its scenario's expectation -- most
// importantly, when the rephrasing reviewer stops converging under the
// guarded loop, when the legacy loop stops reproducing the non-convergence,
// or when the genuine blocker stops parking for a human -- and when any case
// regresses against the saved baseline.
func TestDefaultSuiteScripted(t *testing.T) {
	suite, err := LoadSuite(DefaultSuite)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep, err := Run(ctx, Options{Suite: suite, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	var md bytes.Buffer
	if err := rep.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + md.String())

	for _, c := range rep.Cases {
		for _, f := range c.ExpectationFailures {
			t.Errorf("%s: %s", c.Name, f)
		}
		// Scripted runs use the internal executor, so acceptance checks must
		// be reported skipped -- never silently counted as passing.
		if len(c.Checks) > 0 && !c.ChecksSkipped {
			t.Errorf("%s: checks ran against the placeholder executor", c.Name)
		}
	}

	if updateBaseline() {
		var b bytes.Buffer
		if err := normalized(rep).WriteJSON(&b); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(baselinePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(baselinePath, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", baselinePath)
		return
	}
	base, err := LoadReport(baselinePath)
	if err != nil {
		t.Fatalf("%v (regenerate with -update)", err)
	}
	regs, err := Compare(base, rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range regs {
		t.Errorf("regression in %s: %s %s -> %s", g.Case, g.Metric, g.Baseline, g.Current)
	}
	if len(base.Cases) != len(rep.Cases) {
		t.Errorf("baseline has %d cases, the suite %d: regenerate it with -update", len(base.Cases), len(rep.Cases))
	}
}

// normalized strips what varies run to run (times) so the saved baseline
// only changes when an outcome does.
func normalized(r *Report) *Report {
	n := *r
	n.StartedAt, n.FinishedAt = time.Time{}, time.Time{}
	n.Aggregate.WallSeconds = 0
	n.Cases = append([]CaseResult(nil), r.Cases...)
	for i := range n.Cases {
		n.Cases[i].WallSeconds = 0
	}
	return &n
}

// The stop policy ends a case at its first human-judgment stop.
func TestStopPolicyEndsAtTheFirstStop(t *testing.T) {
	suite, _ := LoadSuite(DefaultSuite)
	suite, err := suite.Filter([]string{"habit-tracker"})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), Options{Suite: suite, Policy: PolicyStop, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c := rep.Cases[0]
	if len(c.HumanStops) != 1 || c.HumanStops[0].Stage != StopPRDReview || c.Reached != StagePRD || c.Error != "" {
		t.Fatalf("want one prd_review stop and nothing after it, got %+v", c)
	}
}

func TestCompareFlagsEachRegression(t *testing.T) {
	base := &Report{Provider: "real", Cases: []CaseResult{{
		Name: "a", Reached: StageComplete, PRDConverged: true, PRDCycles: 1, PlanConverged: true, VerifyPassed: true, CostUSD: 1,
	}}}
	cur := &Report{Provider: "real", Cases: []CaseResult{{
		Name: "a", Reached: StageApplied, PRDCycles: 3, HumanStops: []Stop{{Stage: StopPRDReview}}, Error: "boom", CostUSD: 2,
	}, {Name: "only-now", Reached: StageNone}}}
	regs, err := Compare(base, cur)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, g := range regs {
		got[g.Metric] = true
	}
	for _, m := range []string{"reached", "prd_converged", "prd_cycles", "plan_converged", "human_stops", "verify_passed", "error", "cost_usd"} {
		if !got[m] {
			t.Errorf("missing regression %q in %+v", m, regs)
		}
	}
	if len(regs) != 8 {
		t.Errorf("a case only one side has must be skipped; got %d regressions", len(regs))
	}
	if same, _ := Compare(base, base); len(same) != 0 {
		t.Errorf("a run is no regression of itself: %+v", same)
	}
}

func TestCompareRefusesScriptedAgainstReal(t *testing.T) {
	if _, err := Compare(&Report{Provider: "scripted"}, &Report{Provider: "real"}); err == nil {
		t.Fatal("comparing a scripted baseline with a real run must be refused")
	}
}

func TestSuiteValidation(t *testing.T) {
	for name, body := range map[string]string{
		"no cases":         "name: x\ncases: []\n",
		"no idea":          "name: x\ncases:\n  - name: a\n",
		"duplicate":        "name: x\ncases:\n  - {name: a, idea: i}\n  - {name: a, idea: i}\n",
		"unknown scenario": "name: x\ncases:\n  - {name: a, idea: i, scenario: nope}\n",
	} {
		p := filepath.Join(t.TempDir(), "s.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSuite(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadSuite("no-such-suite"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Errorf("an unknown suite should name the built-in ones: %v", err)
	}
	s, _ := LoadSuite(DefaultSuite)
	if len(s.Cases) < 5 || len(s.Cases) > 8 {
		t.Errorf("the default suite should stay small: %d cases", len(s.Cases))
	}
	if _, err := s.Filter([]string{"todo-cli", "nope"}); err == nil {
		t.Error("filtering on an unknown case must say so")
	}
}

func TestRunRejectsABadModelSpec(t *testing.T) {
	s, _ := LoadSuite(DefaultSuite)
	if _, err := Run(context.Background(), Options{Suite: s, Model: "gpt-5"}); err == nil {
		t.Fatal("a model with no provider must be refused before anything runs")
	}
	if _, err := Run(context.Background(), Options{Suite: s, Policy: "yolo"}); err == nil {
		t.Fatal("an unknown policy must be refused")
	}
}
