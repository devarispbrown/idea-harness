package cli

import (
	"fmt"
	"io"
)

// cliExplainUnderstanding mirrors the projection response for `idea explain`.
type cliExplainUnderstanding struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	NeedsJudgment []string `json:"needs_judgment"`
	Requirements  []cliRequirement `json:"requirements"`
	Decisions     []cliDecision    `json:"decisions"`
	Questions     []cliQuestion    `json:"questions"`
}

// cmdExplain renders the Current Understanding (§5, §60 "Explain").
func cmdExplain(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return err
	}
	if len(inis) == 0 {
		return fmt.Errorf("no initiatives yet; run `idea new` first")
	}
	iniID := inis[len(inis)-1].ID
	if len(args) > 0 {
		iniID = args[0]
	}

	var u cliExplainUnderstanding
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/understanding", nil, &u); err != nil {
		return err
	}

	fmt.Fprintln(stdout, heading("Current Understanding")+"  "+statusBadge(u.Status))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, u.Summary)

	if len(u.NeedsJudgment) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, warn("Needs your judgment"))
		for _, item := range u.NeedsJudgment {
			fmt.Fprintln(stdout, "  · "+item)
		}
	}

	if len(u.Requirements) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, bold("Requirements"))
		for _, r := range u.Requirements {
			fmt.Fprintf(stdout, "  %s %s (%s)\n", code(r.Code), r.Statement, r.Priority)
		}
	}

	if len(u.Questions) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, bold("Open questions"))
		for _, q := range u.Questions {
			if q.Status == "open" {
				fmt.Fprintln(stdout, "  · "+q.Question)
			}
		}
	}
	return nil
}

// cmdExport writes an artifact body as markdown to stdout (§20).
func cmdExport(args []string, stdout, stderr io.Writer) error {
	kind := "prd"
	if len(args) > 0 {
		kind = args[0]
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var artifacts []cliArtifact
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/artifacts", nil, &artifacts); err != nil {
		return err
	}
	var target *cliArtifact
	for i := range artifacts {
		if artifacts[i].Kind == kind {
			target = &artifacts[i]
		}
	}
	if target == nil {
		return fmt.Errorf("no %q artifact yet; run `idea new` first", kind)
	}
	raw, err := c.doRaw("GET", "/api/v1/projects/"+projectID+"/artifacts/"+target.ID+"/raw")
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, raw)
	return nil
}
