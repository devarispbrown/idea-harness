package cli

import (
	"fmt"
	"io"
)

// cmdCapture implements `idea capture token|list`: mint a capture token (shown
// once) and list the project's tokens as metadata.
func cmdCapture(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	switch a.sub {
	case "token":
		var out struct {
			Token string `json:"token"`
		}
		if err := c.do("POST", "/api/v1/projects/"+projectID+"/capture-tokens",
			map[string]string{"label": a.value("--label")}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("capture token (shown once — store it):"))
		fmt.Fprintln(stdout, bold(out.Token))
		fmt.Fprintln(stdout, muted("present it to POST /api/v1/capture as {\"token\", \"brief\"} to create a draft initiative"))
		return nil
	case "list":
		var out []struct {
			ID        string  `json:"id"`
			Label     string  `json:"label"`
			CreatedAt string  `json:"created_at"`
			LastUsed  *string `json:"last_used_at"`
		}
		if err := c.do("GET", "/api/v1/projects/"+projectID+"/capture-tokens", nil, &out); err != nil {
			return err
		}
		if len(out) == 0 {
			fmt.Fprintln(stdout, muted("no capture tokens — `idea capture token` mints one"))
			return nil
		}
		for _, t := range out {
			last := "never used"
			if t.LastUsed != nil {
				last = "used " + *t.LastUsed
			}
			fmt.Fprintf(stdout, "%s  %s  %s\n", t.ID, t.Label, muted(last))
		}
		return nil
	default:
		return errorf(kindUsage, "unknown capture subcommand %q", a.sub)
	}
}
