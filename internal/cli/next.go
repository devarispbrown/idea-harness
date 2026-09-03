package cli

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// nextFlags is the flag spec for `idea next`.
var nextFlags = flagSpec{"--until-human": flagBool}

const (
	// operationPollInterval is how often the --until-human loop asks whether the
	// step it started has finished.
	operationPollInterval = 500 * time.Millisecond
	// operationTimeout bounds one automatable step. A model-backed advance can
	// legitimately take minutes; hanging forever is what this prevents.
	operationTimeout = 15 * time.Minute
	// maxAutomaticSteps stops a loop that keeps being handed the same action
	// without the initiative moving.
	maxAutomaticSteps = 50
)

// cmdNext drives the "continue until I'm needed" loop (§52, Track C): it runs
// the next automatable step (advance / execute / verify) and stops at a human
// judgment point (review / apply / resolve / done).
//
// With --until-human it keeps going instead of stopping after one step, waiting
// for each asynchronous step to finish before asking what is next. That is what
// takes the documented path from 13 commands to 8: the engine already knows the
// sequence, and the human still decides every judgment point. `idea new` is
// deliberately NOT made to drive on its own — that would spend model budget
// through design and implementation before anyone has read the PRD.
func cmdNext(a cmdArgs, stdout, stderr io.Writer) error {
	flags := a.flags
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}

	previous := ""
	for step := 0; ; step++ {
		action, err := nextAction(c, projectID, iniID)
		if err != nil {
			return err
		}

		automatable := action == "advance" || action == "execute" || action == "verify"
		if !flags.has("--until-human") || !automatable {
			return reportNextAction(c, projectID, iniID, action, stdout, stderr)
		}

		// The same action twice in a row means the step ran and changed
		// nothing, which is not a failure -- it is the loop reaching something
		// only a human can supply. The ordinary case is `verify`: the gate runs
		// clean but the initiative stays unverified until someone records
		// evidence, so without this the loop would run the gate until its step
		// budget ran out and then blame the initiative.
		if action == previous {
			return reportStalledAction(c, projectID, iniID, action, stdout)
		}
		previous = action

		if step >= maxAutomaticSteps {
			return fmt.Errorf("stopped after %d automatic steps; the initiative is still asking to %q — run `idea status` to see why", maxAutomaticSteps, action)
		}

		if err := runAutomaticStep(c, projectID, iniID, action, stdout, stderr); err != nil {
			return err
		}
	}
}

// nextAction asks the daemon what the initiative needs next.
func nextAction(c *client, projectID, iniID string) (string, error) {
	var out struct {
		Action string `json:"action"`
	}
	if err := c.do("GET", initiativeResource(projectID, iniID, "next"), nil, &out); err != nil {
		return "", err
	}
	return out.Action, nil
}

// runAutomaticStep performs one automatable step and, for the asynchronous ones,
// waits for it to finish so the caller can ask what is next.
func runAutomaticStep(c *client, projectID, iniID, action string, stdout, stderr io.Writer) error {
	switch action {
	case "verify":
		// Synchronous: the response is the result.
		return cmdVerify(cmdArgs{}, stdout, stderr)
	case "advance", "execute":
		fmt.Fprintln(stdout, muted("→ ")+action+muted(" …"))
		var out map[string]string
		if err := c.do("POST", initiativeResource(projectID, iniID, action), nil, &out); err != nil {
			return err
		}
		if kind := out["executor_kind"]; kind == "internal" || kind == "" {
			if action == "execute" {
				fmt.Fprintln(stdout, warn("⚠ the internal executor writes a placeholder (not real code); set executor.kind = omp|hermes in .idea-harness/harness.yaml to generate real code."))
			}
		}
		opID := out["operation_id"]
		if opID == "" {
			// Nothing to wait on; let the next round decide.
			return nil
		}
		return waitForOperation(c, opID, stdout)
	default:
		return fmt.Errorf("cannot run %q automatically", action)
	}
}

// waitForOperation polls one operation to a terminal state. A failed operation
// is an error here rather than a silent continue: the loop must not carry on
// past a step that did not do what it said.
func waitForOperation(c *client, opID string, stdout io.Writer) error {
	deadline := time.Now().Add(operationTimeout)
	for {
		var op struct {
			Status string `json:"status"`
			Type   string `json:"type"`
		}
		if err := c.do("GET", operationResource(opID, ""), nil, &op); err != nil {
			return err
		}
		switch op.Status {
		case "succeeded":
			return nil
		case "failed":
			return fmt.Errorf("%s failed — run `idea logs` for details", op.Type)
		case "cancelled":
			return fmt.Errorf("%s was cancelled", op.Type)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not finish within %s; it may still be running — `idea watch` follows it, `idea cancel %s` stops it",
				op.Type, operationTimeout, opID)
		}
		time.Sleep(operationPollInterval)
	}
}

// reportStalledAction explains an automatable step that ran without moving the
// initiative, in terms of what the human owes it. Exits 0: waiting on a person
// is the loop working, not failing.
func reportStalledAction(c *client, projectID, iniID, action string, stdout io.Writer) error {
	if action != "verify" {
		fmt.Fprintln(stdout, bold("Next:")+" "+action+" "+muted("ran without changing anything — `idea status` shows where it is"))
		return nil
	}

	// Name the requirements still missing evidence, so the reader does not have
	// to go find them.
	var out struct {
		Coverage []cliCoverage `json:"coverage"`
	}
	if err := c.do("POST", initiativeResource(projectID, iniID, "verify"), map[string]any{}, &out); err != nil {
		return err
	}
	var pending []string
	for _, cov := range out.Coverage {
		if cov.Status == "waived" {
			continue // waived is out of scope, not awaiting evidence
		}
		if cov.Pass < cov.Total || cov.NeedsManual > 0 {
			pending = append(pending, cov.Code)
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(stdout, bold("Next:")+" verify — "+muted("`idea verify` shows what the gate is waiting on"))
		return nil
	}
	fmt.Fprintln(stdout, bold("Next:")+" record verification evidence for "+code(strings.Join(pending, ", ")))
	fmt.Fprintln(stdout, muted("  idea verify "+pending[0]+" --pass --note \"<how you checked>\""))
	return nil
}

// reportNextAction prints the human-facing guidance for an action the loop will
// not take on its own. This is the whole of `idea next` without --until-human.
func reportNextAction(c *client, projectID, iniID, action string, stdout, stderr io.Writer) error {
	switch {
	case action == "advance":
		return cmdAdvance(cmdArgs{}, stdout, stderr)
	case action == "execute":
		return cmdExecute(cmdArgs{}, stdout, stderr)
	case action == "verify":
		return cmdVerify(cmdArgs{}, stdout, stderr)
	case strings.HasPrefix(action, "review "):
		id := strings.TrimPrefix(action, "review ")
		fmt.Fprintln(stdout, bold("Next:")+" review "+code(id)+" — "+muted("idea review "+id+" approve"))
	case strings.HasPrefix(action, "apply "):
		id := strings.TrimPrefix(action, "apply ")
		fmt.Fprintln(stdout, bold("Next:")+" apply "+code(id)+" — "+muted("idea apply "+id))
	case action == "resolve":
		fmt.Fprintln(stdout, bold("Next:")+" resolve open decisions/questions/findings — "+muted("idea decisions"))
	case action == "done":
		fmt.Fprintln(stdout, ok("✓ initiative complete"))
	default:
		fmt.Fprintln(stdout, muted("workflow running — try `idea watch`"))
	}
	return nil
}
