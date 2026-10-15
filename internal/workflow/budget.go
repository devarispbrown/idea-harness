package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/projection"
)

// Spending caps (harness.yaml budget.default_initiative_usd, or one
// initiative's own cap set with `idea budget`).
//
// The cap is checked BEFORE every model call (runModel, describeImage) and
// before the coding agent starts (Execute, for every executor kind -- the
// local container kinds, acp and openhands alike all start through the one
// exec.Start). A call already in flight finishes and is recorded, so an
// initiative can end at most one call over its cap; the next call is
// refused. Spend is the initiative's own recorded spend, the same number GET
// .../costs reports: every model call's billed cost plus the coding agent's,
// where the agent reports one. Hermes and OMP usually print no cost line, so
// their spend is not counted -- the budget view says so rather than
// pretending the meter covers it.

// ErrBudgetExceeded is matched (errors.Is) by every BudgetExceededError.
var ErrBudgetExceeded = errors.New("workflow: budget exceeded")

// BudgetExceededError says an initiative reached its cap before a call.
type BudgetExceededError struct {
	InitiativeID string
	SpentUSD     float64
	CapUSD       float64
	// Before names the call that was refused ("the plan_review model call",
	// "the hermes agent run").
	Before string
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("budget exceeded: this initiative has spent $%.4f of its $%.2f cap, so %s was not started; "+
		"raise the cap with `idea budget %s --set <usd>` and run the step again", e.SpentUSD, e.CapUSD, e.Before, e.InitiativeID)
}

// Is makes errors.Is(err, ErrBudgetExceeded) match.
func (e *BudgetExceededError) Is(target error) bool { return target == ErrBudgetExceeded }

// Detail is the short "$x of $y" form the budget_exceeded event carries.
func (e *BudgetExceededError) Detail() string {
	return fmt.Sprintf("$%.4f of $%.2f (before %s)", e.SpentUSD, e.CapUSD, e.Before)
}

// Budget sources: where an initiative's effective cap comes from.
const (
	BudgetSourceInitiative = "initiative"
	BudgetSourceProject    = "project"
	BudgetSourceNone       = "none"
)

// BudgetStatus is an initiative's cap and spend (GET .../budget).
type BudgetStatus struct {
	// CapUSD is the effective cap; nil when there is none.
	CapUSD *float64 `json:"cap_usd"`
	// Source is initiative, project or none.
	Source string `json:"source"`
	// InitiativeCapUSD is the initiative's own cap, when it set one.
	InitiativeCapUSD *float64 `json:"initiative_cap_usd"`
	// ProjectDefaultUSD is budget.default_initiative_usd (0: none).
	ProjectDefaultUSD float64 `json:"project_default_usd"`
	// SpentUSD is what counts against the cap: ModelUSD plus AgentUSD when
	// the agent's spend was measured.
	SpentUSD float64 `json:"spent_usd"`
	ModelUSD float64 `json:"model_usd"`
	// AgentUSD is the coding agent's measured spend; nil when not measured.
	AgentUSD  *float64 `json:"agent_usd"`
	AgentRuns int      `json:"agent_runs"`
	// AgentNotCounted is true when the agent ran but its spend could not be
	// measured (hermes and omp print no reliable cost), so the meter
	// understates the real spend by that much.
	AgentNotCounted bool `json:"agent_not_counted"`
	// RemainingUSD is CapUSD - SpentUSD, floored at 0; nil without a cap.
	RemainingUSD *float64 `json:"remaining_usd"`
	// Exceeded is true when the next model or agent call would be refused.
	Exceeded bool `json:"exceeded"`
	// Parked is true while the initiative is stopped on budget_exceeded.
	Parked bool `json:"parked"`
	// Note explains what the meter does and does not count.
	Note string `json:"note"`
}

// BudgetStatus reports an initiative's effective cap and its spend so far.
func (e *Engine) BudgetStatus(ctx context.Context, initiativeID string) (BudgetStatus, error) {
	var out BudgetStatus
	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return out, err
	}
	own, err := e.db.GetInitiativeBudget(ctx, initiativeID)
	if err != nil {
		return out, err
	}
	costs, err := projection.BuildCosts(ctx, e.db, initiativeID)
	if err != nil {
		return out, err
	}
	out.ProjectDefaultUSD = e.snapshotConfig().cfg.Budget.DefaultInitiativeUSD
	out.InitiativeCapUSD = own.CapUSD
	switch {
	case own.CapUSD != nil:
		c := *own.CapUSD
		out.CapUSD, out.Source = &c, BudgetSourceInitiative
	case out.ProjectDefaultUSD > 0:
		c := out.ProjectDefaultUSD
		out.CapUSD, out.Source = &c, BudgetSourceProject
	default:
		out.Source = BudgetSourceNone
	}
	for _, p := range costs.ByPhase {
		out.ModelUSD += p.USD
	}
	out.AgentRuns = costs.Agent.Runs
	out.AgentUSD = costs.Agent.USD
	out.SpentUSD = costs.TotalUSD
	out.AgentNotCounted = costs.Agent.Runs > 0 && costs.Agent.USD == nil
	if out.CapUSD != nil {
		rem := *out.CapUSD - out.SpentUSD
		if rem < 0 {
			rem = 0
		}
		out.RemainingUSD = &rem
		out.Exceeded = out.SpentUSD >= *out.CapUSD
	}
	out.Parked = ini.Status == domain.InitiativeNeedsHumanJudgment && ini.JudgmentReason == domain.JudgmentBudget
	switch {
	case out.AgentNotCounted:
		out.Note = fmt.Sprintf("Counts every model call. The coding agent ran %d time(s) but reported no cost (hermes and omp usually do not), so its spend is NOT counted here.", out.AgentRuns)
	case out.AgentRuns > 0:
		out.Note = "Counts every model call and the coding agent's reported spend."
	default:
		out.Note = "Counts every model call. The coding agent has not run yet; its spend is counted only when it reports one (hermes and omp usually do not)."
	}
	return out, nil
}

// checkBudget refuses the next call when the initiative has reached its cap.
// An empty initiativeID (a call made outside any initiative) is never capped.
func (e *Engine) checkBudget(ctx context.Context, initiativeID, before string) error {
	if initiativeID == "" {
		return nil
	}
	st, err := e.BudgetStatus(ctx, initiativeID)
	if err != nil {
		// Never fail a run because the meter could not be read: the store
		// error that caused it will surface on the run's own writes.
		if e.log != nil {
			e.log.Warn("could not read the initiative's budget; not enforcing it for this call", "initiative", initiativeID, "err", err)
		}
		return nil
	}
	if st.CapUSD == nil || !st.Exceeded {
		return nil
	}
	return &BudgetExceededError{InitiativeID: initiativeID, SpentUSD: st.SpentUSD, CapUSD: *st.CapUSD, Before: before}
}
