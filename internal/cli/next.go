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
		next, err := nextAction(c, projectID, iniID)
		if err != nil {
			return err
		}
		action := next.Action

		automatable := action == "advance" || action == "execute" || action == "verify"
		if !flags.has("--until-human") || !automatable {
			return reportNextAction(c, projectID, iniID, next, stdout, stderr)
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

// nextStep is the daemon's answer: the action token, plus the failure reason
// that only accompanies `failed`. The reason travels with the token rather than
// being fetched again by whoever renders it -- a second GET would add a round
// trip and a second way for this to fail.
type nextStep struct {
	Action        string `json:"action"`
	FailureReason string `json:"failure_reason"`
}

// nextAction asks the daemon what the initiative needs next.
func nextAction(c *client, projectID, iniID string) (nextStep, error) {
	var out nextStep
	if err := c.do("GET", initiativeResource(projectID, iniID, "next"), nil, &out); err != nil {
		return nextStep{}, err
	}
	return out, nil
}

// runAutomaticStep performs one automatable step and, for the asynchronous ones,
// waits for it to finish so the caller can ask what is next.
func runAutomaticStep(c *client, projectID, iniID, action string, stdout, stderr io.Writer) error {
	switch action {
	case "verify":
		// Synchronous: the response is the result.
		// Not a failure here: an unmet gate is exactly the human judgment
		// point this loop exists to stop at.
		return runVerify(cmdArgs{}, stdout, stderr, false)
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
		return waitForOperation(c, projectID, iniID, opID, stdout)
	default:
		return fmt.Errorf("cannot run %q automatically", action)
	}
}

// waitForOperation polls one operation to a terminal state. A failed operation
// is an error here rather than a silent continue: the loop must not carry on
// past a step that did not do what it said.
func waitForOperation(c *client, projectID, iniID, opID string, stdout io.Writer) error {
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
			// domain.Operation carries no error text, so the operation row
			// cannot say why. The initiative can: ask /next once, and if it
			// reports `failed` render the reason the daemon is already holding.
			//
			// Without this the fix does not fire on the path it was written
			// for. A failure that happens *inside* the --until-human loop is
			// caught here, before the loop re-asks /next, so the README's own
			// documented 401 produced "advance_initiative failed — run `idea
			// logs`" and the reason went unmentioned until a second, bare
			// `idea next`.
			if step, nerr := nextAction(c, projectID, iniID); nerr == nil && step.Action == "failed" {
				return failedInitiativeError(step.FailureReason)
			}
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
func reportNextAction(c *client, projectID, iniID string, step nextStep, stdout, stderr io.Writer) error {
	action := step.Action
	switch {
	case action == "advance":
		return cmdAdvance(cmdArgs{}, stdout, stderr)
	case action == "execute":
		return cmdExecute(cmdArgs{}, stdout, stderr)
	case action == "verify":
		// Not a failure here: an unmet gate is exactly the human judgment
		// point this loop exists to stop at.
		return runVerify(cmdArgs{}, stdout, stderr, false)
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
	case action == "failed":
		// Returned, not printed: main.go turns a returned error into `idea:
		// <err>` on stderr and exit 1, and a failed initiative is not a state a
		// script should read as success.
		return failedInitiativeError(step.FailureReason)
	default: // running
		// `idea watch` was the only pointer here and it needs a TTY
		// (cmdWatch -> Bubble Tea -> /dev/tty), so from a script this branch
		// named a command that cannot run. `idea status` leads because it works
		// anywhere; watch is still offered. Other commands still point at watch
		// on their own success paths -- advance.go:52 -- which is fine there and
		// out of scope here.
		fmt.Fprintln(stdout, muted("workflow running — `idea status` shows where it is, `idea watch` follows it live"))
	}
	return nil
}

// failedInitiativeError renders a failed initiative as an error naming both the
// cause and the way out.
//
// The reason is not always there to name. SetInitiativeStatus deliberately does
// not write failure_reason (store/repo.go:195), so anything that failed an
// initiative through it -- rather than through FailInitiative -- leaves the
// field empty. Rendering "failed: " in that case would replace one uninformative
// message with another, so the empty case gets its own text and still names the
// recovery.
func failedInitiativeError(reason string) error {
	// `idea status` leads, because retry is not always the right move: an
	// initiative can be `failed` while a change set still waits on review, and
	// re-running the pipeline is not what that needs.
	const recovery = "`idea status` shows where it is; `idea retry` re-runs it, `idea logs` has the full text"
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("the initiative failed, and no reason was recorded — %s", recovery)
	}
	return fmt.Errorf("the initiative failed: %s — %s", reason, recovery)
}
