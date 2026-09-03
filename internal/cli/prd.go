package cli

import (
	"fmt"
	"io"

	"github.com/charmbracelet/glamour"
)

type cliArtifact struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type cliArtifactBody struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	Body    string `json:"body"`
}

// cmdPRD renders the PRD (or another artifact) with Glamour (§64). This used
// to duplicate cmdExport's bug: it indexed args[0] directly for the kind (so a
// stray flag was silently treated as one) and picked the last artifact of that
// kind in the whole project, with no initiative scoping — in a project with
// more than one initiative it could render an artifact from a different
// initiative than the one the caller is working on. It now shares
// resolveInitiative + resolveArtifact (explain_export.go) with cmdExport, so
// `idea prd`, `idea export prd` and `idea plan` all agree on which artifact to
// show — `idea prd [kind] [initiative]`.
func cmdPRD(a cmdArgs, stdout, stderr io.Writer) error {
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

	var body cliArtifactBody
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/artifacts/"+target.ID+"/body", nil, &body); err != nil {
		return err
	}

	if !colorEnabled {
		fmt.Fprintln(stdout, body.Body)
		return nil
	}
	rendered, err := glamour.Render(body.Body, "auto")
	if err != nil {
		// Fall back to plain markdown if glamour fails.
		fmt.Fprintln(stdout, body.Body)
		return nil
	}
	fmt.Fprintln(stdout, rendered)
	return nil
}
