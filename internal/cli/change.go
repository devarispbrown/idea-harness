package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/idea-harness/idea-harness/internal/executor"
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
func cmdExecute(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/execute", nil, &out); err != nil {
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
func cmdCancel(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: idea cancel <operation-id>")
	}
	c, _, err := connect()
	if err != nil {
		return err
	}
	opID := args[0]
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
	"draft":            "draft",
	"checks_running":   "checking",
	"review_required":  "needs your approval",
	"rejected":         "rejected",
	"changes_requested": "changes requested",
	"superseded":       "superseded",
	"approved":         "approved — apply it",
	"applying":         "applying",
	"applied":          "applied",
	"reverted":         "reverted",
	"conflicted":       "conflicted",
}

func cmdChanges(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return err
	}
	var changes []cliChangeSet
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/change-sets?initiative_id="+iniID, nil, &changes); err != nil {
		return err
	}
	if wantJSON(args) {
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
func cmdChange(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: idea change <id>")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var cs cliChangeSet
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/change-sets/"+args[0], nil, &cs); err != nil {
		return err
	}
	if wantJSON(args) {
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
func cmdApply(args []string, stdout, stderr io.Writer) error {
	return runChangeSetAction("apply", args, stdout)
}

// cmdRevert rolls back a change set by id.
func cmdRevert(args []string, stdout, stderr io.Writer) error {
	return runChangeSetAction("revert", args, stdout)
}

// cmdReview applies a review verdict to a change set.
func cmdReview(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: idea review <id> <approve|reject|changes_requested>")
	}
	verdict := args[1]
	switch verdict {
	case "approve":
		verdict = "approved"
	case "reject":
		verdict = "rejected"
	case "approved", "rejected", "changes_requested":
	default:
		return fmt.Errorf("unknown verdict %q (use approve|reject|changes_requested)", args[1])
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/change-sets/"+args[0]+"/review",
		map[string]string{"verdict": verdict}, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ review recorded")+" "+code(args[0])+" → "+verdict)
	return nil
}

// runChangeSetAction POSTs to /change-sets/{id}/{action} and prints the result.
func runChangeSetAction(action string, args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: idea %s <id>", action)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/change-sets/"+args[0]+"/"+action, nil, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ "+action+" started")+" "+code(args[0]))
	if opID := out["operation_id"]; opID != "" {
		fmt.Fprintln(stdout, muted("cancel with: ")+bold("idea cancel "+opID))
	}
	return nil
}

// cmdRisk prints the deterministic risk profile for an initiative.
func cmdRisk(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return err
	}
	var profile cliRiskProfile
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/risk?initiative_id="+iniID, nil, &profile); err != nil {
		return err
	}
	if wantJSON(args) {
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
func cmdExecutors(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "setup" {
		return cmdExecutorSetup(args[1:], stdout, stderr)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var executors []cliExecutor
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/executors", nil, &executors); err != nil {
		return err
	}
	if wantJSON(args) {
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
func cmdExecutorSetup(args []string, stdout, stderr io.Writer) error {
	kind := "hermes"
	image := ""
	model := ""
	yes := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			if i+1 >= len(args) {
				return fmt.Errorf("--kind requires a value")
			}
			kind = args[i+1]
			i++
		case "--image":
			if i+1 >= len(args) {
				return fmt.Errorf("--image requires a value")
			}
			image = args[i+1]
			i++
		case "--model":
			if i+1 >= len(args) {
				return fmt.Errorf("--model requires a value")
			}
			model = args[i+1]
			i++
		case "--yes":
			yes = true
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if kind != "hermes" && kind != "omp" {
		return fmt.Errorf("unsupported executor kind %q (use hermes or omp)", kind)
	}
	if image == "" {
		return fmt.Errorf("--image is required (the container image containing the %s CLI)", kind)
	}

	fmt.Fprintln(stderr, warn("⚠ security tradeoff:")+" a "+kind+" executor runs with network egress and auto-approve, and receives its provider API key. Only use an image you trust.")
	if !yes {
		return fmt.Errorf("pass --yes to accept the security tradeoff and configure executor.kind=%s", kind)
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
	fmt.Fprintln(stdout, muted("run ")+bold("idea execute")+muted(" to produce a real change set"))
	return nil
}

// cliCoverage mirrors the per-requirement verification coverage.
type cliCoverage struct {
	Code        string `json:"code"`
	Pass        int    `json:"pass"`
	Total       int    `json:"total"`
	NeedsManual int    `json:"needs_manual"`
}

// cmdVerify records human pass evidence for a requirement and/or runs the
// verification gate (§70, review B4/B10). Usage: idea verify [<req>] [--pass] [--note <text>].
func cmdVerify(args []string, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	req := ""
	pass := false
	note := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pass":
			pass = true
		case "--note":
			if i+1 < len(args) {
				note = args[i+1]
				i++
			}
		default:
			if req == "" && !strings.HasPrefix(args[i], "-") {
				req = args[i]
			}
		}
	}
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if req != "" && pass {
		body = map[string]any{"requirement": req, "pass": true, "note": note}
	}
	var out struct {
		Status   string        `json:"status"`
		Coverage []cliCoverage `json:"coverage"`
	}
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/verify", body, &out); err != nil {
		return err
	}
	if req != "" && pass {
		fmt.Fprintln(stdout, ok("✓ evidence recorded for")+" "+code(req))
	}
	fmt.Fprintln(stdout, ok("✓ verified")+" "+muted("status:")+" "+code(out.Status))
	for _, cov := range out.Coverage {
		fmt.Fprintf(stdout, "  %s  %d/%d verified", code(cov.Code), cov.Pass, cov.Total)
		if cov.NeedsManual > 0 {
			fmt.Fprintf(stdout, "  "+muted("(%d need manual sign-off)"), cov.NeedsManual)
		}
		fmt.Fprintln(stdout)
	}
	return nil
}
