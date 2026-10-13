package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/daemon"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"gopkg.in/yaml.v3"
)

// Policy is the automatic judgment policy: what the eval does where the
// workflow stops for a human.
type Policy string

const (
	// PolicyApprove accepts, at every stop, what a human accepting the
	// work as it stands would: `idea approve`, `idea approve-plan`,
	// `idea review --accept-findings`, `idea revise-plan` for an unresolved
	// dependency. Each is recorded as a human-judgment stop, and the run
	// goes on, so everything downstream is still measured.
	PolicyApprove Policy = "approve"
	// PolicyStop ends the case at its first human-judgment stop.
	PolicyStop Policy = "stop"
)

// Options configures a run.
type Options struct {
	Suite Suite
	// Model, when set ("provider/model"), runs every case against that real
	// model instead of the scripted provider. The provider's key must be in
	// the environment (or the case project's .env) -- the in-process daemon
	// reads it exactly as `idea daemon` would.
	Model string
	// Harness is extra harness.yaml appended to every case's project (after
	// the case's own), e.g. an executor block for a real coding agent.
	Harness string
	Policy  Policy
	// WorkDir holds every case's state root and project directory. Empty is
	// a fresh temporary directory, removed afterwards unless Keep is set.
	WorkDir string
	Keep    bool
	// CaseTimeout bounds one case end to end (default 20 minutes; a scripted
	// case takes well under a second).
	CaseTimeout time.Duration
	// CheckTimeout bounds one acceptance check (default 2 minutes).
	CheckTimeout time.Duration
	// Log receives daemon logs; nil discards them.
	Log *slog.Logger
	// Progress, when set, is told as each case starts and finishes.
	Progress func(c Case, r *CaseResult)
}

// Run runs every case of the suite and returns the report. A case that fails
// is recorded as failed and the run continues; Run itself errors only when it
// cannot run at all.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Policy == "" {
		opts.Policy = PolicyApprove
	}
	if opts.Policy != PolicyApprove && opts.Policy != PolicyStop {
		return nil, fmt.Errorf("eval: unknown policy %q (approve|stop)", opts.Policy)
	}
	if opts.Model != "" {
		p, m := config.ParseModelSpec(opts.Model)
		if p == "" || m == "" {
			return nil, fmt.Errorf("eval: --model must be provider/model, got %q", opts.Model)
		}
		// Refused up front rather than discovered seven times: a missing key
		// makes the first model call of every case a non-retryable 401, so
		// the whole run would be seven identical failures, each billed for
		// whatever preceded it. A custom provider (configured through
		// Harness) is not a preset and is not checked here.
		if pre := config.PresetFor(p); pre != nil && pre.NeedsKey && os.Getenv(pre.DefaultKeyEnv) == "" {
			return nil, fmt.Errorf("eval: --model %s needs %s in the environment", opts.Model, pre.DefaultKeyEnv)
		}
	}
	if opts.CaseTimeout <= 0 {
		opts.CaseTimeout = 20 * time.Minute
	}
	if opts.CheckTimeout <= 0 {
		opts.CheckTimeout = 2 * time.Minute
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	work := opts.WorkDir
	if work == "" {
		dir, err := os.MkdirTemp("", "idea-eval-")
		if err != nil {
			return nil, err
		}
		work = dir
		if !opts.Keep {
			defer os.RemoveAll(dir)
		}
	}

	rep := &Report{Suite: opts.Suite.Name, Model: opts.Model, Policy: opts.Policy, StartedAt: time.Now().UTC()}
	if opts.Model == "" {
		rep.Provider = "scripted"
	} else {
		rep.Provider = "real"
	}
	for _, c := range opts.Suite.Cases {
		if opts.Progress != nil {
			opts.Progress(c, nil)
		}
		res := runCase(ctx, opts, c, filepath.Join(work, c.Name))
		if opts.Model == "" && c.Expect != nil {
			res.ExpectationFailures = checkExpect(*c.Expect, res)
		}
		rep.Cases = append(rep.Cases, res)
		if opts.Progress != nil {
			opts.Progress(c, &rep.Cases[len(rep.Cases)-1])
		}
		if ctx.Err() != nil {
			break
		}
	}
	rep.FinishedAt = time.Now().UTC()
	rep.Aggregate = aggregate(rep.Cases)
	return rep, nil
}

// runCase runs one case on its own in-process daemon: its own state root,
// its own project directory, and (scripted) its own provider, so no case can
// see another's queue, calls or files.
func runCase(parent context.Context, opts Options, c Case, dir string) CaseResult {
	start := time.Now()
	res := CaseResult{Name: c.Name, Scenario: c.Scenario, Reached: StageNone}
	if opts.Model != "" {
		res.Scenario = ""
	}
	fail := func(format string, args ...any) CaseResult {
		res.Error = fmt.Sprintf(format, args...)
		res.WallSeconds = time.Since(start).Seconds()
		return res
	}

	ctx, cancel := context.WithTimeout(parent, opts.CaseTimeout)
	defer cancel()

	stateRoot, projDir := filepath.Join(dir, "state"), filepath.Join(dir, "project")
	for _, d := range []string{stateRoot, projDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail("%v", err)
		}
	}
	rt, err := daemon.NewRuntime(stateRoot, opts.Log)
	if err != nil {
		return fail("start daemon: %v", err)
	}
	defer rt.Close()
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		_ = rt.Shutdown(sctx)
	}()
	if opts.Model == "" {
		rt.RegisterProvider(ProviderName, NewScriptedProvider(c.Scenario))
	}
	srv := httptest.NewServer(daemon.NewServer(rt).Handler())
	defer srv.Close()
	cl := &client{base: srv.URL + "/api/v1", token: rt.Token(), hc: srv.Client()}

	var created struct {
		ProjectID string `json:"project_id"`
	}
	if err := cl.do(ctx, "POST", "/projects", map[string]string{"root_path": projDir}, &created); err != nil {
		return fail("create project: %v", err)
	}
	model := opts.Model
	if model == "" {
		model = ProviderName + "/" + nonEmpty(c.Scenario, ScenarioDemo)
	}
	if err := mergeHarness(filepath.Join(projDir, config.Dir, "harness.yaml"), "default_model: "+model, c.Harness, opts.Harness); err != nil {
		return fail("configure project: %v", err)
	}
	// A reload, not a restart: the config is read once per project open,
	// and the provider override is applied to the rebuilt registry.
	if _, err := rt.ReloadProject(ctx, created.ProjectID); err != nil {
		return fail("load case config: %v", err)
	}

	d := &driver{opts: opts, c: c, cl: cl, ctx: ctx, res: &res, projDir: projDir, project: "/projects/" + created.ProjectID}
	d.run()

	stats, tel, err := rt.InitiativeRunStats(context.Background(), created.ProjectID, d.initiative)
	if err == nil && d.initiative != "" {
		res.Calls, res.InputTokens, res.OutputTokens = stats.Calls, stats.InputTokens, stats.OutputTokens
		res.CostUSD = stats.CostUSD
		res.Telemetry = tel
		summarizeTelemetry(&res, tel)
	}
	var costs domain.InitiativeCosts
	if d.initiative != "" && cl.do(context.Background(), "GET", d.project+"/initiatives/"+d.initiative+"/costs", nil, &costs) == nil && costs.TotalUSD > res.CostUSD {
		res.CostUSD = costs.TotalUSD
	}
	res.WallSeconds = time.Since(start).Seconds()
	return res
}

// driver walks one initiative from idea to complete through the daemon's own
// "what is next" answer (GET .../next -- the token `idea next` acts on), with
// the judgment policy standing in for the human at each stop.
type driver struct {
	opts       Options
	c          Case
	cl         *client
	ctx        context.Context
	res        *CaseResult
	projDir    string
	project    string
	initiative string
	executor   string
}

func (d *driver) ini() string { return d.project + "/initiatives/" + d.initiative }

func (d *driver) run() {
	var created struct {
		ID string `json:"initiative_id"`
		Op string `json:"operation_id"`
	}
	title := nonEmpty(d.c.Title, d.c.Name)
	if err := d.cl.do(d.ctx, "POST", d.project+"/initiatives", map[string]string{"title": title, "description": strings.TrimSpace(d.c.Idea)}, &created); err != nil {
		d.res.Error = "create initiative: " + err.Error()
		return
	}
	d.initiative = created.ID
	if err := d.cl.wait(d.ctx, created.Op); err != nil {
		d.res.Error = "PRD: " + err.Error()
		return
	}
	d.res.reach(StagePRD)

	// The loop is bounded: every action either moves the initiative forward
	// or records a stop, so a run that has not finished in this many steps
	// is itself the finding.
	for step := 0; step < 80; step++ {
		var next struct {
			Action        string `json:"action"`
			FailureReason string `json:"failure_reason"`
		}
		if err := d.cl.do(d.ctx, "GET", d.ini()+"/next", nil, &next); err != nil {
			d.res.Error = "next: " + err.Error()
			return
		}
		action, arg, _ := strings.Cut(next.Action, " ")
		var err error
		switch action {
		case "done":
			d.res.reach(StageComplete)
			return
		case "failed":
			d.res.Error = "initiative failed: " + next.FailureReason
			return
		case "wait":
			err = sleep(d.ctx, 50*time.Millisecond)
		case "advance":
			d.res.reach(StagePRDApproved)
			err = d.post("/advance", nil)
		case "execute":
			d.res.reach(StagePlanApproved)
			err = d.execute()
		case "review":
			d.res.reach(StageChangeSet)
			err = d.review(arg)
		case "apply":
			err = d.postAt(d.project+"/change-sets/"+arg+"/apply", nil)
			if err == nil {
				d.res.AppliedChangeSets++
			}
		case "verify":
			d.res.reach(StageApplied)
			if d.verify() {
				// verify() recorded why it cannot go on (a failed check
				// under the stop policy, or a gate that will not complete).
				return
			}
		case "resolve":
			if d.resolve(arg) {
				return
			}
		default:
			d.res.Error = "unexpected next action " + strings.TrimSpace(next.Action)
			return
		}
		if errors.Is(err, errStopped) {
			return
		}
		if err != nil {
			d.res.Error = action + ": " + err.Error()
			return
		}
	}
	d.res.Error = "did not finish within 80 steps"
}

// post sends an action to the initiative and waits for the operation it
// starts, when it starts one.
func (d *driver) post(path string, body any) error { return d.postAt(d.ini()+path, body) }

// postAt is post for a project-relative path outside the initiative (a
// change set's apply).
func (d *driver) postAt(path string, body any) error {
	var out struct {
		Op string `json:"operation_id"`
	}
	if err := d.cl.do(d.ctx, "POST", path, body, &out); err != nil {
		return err
	}
	if out.Op == "" {
		return nil
	}
	return d.cl.wait(d.ctx, out.Op)
}

func (d *driver) execute() error {
	var out struct {
		Op       string `json:"operation_id"`
		Executor string `json:"executor_kind"`
		Resolved string `json:"executor_resolved"`
	}
	if err := d.cl.do(d.ctx, "POST", d.ini()+"/execute", map[string]any{}, &out); err != nil {
		return err
	}
	// executor_kind is what harness.yaml asked for, and an unset kind is
	// "auto", which can still fall back to the placeholder. What decides
	// whether the acceptance checks mean anything is the kind that actually
	// ran.
	d.executor = out.Resolved
	if d.executor == "" {
		d.executor = out.Executor
	}
	if err := d.cl.wait(d.ctx, out.Op); err != nil {
		return err
	}
	d.res.ChangeSets++
	return nil
}

// review approves a change set. A refusal is the change-set reviewer's
// blocking finding: a human stop, which the approve policy signs off.
func (d *driver) review(ref string) error {
	path := d.project + "/change-sets/" + ref + "/review"
	err := d.cl.do(d.ctx, "POST", path, map[string]any{"verdict": "approved"}, nil)
	if err == nil {
		return nil
	}
	var he *httpError
	if !errors.As(err, &he) || he.status >= 500 {
		return err
	}
	if d.stop(StopChangeSetReview, he.msg) {
		return errStopped
	}
	return d.cl.do(d.ctx, "POST", path, map[string]any{"verdict": "approved", "accept_findings": true}, nil)
}

var errStopped = errors.New("stopped by policy")

// resolve handles a needs_human_judgment park. It returns true when the case
// ends here.
func (d *driver) resolve(reason string) bool {
	var stage, path string
	switch reason {
	case "prd":
		stage, path = StopPRDReview, "/approve"
	case "plan":
		stage, path = StopPlanReview, "/approve-plan"
	case "plan-dependencies":
		stage, path = StopPlanDependencies, "/revise-plan"
	default:
		d.res.Error = "parked for a human: " + nonEmpty(reason, "unknown reason")
		return true
	}
	if d.stop(stage, "") {
		return true
	}
	if err := d.post(path, nil); err != nil {
		d.res.Error = stage + ": " + err.Error()
		return true
	}
	return false
}

// stop records a human-judgment stop and reports whether the policy ends the
// case there.
func (d *driver) stop(stage, detail string) bool {
	d.res.HumanStops = append(d.res.HumanStops, Stop{Stage: stage, Detail: detail})
	return d.opts.Policy == PolicyStop
}

// verify runs the acceptance checks, records each live requirement's
// evidence from them, and runs the gate. Returns true when the case ends.
func (d *driver) verify() bool {
	if len(d.res.Checks) == 0 {
		d.runChecks()
	}
	passed := true
	for _, ch := range d.res.Checks {
		if ch.Status == CheckFailed {
			passed = false
		}
	}
	note := "idea eval: every acceptance check passed"
	switch {
	case !passed:
		note = "idea eval: an acceptance check failed"
	case d.res.ChecksSkipped:
		note = "idea eval: acceptance checks not run (placeholder executor); recorded so the gate can be exercised"
	}
	var reqs []domain.Requirement
	if err := d.cl.do(d.ctx, "GET", d.ini()+"/requirements", nil, &reqs); err != nil {
		d.res.Error = "list requirements: " + err.Error()
		return true
	}
	for _, r := range reqs {
		if r.Status != domain.RequirementApproved && r.Status != domain.RequirementFailed {
			continue
		}
		body := map[string]any{"requirement": r.Code, "note": note}
		if passed {
			body["pass"] = true
		} else {
			body["fail"] = true
		}
		if err := d.cl.do(d.ctx, "POST", d.ini()+"/verify", body, nil); err != nil {
			d.res.Error = "verify " + r.Code + ": " + err.Error()
			return true
		}
	}
	var gate struct {
		Status string `json:"status"`
	}
	if err := d.cl.do(d.ctx, "POST", d.ini()+"/verify", map[string]any{}, &gate); err != nil {
		d.res.Error = "verification gate: " + err.Error()
		return true
	}
	d.res.VerifyPassed = passed && gate.Status == string(domain.InitiativeComplete)
	if gate.Status != string(domain.InitiativeComplete) {
		if !passed {
			d.res.Error = "acceptance checks failed"
		} else {
			d.res.Error = "verification gate did not complete (status " + gate.Status + ")"
		}
		return true
	}
	d.res.reach(StageVerified)
	return false
}

// runChecks runs the case's acceptance checks in the project directory.
func (d *driver) runChecks() {
	if d.executor == "" || d.executor == string(config.ExecutorInternal) {
		d.res.ChecksSkipped = true
		for _, ch := range d.c.Checks {
			d.res.Checks = append(d.res.Checks, CheckResult{Name: ch.Name, Status: CheckSkipped})
		}
		return
	}
	for _, ch := range d.c.Checks {
		ctx, cancel := context.WithTimeout(d.ctx, d.opts.CheckTimeout)
		cmd := exec.CommandContext(ctx, "sh", "-c", ch.Run)
		cmd.Dir = d.projDir
		out, err := cmd.CombinedOutput()
		cancel()
		r := CheckResult{Name: ch.Name, Status: CheckPassed}
		if err != nil {
			r.Status = CheckFailed
			r.Output = tail(string(out), 2000)
		}
		d.res.Checks = append(d.res.Checks, r)
	}
}

// ---------- HTTP ----------

type client struct {
	base, token string
	hc          *http.Client
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.msg) }

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Idea-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &httpError{status: resp.StatusCode, msg: nonEmpty(e.Error, strings.TrimSpace(string(raw)))}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// wait polls an operation until it leaves running.
func (c *client) wait(ctx context.Context, opID string) error {
	for {
		var op domain.Operation
		if err := c.do(ctx, "GET", "/operations/"+opID, nil, &op); err != nil {
			return err
		}
		switch op.Status {
		case domain.OperationSucceeded:
			return nil
		case domain.OperationFailed, domain.OperationCancelled:
			return fmt.Errorf("operation %s %s: %s", op.Type, op.Status, op.Error)
		}
		if err := sleep(ctx, 25*time.Millisecond); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// mergeHarness deep-merges YAML overlays, in order, into the harness.yaml
// `idea init` wrote. Merged rather than appended: the generated file already
// sets default_model, review and executor, and YAML refuses a repeated key,
// so an appended `review:` block would make the project unloadable. The
// generated file's comments are lost; the case project is throwaway.
func mergeHarness(path string, overlays ...string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	for _, o := range overlays {
		if strings.TrimSpace(o) == "" {
			continue
		}
		over := map[string]any{}
		if err := yaml.Unmarshal([]byte(o), &over); err != nil {
			return fmt.Errorf("harness overlay: %w", err)
		}
		deepMerge(doc, over)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
