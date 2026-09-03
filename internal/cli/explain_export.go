package cli

import (
	"fmt"
	"io"
)

// cliExplainUnderstanding mirrors the projection response for `idea explain`.
type cliExplainUnderstanding struct {
	Title         string           `json:"title"`
	Description   string           `json:"description"`
	Status        string           `json:"status"`
	Summary       string           `json:"summary"`
	NeedsJudgment []string         `json:"needs_judgment"`
	Requirements  []cliRequirement `json:"requirements"`
	Decisions     []cliDecision    `json:"decisions"`
	Questions     []cliQuestion    `json:"questions"`
}

// cmdExplain renders the Current Understanding (§5, §60 "Explain").
func cmdExplain(a cmdArgs, stdout, stderr io.Writer) error {
	positional, flags := a.positional, a.flags
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
	if len(positional) > 0 {
		iniID = positional[0]
	}

	var u cliExplainUnderstanding
	if err := c.do("GET", initiativeResource(projectID, iniID, "understanding"), nil, &u); err != nil {
		return err
	}
	if flags.has("--json") {
		return dumpJSON(stdout, u)
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

// cmdExport writes an artifact body as markdown to stdout (§20). It used to
// index args[0] directly for the kind, which meant `idea export --json` (or
// any flag) was silently treated as the kind, and it picked the last artifact
// of that kind in the *whole project* with no initiative scoping at all — in a
// project with more than one initiative it could render an artifact that
// belongs to a different initiative than the one the caller is working on.
// Both are fixed here: flags go through parseArgs (any flag is now a reported
// error, since export accepts none), and the artifact list is scoped to the
// resolved initiative via resolveInitiative, exactly like `idea tasks` /
// `idea changes` — `idea export [kind] [initiative]`.
func cmdExport(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	kind := "prd"
	if len(positional) > 0 {
		kind = positional[0]
		positional = positional[1:]
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, positional)
	if err != nil {
		return err
	}
	target, err := resolveArtifact(c, projectID, iniID, kind)
	if err != nil {
		return err
	}
	raw, err := c.doRaw("GET", "/api/v1/projects/"+projectID+"/artifacts/"+target.ID+"/raw")
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, raw)
	return nil
}

// resolveArtifact returns the latest artifact of kind belonging to iniID. The
// artifacts list endpoint scopes to a single initiative via initiative_id (the
// same query param `idea tasks`/`idea changes` use); without it the daemon
// defaults to an arbitrary initiative rather than "all artifacts in the
// project", which is why every caller here must resolve iniID first.
func resolveArtifact(c *client, projectID, iniID, kind string) (*cliArtifact, error) {
	var artifacts []cliArtifact
	if err := c.do("GET", initiativeResource(projectID, iniID, "artifacts"), nil, &artifacts); err != nil {
		return nil, err
	}
	var target *cliArtifact
	for i := range artifacts {
		if artifacts[i].Kind == kind {
			target = &artifacts[i]
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no %q artifact yet; run `idea new` first", kind)
	}
	return target, nil
}
