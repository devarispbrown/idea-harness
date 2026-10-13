package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
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
			return errorf(kindConflict, "revise regenerates the PRD, which would discard the approved plan below it; "+
				"use `idea revise-plan` to change the plan, or `idea reopen-phase` to go further back")
		case "complete":
			return errorf(kindConflict, "this initiative is complete; revising its PRD would reopen it and discard the plan. "+
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
		return usageErrorf("usage: idea reopen-phase <%s> [<initiative>]",
			strings.Join(reopenPhases, "|"))
	}
	phase := a.positional[0]
	if !slices.Contains(reopenPhases, phase) {
		return usageErrorf("unknown phase %q; use one of: %s", phase, strings.Join(reopenPhases, ", "))
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

// ---------- the judgment loop: questions, decisions, triage ----------
//
// The four commands above escape needs_human_judgment at the initiative
// level. The finer-grained loop underneath it -- individual open questions
// and decisions -- had the same gap: `idea decisions` lists them, `idea
// next`'s own guidance says "Resolve open decisions" and "Resolve open
// questions" (projection.BuildGuidance), and nothing in this package could
// act on either one. Answering a question or resolving a decision meant
// reaching past the CLI into POST …/questions/{id}/answer or
// …/decisions/{id}/resolve directly -- both routes existed, routed, and were
// exercised only by the web UI and raw HTTP. Closed here alongside `idea
// accept-recommendations` (the daemon's own batch escape, review B4 / #8)
// and `idea triage` (its ranked view, same review): the whole loop -- see
// what needs judgment, act on one item or all of them, check what is left --
// is now reachable without leaving the CLI.

// shortRef renders a stable, short form of a raw id for display. Questions
// (unlike decisions/tasks/change sets) have no human code, only a content-hash
// id, so `idea questions` prints this and `idea answer` accepts it back.
func shortRef(id string) string {
	const n = 8
	if len(id) > n {
		return id[:n]
	}
	return id
}

// resolveQuestionRef resolves a question reference -- a full id or an
// unambiguous prefix of one, exactly what `idea questions` prints via
// shortRef -- to its full id.
func resolveQuestionRef(c *client, projectID, iniID, ref string) (string, error) {
	var questions []cliQuestion
	if err := c.do("GET", initiativeResource(projectID, iniID, "questions"), nil, &questions); err != nil {
		return "", err
	}
	for _, q := range questions {
		if q.ID == ref {
			return q.ID, nil
		}
	}
	var matches []string
	for _, q := range questions {
		if strings.HasPrefix(q.ID, ref) {
			matches = append(matches, q.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", errorf(kindNotFound, "no question matching %q; `idea questions` lists them", ref)
	default:
		return "", usageErrorf("%q matches more than one question; use more of the id shown by `idea questions`", ref)
	}
}

// cmdQuestions lists an initiative's questions -- the read half of the loop
// `idea decisions` already covers for decisions.
func cmdQuestions(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var questions []cliQuestion
	if err := c.do("GET", initiativeResource(projectID, iniID, "questions"), nil, &questions); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, questions)
	}
	if len(questions) == 0 {
		fmt.Fprintln(stdout, muted("No questions yet."))
		return nil
	}
	open := 0
	for _, q := range questions {
		if q.Status == "open" {
			open++
		}
	}
	fmt.Fprintln(stdout, heading("Questions")+muted(fmt.Sprintf("  (%d open)", open)))
	for _, q := range questions {
		marker := ok("✓ ")
		if q.Status == "open" {
			marker = warn("● ")
		}
		fmt.Fprintln(stdout, marker+code(shortRef(q.ID))+" "+q.Question)
	}
	return nil
}

// cmdAnswer answers an open question.
func cmdAnswer(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 2 {
		return usageErrorf(`usage: idea answer <question> "<answer>" [<initiative>]`)
	}
	ref, answer := a.positional[0], a.positional[1]
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[2:])
	if err != nil {
		return err
	}
	qID, err := resolveQuestionRef(c, projectID, iniID, ref)
	if err != nil {
		return err
	}
	if err := c.do("POST", questionResource(projectID, qID, "answer"), map[string]string{"answer": answer}, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ answered")+" "+code(shortRef(qID)))
	fmt.Fprintln(stdout, muted("next: ")+bold("idea next --until-human"))
	return nil
}

// cmdResolve resolves an open decision, by option (a 1-based index into its
// options, when it has any -- "idea decisions --json" or the web UI shows
// them in that order) or free text for a decision with none.
func cmdResolve(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 2 {
		return usageErrorf(`usage: idea resolve <decision> <option#>|"<resolution>" [<initiative>]`)
	}
	ref, resolution := a.positional[0], a.positional[1]
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[2:])
	if err != nil {
		return err
	}
	target, resolved, err := resolveDecision(c, projectID, iniID, ref, resolution)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ resolved")+" "+code(target.Code)+" → "+resolved)
	fmt.Fprintln(stdout, muted("next: ")+bold("idea next --until-human"))
	return nil
}

// resolveDecision finds the decision ref names (id or code) in an initiative,
// turns an option number into that option's text, and resolves it. It returns
// the decision and the resolution text actually recorded. Shared with the MCP
// server's resolve_decision tool, so an option number means the same thing on
// both surfaces.
func resolveDecision(c *client, projectID, iniID, ref, resolution string) (cliDecision, string, error) {
	var decs []cliDecision
	if err := c.do("GET", initiativeResource(projectID, iniID, "decisions"), nil, &decs); err != nil {
		return cliDecision{}, "", err
	}
	var target *cliDecision
	for i := range decs {
		if decs[i].ID == ref || decs[i].Code == ref {
			target = &decs[i]
			break
		}
	}
	if target == nil {
		return cliDecision{}, "", errorf(kindNotFound, "no decision matching %q; `idea decisions` lists them", ref)
	}
	resolved := resolution
	if len(target.Options) > 0 {
		if n, convErr := strconv.Atoi(resolution); convErr == nil {
			if n < 1 || n > len(target.Options) {
				return cliDecision{}, "", usageErrorf("%s has %d option(s); %d is out of range -- `idea decisions --json` lists them",
					target.Code, len(target.Options), n)
			}
			resolved = target.Options[n-1]
		}
	}
	if err := c.do("POST", decisionResource(projectID, target.ID, "resolve"), map[string]string{"resolution": resolved}, nil); err != nil {
		return cliDecision{}, "", err
	}
	return *target, resolved, nil
}

// cmdReopenDecision returns a resolved decision to open, so it can be
// answered differently -- the CLI half of POST …/decisions/{id}/reopen,
// which until now only the web UI could reach.
func cmdReopenDecision(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 1 {
		return usageErrorf("usage: idea reopen-decision <decision> [<initiative>]")
	}
	ref := a.positional[0]
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[1:])
	if err != nil {
		return err
	}
	dID, err := resolveObjectID(c, projectID, iniID, "decision", ref)
	if err != nil {
		return err
	}
	if err := c.do("POST", decisionResource(projectID, dID, "reopen"), nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ reopened")+" "+code(ref))
	return nil
}

// cliAcceptRecommendationsResult mirrors POST .../decisions/accept-recommendations.
type cliAcceptRecommendationsResult struct {
	Resolved []string `json:"resolved"`
	Skipped  []struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// cmdAcceptRecommendations resolves every open decision with its own
// recommendation in one call (review B4, adopted #8's batch escape) -- until
// now reachable only through raw HTTP.
func cmdAcceptRecommendations(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var out cliAcceptRecommendationsResult
	if err := c.do("POST", initiativeResource(projectID, iniID, "decisions/accept-recommendations"), nil, &out); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, out)
	}
	if len(out.Resolved) == 0 && len(out.Skipped) == 0 {
		fmt.Fprintln(stdout, muted("No open decisions."))
		return nil
	}
	for _, c := range out.Resolved {
		fmt.Fprintln(stdout, ok("✓ resolved")+" "+code(c))
	}
	for _, sk := range out.Skipped {
		fmt.Fprintln(stdout, warn("· skipped")+" "+code(sk.Code)+" "+muted("— "+sk.Reason))
	}
	return nil
}

// cliTriageItem mirrors one entry of GET .../triage (review B4, adopted #8).
type cliTriageItem struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Label      string `json:"label"`
	Blocking   bool   `json:"blocking"`
	Severity   string `json:"severity,omitempty"`
	Dependents int    `json:"dependents"`
	Rank       string `json:"rank"`
}

// cmdTriage shows the ranked "what needs your judgment first" view: the top
// items by impact (blocking, then severity, then dependents), the rest
// labelled optional.
func cmdTriage(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var out struct {
		Items []cliTriageItem `json:"items"`
	}
	if err := c.do("GET", initiativeResource(projectID, iniID, "triage"), nil, &out); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, out.Items)
	}
	if len(out.Items) == 0 {
		fmt.Fprintln(stdout, ok("Nothing needs your judgment."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Triage"))
	printGroup := func(rank, label string) {
		var items []cliTriageItem
		for _, it := range out.Items {
			if it.Rank == rank {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			return
		}
		fmt.Fprintln(stdout, "  "+bold(label))
		for _, it := range items {
			marker := muted(it.Type)
			if it.Blocking {
				marker = errText("blocking " + it.Type)
			}
			fmt.Fprintf(stdout, "    %s [%s] %s\n", code(shortRef(it.ID)), marker, it.Label)
		}
	}
	printGroup("priority", "Priority")
	printGroup("optional", "Optional")
	return nil
}
