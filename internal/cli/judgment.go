package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// The four commands that leave `needs_human_judgment`.
//
// Every one of these endpoints already existed and none of them had a caller in
// this package. So an initiative whose adversarial review did not converge --
// the ordinary outcome with a real model, 4/4 runs of the README's own quick
// start on the model the README names -- could not be moved by the CLI at all.
// `idea next` pointed at `idea decisions`, which prints "No decisions yet.";
// `idea advance` refused; dispositioning every finding through the API changed
// nothing, because the gate is the initiative's status. The only way out was the
// web UI, and the README's canonical `go install ./cmd/idea` serves a placeholder
// for that.

// cmdApprove accepts the reviewed PRD as it stands and moves to design.
func cmdApprove(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, iniID, err := judgmentTarget(a)
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, "approve"), nil, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ PRD approved")+" "+code(iniID))
	fmt.Fprintln(stdout, muted("next: ")+bold("idea next --until-human"))
	return nil
}

// cmdRevise regenerates the PRD from the review findings and re-runs the review.
//
// Guarded, because putting this on the CLI is what makes it reachable by
// accident. `ReviseInitiative` claims from plan_approved and complete as well
// (runtime.go), and Engine.Revise ends by writing `approved` over whatever it
// found -- so `idea revise` on an initiative whose plan was approved silently
// discards that approval, the same defect Resume was just fixed for. The web UI
// only offers it from the Define page, where the stage made it safe; `idea help`
// offers it unconditionally.
//
// The check is client-side and therefore racy, and that is acceptable here: the
// server still takes the claim atomically, so the worst case is a message rather
// than a corrupt transition. Narrowing the claim itself would change behaviour
// for the web UI, which no test covers -- recorded as `audit 6 / J4` instead.
func cmdRevise(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, iniID, err := judgmentTarget(a)
	if err != nil {
		return err
	}
	var ini struct {
		Status string `json:"status"`
	}
	// TrimSuffix: initiativeResource always appends a sub-resource segment, and
	// a trailing slash matches no route.
	path := strings.TrimSuffix(initiativeResource(projectID, iniID, ""), "/")
	if err := c.do("GET", path, nil, &ini); err == nil {
		switch ini.Status {
		case "plan_approved":
			return fmt.Errorf("revise regenerates the PRD, which would discard the approved plan below it; " +
				"use `idea revise-plan` to change the plan, or `idea reopen-phase` to go further back")
		case "complete":
			return fmt.Errorf("this initiative is complete; revising its PRD would reopen it and discard the plan. " +
				"Start a new initiative with `idea new`, or `idea reopen-phase` if you mean to reopen this one")
		}
	}
	return judgmentPost(c, projectID, iniID, stdout, "revise", "revising the PRD")
}

// cmdApprovePlan accepts the execution plan and unblocks the build.
func cmdApprovePlan(a cmdArgs, stdout, stderr io.Writer) error {
	return judgmentOperation(a, stdout, "approve-plan", "approving the plan")
}

// cmdRevisePlan regenerates the execution plan.
func cmdRevisePlan(a cmdArgs, stdout, stderr io.Writer) error {
	return judgmentOperation(a, stdout, "revise-plan", "regenerating the plan")
}

// judgmentTarget resolves the client and the initiative these commands act on.
func judgmentTarget(a cmdArgs) (*client, string, string, error) {
	c, projectID, err := connect()
	if err != nil {
		return nil, "", "", err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return nil, "", "", err
	}
	return c, projectID, iniID, nil
}

// judgmentOperation posts one of the asynchronous escapes and waits for it, so
// the command after this one has something to act on. `idea new` established
// that pattern: returning immediately left the next command racing the daemon.
func judgmentOperation(a cmdArgs, stdout io.Writer, path, gerund string) error {
	c, projectID, iniID, err := judgmentTarget(a)
	if err != nil {
		return err
	}
	return judgmentPost(c, projectID, iniID, stdout, path, gerund)
}

// judgmentPost is the half after the target is resolved.
func judgmentPost(c *client, projectID, iniID string, stdout io.Writer, path, gerund string) error {
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, path), nil, &out); err != nil {
		return err
	}
	opID := out["operation_id"]
	if opID == "" {
		fmt.Fprintln(stdout, ok("✓ "+gerund)+" "+code(iniID))
		return nil
	}
	fmt.Fprintln(stdout, muted(gerund+"… (")+bold("idea watch")+muted(" follows it)"))
	if err := waitForOperation(c, projectID, iniID, opID, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ done")+" "+muted("— next: ")+bold("idea next --until-human"))
	return nil
}

// reopenPhases are the design phases the daemon accepts. Kept here so the CLI
// refuses a bad one with the list, rather than sending a body the daemon 400s.
var reopenPhases = []string{"architecture", "product_design", "verification_design"}

// cmdReopenPhase reopens a design phase.
//
// `idea revise` has prescribed `idea reopen-phase` since audit 7 — twice, in
// the two states where revising the PRD would discard work below it — and the
// verb did not exist: the route was there, the CLI had no row for it, and
// following the advice answered `unknown command "reopen-phase"`. Half a
// prescription is worse than none, because it reads as though the user typed
// it wrong.
func cmdReopenPhase(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) == 0 {
		return fmt.Errorf("usage: idea reopen-phase <%s> [<initiative>]",
			strings.Join(reopenPhases, "|"))
	}
	phase := a.positional[0]
	if !slices.Contains(reopenPhases, phase) {
		return fmt.Errorf("unknown phase %q; use one of: %s", phase, strings.Join(reopenPhases, ", "))
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[1:])
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, "reopen-phase"),
		map[string]string{"phase": phase}, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ reopened")+" "+code(phase)+" "+muted("on")+" "+code(iniID))
	opID := out["operation_id"]
	if opID == "" || a.has("--no-wait") {
		fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
		return nil
	}
	fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	if err := waitForOperation(c, projectID, iniID, opID, stdout); err != nil {
		return err
	}
	step, nerr := nextAction(c, projectID, iniID)
	if nerr != nil {
		return fmt.Errorf("the phase reopened but the next step could not be read: %w\n"+
			"  `idea status` shows where it is", nerr)
	}
	return renderNextAction(step, stdout)
}
