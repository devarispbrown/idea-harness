package config

import (
	"fmt"
	"strings"
)

// BudgetConfig is harness.yaml's `budget` section: the spending cap every
// initiative in the project is held to unless it sets its own (`idea budget
// <initiative> --set <usd>`, stored in the project database because a cap is
// a decision about one piece of work, not portable configuration).
//
// It is a different control from review.max_cost_usd. That one is a
// process-lifetime ceiling on model calls, reset whenever the daemon reloads
// the project; this one is measured from the initiative's own recorded spend
// (GET .../costs), so it survives restarts and counts the coding agent's
// measured spend too.
type BudgetConfig struct {
	// DefaultInitiativeUSD caps each initiative's spend, in US dollars. Zero
	// (the default) means no cap.
	DefaultInitiativeUSD float64 `json:"default_initiative_usd,omitempty" yaml:"default_initiative_usd,omitempty"`
}

// AutomationsConfig is harness.yaml's `automations` section: background jobs
// the daemon runs on a schedule. Every one is off unless its `enabled` is
// true -- nothing here runs because a section merely exists, and nothing in
// the harness turns one on by itself. (Linear inbound polling, the third
// background job the Automations view lists, keeps its switch where it
// always was: integrations.linear.inbound.)
type AutomationsConfig struct {
	MetricMeasurement *MetricAutomationConfig `json:"metric_measurement,omitempty" yaml:"metric_measurement,omitempty"`
	Eval              *EvalAutomationConfig   `json:"eval,omitempty" yaml:"eval,omitempty"`
}

// IsZero lets yaml.v3's omitempty drop an empty automations block.
func (a AutomationsConfig) IsZero() bool { return a.MetricMeasurement == nil && a.Eval == nil }

// MetricAutomationConfig schedules `idea metric measure` for every live
// outcome metric whose source is a command or a url, each on its own
// cadence. Manual metrics are never touched (there is nothing to measure),
// and url sources still need outcomes.allow_url_sources -- enabling the
// schedule does not imply network access.
type MetricAutomationConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// MinMetricCadenceMinutes is the floor on how often a scheduled measurement
// runs, whatever a metric's cadence says: a command source is a container
// start, and "1m" would be one a minute forever.
const MinMetricCadenceMinutes = 15

// EvalAutomationConfig schedules `idea eval` against a real model.
type EvalAutomationConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Model is the provider/model every case runs against. Required: a
	// scheduled scripted run measures nothing that changes between runs.
	Model string `json:"model" yaml:"model"`
	// Suite is a suite file path relative to the project root; empty is the
	// built-in default suite.
	Suite string `json:"suite,omitempty" yaml:"suite,omitempty"`
	// Cases narrows the suite to these case names; empty runs every case.
	Cases []string `json:"cases,omitempty" yaml:"cases,omitempty"`
	// IntervalHours is how often it runs. Default 24 (daily), minimum 1.
	IntervalHours int `json:"interval_hours,omitempty" yaml:"interval_hours,omitempty"`
	// MaxCostUSD caps one run's total spend. Required and > 0 when enabled:
	// a scheduled job that spends real money has to say how much.
	MaxCostUSD float64 `json:"max_cost_usd" yaml:"max_cost_usd"`
	// Baseline is a saved report (`idea eval --json-out`) to compare each run
	// with, relative to the project root. Empty compares with the previous
	// scheduled run that finished.
	Baseline string `json:"baseline,omitempty" yaml:"baseline,omitempty"`
}

// EffectiveIntervalHours returns IntervalHours with its default applied.
func (e EvalAutomationConfig) EffectiveIntervalHours() int {
	if e.IntervalHours <= 0 {
		return 24
	}
	return e.IntervalHours
}

// validate checks what the schema cannot: enabled jobs carry what they need.
func (b BudgetConfig) validate() error {
	if b.DefaultInitiativeUSD < 0 {
		return fmt.Errorf("config: harness.budget.default_initiative_usd must be >= 0")
	}
	return nil
}

func (a AutomationsConfig) validate() error {
	if e := a.Eval; e != nil {
		if e.IntervalHours < 0 {
			return fmt.Errorf("config: harness.automations.eval.interval_hours must be >= 1")
		}
		if e.MaxCostUSD < 0 {
			return fmt.Errorf("config: harness.automations.eval.max_cost_usd must be >= 0")
		}
		if e.Enabled {
			p, m := ParseModelSpec(e.Model)
			if p == "" || m == "" {
				return fmt.Errorf("config: harness.automations.eval.model must be provider/model when the eval schedule is enabled")
			}
			if e.MaxCostUSD <= 0 {
				return fmt.Errorf("config: harness.automations.eval.max_cost_usd must be > 0 when the eval schedule is enabled; a scheduled job that spends real money has to say how much")
			}
		}
		for _, p := range []string{e.Suite, e.Baseline} {
			if p != "" && (strings.HasPrefix(p, "/") || strings.Contains(p, "..")) {
				return fmt.Errorf("config: harness.automations.eval paths must be relative to the project root, without '..' (got %q)", p)
			}
		}
	}
	return nil
}
