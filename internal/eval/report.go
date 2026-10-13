package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Stage is how far a case got, in pipeline order (see stageRank).
type Stage string

const (
	StageNone         Stage = "none"
	StagePRD          Stage = "prd"           // a PRD was written and reviewed
	StagePRDApproved  Stage = "prd_approved"  // the PRD review converged, or was approved
	StagePlanApproved Stage = "plan_approved" // the plan review converged, or was approved
	StageChangeSet    Stage = "change_set"    // a change set was produced
	StageApplied      Stage = "applied"       // every change set applied
	StageVerified     Stage = "verified"      // the verification gate passed
	StageComplete     Stage = "complete"      // `idea next` answers done
)

var stageOrder = []Stage{StageNone, StagePRD, StagePRDApproved, StagePlanApproved, StageChangeSet, StageApplied, StageVerified, StageComplete}

func stageRank(s Stage) int {
	for i, x := range stageOrder {
		if x == s {
			return i
		}
	}
	return -1
}

// Human-judgment stop points (Stop.Stage).
const (
	StopPRDReview        = "prd_review"
	StopPlanReview       = "plan_review"
	StopPlanDependencies = "plan_dependencies"
	StopChangeSetReview  = "change_set_review"
)

// Stop is one place the workflow needed a human. The routine gates every run
// has by design -- approving a reviewed change set, applying it, recording
// verification -- are not stops; a stop is the workflow parking, or a review
// refusing approval.
type Stop struct {
	Stage  string `json:"stage"`
	Detail string `json:"detail,omitempty"`
}

// Check outcomes.
const (
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckSkipped = "skipped"
)

// CheckResult is one acceptance check's outcome.
type CheckResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
}

// CaseResult is one case's measurement.
type CaseResult struct {
	Name     string `json:"name"`
	Scenario string `json:"scenario,omitempty"`
	Reached  Stage  `json:"reached"`

	PRDConverged  bool   `json:"prd_converged"`
	PRDCycles     int    `json:"prd_cycles"`
	PRDOutcome    string `json:"prd_outcome,omitempty"`
	PlanConverged bool   `json:"plan_converged"`

	ChangeSets        int  `json:"change_sets"`
	AppliedChangeSets int  `json:"applied_change_sets"`
	VerifyPassed      bool `json:"verify_passed"`

	Checks        []CheckResult `json:"checks,omitempty"`
	ChecksSkipped bool          `json:"checks_skipped,omitempty"`

	HumanStops []Stop `json:"human_stops"`

	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	WallSeconds  float64 `json:"wall_seconds"`

	// ReRaised and Demoted total the review telemetry over every cycle.
	ReRaised int            `json:"reraised"`
	Demoted  map[string]int `json:"demoted,omitempty"`
	// Telemetry is every review cycle's convergence record, oldest first.
	Telemetry []domain.ReviewTelemetry `json:"telemetry,omitempty"`

	Error               string   `json:"error,omitempty"`
	ExpectationFailures []string `json:"expectation_failures,omitempty"`
}

func (r *CaseResult) reach(s Stage) {
	if stageRank(s) > stageRank(r.Reached) {
		r.Reached = s
	}
}

func (r *CaseResult) stopped(stage string) bool {
	for _, s := range r.HumanStops {
		if s.Stage == stage {
			return true
		}
	}
	return false
}

// summarizeTelemetry derives the review figures from the convergence records
// and the stops the driver saw.
func summarizeTelemetry(r *CaseResult, tel []domain.ReviewTelemetry) {
	prdConverged, planConverged := false, false
	for _, t := range tel {
		for _, n := range t.ReRaised {
			r.ReRaised += n
		}
		for k, n := range t.Demoted {
			if r.Demoted == nil {
				r.Demoted = map[string]int{}
			}
			r.Demoted[k] += n
		}
		switch t.Review {
		case "prd":
			r.PRDCycles++
			r.PRDOutcome = t.Outcome
			if t.Outcome == "converged" {
				prdConverged = true
			}
		case "execution_plan":
			if t.Outcome == "converged" {
				planConverged = true
			}
		}
	}
	r.PRDConverged = prdConverged && !r.stopped(StopPRDReview)
	r.PlanConverged = planConverged && !r.stopped(StopPlanReview) && !r.stopped(StopPlanDependencies)
}

// checkExpect compares a scripted case's result to its expectation.
func checkExpect(e Expect, r CaseResult) []string {
	var out []string
	if r.Error != "" {
		out = append(out, "error: "+r.Error)
	}
	if e.PRDConverged != nil && *e.PRDConverged != r.PRDConverged {
		out = append(out, fmt.Sprintf("prd_converged = %v, want %v", r.PRDConverged, *e.PRDConverged))
	}
	if e.PRDCycles != nil && *e.PRDCycles != r.PRDCycles {
		out = append(out, fmt.Sprintf("prd_cycles = %d, want %d", r.PRDCycles, *e.PRDCycles))
	}
	if e.PlanConverged != nil && *e.PlanConverged != r.PlanConverged {
		out = append(out, fmt.Sprintf("plan_converged = %v, want %v", r.PlanConverged, *e.PlanConverged))
	}
	if e.HumanStops != nil && *e.HumanStops != len(r.HumanStops) {
		out = append(out, fmt.Sprintf("human_stops = %d, want %d", len(r.HumanStops), *e.HumanStops))
	}
	if e.Reached != "" && Stage(e.Reached) != r.Reached {
		out = append(out, fmt.Sprintf("reached = %s, want %s", r.Reached, e.Reached))
	}
	return out
}

// Aggregate summarizes a run.
type Aggregate struct {
	Cases               int            `json:"cases"`
	Completed           int            `json:"completed"`
	PRDConverged        int            `json:"prd_converged"`
	MeanPRDCycles       float64        `json:"mean_prd_cycles"`
	PlanConverged       int            `json:"plan_converged"`
	ProducedChangeSet   int            `json:"produced_change_set"`
	Applied             int            `json:"applied"`
	VerifyPassed        int            `json:"verify_passed"`
	HumanStops          int            `json:"human_stops"`
	HumanStopsByStage   map[string]int `json:"human_stops_by_stage,omitempty"`
	Errors              int            `json:"errors"`
	ExpectationFailures int            `json:"expectation_failures"`
	CostUSD             float64        `json:"cost_usd"`
	InputTokens         int64          `json:"input_tokens"`
	OutputTokens        int64          `json:"output_tokens"`
	WallSeconds         float64        `json:"wall_seconds"`
}

func aggregate(cases []CaseResult) Aggregate {
	a := Aggregate{Cases: len(cases), HumanStopsByStage: map[string]int{}}
	cycles := 0
	for _, c := range cases {
		if c.Reached == StageComplete {
			a.Completed++
		}
		if c.PRDConverged {
			a.PRDConverged++
		}
		cycles += c.PRDCycles
		if c.PlanConverged {
			a.PlanConverged++
		}
		if c.ChangeSets > 0 {
			a.ProducedChangeSet++
		}
		if c.AppliedChangeSets > 0 {
			a.Applied++
		}
		if c.VerifyPassed {
			a.VerifyPassed++
		}
		a.HumanStops += len(c.HumanStops)
		for _, s := range c.HumanStops {
			a.HumanStopsByStage[s.Stage]++
		}
		if c.Error != "" {
			a.Errors++
		}
		if len(c.ExpectationFailures) > 0 {
			a.ExpectationFailures++
		}
		a.CostUSD += c.CostUSD
		a.InputTokens += c.InputTokens
		a.OutputTokens += c.OutputTokens
		a.WallSeconds += c.WallSeconds
	}
	if len(cases) > 0 {
		a.MeanPRDCycles = float64(cycles) / float64(len(cases))
	}
	return a
}

// Report is one eval run.
type Report struct {
	Suite string `json:"suite"`
	// Provider is "scripted" (no --model: deterministic, CI) or "real".
	Provider   string       `json:"provider"`
	Model      string       `json:"model,omitempty"`
	Policy     Policy       `json:"policy"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Cases      []CaseResult `json:"cases"`
	Aggregate  Aggregate    `json:"aggregate"`
	// Regressions is filled when the run was compared to a baseline.
	Regressions []Regression `json:"regressions,omitempty"`
	Baseline    string       `json:"baseline,omitempty"`
}

// Failed reports whether the run should fail a gate: a scripted case missed
// its expectation, or the baseline comparison found a regression.
func (r *Report) Failed() bool {
	return r.Aggregate.ExpectationFailures > 0 || len(r.Regressions) > 0
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// LoadReport reads a report written by WriteJSON (a saved baseline).
func LoadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("eval: parse %s: %w", path, err)
	}
	return &r, nil
}

// WriteMarkdown renders the report for a human: the aggregate, one row per
// case, and each case's review cycles.
func (r *Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# idea eval: %s\n\n", r.Suite)
	model := "scripted provider (deterministic; no model was called)"
	if r.Provider == "real" {
		model = "`" + r.Model + "`"
	}
	fmt.Fprintf(&b, "Model: %s · policy: `%s` · %s\n\n", model, r.Policy, r.StartedAt.Format(time.RFC3339))
	a := r.Aggregate
	b.WriteString("## Aggregate\n\n| Metric | Value |\n| --- | --- |\n")
	row := func(k, v string) { fmt.Fprintf(&b, "| %s | %s |\n", k, v) }
	row("cases", fmt.Sprint(a.Cases))
	row("reached complete", frac(a.Completed, a.Cases))
	row("PRD review converged", frac(a.PRDConverged, a.Cases))
	row("mean PRD review cycles", fmt.Sprintf("%.2f", a.MeanPRDCycles))
	row("plan review converged", frac(a.PlanConverged, a.Cases))
	row("produced a change set", frac(a.ProducedChangeSet, a.Cases))
	row("applied", frac(a.Applied, a.Cases))
	row("verify passed", frac(a.VerifyPassed, a.Cases))
	row("human-judgment stops", fmt.Sprintf("%d %s", a.HumanStops, byStage(a.HumanStopsByStage)))
	row("errors", fmt.Sprint(a.Errors))
	if r.Provider == "scripted" {
		row("expectation failures", fmt.Sprint(a.ExpectationFailures))
	}
	row("cost", fmt.Sprintf("$%.4f", a.CostUSD))
	row("tokens (in / out)", fmt.Sprintf("%d / %d", a.InputTokens, a.OutputTokens))
	row("wall time", fmt.Sprintf("%.1fs", a.WallSeconds))

	b.WriteString("\n## Cases\n\n")
	b.WriteString("| Case | Scenario | Reached | PRD review | Plan review | Change sets | Applied | Verify | Stops | Cost | Tokens in/out | Wall |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, c := range r.Cases {
		prd := fmt.Sprintf("%s in %d", yesNo(c.PRDConverged, "converged", "did not converge"), c.PRDCycles)
		if c.PRDOutcome != "" && !c.PRDConverged {
			prd += " (" + c.PRDOutcome + ")"
		}
		verify := yesNo(c.VerifyPassed, "passed", "no")
		if c.ChecksSkipped && c.VerifyPassed {
			verify = "passed (checks skipped)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d | %d | %s | %s | $%.4f | %d/%d | %.1fs |\n",
			c.Name, nonEmpty(c.Scenario, "—"), c.Reached, prd, yesNo(c.PlanConverged, "converged", "stopped"),
			c.ChangeSets, c.AppliedChangeSets, verify, stopsCell(c.HumanStops), c.CostUSD, c.InputTokens, c.OutputTokens, c.WallSeconds)
	}

	var problems []string
	for _, c := range r.Cases {
		if c.Error != "" {
			problems = append(problems, fmt.Sprintf("- **%s**: %s", c.Name, c.Error))
		}
		for _, f := range c.ExpectationFailures {
			problems = append(problems, fmt.Sprintf("- **%s** expectation: %s", c.Name, f))
		}
		for _, ch := range c.Checks {
			if ch.Status == CheckFailed {
				problems = append(problems, fmt.Sprintf("- **%s** check `%s` failed", c.Name, ch.Name))
			}
		}
	}
	if len(problems) > 0 {
		b.WriteString("\n## Problems\n\n" + strings.Join(problems, "\n") + "\n")
	}
	if len(r.Regressions) > 0 {
		fmt.Fprintf(&b, "\n## Regressions against %s\n\n| Case | Metric | Baseline | Now |\n| --- | --- | --- | --- |\n", r.Baseline)
		for _, g := range r.Regressions {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", g.Case, g.Metric, g.Baseline, g.Current)
		}
	} else if r.Baseline != "" {
		fmt.Fprintf(&b, "\nNo regressions against %s.\n", r.Baseline)
	}

	b.WriteString("\n## Review cycles\n\n")
	b.WriteString("| Case | Review | Cycle | Mode | Raised | New | Re-raised | Resolved | Demoted | Open blocking | Outcome |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, c := range r.Cases {
		for _, t := range c.Telemetry {
			prev := ""
			if t.PrevOpenBlocking >= 0 {
				prev = fmt.Sprintf("%d → ", t.PrevOpenBlocking)
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s | %s | %s | %s%d | %s |\n",
				c.Name, t.Review, t.Cycle, t.Mode, sevs(t.Raised), sevs(t.New), sevs(t.ReRaised), sevs(t.Resolved),
				counts(t.Demoted), prev, t.OpenBlocking, t.Outcome)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func frac(n, d int) string {
	if d == 0 {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", n, d, 100*float64(n)/float64(d))
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func stopsCell(stops []Stop) string {
	if len(stops) == 0 {
		return "0"
	}
	var names []string
	for _, s := range stops {
		names = append(names, s.Stage)
	}
	return fmt.Sprintf("%d (%s)", len(stops), strings.Join(names, ", "))
}

func byStage(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	return "(" + counts(m) + ")"
}

var sevOrder = []domain.Severity{domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow, domain.SeverityObservation}

// sevs renders a per-severity count as "2H 1M" (critical C, high H, ...).
func sevs(m map[domain.Severity]int) string {
	var parts []string
	for _, s := range sevOrder {
		if n := m[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d%s", n, strings.ToUpper(string(s)[:1])))
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if m[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}
