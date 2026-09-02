package cli

import (
	"fmt"
	"io"
)

type cliTask struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Title     string `json:"title"`
	Statement string `json:"statement"`
	Status    string `json:"status"`
	Priority  string `json:"priority"`
}

// resolveInitiative returns the initiative id (args[0] or the newest).
func resolveInitiative(c *client, projectID string, args []string) (string, error) {
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return "", err
	}
	if len(inis) == 0 {
		return "", fmt.Errorf("no initiatives yet; run `idea new` first")
	}
	if len(args) > 0 {
		return args[0], nil
	}
	return inis[len(inis)-1].ID, nil
}

// cmdAdvance launches the M2 Design → Execution Plan pipeline.
func cmdAdvance(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/advance", nil, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ advance started")+" "+code(iniID))
	if opID := out["operation_id"]; opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	}
	fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
	return nil
}

// cmdPlan renders the execution plan artifact.
func cmdPlan(args []string, stdout, stderr io.Writer) error {
	return cmdExport(append([]string{"execution_plan"}, args...), stdout, stderr)
}

// cmdTasks lists the ordered execution-plan tasks.
func cmdTasks(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return err
	}
	var tasks []cliTask
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", nil, &tasks); err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, muted("No tasks yet — run `idea advance`."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Execution Plan Tasks"))
	for _, t := range tasks {
		fmt.Fprintf(stdout, "  %s [%s] %s\n", code(t.Code), t.Priority, t.Title)
	}
	return nil
}

type idCode struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// resolveObjectID resolves an object reference (code or id) to its id.
func resolveObjectID(c *client, projectID, iniID, typ, ref string) (string, error) {
	endpoint := ""
	switch typ {
	case "requirement":
		endpoint = "/api/v1/projects/" + projectID + "/requirements?initiative_id=" + iniID
	case "decision":
		endpoint = "/api/v1/projects/" + projectID + "/decisions?initiative_id=" + iniID
	case "task":
		endpoint = "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks"
	case "artifact":
		endpoint = "/api/v1/projects/" + projectID + "/artifacts?initiative_id=" + iniID
	case "context_doc":
		endpoint = "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/docs"
	default:
		return ref, nil // run/assertion/etc: treat ref as the id
	}
	var list []idCode
	if err := c.do("GET", endpoint, nil, &list); err != nil {
		return "", err
	}
	for _, o := range list {
		if o.ID == ref || o.Code == ref || o.Kind == ref || o.Title == ref {
			return o.ID, nil
		}
	}
	return "", fmt.Errorf("no %s matching %q", typ, ref)
}

type cliWhyItem struct {
	EdgeType  string `json:"edge_type"`
	FromType  string `json:"from_type"`
	FromID    string `json:"from_id"`
	FromLabel string `json:"from_label"`
	ToType    string `json:"to_type"`
	ToID      string `json:"to_id"`
	ToLabel   string `json:"to_label"`
}

type cliImpactItem struct {
	cliWhyItem
	Staleness string `json:"staleness"`
}

func cmdWhy(args []string, stdout, stderr io.Writer) error { return runTraceCmd("why", args, stdout) }
func cmdImpact(args []string, stdout, stderr io.Writer) error {
	return runTraceCmd("impact", args, stdout)
}

// cmdTrace renders both provenance (why) and dependents (impact) for a node.
func cmdTrace(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: idea trace <requirement|task|decision|artifact|assertion|context_doc> <code-or-id>")
	}
	if err := runTraceCmd("why", args, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout)
	return runTraceCmd("impact", args, stdout)
}

// cmdDiff renders the structured requirement/decision diff.
func cmdDiff(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return err
	}
	var d struct {
		Requirements struct {
			Added   []cliRequirement `json:"added"`
			Removed []cliRequirement `json:"removed"`
		} `json:"requirements"`
		Decisions struct {
			Open       []cliDecision `json:"open"`
			Decided    []cliDecision `json:"decided"`
			Superseded []cliDecision `json:"superseded"`
		} `json:"decisions"`
	}
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/diff", nil, &d); err != nil {
		return err
	}
	fmt.Fprintln(stdout, heading("Semantic Diff"))
	fmt.Fprintln(stdout, bold("Requirements added")+": "+fmt.Sprint(len(d.Requirements.Added)))
	for _, r := range d.Requirements.Added {
		fmt.Fprintf(stdout, "  + %s %s\n", code(r.Code), r.Statement)
	}
	fmt.Fprintln(stdout, bold("Requirements removed")+": "+fmt.Sprint(len(d.Requirements.Removed)))
	for _, r := range d.Requirements.Removed {
		fmt.Fprintf(stdout, "  - %s %s\n", code(r.Code), r.Statement)
	}
	fmt.Fprintln(stdout, bold("Decisions")+fmt.Sprintf(": %d open · %d decided · %d superseded", len(d.Decisions.Open), len(d.Decisions.Decided), len(d.Decisions.Superseded)))
	for _, x := range d.Decisions.Superseded {
		fmt.Fprintf(stdout, "  ~ %s %s\n", code(x.Code), x.Question)
	}
	return nil
}

func runTraceCmd(mode string, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: idea %s <requirement|task|decision|artifact|assertion|context_doc> <code-or-id>", mode)
	}
	typ, ref := args[0], args[1]
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args[2:])
	if err != nil {
		return err
	}
	objID, err := resolveObjectID(c, projectID, iniID, typ, ref)
	if err != nil {
		return err
	}
	u := "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/" + mode + "?type=" + typ + "&id=" + objID
	if mode == "why" {
		var items []cliWhyItem
		if err := c.do("GET", u, nil, &items); err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(stdout, muted("No provenance edges found."))
			return nil
		}
		fmt.Fprintln(stdout, heading("Why "+code(ref)))
		for _, it := range items {
			fmt.Fprintf(stdout, "  %s —%s→ %s\n", it.FromLabel, it.EdgeType, it.ToLabel)
		}
		return nil
	}
	var items []cliImpactItem
	if err := c.do("GET", u, nil, &items); err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, muted("No dependents found."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Impact "+code(ref)))
	for _, it := range items {
		fmt.Fprintf(stdout, "  %s [%s] via %s\n", it.FromLabel, it.Staleness, it.EdgeType)
	}
	return nil
}
