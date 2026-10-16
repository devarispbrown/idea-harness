package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"
)

// `idea gaps` asks the Product Graph what is missing or no longer holds --
// the deterministic gap analysis behind GET …/gaps (projection.BuildGaps).
// It reads only. It exits 0 whatever it finds unless --fail-on names a
// severity, which is what turns it into a CI gate: any gap at or above that
// severity exits 5 (gate_unmet).

// gapsFlags is the flag spec for `idea gaps`.
var gapsFlags = flagSpec{"--json": flagBool, "--kind": flagValue, "--fail-on": flagValue, "--all": flagBool}

// cliGapNode mirrors domain.GapNode.
type cliGapNode struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Code  string `json:"code"`
	Label string `json:"label,omitempty"`
}

// cliGap mirrors domain.Gap.
type cliGap struct {
	Kind         string       `json:"kind"`
	State        string       `json:"state"`
	Severity     string       `json:"severity"`
	Nodes        []cliGapNode `json:"nodes"`
	Why          string       `json:"why"`
	Next         string       `json:"next"`
	Dependents   int          `json:"dependents"`
	InitiativeID string       `json:"initiative_id"`
}

// cliGapSummary mirrors domain.GapSummary.
type cliGapSummary struct {
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"by_severity"`
	ByKind     map[string]int `json:"by_kind"`
}

// cliGapReport mirrors domain.GapReport.
type cliGapReport struct {
	InitiativeID string        `json:"initiative_id,omitempty"`
	Gaps         []cliGap      `json:"gaps"`
	Summary      cliGapSummary `json:"summary"`
}

// gapSeverities is domain.Severity most severe first: the --fail-on
// vocabulary and the order counts print in.
var gapSeverities = []string{"critical", "high", "medium", "low", "observation"}

func gapSeverityRank(s string) int {
	for i, v := range gapSeverities {
		if v == s {
			return i
		}
	}
	return len(gapSeverities)
}

// summaryLine renders "2 high · 1 low", skipping zeros.
func (s cliGapSummary) summaryLine() string {
	var parts []string
	for _, sev := range gapSeverities {
		if n := s.BySeverity[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	return strings.Join(parts, " · ")
}

func cmdGaps(a cmdArgs, stdout, stderr io.Writer) error {
	failOn := a.value("--fail-on")
	// Validated before connecting: a mistyped severity must not read as "the
	// gate passed", which is what silently ignoring it would do.
	if failOn != "" && gapSeverityRank(failOn) == len(gapSeverities) {
		return usageErrorf("--fail-on takes a severity: %s (got %q)", strings.Join(gapSeverities, ", "), failOn)
	}
	if a.has("--all") && len(a.positional) > 0 {
		return usageErrorf("--all reports every initiative; drop the initiative argument or --all")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	q := ""
	if k := a.value("--kind"); k != "" {
		q = "?kind=" + url.QueryEscape(k)
	}
	var path, iniID string
	if a.has("--all") {
		path = "/api/v1/projects/" + url.PathEscape(projectID) + "/gaps" + q
	} else {
		if iniID, err = resolveInitiative(c, projectID, a.positional); err != nil {
			return err
		}
		path = initiativeResource(projectID, iniID, "gaps") + q
	}
	var report cliGapReport
	if err := c.do("GET", path, nil, &report); err != nil {
		return err
	}
	if report.Gaps == nil {
		report.Gaps = []cliGap{}
	}

	if a.has("--json") {
		if err := dumpJSON(stdout, report); err != nil {
			return err
		}
	} else {
		renderGaps(report, a.has("--all"), stdout)
	}
	return gapsGate(report, failOn)
}

// gapsGate is --fail-on: a gate_unmet error (exit 5) when any gap is at least
// as severe as failOn. With no --fail-on it never fails -- a report is not a
// verdict until a script asks for one.
func gapsGate(report cliGapReport, failOn string) error {
	if failOn == "" {
		return nil
	}
	limit := gapSeverityRank(failOn)
	n := 0
	for _, g := range report.Gaps {
		if gapSeverityRank(g.Severity) <= limit {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &cliError{
		kind: kindGateUnmet,
		err:  fmt.Errorf("%d gap(s) at or above %s", n, failOn),
		hint: "each gap's `next` names the command that closes it; `idea gaps` lists them",
	}
}

// renderGaps prints the report grouped by kind, in the daemon's order (most
// severe first), with the closing command under each gap.
func renderGaps(report cliGapReport, all bool, stdout io.Writer) {
	if len(report.Gaps) == 0 {
		fmt.Fprintln(stdout, ok("✓ No gaps: every in-scope requirement is implemented, verified and traced to a measured objective that is not drifting, and nothing the graph rests on has moved."))
		return
	}
	fmt.Fprintf(stdout, "%s %s\n", heading(fmt.Sprintf("Gaps · %d", report.Summary.Total)), muted("("+report.Summary.summaryLine()+")"))
	var kinds []string
	byKind := map[string][]cliGap{}
	for _, g := range report.Gaps {
		if _, seen := byKind[g.Kind]; !seen {
			kinds = append(kinds, g.Kind)
		}
		byKind[g.Kind] = append(byKind[g.Kind], g)
	}
	for _, k := range kinds {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, bold(k)+muted(fmt.Sprintf(" · %d", len(byKind[k]))))
		for _, g := range byKind[k] {
			sev := g.Severity
			switch {
			case gapSeverityRank(sev) <= gapSeverityRank("high"):
				sev = errText(fmt.Sprintf("%-6s", sev))
			case sev == "medium":
				sev = warn(fmt.Sprintf("%-6s", sev))
			default:
				sev = muted(fmt.Sprintf("%-6s", sev))
			}
			subject := ""
			if len(g.Nodes) > 0 {
				subject = g.Nodes[0].Code
			}
			if all {
				subject = shortRef(g.InitiativeID) + " " + subject
			}
			fmt.Fprintf(stdout, "  %s %s  %s\n", sev, code(subject), g.Why)
			if g.Next != "" {
				fmt.Fprintf(stdout, "         %s %s\n", accent("→"), g.Next)
			}
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, muted("Commands act on the newest initiative unless given one; `idea gaps --json` has every node and id."))
}

// cmdAssumption records a human's judgment on an assumption or hypothesis --
// what closes an unvalidated_assumption gap.
func cmdAssumption(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 2 {
		return usageErrorf("usage: idea assumption <ASM-n|id> accept|dispute|retract [<initiative>]")
	}
	ref, verdict := a.positional[0], a.positional[1]
	status := map[string]string{"accept": "accepted", "dispute": "disputed", "retract": "retracted"}[verdict]
	if status == "" {
		return usageErrorf("unknown judgment %q (use accept|dispute|retract)", verdict)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[2:])
	if err != nil {
		return err
	}
	var assertions []struct {
		ID        string `json:"id"`
		Code      string `json:"code"`
		Type      string `json:"type"`
		Statement string `json:"statement"`
	}
	if err := c.do("GET", initiativeResource(projectID, iniID, "assertions"), nil, &assertions); err != nil {
		return err
	}
	var matches []string
	for _, as := range assertions {
		// A code (ASM-3) is exact and case-insensitive; an id may be a prefix.
		if as.ID == ref || (as.Code != "" && strings.EqualFold(as.Code, ref)) {
			matches = []string{as.ID}
			break
		}
		if strings.HasPrefix(as.ID, ref) {
			matches = append(matches, as.ID)
		}
	}
	switch len(matches) {
	case 0:
		return errorf(kindNotFound, "no assumption matching %q; `idea gaps --kind unvalidated_assumption` lists them", ref)
	case 1:
	default:
		return usageErrorf("%q matches more than one assumption; use more of the id", ref)
	}
	p := "/api/v1/projects/" + url.PathEscape(projectID) + "/assertions/" + url.PathEscape(matches[0]) + "/status"
	if err := c.do("POST", p, map[string]string{"status": status}, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ "+status)+" "+code(shortRef(matches[0])))
	return nil
}
