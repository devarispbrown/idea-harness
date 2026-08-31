package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ---------- shared API response types ----------

type cliInitiative struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type cliUnderstanding struct {
	Title        string          `json:"title"`
	Status       string          `json:"status"`
	Assertions   []cliAssertion  `json:"assertions"`
	Requirements []cliRequirement `json:"requirements"`
	Decisions    []cliDecision   `json:"decisions"`
	Questions    []cliQuestion   `json:"questions"`
}

type cliAssertion struct {
	Type       string `json:"type"`
	Statement  string `json:"statement"`
	Confidence string `json:"confidence"`
	Source     string `json:"source"`
}

type cliRequirement struct {
	Code      string `json:"code"`
	Statement string `json:"statement"`
	Priority  string `json:"priority"`
}

type cliDecision struct {
	ID             string   `json:"id"`
	Code           string   `json:"code"`
	Question       string   `json:"question"`
	Recommendation string   `json:"recommendation"`
	Options        []string `json:"options"`
	Status         string   `json:"status"`
}

type cliQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Status   string `json:"status"`
}

type cliNextMove struct {
	Action   string   `json:"action"`
	Why      string   `json:"why"`
	Affected []string `json:"affected"`
}

type cliChanged struct {
	Groups []struct {
		Category string   `json:"category"`
		Items    []string `json:"items"`
	} `json:"groups"`
}

type cliFinding struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	Confidence     string `json:"confidence"`
	Category       string `json:"category"`
	Finding        string `json:"finding"`
	Recommendation string `json:"recommendation"`
	Disposition    string `json:"disposition"`
}

func wantJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

func statusBadge(s string) string {
	switch s {
	case "approved":
		return ok("● approved")
	case "needs_human_judgment":
		return warn("● needs your judgment")
	case "running":
		return accent("● running")
	case "failed":
		return errText("● failed")
	default:
		return muted("● " + s)
	}
}

// cmdStatus shows initiative + current work.
func cmdStatus(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return err
	}
	if wantJSON(args) {
		return dumpJSON(stdout, inis)
	}
	if isDemoMode(c, projectID) {
		fmt.Fprintln(stdout, warn("demo mode — synthetic output. Configure a model with:")+" "+bold("idea model set <provider/model>"))
		fmt.Fprintln(stdout)
	}
	if len(inis) == 0 {
		fmt.Fprintln(stdout, muted("No initiatives yet. Run ") + bold("idea new") + muted(" to start."))
		return nil
	}
	for _, ini := range inis {
		fmt.Fprintln(stdout, bold(ini.Title)+"  "+statusBadge(ini.Status))
		// Understanding + next move for context.
		var u cliUnderstanding
		if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+ini.ID+"/understanding", nil, &u); err == nil {
			fmt.Fprintf(stdout, "  %s %d · %s %d · %s %d\n",
				muted("requirements:"), len(u.Requirements),
				muted("decisions:"), len(u.Decisions),
				muted("questions:"), len(u.Questions))
		}
		var nm cliNextMove
		if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+ini.ID+"/next-move", nil, &nm); err == nil && nm.Action != "" {
			fmt.Fprintln(stdout, "  "+accent("→ ")+nm.Action)
			if nm.Why != "" {
				fmt.Fprintln(stdout, "    "+muted(nm.Why))
			}
		}
		fmt.Fprintln(stdout)
	}
	return nil
}

// cmdChanged shows What Changed since last look.
func cmdChanged(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return err
	}
	if len(inis) == 0 {
		fmt.Fprintln(stdout, muted("No changes yet."))
		return nil
	}
	var changed cliChanged
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+inis[0].ID+"/changed", nil, &changed); err != nil {
		return err
	}
	if wantJSON(args) {
		return dumpJSON(stdout, changed)
	}
	if len(changed.Groups) == 0 {
		fmt.Fprintln(stdout, muted("No changes yet."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Since you last looked"))
	for _, g := range changed.Groups {
		fmt.Fprintln(stdout, "  "+bold(g.Category))
		for _, item := range g.Items {
			fmt.Fprintln(stdout, "    · "+item)
		}
	}
	return nil
}

// cmdDecisions lists decisions.
func cmdDecisions(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var decisions []cliDecision
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/decisions", nil, &decisions); err != nil {
		return err
	}
	if wantJSON(args) {
		return dumpJSON(stdout, decisions)
	}
	open := 0
	for _, d := range decisions {
		if d.Status == "open" {
			open++
		}
	}
	if len(decisions) == 0 {
		fmt.Fprintln(stdout, muted("No decisions yet."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Decisions")+muted(fmt.Sprintf("  (%d open)", open)))
	for _, d := range decisions {
		marker := "  "
		if d.Status == "open" {
			marker = warn("● ")
		} else {
			marker = ok("✓ ")
		}
		fmt.Fprintln(stdout, marker+code(d.Code)+" "+d.Question)
		if d.Recommendation != "" {
			fmt.Fprintln(stdout, "      "+muted("recommendation: ")+d.Recommendation)
		}
	}
	return nil
}

// cmdReviews lists review findings.
func cmdReviews(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var findings []cliFinding
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/findings", nil, &findings); err != nil {
		return err
	}
	if wantJSON(args) {
		return dumpJSON(stdout, findings)
	}
	if len(findings) == 0 {
		fmt.Fprintln(stdout, ok("No open findings."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Review findings"))
	for _, f := range findings {
		sev := f.Severity
		switch sev {
		case "critical", "high":
			sev = errText(sev)
		case "medium":
			sev = warn(sev)
		default:
			sev = muted(sev)
		}
		fmt.Fprintln(stdout, "  "+sev+" "+f.Finding)
		if f.Recommendation != "" {
			fmt.Fprintln(stdout, "      "+muted("→ ")+f.Recommendation)
		}
	}
	return nil
}

func dumpJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}

var _ = strings.TrimSpace
