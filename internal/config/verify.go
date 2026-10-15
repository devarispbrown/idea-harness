package config

import (
	"fmt"
	"regexp"
)

// VerifyConfig is harness.yaml's `verify` section: the quality dimensions
// `idea verify` measures beyond the acceptance tests, and the commands some
// of them run. Everything here is optional. With the section absent, each
// dimension's mode comes from the effective review rigor (see
// workflow.QualityPolicy), the documented setup commands are detected from the
// README/Makefile/package.json, nothing runs with network access, and
// performance has no budgets (so it reports not_measured).
type VerifyConfig struct {
	// Dimensions sets a dimension's mode, overriding the rigor default:
	// dimension name -> "off" | "advisory" | "gate". Unknown names are refused
	// by the schema rather than ignored, so a typo cannot silently leave a
	// dimension at its default.
	Dimensions map[string]string `json:"dimensions,omitempty" yaml:"dimensions,omitempty"`
	// Setup configures setup_reproducibility.
	Setup VerifySetupConfig `json:"setup,omitempty" yaml:"setup,omitempty"`
	// Security configures the security dimension's scanners.
	Security VerifySecurityConfig `json:"security,omitempty" yaml:"security,omitempty"`
	// Performance declares performance budgets.
	Performance VerifyPerformanceConfig `json:"performance,omitempty" yaml:"performance,omitempty"`
	// ModelReview adds a model-reviewed check (the verification_engineer role,
	// citations required) to the named deterministic dimensions. Only
	// docs_completeness and operational_readiness take one; api_ergonomics is
	// model-reviewed by definition and needs no entry here.
	ModelReview []string `json:"model_review,omitempty" yaml:"model_review,omitempty"`
	// TimeoutSeconds bounds each sandboxed command a dimension runs. Zero uses
	// check_timeout_seconds, and that unset uses 600.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}

// VerifySetupConfig configures setup_reproducibility.
type VerifySetupConfig struct {
	// Commands replaces documented-command detection outright: run exactly
	// these, in order, in the no-network sandbox against a fresh copy of the
	// workspace. Empty means detect them from the README, Makefile and
	// package.json.
	Commands []string `json:"commands,omitempty" yaml:"commands,omitempty"`
	// Prepare are commands run BEFORE the offline phase, in a sandbox WITH
	// network access, to fetch dependencies (e.g. `go mod download`, `npm ci`).
	// Only commands listed here ever get the network; a documented install
	// command found in the README is skipped in the offline phase and
	// recorded, never promoted to this phase. See docs/security-model.md.
	Prepare []string `json:"prepare,omitempty" yaml:"prepare,omitempty"`
}

// VerifySecurityConfig configures the security dimension.
type VerifySecurityConfig struct {
	// Scanners are commands run in the sandbox against a fresh copy of the
	// workspace; a non-zero exit is a fail. None are run by default: the
	// common ones (govulncheck, npm audit, pip-audit) need a vulnerability
	// database from the network, so a scanner that needs it must say
	// `network: true`, and without any scanner the vulnerability check is
	// recorded as not_measured rather than passed.
	Scanners []VerifyScanner `json:"scanners,omitempty" yaml:"scanners,omitempty"`
}

// VerifyScanner is one security scanner command.
type VerifyScanner struct {
	Name    string `json:"name" yaml:"name"`
	Command string `json:"command" yaml:"command"`
	// Network runs this scanner with network access (a vulnerability database
	// download). Off by default. See docs/security-model.md.
	Network bool `json:"network,omitempty" yaml:"network,omitempty"`
}

// VerifyPerformanceConfig declares performance budgets.
type VerifyPerformanceConfig struct {
	Budgets []PerformanceBudget `json:"budgets,omitempty" yaml:"budgets,omitempty"`
}

// PerformanceBudget is one user-declared performance budget: a command run in
// the no-network sandbox, a regular expression whose first capture group is
// the measured number, and a threshold -- absolute (Max), relative to the
// previous measurement (MaxRegressionPct), or both.
type PerformanceBudget struct {
	Name    string `json:"name" yaml:"name"`
	Command string `json:"command" yaml:"command"`
	// Metric is a regular expression matched against the command's output;
	// its first capture group must parse as a number (e.g.
	// `BenchmarkParse\S*\s+\d+\s+([0-9.]+) ns/op`).
	Metric string `json:"metric" yaml:"metric"`
	// Unit labels the number in reports ("ns/op", "bytes"). Cosmetic.
	Unit string `json:"unit,omitempty" yaml:"unit,omitempty"`
	// Max fails the budget when the measured value exceeds it. Nil: no
	// absolute ceiling.
	Max *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	// MaxRegressionPct fails the budget when the value grew by more than this
	// percentage over the previous measurement's. Zero: no regression check.
	MaxRegressionPct float64 `json:"max_regression_pct,omitempty" yaml:"max_regression_pct,omitempty"`
}

// VerifyModelReviewDimensions are the deterministic dimensions that accept an
// optional model-reviewed check (verify.model_review).
var VerifyModelReviewDimensions = []string{"docs_completeness", "operational_readiness"}

// WantsModelReview reports whether verify.model_review names dimension.
func (v VerifyConfig) WantsModelReview(dimension string) bool {
	for _, d := range v.ModelReview {
		if d == dimension {
			return true
		}
	}
	return false
}

// validateVerify checks what the schema cannot: that each performance
// budget's metric compiles and has a capture group to read the number from,
// and that budget names are unique (a budget's name is the key its baseline
// is stored under, so two budgets sharing one would compare against each
// other's numbers).
func validateVerify(v VerifyConfig) error {
	seen := map[string]bool{}
	for i, b := range v.Performance.Budgets {
		re, err := regexp.Compile(b.Metric)
		if err != nil {
			return fmt.Errorf("config: harness.verify.performance.budgets[%d].metric does not compile: %v", i, err)
		}
		if re.NumSubexp() < 1 {
			return fmt.Errorf("config: harness.verify.performance.budgets[%d].metric needs a capture group around the number, e.g. `([0-9.]+) ns/op`", i)
		}
		if seen[b.Name] {
			return fmt.Errorf("config: harness.verify.performance.budgets: the name %q is used twice; each budget's name keys its baseline", b.Name)
		}
		seen[b.Name] = true
	}
	return nil
}
