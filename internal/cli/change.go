package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// cliChangeSet mirrors the change-set API response.
type cliChangeSet struct {
	ID             string `json:"id"`
	HumanID        string `json:"human_id"`
	InitiativeID   string `json:"initiative_id"`
	TaskID         string `json:"task_id"`
	BaseSnapshotID string `json:"base_snapshot_id"`
	Status         string `json:"status"`
	PatchDigest    string `json:"patch_digest"`
	ReviewID       string `json:"review_id"`
	GitCommitSHA   string `json:"git_commit_sha"`
	CreatedAt      string `json:"created_at"`
	// ReviewCompletedAt mirrors domain.ChangeSet.ReviewCompletedAt (omitted by
	// the API when nil, same as there): non-nil once the adversarial reviewer
	// has actually run for this change set. Used to distinguish "awaiting
	// your approval" from "never finished its adversarial review" (ICP re-run
	// D4) -- both surface as status review_required, and only this field
	// tells them apart.
	ReviewCompletedAt *string `json:"review_completed_at,omitempty"`
}

// changeSetReviewStatusLabel is cmdChanges's per-item status label (ICP
// re-run D4): `idea changes` used to answer "needs your approval" for a
// change set whose adversarial review never completed at all (most often
// cancelled mid-review, per domain.ChangeSet.ReviewNeverCompletedCause) --
// the same state `idea status`'s guidance already described correctly as
// "never finished its adversarial review". There is nothing to approve yet
// in that state (`idea review <id> approve` refuses it and names `rerun`),
// so the two commands must not disagree about what to call it.
func changeSetReviewStatusLabel(cs cliChangeSet) string {
	if cs.Status == "review_required" && cs.ReviewCompletedAt == nil {
		return "never finished its adversarial review"
	}
	label := humanStatus[cs.Status]
	if label == "" {
		return cs.Status
	}
	return label
}

// cliRiskSignal mirrors a risk signal.
type cliRiskSignal struct {
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
	Detail   string `json:"detail"`
}

// cliRiskProfile mirrors a risk profile.
type cliRiskProfile struct {
	ID           string          `json:"id"`
	InitiativeID string          `json:"initiative_id"`
	Tier         string          `json:"tier"`
	Signals      []cliRiskSignal `json:"signals"`
	Reason       []string        `json:"reason"`
}

// cliExecutor mirrors an executor + capabilities view.
type cliExecutor struct {
	Name         string   `json:"name"`
	Trust        string   `json:"trust"`
	SandboxMode  string   `json:"sandbox_mode"`
	Network      bool     `json:"network"`
	Capabilities []string `json:"capabilities"`
	Selected     bool     `json:"selected"`
	// Resolution is present on the row the next run would use.
	Resolution *workflow.ExecutorResolution `json:"resolution,omitempty"`
}

// cmdExecute launches the execute pipeline for the next ready task.
func cmdExecute(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, positional)
	if err != nil {
		return err
	}
	// D-a, wave 3 contract: an optional note steering this one run, appended to
	// the implementer's brief and recorded on the agent run. body stays an
	// untyped nil (not a nil map[string]any) when there is no note: `do`
	// checks `body != nil`, and a nil map boxed into `any` is a non-nil
	// interface — that would send a literal JSON `null` body instead of none.
	var body any
	if note := a.value("--note"); note != "" {
		body = map[string]any{"note": note}
	}
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, "execute"), body, &out); err != nil {
		return err
	}
	printExecutorResolution(out, stdout)
	fmt.Fprintln(stdout, ok("✓ execute started")+" "+code(iniID))
	opID := out["operation_id"]
	if opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	}
	if a.has("--wait") {
		if err := waitIfRequested(a, c, projectID, iniID, opID, stdout); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("✓ done"))
		return nil
	}
	fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
	return nil
}

// cmdCancel interrupts a running asynchronous operation by id (§16, M3).
func cmdCancel(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	if len(positional) < 1 {
		return usageErrorf("usage: idea cancel <operation-id>")
	}
	c, _, err := connect()
	if err != nil {
		return err
	}
	opID := positional[0]
	var out map[string]string
	if err := c.do("POST", operationResource(opID, "cancel"), nil, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ cancelled operation")+" "+code(opID))
	return nil
}

// cmdChanges lists the initiative's change sets.
// humanStatus maps a ChangeSet status to human language (§52, Track C). Raw
// status stays available under --json.
var humanStatus = map[string]string{
	"draft":             "draft",
	"checks_running":    "checking",
	"review_required":   "needs your approval",
	"rejected":          "rejected",
	"changes_requested": "changes requested",
	"superseded":        "superseded",
	"approved":          "approved — apply it",
	"applying":          "applying",
	"applied":           "applied",
	"reverted":          "reverted",
	"conflicted":        "conflicted",
}

func cmdChanges(a cmdArgs, stdout, stderr io.Writer) error {
	positional, flags := a.positional, a.flags
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, positional)
	if err != nil {
		return err
	}
	var changes []cliChangeSet
	if err := c.do("GET", initiativeResource(projectID, iniID, "change-sets"), nil, &changes); err != nil {
		return err
	}
	if flags.has("--json") {
		return dumpJSON(stdout, changes)
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, muted("No change sets yet — run `idea execute`."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Change Sets"))
	for _, cs := range changes {
		fmt.Fprintf(stdout, "  %s  %s\n", code(cs.HumanID), muted(changeSetReviewStatusLabel(cs)))
	}
	return nil
}

// cmdChange shows a single change set by id.
func cmdChange(a cmdArgs, stdout, stderr io.Writer) error {
	positional, flags := a.positional, a.flags
	if len(positional) < 1 {
		return usageErrorf("usage: idea change <id>")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var cs cliChangeSet
	if err := c.do("GET", changeSetResource(projectID, positional[0], ""), nil, &cs); err != nil {
		return err
	}
	if flags.has("--json") {
		return dumpJSON(stdout, cs)
	}
	fmt.Fprintln(stdout, heading("Change Set "+code(cs.HumanID)))
	fmt.Fprintf(stdout, "  %s %s\n", muted("status:"), cs.Status)
	if cs.TaskID != "" {
		fmt.Fprintf(stdout, "  %s %s\n", muted("task:"), cs.TaskID)
	}
	if cs.PatchDigest != "" {
		fmt.Fprintf(stdout, "  %s %s\n", muted("patch:"), cs.PatchDigest)
	}
	if cs.GitCommitSHA != "" {
		fmt.Fprintf(stdout, "  %s %s\n", muted("commit:"), cs.GitCommitSHA)
	}
	return nil
}

// cmdApply applies an approved change set by id.
func cmdApply(a cmdArgs, stdout, stderr io.Writer) error {
	return runChangeSetAction("apply", a, stdout)
}

// cmdRevert rolls back a change set by id.
func cmdRevert(a cmdArgs, stdout, stderr io.Writer) error {
	return runChangeSetAction("revert", a, stdout)
}

// cliPRChecks mirrors the pr-checks API response (git.PRChecksResult).
type cliPRChecks struct {
	Overall string           `json:"overall"`
	Checks  []cliCheckStatus `json:"checks"`
}

// cliCheckStatus mirrors one entry of git.PRChecksResult.Checks.
type cliCheckStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Link  string `json:"link,omitempty"`
}

// cmdPR opens (idempotently) a pull request for an initiative's dedicated
// branch, or with the "checks" subcommand, reports the latest CI status of a
// change set's pull request (review C4 piece 3).
func cmdPR(a cmdArgs, stdout, stderr io.Writer) error {
	if a.sub == "checks" {
		return cmdPRChecks(a, stdout)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, "pr"), nil, &out); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, out)
	}
	fmt.Fprintln(stdout, ok("✓ pull request")+" "+code(out["pr_url"]))
	return nil
}

// cmdPRChecks shows the latest CI status of a change set's initiative's pull
// request, via `idea pr checks <change-set-id>`.
func cmdPRChecks(a cmdArgs, stdout io.Writer) error {
	positional := a.positional
	if len(positional) < 1 {
		return usageErrorf("usage: idea pr checks <change-set-id>")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var res cliPRChecks
	if err := c.do("GET", changeSetResource(projectID, positional[0], "pr-checks"), nil, &res); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, res)
	}
	if res.Overall == "none" {
		fmt.Fprintln(stdout, muted("no CI checks reported yet"))
		return nil
	}
	fmt.Fprintln(stdout, heading("CI status: ")+res.Overall)
	for _, chk := range res.Checks {
		fmt.Fprintf(stdout, "  %s  %s\n", chk.Name, chk.State)
	}
	return nil
}

// cmdReview applies a review verdict to a change set, or -- with "rerun" --
// runs the adversarial reviewer again for a change set whose review never
// completed the first time (ICP re-run defect #1: Execute's own review call
// errored, most often cancelled mid-review, leaving only mechanical-check
// observations behind). rerun is not itself a ChangeSet-state-machine
// verdict, but it shares this command and endpoint.
//
// rerun is a real model call (ICP re-run D2: observed 1m12s-1m38s across
// four founder-journey runs) and the daemon now runs it as an async
// operation rather than inside the request -- running it inside the request
// used to mean the daemon always finished the review while the CLI's own 60s
// http.Client timed out waiting for the response, so a brand-new command
// appeared to fail on every single use even though the work completed every
// time. rerun therefore waits on the operation by default (the same
// reasoning `idea retry` waits by default: it prints a start line and then
// polls, so a client-side timeout can never again be mistaken for the review
// itself failing); --no-wait returns a handle immediately instead, matching
// every other long-running command's escape hatch. approve/reject/
// changes_requested are unchanged: no model call, still answered
// synchronously.
func cmdReview(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	if len(positional) < 2 {
		return usageErrorf("usage: idea review <id> <approve|reject|changes_requested|rerun>")
	}
	verdict := positional[1]
	switch verdict {
	case "approve":
		verdict = "approved"
	case "reject":
		verdict = "rejected"
	case "approved", "rejected", "changes_requested", "rerun":
	default:
		return usageErrorf("unknown verdict %q (use approve|reject|changes_requested|rerun)", positional[1])
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	body := map[string]any{"verdict": verdict}
	accepting := a.has("--accept-findings")
	if accepting {
		if verdict != "approved" {
			return usageErrorf("--accept-findings only applies to `approve`")
		}
		body["accept_findings"] = true
	}
	var out map[string]string
	if err := c.do("POST", changeSetResource(projectID, positional[0], "review"), body, &out); err != nil {
		return err
	}
	if verdict == "rerun" {
		fmt.Fprintln(stdout, ok("✓ review rerun started")+" "+code(positional[0]))
		opID := out["operation_id"]
		if opID != "" {
			fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
		}
		if a.has("--no-wait") || opID == "" {
			fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
			return nil
		}
		fmt.Fprintln(stdout, muted("working… (")+bold("--no-wait")+muted(" returns immediately; ")+bold("idea watch")+muted(" follows it)"))
		if err := waitForOperation(c, projectID, "", opID, stdout); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("✓ review rerun complete")+" "+code(positional[0]))
		return nil
	}
	line := ok("✓ review recorded") + " " + code(positional[0]) + " → " + verdict
	if accepting {
		line += " " + muted("(findings accepted)")
	}
	fmt.Fprintln(stdout, line)
	return nil
}

// runChangeSetAction POSTs to /change-sets/{id}/{action} and prints the
// result. `--wait` (item: long actions return a handle by default, `idea new`
// is the one exception -- see waitIfRequested) blocks and streams progress
// the same way `idea advance`/`idea execute --wait` do.
func runChangeSetAction(action string, a cmdArgs, stdout io.Writer) error {
	positional := a.positional
	if len(positional) < 1 {
		return usageErrorf("usage: idea %s <id> [--wait]", action)
	}
	id := positional[0]
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", changeSetResource(projectID, id, action), nil, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ "+action+" started")+" "+code(id))
	opID := out["operation_id"]
	if opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	}
	if a.has("--wait") {
		// No initiative id in hand here (this resolves a change set, not an
		// initiative) -- waitForOperation's use of it is best-effort only.
		if err := waitIfRequested(a, c, projectID, "", opID, stdout); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("✓ done"))
	}
	return nil
}

// cmdRisk prints the deterministic risk profile for an initiative.
func cmdRisk(a cmdArgs, stdout, stderr io.Writer) error {
	positional, flags := a.positional, a.flags
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, positional)
	if err != nil {
		return err
	}
	var profile cliRiskProfile
	if err := c.do("GET", initiativeResource(projectID, iniID, "risk"), nil, &profile); err != nil {
		return err
	}
	if flags.has("--json") {
		return dumpJSON(stdout, profile)
	}
	fmt.Fprintln(stdout, heading("Risk Profile")+" "+muted("("+profile.Tier+")"))
	for _, s := range profile.Signals {
		if s.Detected {
			fmt.Fprintf(stdout, "  "+warn("● ")+"%s\n", s.Name)
		}
	}
	if len(profile.Signals) == 0 {
		fmt.Fprintln(stdout, muted("  no elevated risk signals detected"))
	}
	return nil
}

// cmdExecutors lists the available executors and their capabilities.
func cmdExecutors(a cmdArgs, stdout, stderr io.Writer) error {
	if a.sub == "setup" {
		return cmdExecutorSetup(a, stdout, stderr)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var executors []cliExecutor
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/executors", nil, &executors); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, executors)
	}
	fmt.Fprintln(stdout, heading("Executors"))
	for _, e := range executors {
		sel := ""
		if e.Selected {
			sel = " " + ok("(selected)")
		}
		fmt.Fprintf(stdout, "  %s [%s]%s\n", code(e.Name), muted(e.Trust), sel)
		for _, cap := range e.Capabilities {
			fmt.Fprintf(stdout, "    · %s\n", cap)
		}
	}
	return nil
}

// cmdExecutorSetup wires a real coding agent (Hermes default, OMP alternative)
// with a single command: it verifies the CLI is present in the image (hardened,
// no pull), then writes executor config via the daemon. Requires --yes to accept
// the network-egress/auto-approve/key-forwarding security tradeoff.
func cmdExecutorSetup(a cmdArgs, stdout, stderr io.Writer) error {
	// Flags come from the table's spec for `executors setup`; this used to be a
	// hand-rolled loop, one of four that predated parseArgs.
	//
	// The kind can be given positionally (`executors setup omp`) or via --kind;
	// `idea executors setup omp --yes` used to silently ignore the positional
	// and default to hermes, because only a.value("--kind") was read. Both are
	// accepted here, and a disagreement between them is refused rather than
	// picking one silently.
	positionalKind, flagKind := a.arg(0), a.value("--kind")
	kind := positionalKind
	if flagKind != "" {
		if positionalKind != "" && positionalKind != flagKind {
			return usageErrorf("conflicting executor kind: positional argument %q and --kind %q disagree", positionalKind, flagKind)
		}
		kind = flagKind
	}
	if kind == "" {
		kind = "hermes"
	}
	image, model := a.value("--image"), a.value("--model")
	yes, smoke := a.has("--yes"), a.has("--smoke")
	if kind != "hermes" && kind != "omp" && kind != "acp" {
		return usageErrorf("unsupported executor kind %q (use hermes, omp or acp)", kind)
	}
	acpCommand, err := parseACPCommand(a.value("--acp-command"))
	if err != nil {
		return err
	}
	acpKeyEnv := a.value("--acp-key-env")
	if kind != "acp" && (len(acpCommand) > 0 || acpKeyEnv != "") {
		return usageErrorf("--acp-command and --acp-key-env only apply to --kind acp")
	}
	if kind == "acp" && len(acpCommand) == 0 {
		return usageErrorf("--acp-command is required for --kind acp: the agent's argv inside the image, e.g. --acp-command claude-code-acp " +
			"or --acp-command \"gemini --experimental-acp\"")
	}
	if smoke && model == "" {
		return usageErrorf("--smoke requires --model (to resolve the provider API key)")
	}
	// Resolve the image: Hermes has a pinned default (auto-downloaded); OMP has
	// no canonical image so --image is required.
	if image == "" {
		image = executor.DefaultAgentImage(kind)
		if image == "" {
			return usageErrorf("--image is required: no canonical %s image (provide one you trust)", kind)
		}
		fmt.Fprintln(stderr, muted("no --image; using pinned default ")+code(image)+muted(" and downloading it now"))
	}

	fmt.Fprintln(stderr, warn("⚠ security tradeoff:")+" a "+kind+" executor runs with network egress and auto-approve, and receives its provider API key. Only use an image you trust.")
	if !yes {
		return usageErrorf("pass --yes to accept the security tradeoff and configure executor.kind=%s", kind)
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	// Checked before the pull, which is several gigabytes. A misspelled or
	// unsupported provider used to download the image, verify it, write the
	// config and only then fail -- or, with --smoke, surface as an agent that
	// "produced no files".
	var provider executor.AgentProvider
	if model != "" {
		if provider, err = checkAgentProvider(c, projectID, model, stderr); err != nil {
			return err
		}
		// Known-incompatible and untested combinations (docs/agents.md, "Known
		// executor/model compatibility"): also checked before the pull. A known
		// bad pairing (§ ICP walkthrough: hermes + gpt-4.1-mini failed every
		// time) refuses outright; an untested one warns and points at --smoke,
		// same as the model-warning above.
		if compat := executor.CheckModelCompat(kind, model); compat.Message != "" {
			if compat.Blocked {
				return errorf(kindConfigInvalid, "%s", compat.Message)
			}
			fmt.Fprintln(stderr, warn("⚠ ")+compat.Message)
		}
	}

	digest, err := executor.PullImage(context.Background(), image)
	if err != nil {
		return fmt.Errorf("pull failed: %w", err)
	}
	if kind == "acp" {
		// An ACP adapter need not implement --version, so the presence check
		// is the protocol itself: initialize, in the hardened container with
		// no network and no key.
		pctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		agent, err := executor.ProbeACP(pctx, image, acpCommand)
		cancel()
		if err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}
		fmt.Fprintln(stderr, muted("ACP handshake ok: ")+code(agent))
	} else if err := executor.VerifyAgentCLI(context.Background(), image, kind); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	body := map[string]any{"kind": kind, "image": image}
	if model != "" {
		body["model"] = model
	}
	if kind == "acp" {
		body["acp_command"] = acpCommand
		if acpKeyEnv != "" {
			body["acp_key_env"] = acpKeyEnv
		}
	}
	var out struct {
		Warnings []string `json:"warnings"`
	}
	if err := c.do("PUT", "/api/v1/projects/"+projectID+"/executors", body, &out); err != nil {
		return err
	}
	// Printed before the checkmark: a dead model id here is forwarded into the
	// agent container, where the 404 shows up as an agent that produced nothing.
	for _, w := range out.Warnings {
		fmt.Fprintln(stdout, warn("⚠ ")+w)
	}
	fmt.Fprintln(stdout, ok("✓ executor configured")+" "+code(kind)+" ("+code(image)+")")
	if digest != "" {
		fmt.Fprintln(stdout, muted("image digest: ")+code(digest))
	}
	if smoke {
		fmt.Fprintln(stdout, muted("smoke: running a trivial "+kind+" task to verify the CLI contract…"))
		// Ctrl-C cancels the context rather than killing the process, so the
		// smoke's deferred Cancel still removes the agent container, which
		// holds the key. Bounded like a real run.
		sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		sctx, cancel := context.WithTimeout(sctx, 15*time.Minute)
		defer cancel()
		dotEnv := config.LoadDotEnv(".")
		lookup := func(name string) string {
			if v := os.Getenv(name); v != "" {
				return v
			}
			return dotEnv[name]
		}
		var serr error
		if kind == "acp" {
			opts := executor.ACPOptions{Image: image, Model: model, Command: acpCommand, KeyEnv: acpKeyEnv}
			serr = executor.SmokeACPAgent(sctx, opts, provider, lookup, "")
		} else {
			serr = executor.SmokeAgent(sctx, kind, image, model, provider, lookup, "")
		}
		if serr != nil {
			return errorf(kindRunFailed, "smoke failed: %w", serr)
		}
		fmt.Fprintln(stdout, ok("✓ smoke passed")+" — the "+kind+" CLI produced a real file change")
	}
	fmt.Fprintln(stdout, muted("run ")+bold("idea execute")+muted(" to produce a real change set"))
	return nil
}

// parseACPCommand reads --acp-command: a JSON array (`["gemini",
// "--experimental-acp"]`) when an argument itself contains a space, otherwise
// whitespace-separated words. No shell quoting is interpreted -- this argv
// runs inside the container, and guessing at a shell's rules here would be a
// second, subtly different shell.
func parseACPCommand(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "[") {
		var argv []string
		if err := json.Unmarshal([]byte(s), &argv); err != nil {
			return nil, usageErrorf("--acp-command looks like a JSON array but is not one: %w", err)
		}
		if len(argv) == 0 || argv[0] == "" {
			return nil, usageErrorf("--acp-command is an empty array")
		}
		return argv, nil
	}
	return strings.Fields(s), nil
}

// printExecutorResolution prints what POST .../execute said about the
// executor: the fallback warning loudly (this run writes no real code), or
// the quiet placeholder note for an explicit `kind: internal`.
func printExecutorResolution(out map[string]string, stdout io.Writer) {
	if w := out["executor_warning"]; w != "" {
		fmt.Fprintln(stdout, errText("⚠ "+w))
		return
	}
	resolved := out["executor_resolved"]
	if resolved == "" {
		// A daemon from before auto existed reports only the requested kind.
		resolved = out["executor_kind"]
	}
	if resolved == "internal" || resolved == "" {
		fmt.Fprintln(stdout, warn("⚠ the internal executor writes a placeholder (not real code); remove executor.kind from .idea-harness/harness.yaml (auto) or set it to hermes|omp|acp to generate real code."))
	}
}

// cliCoverage mirrors the per-requirement verification coverage.
type cliCoverage struct {
	Code        string `json:"code"`
	Pass        int    `json:"pass"`
	Total       int    `json:"total"`
	NeedsManual int    `json:"needs_manual"`
	Status      string `json:"status"`
}

// cmdVerify records human pass evidence for a requirement and/or runs the
// verification gate (§70, review B4/B10). Usage: idea verify [<req>] [--pass] [--note <text>].
func cmdVerify(a cmdArgs, stdout, stderr io.Writer) error {
	// Invoked directly, an unmet gate is a failure, so that a script branching on
	// this command's exit code learns something from it. (The quickstart shows
	// the command but not that idiom -- an earlier version of this comment said
	// it did.)
	return runVerify(a, stdout, stderr, true)
}

// runVerify runs the verification gate. failOnUnmet distinguishes the two
// callers: `idea verify` reports an unmet gate as an error, while the
// `idea next --until-human` loop treats it as the human judgment point it is and
// stops cleanly.
func runVerify(a cmdArgs, stdout, stderr io.Writer, failOnUnmet bool) error {
	req := a.arg(0)
	pass, fail, waive := a.has("--pass"), a.has("--fail"), a.has("--waive")
	note := a.value("--note")
	confirmPassingTests := a.has("--confirm-passing-tests")

	// Validated before connect(), because the failure here is silence rather
	// than an error: `idea verify --pass --note "checked manually"` with no
	// requirement parsed the flags, discarded them, ran the gate and printed
	// "✓ verified" with exit 0 -- so a user believed they had signed off and no
	// evidence existed anywhere (DX audit).
	dispositions := 0
	for _, set := range []bool{pass, fail, waive} {
		if set {
			dispositions++
		}
	}
	if dispositions > 1 {
		return usageErrorf("--pass, --fail and --waive are alternatives; give exactly one")
	}
	if req == "" && (dispositions > 0 || note != "") {
		return usageErrorf("recording evidence needs the requirement it is about: idea verify <req> --pass|--fail|--waive [--note <text>]")
	}
	if len(a.positional) > 1 {
		return usageErrorf("usage: idea verify [<req>] [--pass|--fail|--waive] [--note <text>]")
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}
	body := map[string]any{}
	switch {
	case req != "" && fail:
		body = map[string]any{"requirement": req, "fail": true, "note": note}
	case req != "" && waive:
		body = map[string]any{"requirement": req, "waived": true, "note": note}
	case req != "" && pass:
		body = map[string]any{"requirement": req, "pass": true, "note": note}
	case req != "":
		// The read path used to fall through to an empty body, silently
		// discarding the argument: `idea verify REQ-99` on a project holding only
		// REQ-1 reported `REQ-1 0/1 verified` and never mentioned REQ-99, exiting
		// on the whole-initiative gate. The write path was already correct --
		// `idea verify REQ-99 --pass` exits 1 with `no requirement matching
		// "REQ-99"` -- so the validation existed and was applied to the opposite
		// case from the one that was reported. Sending the name is what lets the
		// daemon resolve it and answer about the requirement that was asked for.
		body = map[string]any{"requirement": req}
	}
	if confirmPassingTests {
		// C2, wire contract: confirms every requirement whose mapped tests all
		// passed, through the same human-evidence path, actor human. Combines
		// cleanly with a single --pass/--fail/--waive above -- both run in the
		// same gate call.
		body["confirm_passing_tests"] = true
	}
	// Quality dimensions (verify.dimensions) are measured BEFORE the gate
	// runs, so the gate reads a scorecard for the work as it is now. Only
	// when something enabled is unmeasured or stale; --no-measure keeps the
	// last recorded scorecard (a stale gated dimension then stays unmet).
	if !a.has("--no-measure") {
		progress := stdout
		if a.has("--json") {
			progress = stderr // stdout is the JSON document
		}
		if err := measureQualityIfStale(c, projectID, iniID, progress); err != nil {
			return err
		}
	}
	var out struct {
		Status   string        `json:"status"`
		Coverage []cliCoverage `json:"coverage"`
		Quality  *cliScorecard `json:"quality,omitempty"`
	}
	if err := c.do("POST", initiativeResource(projectID, iniID, "verify"), body, &out); err != nil {
		return err
	}
	// Headline from the gate, not from the initiative's status. This said
	// "✓ verified" even for a recorded failure.
	//
	// Computed BEFORE the --json branch below (H10 fix): `idea verify --json`
	// used to `return dumpJSON(...)` here, before `unmet` even existed, so the
	// only machine-readable form of this command always exited 0 -- silently
	// contradicting the exact "an unmet gate exits non-zero, so idea verify is
	// usable in a script" promise docs/demo-greenfield.md makes for this
	// command, script-usability being the entire point of --json.
	var unmet []string
	for _, cov := range out.Coverage {
		switch cov.Status {
		case "failed":
			unmet = append(unmet, cov.Code+" failed")
		case "waived":
			// A waiver is a human decision that the requirement is out of scope,
			// so it satisfies the gate.
		default:
			if cov.Pass < cov.Total {
				unmet = append(unmet, fmt.Sprintf("%s %d/%d verified", cov.Code, cov.Pass, cov.Total))
			}
		}
	}
	// Only GATED quality dimensions count; the daemon has already applied the
	// policy, so gate_unmet is exactly that list.
	if out.Quality != nil {
		for _, u := range out.Quality.GateUnmet {
			unmet = append(unmet, "quality: "+u)
		}
	}
	if a.has("--json") {
		// {status, coverage, quality} -- and the exit code applies the same
		// gate the human-readable path applies below.
		if err := dumpJSON(stdout, out); err != nil {
			return err
		}
		if failOnUnmet && len(unmet) > 0 {
			return errorf(kindGateUnmet, "verification incomplete: %s", strings.Join(unmet, "; "))
		}
		return nil
	}
	if req != "" && (pass || fail || waive) {
		fmt.Fprintln(stdout, ok("✓ evidence recorded for")+" "+code(req))
	}
	if len(unmet) == 0 {
		fmt.Fprintln(stdout, ok("✓ verified")+" "+muted("status:")+" "+code(out.Status))
	} else {
		fmt.Fprintln(stdout, warn("● not verified")+" "+muted("status:")+" "+code(out.Status))
	}
	for _, cov := range out.Coverage {
		fmt.Fprintf(stdout, "  %s  %d/%d verified", code(cov.Code), cov.Pass, cov.Total)
		switch cov.Status {
		case "waived":
			fmt.Fprint(stdout, "  "+muted("(waived)"))
		case "failed":
			fmt.Fprint(stdout, "  "+muted("(failed)"))
		default:
			if cov.NeedsManual > 0 {
				fmt.Fprintf(stdout, "  "+muted("(%d need manual sign-off)"), cov.NeedsManual)
			}
		}
		fmt.Fprintln(stdout)
	}
	renderScorecard(stdout, out.Quality)

	// The gate has to be able to fail.
	//
	// This printed "✓ verified" and returned nil for every state, including a
	// recorded failure -- `idea verify REQ-1 --fail` reported success -- because
	// out.Status is the *initiative's* status, not the gate's. So the exit code
	// was 0 whatever the gate said, and any script branching on it saw success
	// unconditionally. The previous round fixed this command's recording half
	// and never looked at its reporting half.
	if failOnUnmet && len(unmet) > 0 {
		return errorf(kindGateUnmet, "verification incomplete: %s", strings.Join(unmet, "; "))
	}
	return nil
}

// cmdRetry re-runs a failed initiative, optionally on a different model.
//
// The command existed as a route and a Runtime method but never as a CLI verb,
// while four separate comments in internal/daemon — added by the audit-4
// remediation — told the reader that "`idea retry` runs from exactly there" as
// the recovery for a stranded or failed run. Audit 5 found the recovery story
// pointing at a command that did not exist.
func cmdRetry(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}

	// The endpoint requires a model. Defaulting to the configured one is what
	// makes `idea retry` usable as the bare recovery command the comments
	// promise; --model overrides it for the "same failure, different model"
	// case, which is the other reason to retry.
	model := a.value("--model")
	if model == "" {
		var view cliProvidersView
		if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/providers", nil, &view); err != nil {
			return err
		}
		model = view.DefaultModel
	}
	if model == "" {
		return errorf(kindConfigInvalid, "no model configured; set one with `idea model set <provider>/<model>` or pass --model")
	}

	// D-a, wave 3 contract: an optional note steering this retry, joining the
	// framing prompt that restarts the pipeline and recorded on that run.
	body := map[string]string{"model": model}
	if note := a.value("--note"); note != "" {
		body["note"] = note
	}
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, "retry"), body, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ retry started")+" "+code(iniID)+" "+muted("on")+" "+accent(model))
	opID := out["operation_id"]
	if opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	}

	// Wait, for the reason `idea new` waits. retry posts and returned, so a
	// retry against an unchanged cause -- the same 401, the same unreachable
	// base_url -- printed `✓ retry started` and exited 0 while the operation
	// failed half a second later. The tick said the opposite of what happened,
	// on the command whose entire job is recovering from a failure.
	if a.has("--no-wait") || opID == "" {
		fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
		return nil
	}
	fmt.Fprintln(stdout, muted("working… (")+bold("--no-wait")+muted(" returns immediately; ")+bold("idea watch")+muted(" follows it)"))
	if err := waitForOperation(c, projectID, iniID, opID, stdout); err != nil {
		return err
	}
	return reportRetryOutcome(c, projectID, iniID, stdout)
}

// reportRetryOutcome names the state the retry actually reached, rather than
// reporting the start as the result.
func reportRetryOutcome(c *client, projectID, iniID string, stdout io.Writer) error {
	step, nerr := nextAction(c, projectID, iniID)
	if nerr != nil {
		// Not `return nil`. The retry ran and its outcome is unknown, which is
		// the same "reported success without checking" this command was changed
		// to stop doing -- one layer up.
		return fmt.Errorf("the retry finished but its outcome could not be read: %w\n"+
			"  `idea status` shows where it is", nerr)
	}
	// renderNextAction already turns a `failed` action into failedInitiativeError.
	return renderNextAction(step, stdout)
}

// checkAgentProvider confirms a coding-agent model names a provider this
// project can reach: one of the presets, or one configured under providers: in
// harness.yaml. The message lists them, because the valid set is short and the
// user is choosing from it.
//
// It returns the provider as the daemon resolved it -- endpoint and key
// variable after harness.yaml overrides -- so --smoke calls what the real
// executor will, custom providers included.
func checkAgentProvider(c *client, projectID, model string, stderr io.Writer) (executor.AgentProvider, error) {
	i := strings.Index(model, "/")
	if i <= 0 || i == len(model)-1 {
		return executor.AgentProvider{}, usageErrorf("--model %q must be provider/model, e.g. openai/gpt-5.6-luna or ollama/qwen3.8", model)
	}
	name := model[:i]
	var view cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/providers", nil, &view); err != nil {
		return executor.AgentProvider{}, err
	}
	names := make([]string, 0, len(view.Providers))
	for _, p := range view.Providers {
		if p.Name == name {
			if p.Problem != "" {
				return executor.AgentProvider{}, errorf(kindConfigInvalid, "provider %q is not usable: %s", name, p.Problem)
			}
			ap := executor.AgentProvider{Name: p.Name, Type: config.ProviderType(p.Type), KeyEnv: p.KeyEnv, BaseURL: p.BaseURL}
			if err := ap.Validate(); err != nil {
				return executor.AgentProvider{}, err
			}
			// models.implementer outranks executor.model when the engine picks
			// the agent's model, so the model set here -- and smoked -- would
			// not be the one that runs.
			if impl := view.Models["implementer"]; impl != "" && impl != model {
				fmt.Fprintln(stderr, warn("⚠ ")+"models.implementer is "+code(impl)+
					", which the coding agent uses instead of "+code(model)+
					". To run "+code(model)+", also run "+code("idea model set --role implementer "+model)+".")
			}
			return ap, nil
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return executor.AgentProvider{}, usageErrorf("unknown provider %q in --model; use one of: %s", name, strings.Join(names, ", "))
}
