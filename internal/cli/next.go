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

// How long to wait before saying anything while an operation runs, and how
// often after that. A demo-provider step finishes in well under a second, so
// these never fire on the no-key path; they exist for the model-backed one,
// where the only signal used to be a cursor.
//
// var, not const, so a test can drive the reporting without waiting 20 seconds
// for it. Nothing in the product reassigns them.
var (
	progressNoteAfter = 20 * time.Second
	progressNoteEvery = 30 * time.Second
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
		if action == "execute" {
			printExecutorResolution(out, stdout)
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

// waitIfRequested is the shared half of `--wait`, opt-in on every long action
// that otherwise returns a handle immediately (advance/execute/apply/revert).
//
// `idea new` blocks and streams progress by default while these return
// instantly — a real inconsistency, but flipping `idea new`'s own default
// would reopen the exact race its own doc comment describes fixing (the
// README's own quick start: the very next command used to see the initiative
// still `running`). So both defaults stay: `idea new` waits unless told
// `--no-wait`, everything else returns a handle unless told `--wait` — but
// every long action now understands the same `--wait`/`--no-wait`
// vocabulary, and `idea new --wait` is accepted too (a no-op restating its
// own default), so a script can spell "wait for this" the same way everywhere
// rather than needing to know which commands default which way.
//
// iniID may be "" when the caller has no initiative id in hand (apply/revert
// resolve a change set, not an initiative) -- waitForOperation's own use of it
// is best-effort (a nicer failure message), and degrades to a generic one.
func waitIfRequested(a cmdArgs, c *client, projectID, iniID, opID string, stdout io.Writer) error {
	if opID == "" || !a.has("--wait") {
		return nil
	}
	fmt.Fprintln(stdout, muted("waiting… (")+bold("idea watch")+muted(" follows it from another terminal; ^C returns you to the shell without cancelling it)"))
	return waitForOperation(c, projectID, iniID, opID, stdout)
}

// waitForOperation polls one operation to a terminal state. A failed operation
// is an error here rather than a silent continue: the loop must not carry on
// past a step that did not do what it said.
func waitForOperation(c *client, projectID, iniID, opID string, stdout io.Writer) error {
	started := time.Now()
	deadline := started.Add(operationTimeout)
	nextNote := started.Add(progressNoteAfter)
	for {
		var op struct {
			Status string `json:"status"`
			Type   string `json:"type"`
			// Stage (C3, wave 3 contract) is the engine's current step, e.g.
			// "Writing the PRD", "Agent running TASK-2". Printed in place of a
			// bare "still working…" below, when the daemon has one to report.
			Stage string `json:"stage"`
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
		// A model call that hangs is indistinguishable from one that is merely
		// slow, and both looked like `working…` forever. The provider client
		// allows 5 minutes per attempt and the engine retries three times, so a
		// black-holing endpoint produces ~15 minutes of total silence before
		// anything is written anywhere the user looks.
		if now := time.Now(); now.After(nextNote) {
			label := "still working…"
			if op.Stage != "" {
				label = op.Stage + "…"
			}
			fmt.Fprintf(stdout, "%s\n", muted(fmt.Sprintf(
				"  %s %s elapsed (gives up at %s; `idea cancel %s` stops it)",
				label, now.Sub(started).Round(time.Second), operationTimeout, opID)))
			nextNote = now.Add(progressNoteEvery)
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

// reportNextAction takes the next step. Despite the name it is not a renderer:
// the first three arms RUN the thing they name, which is what makes `idea next`
// a driver. Everything it does not run is delegated to renderNextAction.
//
// The split exists because `idea new` needs the reporting half and must not
// have the running half: routing its tail through this function would have made
// a successful `idea new` POST /advance and start the M2 pipeline, spending
// model budget through design before anyone read the PRD -- the thing the
// comment at the top of this file says the CLI deliberately does not do.
func reportNextAction(c *client, projectID, iniID string, step nextStep, stdout, stderr io.Writer) error {
	switch step.Action {
	case "advance":
		return cmdAdvance(cmdArgs{}, stdout, stderr)
	case "execute":
		return cmdExecute(cmdArgs{}, stdout, stderr)
	case "verify":
		// Not a failure here: an unmet gate is exactly the human judgment
		// point this loop exists to stop at.
		return runVerify(cmdArgs{}, stdout, stderr, false)
	}
	return renderNextAction(step, stdout)
}

// renderNextAction prints the guidance for a step nothing will take on its own.
// It writes and returns; it never changes state.
func renderNextAction(step nextStep, stdout io.Writer) error {
	action := step.Action
	switch {
	case strings.HasPrefix(action, "review "):
		id := strings.TrimPrefix(action, "review ")
		fmt.Fprintln(stdout, bold("Next:")+" review "+code(id))
		// Two lines, like the judgment arms. `idea review <id> approve` runs the
		// adversarial reviewer and approves only if it passes -- and on a real
		// model it often does not, after which the deny gate refuses outright.
		// Naming only the first command left the one command this arm offered
		// refused, with nothing else named: the live-lock the keyed magic path hit
		// every run.
		line := func(cmd, what string) {
			fmt.Fprintln(stdout, "  "+bold(cmd)+"  "+muted(what))
		}
		line("idea review "+id+" approve", "review it, and approve if it passes")
		line("idea review "+id+" approve --accept-findings", "accept it, findings and all")
	case strings.HasPrefix(action, "apply "):
		id := strings.TrimPrefix(action, "apply ")
		fmt.Fprintln(stdout, bold("Next:")+" apply "+code(id)+" — "+muted("idea apply "+id))
	case strings.HasPrefix(action, "resolve"):
		// Prefix, not equality. The token gained an argument naming *why* the
		// initiative is parked, and an exact-match arm would have gone dead and
		// dropped every one of these states into the default branch -- which
		// says the workflow is running. That is worse than the bug being fixed.
		reportJudgment(strings.TrimSpace(strings.TrimPrefix(action, "resolve")), stdout)
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

// reportJudgment names the command that actually moves an initiative parked on a
// human. It used to name `idea decisions` for every case, which prints "No
// decisions yet." in the one that matters -- the findings live under
// `idea reviews`, and no CLI command could act on any of it.
//
// The advice differs per reason, and getting it wrong is not cosmetic:
// approve-plan on a plan whose dependencies never resolved would set
// plan_approved over a graph known to be incomplete and leave the blocking
// finding undisposed, wedging the completion gate later.
func reportJudgment(reason string, stdout io.Writer) {
	line := func(cmd, what string) {
		fmt.Fprintln(stdout, "  "+bold(cmd)+"  "+muted(what))
	}
	switch reason {
	case "prd":
		fmt.Fprintln(stdout, bold("Next:")+" the PRD review did not converge — "+muted("idea reviews")+muted(" lists the findings"))
		// "accept ... and move to design" promised more than approve does:
		// cmdApprove posts and prints `next: idea next --until-human`, so the
		// command's own output contradicted this line one step earlier.
		line("idea approve", "accept the PRD as it stands; `idea next` then moves to design")
		line("idea revise ", "regenerate it from the findings (may not converge either)")
	case "plan-dependencies":
		fmt.Fprintln(stdout, bold("Next:")+" the execution plan has unresolved task dependencies — "+muted("idea tasks")+muted(" shows the order"))
		// Deliberately only one option. The API refuses approve-plan on this
		// reason -- the undisposed plan_dependency finding is the guard keeping
		// an incomplete graph out of `complete` -- so offering it would name a
		// command that 409s.
		line("idea revise-plan", "regenerate the plan")
	case "plan":
		fmt.Fprintln(stdout, bold("Next:")+" the plan review raised blocking findings — "+muted("idea reviews")+muted(" lists them"))
		line("idea revise-plan ", "regenerate the plan from the findings")
		// This used to read "re-run the plan review (it can block again)", and
		// that was honest about a defect rather than a feature: ApprovePlan
		// re-ran the adversarial review, so whether it escaped depended on the
		// model not raising a blocking finding this time. DX audit 7 observed it
		// blocking eleven consecutive times on a real provider, with no route to
		// plan_approved from any surface. It is now a terminal accept, matching
		// `idea approve` on the PRD half, so this line can promise an outcome.
		//
		// It can still be refused, and the refusals are 409s rather than silent
		// re-blocks. Reachable from THIS state: the plan carries a `critical`
		// deterministic finding -- a dependency cycle -- which no human can sign
		// off because nextReadyTask could never schedule it, and whose message
		// names revise-plan; a change set already exists, which names nothing
		// because nothing clears that state (see below); or the initiative is
		// running. The judgment-reason refusal is NOT reachable from here: this
		// branch is entered only when the reason already is plan_review, which
		// is exactly what the API accepts.
		//
		// `idea execute` is deliberately NOT offered, though it works from this
		// state. It is a one-way door: the blocking findings stay undisposed,
		// and the only routes that disposition them -- approve-plan,
		// revise-plan and reopen-phase -- all refuse once a change set exists.
		// One execute here therefore wedges the completion gate, which requires
		// no open blocking findings.
		line("idea approve-plan", "accept the plan, findings and all, and move to Build")
	case "verification":
		fmt.Fprintln(stdout, bold("Next:")+" a requirement was failed after this initiative completed")
		line("idea verify <REQ-n> --pass|--fail --note \"<how you checked>\"", "record the evidence")
	default:
		// A daemon older than the recorded reason, or an initiative parked
		// before it existed. Name where to look rather than a wrong command.
		fmt.Fprintln(stdout, bold("Next:")+" this initiative is waiting on your judgment — "+muted("idea status")+muted(" says why, ")+muted("idea reviews")+muted(" lists any findings"))
	}
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
