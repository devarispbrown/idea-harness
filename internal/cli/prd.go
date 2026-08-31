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

// cmdPRD renders the PRD (or another artifact) with Glamour (§64).
func cmdPRD(args []string, stdout, stderr io.Writer) error {
	kind := "prd"
	if len(args) > 0 && args[0] != "--json" {
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
