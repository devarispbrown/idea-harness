package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/devarispbrown/idea-harness/internal/executor"
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
	var out map[string]string
	if err := c.do("POST", initiativeResource(projectID, iniID, "execute"), nil, &out); err != nil {
		return err
	}
	if kind := out["executor_kind"]; kind == "internal" || kind == "" {
		fmt.Fprintln(stdout, warn("⚠ the internal executor writes a placeholder (not real code); set executor.kind = omp|hermes in .idea-harness/harness.yaml to generate real code."))
	}
	fmt.Fprintln(stdout, ok("✓ execute started")+" "+code(iniID))
	if opID := out["operation_id"]; opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	}
	fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
	return nil
}

// cmdCancel interrupts a running asynchronous operation by id (§16, M3).
func cmdCancel(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	if len(positional) < 1 {
		return fmt.Errorf("usage: idea cancel <operation-id>")
	}
	c, _, err := connect()
	if err != nil {
		return err
	}
	opID := positional[0]
	var out map[string]string
	if err := c.do("POST", "/api/v1/operations/"+opID+"/cancel", nil, &out); err != nil {
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
		label := humanStatus[cs.Status]
		if label == "" {
			label = cs.Status
		}
		fmt.Fprintf(stdout, "  %s  %s\n", code(cs.HumanID), muted(label))
	}
	return nil
}

// cmdChange shows a single change set by id.
func cmdChange(a cmdArgs, stdout, stderr io.Writer) error {
	positional, flags := a.positional, a.flags
	if len(positional) < 1 {
		return fmt.Errorf("usage: idea change <id>")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var cs cliChangeSet
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/change-sets/"+positional[0], nil, &cs); err != nil {
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

// cmdReview applies a review verdict to a change set.
func cmdReview(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	if len(positional) < 2 {
		return fmt.Errorf("usage: idea review <id> <approve|reject|changes_requested>")
	}
	verdict := positional[1]
	switch verdict {
	case "approve":
		verdict = "approved"
	case "reject":
		verdict = "rejected"
	case "approved", "rejected", "changes_requested":
	default:
		return fmt.Errorf("unknown verdict %q (use approve|reject|changes_requested)", positional[1])
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/change-sets/"+positional[0]+"/review",
		map[string]string{"verdict": verdict}, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ review recorded")+" "+code(positional[0])+" → "+verdict)
	return nil
}

// runChangeSetAction POSTs to /change-sets/{id}/{action} and prints the result.
func runChangeSetAction(action string, a cmdArgs, stdout io.Writer) error {
	positional := a.positional
	if len(positional) < 1 {
		return fmt.Errorf("usage: idea %s <id>", action)
	}
	id := positional[0]
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/change-sets/"+id+"/"+action, nil, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ "+action+" started")+" "+code(id))
	if opID := out["operation_id"]; opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
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
	kind := a.value("--kind")
	if kind == "" {
		kind = "hermes"
	}
	image, model := a.value("--image"), a.value("--model")
	yes, smoke := a.has("--yes"), a.has("--smoke")
	if kind != "hermes" && kind != "omp" {
		return fmt.Errorf("unsupported executor kind %q (use hermes or omp)", kind)
	}
	if smoke && model == "" {
		return fmt.Errorf("--smoke requires --model (to resolve the provider API key)")
	}
	// Resolve the image: Hermes has a pinned default (auto-downloaded); OMP has
	// no canonical image so --image is required.
	if image == "" {
		image = executor.DefaultAgentImage(kind)
		if image == "" {
			return fmt.Errorf("--image is required: no canonical %s image (provide one you trust)", kind)
		}
		fmt.Fprintln(stderr, muted("no --image; using pinned default ")+code(image)+muted(" and downloading it now"))
	}

	fmt.Fprintln(stderr, warn("⚠ security tradeoff:")+" a "+kind+" executor runs with network egress and auto-approve, and receives its provider API key. Only use an image you trust.")
	if !yes {
		return fmt.Errorf("pass --yes to accept the security tradeoff and configure executor.kind=%s", kind)
	}

	digest, err := executor.PullImage(context.Background(), image)
	if err != nil {
		return fmt.Errorf("pull failed: %w", err)
	}
	if err := executor.VerifyAgentCLI(context.Background(), image, kind); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	body := map[string]string{"kind": kind, "image": image}
	if model != "" {
		body["model"] = model
	}
	var out map[string]string
	if err := c.do("PUT", "/api/v1/projects/"+projectID+"/executors", body, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ executor configured")+" "+code(kind)+" ("+code(image)+")")
	if digest != "" {
		fmt.Fprintln(stdout, muted("image digest: ")+code(digest))
	}
	if smoke {
		fmt.Fprintln(stdout, muted("smoke: running a trivial "+kind+" task to verify the CLI contract…"))
		if err := executor.SmokeAgent(context.Background(), kind, image, model); err != nil {
			return fmt.Errorf("smoke failed: %w", err)
		}
		fmt.Fprintln(stdout, ok("✓ smoke passed")+" — the "+kind+" CLI produced a real file change")
	}
	fmt.Fprintln(stdout, muted("run ")+bold("idea execute")+muted(" to produce a real change set"))
	return nil
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
	req := a.arg(0)
	pass, fail, waive := a.has("--pass"), a.has("--fail"), a.has("--waive")
	note := a.value("--note")

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
		return fmt.Errorf("--pass, --fail and --waive are alternatives; give exactly one")
	}
	if req == "" && (dispositions > 0 || note != "") {
		return fmt.Errorf("recording evidence needs the requirement it is about: idea verify <req> --pass|--fail|--waive [--note <text>]")
	}
	if len(a.positional) > 1 {
		return fmt.Errorf("usage: idea verify [<req>] [--pass|--fail|--waive] [--note <text>]")
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
	}
	var out struct {
		Status   string        `json:"status"`
		Coverage []cliCoverage `json:"coverage"`
	}
	if err := c.do("POST", initiativeResource(projectID, iniID, "verify"), body, &out); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, out)
	}
	if req != "" && (pass || fail || waive) {
		fmt.Fprintln(stdout, ok("✓ evidence recorded for")+" "+code(req))
	}
	fmt.Fprintln(stdout, ok("✓ verified")+" "+muted("status:")+" "+code(out.Status))
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
	return nil
}
