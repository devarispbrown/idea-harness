package cli

import "testing"

// TestCliCostsSummaryFlagsUnmeasuredAgentSpend is a regression test for
// review defect #5: GET .../costs correctly returned agent.usd null with
// agent.note "agent spend not measured" for six real Sonnet runs, but
// cliCosts never even decoded the note field, and `idea status` printed a
// bare "spend: $0.0032" -- the model-phase cost alone -- with nothing to say
// the coding agent's own (almost certainly larger) spend was not in that
// number at all. It must read as a partial total, not as this initiative's
// total spend.
func TestCliCostsSummaryFlagsUnmeasuredAgentSpend(t *testing.T) {
	c := cliCosts{TotalUSD: 0.0032}
	c.Agent.Runs = 6
	c.Agent.Seconds = 900
	c.Agent.USD = nil
	c.Agent.Note = "agent spend not measured"

	got := c.summary()
	if got != "spend: $0.0032 (model phases only; agent spend not measured) · time: 15m0s" {
		t.Fatalf("summary() = %q", got)
	}
}

// TestCliCostsSummaryFallsBackToADefaultNote covers a daemon that returns
// agent.usd: null with no note at all (e.g. an older daemon, or the note
// field renamed) -- the qualifier must still appear rather than silently
// reverting to a bare total.
func TestCliCostsSummaryFallsBackToADefaultNote(t *testing.T) {
	c := cliCosts{TotalUSD: 1.5}
	c.Agent.Runs = 2
	c.Agent.USD = nil
	c.Agent.Note = ""

	got := c.summary()
	if got != "spend: $1.5000 (model phases only; agent spend not measured) · time: 0s" {
		t.Fatalf("summary() = %q", got)
	}
}

// TestCliCostsSummaryMeasuredAgentSpendPrintsBareTotal is the counterpart:
// once agent spend IS measured, the total already includes it
// (InitiativeCosts.TotalUSD folds in a measured agent USD), so no qualifier
// belongs on the line.
func TestCliCostsSummaryMeasuredAgentSpendPrintsBareTotal(t *testing.T) {
	usd := 2.5
	c := cliCosts{TotalUSD: 2.5032}
	c.Agent.Runs = 1
	c.Agent.USD = &usd

	got := c.summary()
	if got != "spend: $2.5032 · time: 0s" {
		t.Fatalf("summary() = %q", got)
	}
}

// TestCliCostsSummaryNoAgentRunsYet: no coding agent has run at all (e.g. the
// internal/deterministic executor, or nothing executed yet) -- there is
// nothing to measure and nothing to caveat.
func TestCliCostsSummaryNoAgentRunsYet(t *testing.T) {
	c := cliCosts{TotalUSD: 0}
	got := c.summary()
	if got != "spend: $0.0000 · time: 0s" {
		t.Fatalf("summary() = %q", got)
	}
}
