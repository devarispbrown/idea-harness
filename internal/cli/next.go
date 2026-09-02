package cli

import (
	"fmt"
	"io"
	"strings"
)

// cmdNext drives the "continue until I'm needed" loop (§52, Track C): it runs
// the next automatable step (advance / execute / verify) and stops at a human
// judgment point (review / apply / resolve / done).
func cmdNext(args []string, stdout, stderr io.Writer) error {
	// next takes no flags today; parsing rejects anything typed after it
	// instead of silently discarding it.
	if _, _, err := parseArgs(flagSpec{}, args); err != nil {
		return err
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}
	var out struct {
		Action string `json:"action"`
	}
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/next", nil, &out); err != nil {
		return err
	}
	switch {
	case out.Action == "advance":
		return cmdAdvance([]string{iniID}, stdout, stderr)
	case out.Action == "execute":
		return cmdExecute([]string{iniID}, stdout, stderr)
	case out.Action == "verify":
		return cmdVerify(nil, stdout, stderr)
	case strings.HasPrefix(out.Action, "review "):
		id := strings.TrimPrefix(out.Action, "review ")
		fmt.Fprintln(stdout, bold("Next:")+" review "+code(id)+" — "+muted("idea review "+id+" approve"))
	case strings.HasPrefix(out.Action, "apply "):
		id := strings.TrimPrefix(out.Action, "apply ")
		fmt.Fprintln(stdout, bold("Next:")+" apply "+code(id)+" — "+muted("idea apply "+id))
	case out.Action == "resolve":
		fmt.Fprintln(stdout, bold("Next:")+" resolve open decisions/questions/findings — "+muted("idea decisions"))
	case out.Action == "done":
		fmt.Fprintln(stdout, ok("✓ initiative complete"))
	default:
		fmt.Fprintln(stdout, muted("workflow running — try `idea watch`"))
	}
	return nil
}
