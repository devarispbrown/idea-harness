package cli

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// budgetFlags is `idea budget`'s flag spec.
var budgetFlags = flagSpec{"--set": flagValue, "--clear": flagBool, "--json": flagBool}

// cliBudget mirrors workflow.BudgetStatus.
type cliBudget struct {
	CapUSD            *float64 `json:"cap_usd"`
	Source            string   `json:"source"`
	InitiativeCapUSD  *float64 `json:"initiative_cap_usd"`
	ProjectDefaultUSD float64  `json:"project_default_usd"`
	SpentUSD          float64  `json:"spent_usd"`
	ModelUSD          float64  `json:"model_usd"`
	AgentUSD          *float64 `json:"agent_usd"`
	AgentRuns         int      `json:"agent_runs"`
	AgentNotCounted   bool     `json:"agent_not_counted"`
	RemainingUSD      *float64 `json:"remaining_usd"`
	Exceeded          bool     `json:"exceeded"`
	Parked            bool     `json:"parked"`
	Note              string   `json:"note"`
}

// parseUSD reads a --set amount: a plain number, optionally with a leading $.
func parseUSD(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(s), "$"), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, usageErrorf("--set takes a dollar amount, e.g. --set 5 or --set 2.50 (got %q)", s)
	}
	return v, nil
}

// cmdBudget is `idea budget [<initiative>] [--set <usd>|--clear]`.
//
// With an initiative it shows (or sets) that initiative's cap and meter; with
// none it shows the project default and every initiative's meter, and --set
// changes the project default (budget.default_initiative_usd in harness.yaml).
func cmdBudget(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	set, clear := a.flags.has("--set"), a.flags.has("--clear")
	if set && clear {
		return usageErrorf("give --set <usd> or --clear, not both")
	}
	if len(a.positional) == 0 {
		if clear {
			return usageErrorf("--clear applies to one initiative (it returns it to the project default); to remove the project default, use --set 0")
		}
		if set {
			v, err := parseUSD(a.flags.value("--set"))
			if err != nil {
				return err
			}
			var out struct {
				Resumed []string `json:"resumed"`
			}
			if err := c.do("PUT", "/api/v1/projects/"+projectID+"/budget", map[string]any{"default_initiative_usd": v}, &out); err != nil {
				return err
			}
			if a.flags.has("--json") {
				return dumpJSON(stdout, out)
			}
			if v == 0 {
				fmt.Fprintln(stdout, ok("✓")+" project default cap removed: initiatives without their own cap are no longer capped")
			} else {
				fmt.Fprintf(stdout, "%s project default cap set to $%.2f per initiative\n", ok("✓"), v)
			}
			for _, id := range out.Resumed {
				fmt.Fprintf(stdout, "  %s %s is back where it stopped; %s runs the next step\n", ok("↺"), id, bold("idea next"))
			}
			return nil
		}
		var view struct {
			DefaultInitiativeUSD float64 `json:"default_initiative_usd"`
			Initiatives          []struct {
				InitiativeID string    `json:"initiative_id"`
				Title        string    `json:"title"`
				Status       string    `json:"status"`
				Budget       cliBudget `json:"budget"`
			} `json:"initiatives"`
		}
		if err := c.do("GET", "/api/v1/projects/"+projectID+"/budget", nil, &view); err != nil {
			return err
		}
		if a.flags.has("--json") {
			return dumpJSON(stdout, view)
		}
		fmt.Fprintln(stdout, heading("Budget"))
		if view.DefaultInitiativeUSD > 0 {
			fmt.Fprintf(stdout, "  project default: $%.2f per initiative %s\n", view.DefaultInitiativeUSD, muted("(budget.default_initiative_usd)"))
		} else {
			fmt.Fprintln(stdout, "  project default: none "+muted("— `idea budget --set <usd>` caps every initiative"))
		}
		for _, ini := range view.Initiatives {
			fmt.Fprintf(stdout, "  %s %s  %s\n", code(ini.InitiativeID), ini.Title, budgetLine(ini.Budget))
		}
		return nil
	}

	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	path := initiativeResource(projectID, iniID, "budget")
	if set || clear {
		body := map[string]any{"clear": true}
		if set {
			v, err := parseUSD(a.flags.value("--set"))
			if err != nil {
				return err
			}
			if v == 0 {
				return usageErrorf("an initiative's cap must be > 0; --clear returns it to the project default")
			}
			body = map[string]any{"cap_usd": v}
		}
		var out struct {
			Budget  cliBudget `json:"budget"`
			Resumed bool      `json:"resumed"`
		}
		if err := c.do("PUT", path, body, &out); err != nil {
			return err
		}
		if a.flags.has("--json") {
			return dumpJSON(stdout, out)
		}
		fmt.Fprintln(stdout, ok("✓")+" "+budgetLine(out.Budget))
		if out.Resumed {
			fmt.Fprintln(stdout, ok("↺")+" the initiative is back where it stopped; "+bold("idea next")+" runs the step again")
		} else if out.Budget.Parked {
			fmt.Fprintln(stdout, warn("!")+" still at its cap: raise it above what it has already spent")
		}
		return nil
	}
	var st cliBudget
	if err := c.do("GET", path, nil, &st); err != nil {
		return err
	}
	if a.flags.has("--json") {
		return dumpJSON(stdout, st)
	}
	fmt.Fprintln(stdout, heading("Budget")+" "+muted(iniID))
	fmt.Fprintln(stdout, "  "+budgetLine(st))
	fmt.Fprintf(stdout, "  %s model calls $%.4f", muted("·"), st.ModelUSD)
	switch {
	case st.AgentUSD != nil:
		fmt.Fprintf(stdout, ", coding agent $%.4f", *st.AgentUSD)
	case st.AgentNotCounted:
		fmt.Fprintf(stdout, ", coding agent %s", warn("not counted"))
	}
	fmt.Fprintln(stdout)
	if st.Note != "" {
		fmt.Fprintln(stdout, "  "+muted(st.Note))
	}
	if st.Parked {
		fmt.Fprintln(stdout, warn("Stopped at its cap.")+" "+bold("idea budget "+iniID+" --set <usd>")+" raises it and returns the initiative to where it stopped.")
	}
	return nil
}

// budgetLine is the one-line meter: "$0.42 of $5.00 (initiative cap)".
func budgetLine(b cliBudget) string {
	if b.CapUSD == nil {
		return fmt.Sprintf("$%.4f spent, no cap", b.SpentUSD)
	}
	src := "project default"
	if b.Source == "initiative" {
		src = "its own cap"
	}
	line := fmt.Sprintf("$%.4f of $%.2f (%s)", b.SpentUSD, *b.CapUSD, src)
	if b.Exceeded {
		line += " " + warn("at cap")
	}
	return line
}
