// Package eval is `idea eval`: a fixed suite of small greenfield ideas run end
// to end through a real, in-process daemon -- idea → PRD review → plan →
// change set → apply → verify -- with an automatic judgment policy standing
// in for the human, reporting per case and in aggregate whether each review
// converged, where a human would have been needed, and what it cost.
//
// It exists because there was no measurement at all. The README's real-model
// numbers ("the PRD review did not converge in 2 of 2 runs") came from two
// hand-driven runs, and nothing could tell whether a change to the review loop
// made that better or worse. A suite run against the deterministic scripted
// provider is a CI regression gate (eval_test.go); the same suite run with
// --model against a real provider is the measurement.
package eval

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed suites/*.yaml
var suitesFS embed.FS

// Suite is a named, fixed list of cases.
type Suite struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Cases       []Case `yaml:"cases" json:"cases"`
}

// Case is one seed idea and what it should achieve.
type Case struct {
	Name  string `yaml:"name" json:"name"`
	Title string `yaml:"title" json:"title"`
	Idea  string `yaml:"idea" json:"idea"`
	// Scenario selects the scripted provider's behaviour when the suite runs
	// without --model (see scenario.go). Ignored against a real model.
	Scenario string `yaml:"scenario" json:"scenario,omitempty"`
	// Harness is YAML appended to the case project's harness.yaml -- e.g. a
	// review.convergence override, so the suite can run one scenario under
	// both the guarded and the legacy loop.
	Harness string `yaml:"harness" json:"harness,omitempty"`
	// Checks are the case's acceptance checks: shell commands run in the
	// project directory after every change set is applied. They are what
	// "verify passed" means against a real coding agent. The built-in
	// internal executor writes a placeholder file, so against it they are
	// skipped and reported as such -- never counted as passing.
	Checks []Check `yaml:"checks" json:"checks,omitempty"`
	// Expect is what the scripted scenario must produce. Checked only when
	// the suite runs against the scripted provider: a real model's behaviour
	// is the measurement, not an assertion.
	Expect *Expect `yaml:"expect" json:"expect,omitempty"`
}

// Check is one acceptance check.
type Check struct {
	Name string `yaml:"name" json:"name"`
	Run  string `yaml:"run" json:"run"`
}

// Expect is a scripted case's expected outcome. Nil fields are not checked.
type Expect struct {
	PRDConverged  *bool  `yaml:"prd_converged" json:"prd_converged,omitempty"`
	PRDCycles     *int   `yaml:"prd_cycles" json:"prd_cycles,omitempty"`
	PlanConverged *bool  `yaml:"plan_converged" json:"plan_converged,omitempty"`
	HumanStops    *int   `yaml:"human_stops" json:"human_stops,omitempty"`
	Reached       string `yaml:"reached" json:"reached,omitempty"`
}

// DefaultSuite is the suite `idea eval` runs when none is named.
const DefaultSuite = "default"

// SuiteNames lists the built-in suites.
func SuiteNames() []string {
	entries, _ := suitesFS.ReadDir("suites")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(out)
	return out
}

// LoadSuite resolves ref as a built-in suite name, or else as a path to a
// suite YAML file.
func LoadSuite(ref string) (Suite, error) {
	if ref == "" {
		ref = DefaultSuite
	}
	data, err := suitesFS.ReadFile("suites/" + ref + ".yaml")
	if err != nil {
		data, err = os.ReadFile(ref)
		if err != nil {
			return Suite{}, fmt.Errorf("eval: no built-in suite %q (have %s), and no file at that path", ref, strings.Join(SuiteNames(), ", "))
		}
	}
	var s Suite
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Suite{}, fmt.Errorf("eval: parse suite %s: %w", ref, err)
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(ref), ".yaml")
	}
	return s, s.validate()
}

func (s Suite) validate() error {
	if len(s.Cases) == 0 {
		return fmt.Errorf("eval: suite %q has no cases", s.Name)
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		if c.Name == "" || strings.TrimSpace(c.Idea) == "" {
			return fmt.Errorf("eval: suite %q case %d needs a name and an idea", s.Name, i+1)
		}
		if seen[c.Name] {
			return fmt.Errorf("eval: suite %q names case %q twice", s.Name, c.Name)
		}
		seen[c.Name] = true
		if c.Scenario != "" && !knownScenario(c.Scenario) {
			return fmt.Errorf("eval: case %q: unknown scenario %q (have %s)", c.Name, c.Scenario, strings.Join(Scenarios(), ", "))
		}
	}
	return nil
}

// Filter keeps only the named cases (all of them when names is empty).
func (s Suite) Filter(names []string) (Suite, error) {
	if len(names) == 0 {
		return s, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var kept []Case
	for _, c := range s.Cases {
		if want[c.Name] {
			kept = append(kept, c)
			delete(want, c.Name)
		}
	}
	if len(want) > 0 {
		var missing []string
		for n := range want {
			missing = append(missing, n)
		}
		sort.Strings(missing)
		return s, fmt.Errorf("eval: suite %q has no case %s", s.Name, strings.Join(missing, ", "))
	}
	s.Cases = kept
	return s, nil
}
