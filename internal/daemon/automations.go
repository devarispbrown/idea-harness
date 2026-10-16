package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/integrations/linear"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Automations: the background jobs the daemon runs on a schedule, and the
// Automations view's API (GET/PUT .../automations, POST .../run).
//
// Three jobs, every one off until a person turns it on:
//
//   - linear_inbound: the Linear label poll (integrations.go). Its switch is
//     integrations.linear.inbound, where it always was; listed here so every
//     background job is in one place.
//   - metric_measurement: `idea metric measure` for each live outcome metric
//     with a command or url source, on that metric's own cadence (floored at
//     config.MinMetricCadenceMinutes). url sources still need
//     outcomes.allow_url_sources; enabling the schedule implies no network.
//   - eval: `idea eval` against automations.eval.model, every
//     interval_hours, capped at max_cost_usd per run; reports go to the
//     project's state directory, and a regression against the baseline is
//     an eval.regressed event on the integration bus.
//
// The scheduler wakes every schedulerInterval, looks at each open project,
// and starts whatever is due on a goroutine of its own -- never two runs of
// the same job and target at once. Its clock is a field, so tests drive it
// with a fake one and call tick directly.

// Automation names.
const (
	AutomationLinearInbound     = "linear_inbound"
	AutomationMetricMeasurement = "metric_measurement"
	AutomationEval              = "eval"
)

// automationNames is the order the view lists them in.
var automationNames = []string{AutomationLinearInbound, AutomationMetricMeasurement, AutomationEval}

func knownAutomation(name string) bool {
	for _, n := range automationNames {
		if n == name {
			return true
		}
	}
	return false
}

const (
	triggerSchedule = "schedule"
	triggerManual   = "manual"

	schedulerInterval = 30 * time.Second
	historyLimit      = 20
)

// EvalRequest is one scheduled (or "Run now") eval run, as the daemon hands
// it to the registered EvalRunner.
type EvalRequest struct {
	ProjectRoot string
	Model       string
	// Suite is a path relative to ProjectRoot; empty is the built-in suite.
	Suite string
	Cases []string
	// MaxCostUSD caps the run; always > 0.
	MaxCostUSD float64
	// Baseline is a report to compare with (absolute path), or "".
	Baseline string
	// ReportDir is where the runner writes <stamp>.json and <stamp>.md.
	ReportDir string
	Stamp     string
}

// EvalResult is what an eval run found.
type EvalResult struct {
	CostUSD      float64
	Summary      string
	Regressions  []string
	ReportPath   string
	StoppedAtCap bool
}

// EvalRunner runs one eval. internal/eval implements it; it is injected
// because that package drives a daemon in-process and so imports this one.
type EvalRunner func(ctx context.Context, req EvalRequest) (EvalResult, error)

var (
	evalRunnerMu sync.Mutex
	evalRunner   EvalRunner
)

// SetEvalRunner registers the eval implementation the scheduled eval job
// uses. Without one (a test binary, an embedder) the job reports itself
// unavailable rather than pretending to run.
func SetEvalRunner(fn EvalRunner) {
	evalRunnerMu.Lock()
	defer evalRunnerMu.Unlock()
	evalRunner = fn
}

func currentEvalRunner() EvalRunner {
	evalRunnerMu.Lock()
	defer evalRunnerMu.Unlock()
	return evalRunner
}

// scheduler runs the due automations of every open project.
type scheduler struct {
	rt  *Runtime
	now func() time.Time

	mu       sync.Mutex
	inflight map[string]bool
	// firstSeen is when the scheduler first saw a project's eval schedule
	// enabled: with no run yet, the first scheduled run is one interval after
	// that, never the moment the switch is flipped. "Run now" is the way to
	// run it at once.
	firstSeen map[string]time.Time
	swept     map[string]bool
	wg        sync.WaitGroup
}

func (r *Runtime) automations() *scheduler {
	r.schedOnce.Do(func() {
		r.sched = &scheduler{rt: r, now: time.Now, inflight: map[string]bool{}, firstSeen: map[string]time.Time{}, swept: map[string]bool{}}
	})
	return r.sched
}

// runScheduler ticks until ctx ends. Started by Run.
func (r *Runtime) runScheduler(ctx context.Context) {
	s := r.automations()
	t := time.NewTicker(schedulerInterval)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// startConfiguredAutomations opens, at daemon start, every registered project
// whose harness.yaml enables a scheduled job, so its schedule resumes after a
// restart without anyone opening the project.
func (r *Runtime) startConfiguredAutomations(ctx context.Context) {
	projects, err := r.ListProjects(ctx)
	if err != nil {
		r.log.Warn("automations: listing projects", "err", err)
		return
	}
	for _, p := range projects {
		cfg, err := config.Load(filepath.Join(p.RootPath, config.Dir))
		if err != nil || !automationsEnabled(cfg.Harness.Automations) {
			continue
		}
		if _, err := r.EnsureProject(ctx, p.ID); err != nil {
			r.log.Warn("automations: opening project", "project", p.ID, "err", err)
		}
	}
}

func automationsEnabled(a config.AutomationsConfig) bool {
	return (a.MetricMeasurement != nil && a.MetricMeasurement.Enabled) || (a.Eval != nil && a.Eval.Enabled)
}

func (s *scheduler) openProjects() map[string]*projectRuntime {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	out := make(map[string]*projectRuntime, len(s.rt.projects))
	for id, pr := range s.rt.projects {
		out[id] = pr
	}
	return out
}

// tick starts every due job of every open project.
func (s *scheduler) tick(ctx context.Context) {
	now := s.now()
	for id, pr := range s.openProjects() {
		// A hand edit of harness.yaml (enabling a job, say) is picked up here
		// as it is on the next HTTP request, not only once someone opens the
		// project in the UI.
		pr = s.rt.maybeAutoReload(ctx, id, pr)
		s.sweepOnce(ctx, id, pr)
		a := pr.cfg.Harness.Automations
		if a.MetricMeasurement != nil && a.MetricMeasurement.Enabled {
			targets, err := s.metricTargets(ctx, pr, now)
			if err != nil {
				s.rt.log.Warn("automations: listing metrics", "project", id, "err", err)
			}
			for _, t := range targets {
				if t.due {
					s.start(id, AutomationMetricMeasurement, t.MetricID, triggerSchedule, func(ctx context.Context) {
						s.measureMetric(ctx, id, pr, t.InitiativeID, t.MetricID, t.Code, triggerSchedule)
					})
				}
			}
		}
		key := id + "/" + AutomationEval
		if e := a.Eval; e != nil && e.Enabled {
			s.mu.Lock()
			if _, ok := s.firstSeen[key]; !ok {
				s.firstSeen[key] = now
			}
			first := s.firstSeen[key]
			s.mu.Unlock()
			if next := s.nextEval(ctx, pr, *e, first); next != nil && !now.Before(*next) {
				ec := *e
				s.start(id, AutomationEval, "", triggerSchedule, func(ctx context.Context) {
					s.runEval(ctx, id, pr, ec, triggerSchedule)
				})
			}
		} else {
			s.mu.Lock()
			delete(s.firstSeen, key)
			s.mu.Unlock()
		}
	}
}

// sweepOnce fails the runs a previous daemon left `running`.
func (s *scheduler) sweepOnce(ctx context.Context, projectID string, pr *projectRuntime) {
	s.mu.Lock()
	done := s.swept[projectID]
	s.swept[projectID] = true
	s.mu.Unlock()
	if done {
		return
	}
	if err := pr.db.FailInterruptedAutomationRuns(ctx); err != nil {
		s.rt.log.Warn("automations: sweeping interrupted runs", "project", projectID, "err", err)
	}
}

// start runs fn on its own goroutine unless the same job and target is
// already running. It reports whether it started.
func (s *scheduler) start(projectID, name, target, trigger string, fn func(ctx context.Context)) bool {
	key := projectID + "/" + name + "/" + target
	s.mu.Lock()
	if s.inflight[key] {
		s.mu.Unlock()
		return false
	}
	s.inflight[key] = true
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()
		// A background job must never take the daemon down with it: a panic
		// in a runner is logged and the run left as the sweep will find it.
		defer func() {
			if p := recover(); p != nil {
				s.rt.log.Error("automations: a run panicked", "job", key, "panic", fmt.Sprint(p))
			}
		}()
		fn(s.rt.lifetimeContext())
	}()
	return true
}

func (s *scheduler) running(projectID, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := projectID + "/" + name + "/"
	for k := range s.inflight {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// wait blocks until every started run has finished (tests, shutdown).
func (s *scheduler) wait() { s.wg.Wait() }

// ---------- metric measurement ----------

// metricTarget is one metric the measurement job covers.
type metricTarget struct {
	MetricID     string     `json:"metric_id"`
	InitiativeID string     `json:"initiative_id"`
	Code         string     `json:"code"`
	Name         string     `json:"name"`
	Source       string     `json:"source"`
	Cadence      string     `json:"cadence"`
	LastAt       *time.Time `json:"last_at,omitempty"`
	NextAt       *time.Time `json:"next_at,omitempty"`
	// Skipped says why the schedule does not run this metric.
	Skipped string `json:"skipped,omitempty"`
	due     bool
}

// metricTargets lists every live command/url metric across the project's
// initiatives and whether each is due.
func (s *scheduler) metricTargets(ctx context.Context, pr *projectRuntime, now time.Time) ([]metricTarget, error) {
	inis, err := pr.db.ListInitiatives(ctx)
	if err != nil {
		return nil, err
	}
	floor := time.Duration(config.MinMetricCadenceMinutes) * time.Minute
	var out []metricTarget
	for _, ini := range inis {
		metrics, err := pr.db.ListMetrics(ctx, ini.ID, now)
		if err != nil {
			return nil, err
		}
		for _, m := range metrics {
			if m.Source.Kind != domain.MetricSourceCommand && m.Source.Kind != domain.MetricSourceURL {
				continue
			}
			t := metricTarget{MetricID: m.ID, InitiativeID: ini.ID, Code: m.Code, Name: m.Name, Source: string(m.Source.Kind), Cadence: m.Cadence}
			cad, err := domain.ParseCadence(m.Cadence)
			switch {
			case err != nil:
				t.Skipped = err.Error()
			case cad == 0:
				t.Skipped = "no cadence: set one (e.g. daily) for the schedule to measure it"
			case m.Source.Kind == domain.MetricSourceURL && !pr.cfg.Harness.Outcomes.AllowURLSources:
				t.Skipped = "url sources are off (outcomes.allow_url_sources)"
			}
			if m.Status != nil && m.Status.LatestAt != nil {
				l := *m.Status.LatestAt
				t.LastAt = &l
			}
			if runs, err := pr.db.ListAutomationRuns(ctx, AutomationMetricMeasurement, m.ID, 1); err == nil && len(runs) > 0 {
				// A failed measurement records no observation; without this
				// the schedule would retry it on every tick.
				if at := time.UnixMilli(runs[0].StartedAt); t.LastAt == nil || at.After(*t.LastAt) {
					t.LastAt = &at
				}
			}
			if t.Skipped == "" {
				if cad < floor {
					cad = floor
				}
				next := now
				if t.LastAt != nil {
					next = t.LastAt.Add(cad)
				}
				t.NextAt = &next
				t.due = !now.Before(next)
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// measureMetric measures one metric and records the run.
func (s *scheduler) measureMetric(ctx context.Context, projectID string, pr *projectRuntime, initiativeID, metricID, code, trigger string) {
	if s.rt.waitSwept(ctx) != nil {
		return
	}
	run, err := pr.db.StartAutomationRun(ctx, AutomationMetricMeasurement, metricID, trigger, s.now().UnixMilli())
	if err != nil {
		s.rt.log.Warn("automations: recording a run", "project", projectID, "err", err)
		return
	}
	results, err := pr.engine.MeasureMetrics(ctx, initiativeID, code, store.ActorOrchestrator)
	run.FinishedAt = s.now().UnixMilli()
	zero := 0.0
	run.CostUSD = &zero // a sandboxed command or one fetch: no model spend
	switch {
	case err != nil:
		run.Status, run.Error = store.AutomationFailed, pr.redactor.RedactString(err.Error())
	case len(results) == 0:
		run.Status, run.Summary = store.AutomationSkipped, code+": nothing to measure"
	case results[0].Error != "":
		run.Status, run.Error = store.AutomationFailed, results[0].Error
	case results[0].Skipped != "":
		run.Status, run.Summary = store.AutomationSkipped, code+": "+results[0].Skipped
	default:
		run.Status = store.AutomationSucceeded
		run.Summary = fmt.Sprintf("%s = %g", code, results[0].Observation.Value)
		if st := results[0].Status; st != nil && st.Drift != "" {
			run.Summary += " (" + string(st.Drift) + ")"
		}
	}
	if err := pr.db.FinishAutomationRun(context.WithoutCancel(ctx), run); err != nil {
		s.rt.log.Warn("automations: recording a run", "project", projectID, "err", err)
	}
}

// ---------- eval ----------

// nextEval returns when the eval job is next due (nil: never -- not enabled
// or not runnable).
func (s *scheduler) nextEval(ctx context.Context, pr *projectRuntime, e config.EvalAutomationConfig, firstSeen time.Time) *time.Time {
	interval := time.Duration(e.EffectiveIntervalHours()) * time.Hour
	runs, err := pr.db.ListAutomationRuns(ctx, AutomationEval, "", 1)
	if err != nil {
		return nil
	}
	next := firstSeen.Add(interval)
	if len(runs) > 0 {
		next = time.UnixMilli(runs[0].StartedAt).Add(interval)
	}
	return &next
}

func (r *Runtime) evalReportDir(projectID string) string {
	return filepath.Join(r.stateRoot, "projects", projectID, "eval-reports")
}

// runEval runs one eval, records it, and raises eval.regressed when it
// regressed against its baseline.
func (s *scheduler) runEval(ctx context.Context, projectID string, pr *projectRuntime, e config.EvalAutomationConfig, trigger string) {
	run, err := pr.db.StartAutomationRun(ctx, AutomationEval, "", trigger, s.now().UnixMilli())
	if err != nil {
		s.rt.log.Warn("automations: recording a run", "project", projectID, "err", err)
		return
	}
	finish := func() {
		run.FinishedAt = s.now().UnixMilli()
		if err := pr.db.FinishAutomationRun(context.WithoutCancel(ctx), run); err != nil {
			s.rt.log.Warn("automations: recording a run", "project", projectID, "err", err)
		}
	}
	runner := currentEvalRunner()
	if runner == nil {
		run.Status, run.Error = store.AutomationFailed, "no eval runner in this daemon build"
		finish()
		return
	}
	baseline := ""
	if e.Baseline != "" {
		baseline = filepath.Join(pr.rootPath, e.Baseline)
	} else if prev, err := pr.db.ListAutomationRuns(ctx, AutomationEval, "", historyLimit); err == nil {
		// The previous run that finished and wrote a report.
		for _, p := range prev {
			if p.ID != run.ID && p.Status == store.AutomationSucceeded && p.ReportPath != "" {
				baseline = p.ReportPath
				break
			}
		}
	}
	dir := s.rt.evalReportDir(projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		run.Status, run.Error = store.AutomationFailed, "report directory: "+err.Error()
		finish()
		return
	}
	res, err := runner(ctx, EvalRequest{
		ProjectRoot: pr.rootPath, Model: e.Model, Suite: e.Suite, Cases: e.Cases,
		MaxCostUSD: e.MaxCostUSD, Baseline: baseline, ReportDir: dir,
		Stamp: s.now().UTC().Format("20060102T150405Z"),
	})
	cost := res.CostUSD
	run.CostUSD = &cost
	run.ReportPath = res.ReportPath
	if err != nil {
		run.Status, run.Error = store.AutomationFailed, pr.redactor.RedactString(err.Error())
		finish()
		return
	}
	run.Status, run.Summary = store.AutomationSucceeded, res.Summary
	if res.StoppedAtCap {
		run.Summary = strings.TrimSpace(run.Summary + fmt.Sprintf(" Stopped at the $%.2f cap before every case ran.", e.MaxCostUSD))
	}
	finish()
	if len(res.Regressions) > 0 {
		detail := fmt.Sprintf("%d regression(s) against %s: %s", len(res.Regressions), filepath.Base(baseline), strings.Join(res.Regressions, "; "))
		if err := pr.db.AppendAutomationEvent(context.WithoutCancel(ctx), AutomationEval, store.EvalRegressedEvent, domain.TrimToWordBoundary(detail, 900)); err != nil {
			s.rt.log.Warn("automations: recording the regression event", "project", projectID, "err", err)
		}
	}
}

// ---------- linear inbound ----------

// recordLinearPoll keeps the polls worth keeping as history: one that
// imported an issue, failed, or was asked for by a person. Every quiet poll
// on a 60-second timer would bury those under a thousand rows a day.
func (r *Runtime) recordLinearPoll(db *store.DB, rec linear.PollRecord) {
	if rec.Imported == 0 && rec.Err == nil && !rec.Manual {
		return
	}
	trigger := triggerSchedule
	if rec.Manual {
		trigger = triggerManual
	}
	ctx := context.Background()
	run, err := db.StartAutomationRun(ctx, AutomationLinearInbound, "", trigger, rec.Started.UnixMilli())
	if err != nil {
		return
	}
	run.FinishedAt = rec.Finished.UnixMilli()
	run.Status = store.AutomationSucceeded
	run.Summary = fmt.Sprintf("imported %d issue(s)", rec.Imported)
	if rec.Err != nil {
		run.Status, run.Error = store.AutomationFailed, rec.Err.Error()
	}
	_ = db.FinishAutomationRun(ctx, run)
}

// ---------- HTTP ----------

// AutomationView is one job in GET .../automations.
type AutomationView struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// Available is false when the job cannot run at all right now, with
	// Unavailable saying why (Linear not configured, no eval model, ...).
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
	// Schedule is the cadence in words.
	Schedule  string               `json:"schedule"`
	Running   bool                 `json:"running"`
	LastRun   *store.AutomationRun `json:"last_run"`
	NextRunAt *time.Time           `json:"next_run_at"`
	// Settings are the job's own harness.yaml fields (eval only).
	Settings *config.EvalAutomationConfig `json:"settings,omitempty"`
	// Targets are the metrics the measurement job covers.
	Targets []metricTarget        `json:"targets,omitempty"`
	History []store.AutomationRun `json:"history"`
	Note    string                `json:"note,omitempty"`
}

func (s *Server) handleListAutomations(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	out := make([]AutomationView, 0, len(automationNames))
	for _, name := range automationNames {
		v, err := s.rt.automationView(r.Context(), projectID, pr, name)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": out})
}

func (r *Runtime) automationView(ctx context.Context, projectID string, pr *projectRuntime, name string) (AutomationView, error) {
	sched := r.automations()
	now := sched.now()
	v := AutomationView{Name: name, Available: true, Running: sched.running(projectID, name)}
	history, err := pr.db.ListAutomationRuns(ctx, name, "", historyLimit)
	if err != nil {
		return v, err
	}
	v.History = history
	if len(history) > 0 {
		v.LastRun = &history[0]
	}
	h := pr.cfg.Harness
	switch name {
	case AutomationLinearInbound:
		v.Label = "Linear inbound polling"
		v.Description = "Imports Linear issues carrying the inbound label as new initiatives."
		v.Note = "Only polls that imported an issue, failed, or were run by hand are kept in the history."
		l := h.Integrations.Linear
		if l == nil {
			v.Available, v.Unavailable = false, "Linear is not configured (Settings → Integrations)"
			v.Schedule = "every 60s once configured"
			break
		}
		v.Enabled = l.InboundEnabled()
		v.Schedule = fmt.Sprintf("every %ds", l.EffectivePollSeconds())
		hub := r.integrations()
		hub.mu.Lock()
		pi := hub.projects[projectID]
		hub.mu.Unlock()
		if pi == nil || pi.syncer == nil {
			v.Available, v.Unavailable = false, l.APIKeyEnvName()+" is not set, so the poller is not running"
			break
		}
		if ps := pi.syncer.PollState(); ps.LastPollAt != nil {
			next := ps.LastPollAt.Add(time.Duration(l.EffectivePollSeconds()) * time.Second)
			v.NextRunAt = &next
			if v.LastRun == nil || ps.LastPollAt.UnixMilli() > v.LastRun.StartedAt {
				// The poll itself is newer than anything kept: show it, unkept.
				st := store.AutomationSucceeded
				if ps.LastError != "" {
					st = store.AutomationFailed
				}
				v.LastRun = &store.AutomationRun{Automation: name, Trigger: triggerSchedule, Status: st, StartedAt: ps.LastPollAt.UnixMilli(), FinishedAt: ps.LastPollAt.UnixMilli(), Error: ps.LastError}
			}
		}
	case AutomationMetricMeasurement:
		v.Label = "Scheduled metric measurement"
		v.Description = "Measures each outcome metric with a command or url source on its own cadence, as `idea metric measure` does."
		v.Schedule = fmt.Sprintf("per metric cadence (at most every %dm)", config.MinMetricCadenceMinutes)
		v.Enabled = h.Automations.MetricMeasurement != nil && h.Automations.MetricMeasurement.Enabled
		targets, err := sched.metricTargets(ctx, pr, now)
		if err != nil {
			return v, err
		}
		v.Targets = targets
		if len(targets) == 0 {
			v.Note = "No outcome metric has a command or url source yet; manual metrics are never measured on a schedule."
		}
		if v.Enabled {
			for _, t := range targets {
				if t.NextAt != nil && (v.NextRunAt == nil || t.NextAt.Before(*v.NextRunAt)) {
					n := *t.NextAt
					v.NextRunAt = &n
				}
			}
		}
	case AutomationEval:
		v.Label = "Scheduled eval"
		v.Description = "Runs `idea eval` against a real model, capped per run, writes the report to the state directory, and raises eval.regressed when it regresses against the baseline."
		e := h.Automations.Eval
		if e == nil {
			e = &config.EvalAutomationConfig{}
		}
		ec := *e
		v.Settings = &ec
		v.Enabled = e.Enabled
		v.Schedule = fmt.Sprintf("every %dh", e.EffectiveIntervalHours())
		switch {
		case currentEvalRunner() == nil:
			v.Available, v.Unavailable = false, "this daemon build has no eval runner"
		case e.Model == "" || e.MaxCostUSD <= 0:
			v.Available, v.Unavailable = false, "set a model and a per-run cost cap first"
		}
		if e.Enabled {
			sched.mu.Lock()
			first, ok := sched.firstSeen[projectID+"/"+AutomationEval]
			sched.mu.Unlock()
			if !ok {
				first = now
			}
			v.NextRunAt = sched.nextEval(ctx, pr, *e, first)
		}
		v.Note = "Reports are written to " + r.evalReportDir(projectID) + "."
	}
	return v, nil
}

// handleUpdateAutomation turns a job on or off and, for eval, sets its
// fields. Every change is a harness.yaml write (writeHarness), validated
// like a hand edit and applied without a restart.
func (s *Server) handleUpdateAutomation(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	name := r.PathValue("name")
	if !knownAutomation(name) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no automation matching %q (use %s)", name, strings.Join(automationNames, ", ")))
		return
	}
	var body struct {
		Enabled       *bool     `json:"enabled"`
		Model         *string   `json:"model"`
		Suite         *string   `json:"suite"`
		Cases         *[]string `json:"cases"`
		IntervalHours *int      `json:"interval_hours"`
		MaxCostUSD    *float64  `json:"max_cost_usd"`
		Baseline      *string   `json:"baseline"`
	}
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	evalOnly := body.Model != nil || body.Suite != nil || body.Cases != nil || body.IntervalHours != nil || body.MaxCostUSD != nil || body.Baseline != nil
	switch name {
	case AutomationLinearInbound, AutomationMetricMeasurement:
		if evalOnly {
			writeError(w, http.StatusBadRequest, name+" takes only enabled")
			return
		}
		if body.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled is required")
			return
		}
	case AutomationEval:
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("no automation matching %q (use %s)", name, strings.Join(automationNames, ", ")))
		return
	}
	if !s.writeHarness(w, r, pr, func(h *config.HarnessConfig) error {
		switch name {
		case AutomationLinearInbound:
			if h.Integrations.Linear == nil {
				return &PreconditionError{Reason: "Linear is not configured; configure it in Settings → Integrations first"}
			}
			v := *body.Enabled
			h.Integrations.Linear.Inbound = &v
		case AutomationMetricMeasurement:
			h.Automations.MetricMeasurement = &config.MetricAutomationConfig{Enabled: *body.Enabled}
		case AutomationEval:
			e := config.EvalAutomationConfig{}
			if h.Automations.Eval != nil {
				e = *h.Automations.Eval
			}
			if body.Enabled != nil {
				e.Enabled = *body.Enabled
			}
			if body.Model != nil {
				e.Model = strings.TrimSpace(*body.Model)
			}
			if body.Suite != nil {
				e.Suite = strings.TrimSpace(*body.Suite)
			}
			if body.Cases != nil {
				e.Cases = *body.Cases
			}
			if body.IntervalHours != nil {
				e.IntervalHours = *body.IntervalHours
			}
			if body.MaxCostUSD != nil {
				e.MaxCostUSD = *body.MaxCostUSD
			}
			if body.Baseline != nil {
				e.Baseline = strings.TrimSpace(*body.Baseline)
			}
			h.Automations.Eval = &e
		}
		return nil
	}) {
		return
	}
	pr, err = s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	v, err := s.rt.automationView(r.Context(), r.PathValue("projectId"), pr, name)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRunAutomation is "Run now": one run, started at once, whether or not
// the schedule is enabled -- a person asking for this one run is the
// enablement. body.target narrows metric measurement to one metric.
func (s *Server) handleRunAutomation(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if !knownAutomation(r.PathValue("name")) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no automation matching %q (use %s)", r.PathValue("name"), strings.Join(automationNames, ", ")))
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	if r.ContentLength != 0 {
		if err := decodeStrict(r.Body, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	sched := s.rt.automations()
	name := r.PathValue("name")
	started := 0
	switch name {
	case AutomationLinearInbound:
		hub := s.rt.integrations()
		hub.mu.Lock()
		pi := hub.projects[projectID]
		hub.mu.Unlock()
		if pi == nil || pi.syncer == nil {
			s.writeErr(w, http.StatusConflict, preconditionf("the Linear poller is not running (Linear not configured, or its API key is not set)"))
			return
		}
		pi.syncer.PollNow()
		started = 1
	case AutomationMetricMeasurement:
		targets, err := sched.metricTargets(r.Context(), pr, sched.now())
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		var skipped []string
		for _, t := range targets {
			if body.Target != "" && t.MetricID != body.Target && t.Code != body.Target {
				continue
			}
			// A missing cadence matters only to the schedule; a person can
			// still measure the metric now. url sources off is a real
			// refusal, which the measurement itself reports.
			t := t
			if sched.start(projectID, name, t.MetricID, triggerManual, func(ctx context.Context) {
				sched.measureMetric(ctx, projectID, pr, t.InitiativeID, t.MetricID, t.Code, triggerManual)
			}) {
				started++
			} else {
				skipped = append(skipped, t.Code+" (already running)")
			}
		}
		if started == 0 && len(skipped) == 0 {
			msg := "no outcome metric has a command or url source to measure"
			if body.Target != "" {
				msg = fmt.Sprintf("no command or url metric matching %q", body.Target)
			}
			s.writeErr(w, http.StatusConflict, preconditionf("%s", msg))
			return
		}
	case AutomationEval:
		e := pr.cfg.Harness.Automations.Eval
		if e == nil || e.Model == "" || e.MaxCostUSD <= 0 {
			s.writeErr(w, http.StatusConflict, preconditionf("set the eval's model and per-run cost cap first"))
			return
		}
		if currentEvalRunner() == nil {
			s.writeErr(w, http.StatusConflict, preconditionf("this daemon build has no eval runner"))
			return
		}
		ec := *e
		if !sched.start(projectID, name, "", triggerManual, func(ctx context.Context) {
			sched.runEval(ctx, projectID, pr, ec, triggerManual)
		}) {
			s.writeErr(w, http.StatusConflict, preconditionf("an eval run is already in progress"))
			return
		}
		started = 1
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("no automation matching %q (use %s)", name, strings.Join(automationNames, ", ")))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "name": name, "runs": started})
}
