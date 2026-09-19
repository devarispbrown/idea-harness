package projection

import (
	"context"
	"regexp"
	"strconv"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// BuildCosts builds an initiative's spend/time summary (C3, wave 3 contract)
// from the run records that already exist: model-call cost/duration (per
// phase, from run_manifests via runs.stage) and agent (coding) run count/
// duration, with agent spend parsed best-effort from each change set's own
// AgentOutput -- Hermes's own token/cost line, when it printed one -- or
// reported as not measured.
func BuildCosts(ctx context.Context, db *store.DB, initiativeID string) (domain.InitiativeCosts, error) {
	var out domain.InitiativeCosts

	phaseRows, err := db.ListModelPhaseCosts(ctx, initiativeID)
	if err != nil {
		return out, err
	}
	out.ByPhase = make([]domain.PhaseCost, 0, len(phaseRows))
	for _, row := range phaseRows {
		out.TotalUSD += row.CostUSD
		out.ByPhase = append(out.ByPhase, domain.PhaseCost{
			Phase: row.Stage, USD: row.CostUSD, Seconds: float64(row.DurationMS) / 1000, Calls: row.Calls,
		})
	}

	agentStats, err := db.ListAgentRunStats(ctx, initiativeID)
	if err != nil {
		return out, err
	}
	out.Agent = AgentCostFromRuns(ctx, db, initiativeID, agentStats)
	out.TotalUSD += valueOr(out.Agent.USD, 0)
	return out, nil
}

// AgentCostFromRuns fills in the agent cost's USD/note from the initiative's
// change sets' AgentOutput, given the run count/duration already computed.
// Exported for tests that want to exercise the parsing rule directly.
func AgentCostFromRuns(ctx context.Context, db *store.DB, initiativeID string, stats store.AgentRunStats) domain.AgentCost {
	out := domain.AgentCost{Runs: stats.Runs, Seconds: float64(stats.DurationMS) / 1000}
	if stats.Runs == 0 {
		out.Note = "agent spend not measured"
		return out
	}
	changeSets, err := db.ListChangeSets(ctx, initiativeID)
	if err != nil || len(changeSets) == 0 {
		out.Note = "agent spend not measured"
		return out
	}
	sum := 0.0
	parsed := 0
	for _, cs := range changeSets {
		if cs.AgentOutput == "" {
			continue
		}
		if usd, ok := ParseAgentCostUSD(cs.AgentOutput); ok {
			sum += usd
			parsed++
		}
	}
	// Only report a total when every actual agent RUN's cost was recovered
	// (M6, review fix: this used to compare against len(changeSets), not the
	// run count -- a placeholder/demo change set (Executor == "internal")
	// never has AgentOutput to parse and never will, so one real run mixed
	// with one placeholder change set could never report a total even though
	// the one real run was fully measured). A partial sum presented as "the"
	// spend would understate it, which is worse than admitting it was not
	// measured.
	if parsed == 0 || parsed != stats.Runs {
		out.Note = "agent spend not measured"
		return out
	}
	out.USD = &sum
	return out
}

// agentTotalCostPattern and agentCostPattern match a best-effort "cost" line
// in a coding agent's own output, e.g. "Cost: $0.1234", "total cost: $1.20",
// "cost=$0.03" (M6, review fix: the "$" is now mandatory -- a bare
// "cost: 10" in, say, YAML the agent happened to print is not a dollar
// figure and must not be read as one). No real Hermes cost-output sample was
// available when this was first written (wave 3 contract, C3) and the
// fallback -- usd: null, "agent spend not measured" -- is always safe, so a
// pattern that never matches real output costs nothing but precision, while
// one that matched the wrong number would misreport spend.
var (
	agentTotalCostPattern = regexp.MustCompile(`(?i)total\s+cost[:=]?\s*\$\s*([0-9]+(?:\.[0-9]+)?)`)
	agentCostPattern      = regexp.MustCompile(`(?i)cost[:=]?\s*\$\s*([0-9]+(?:\.[0-9]+)?)`)
)

// ParseAgentCostUSD extracts a dollar cost from a coding agent's own console
// output, best-effort (C3, wave 3 contract). A line explicitly labelled
// "total cost" wins outright; otherwise the LAST plain "cost: $x" match wins
// (M6, review fix: taking the first match anywhere picked up a per-step cost
// printed before the run's own final total). ok is false when no
// recognizable cost line is present at all.
func ParseAgentCostUSD(output string) (usd float64, ok bool) {
	if m := agentTotalCostPattern.FindStringSubmatch(output); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			return v, true
		}
	}
	matches := agentCostPattern.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(matches[len(matches)-1][1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func valueOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}
