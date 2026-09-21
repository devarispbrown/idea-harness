package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	ccontext "github.com/devarispbrown/idea-harness/internal/context"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/git"
	"github.com/devarispbrown/idea-harness/internal/review"
	"github.com/devarispbrown/idea-harness/internal/risk"
	"github.com/devarispbrown/idea-harness/internal/schema"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/textdiff"
)

// snapshotSanityMaxFiles and snapshotSanityMaxBytes bound how much of an
// agent's write to the work root the orchestrator will ever collect into a
// change set. Past this, something is badly wrong -- most commonly an agent
// downloading a toolchain it should never have needed (review defect #2: an
// ICP run's agent wrote ~14,300 files under .tools/, producing 79 false
// secret-sweep findings and a diff endpoint that never responded) -- and the
// run fails now, with a clear and actionable error, instead of the collector,
// the secret sweep, and the diff endpoint all silently drowning in it later.
const (
	snapshotSanityMaxFiles = 3000
	snapshotSanityMaxBytes = 150 << 20 // 150 MiB
)

// afterRunSnapshotOptions are the fsdiff.SnapshotOptions applied to every
// snapshot taken of a work root AFTER an external agent has run in it --
// i.e. of untrusted content. It excludes whatever the workspace's own
// .gitignore excludes (on top of fsdiff's always-on default-ignored
// directories) and enforces the sanity cap above (review defect #2).
// Snapshots taken BEFORE the agent runs use fsdiff.Snapshot with no options:
// that content came from seedWorkRoot, which already applied the workspace's
// ignore rules once when it copied the files in.
func (e *Engine) afterRunSnapshotOptions() []fsdiff.SnapshotOption {
	return []fsdiff.SnapshotOption{
		fsdiff.WithWorkspaceIgnore(e.workspaceRoot),
		fsdiff.WithSanityCap(snapshotSanityMaxFiles, snapshotSanityMaxBytes),
	}
}

// SetWorkRoot sets the directory that Execute derives per-initiative isolated
// work roots from. The daemon points this at its per-project state directory so
// the executor's WorkRoot mirrors "<state-root>/work/<initiative>".
func (e *Engine) SetWorkRoot(dir string) { e.workRoot = dir }

// SetWorkspaceRoot sets the canonical workspace that ApplyChangeSet writes into
// and RevertChangeSet restores. The daemon points this at the project root.
func (e *Engine) SetWorkspaceRoot(dir string) { e.workspaceRoot = dir }

// implementerProvider resolves the coding agent's provider -- its name, key
// variable and base URL -- through executor.ResolveAgentProvider, which reads
// the project's providers: entries before the presets and fails closed on a
// provider it cannot resolve.
//
// Wired through config rather than a model-prefix switch, so bare and custom
// model specs get their key (review: v1 security boundary).
func (e *Engine) implementerProvider() (executor.AgentProvider, error) {
	return ImplementerProvider(e.cfg, e.registry)
}

// SetDotEnv supplies the project's .env, which the daemon's model registry
// already falls back to for provider keys. The coding agent reads its key the
// same way; with only the process environment it launched keyless for anyone
// whose key lived in .env.
func (e *Engine) SetDotEnv(dotEnv map[string]string) { e.dotEnv = dotEnv }

// lookupKey reads a provider key the way the model registry does: the process
// environment first, then the project's .env.
func (e *Engine) lookupKey(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return e.dotEnv[name]
}

// selectExecutor returns the executor selected by harness.executor.kind. The
// kind is a config field (deterministic), never model output; an unknown kind
// fails closed to the internal executor.
func (e *Engine) selectExecutor() (executor.Executor, error) {
	switch e.cfg.Executor.Kind {
	case config.ExecutorContainer:
		return executor.NewContainerExecutor(e.contentBlobs(), e.cfg.Executor.Image), nil
	case config.ExecutorOMP, config.ExecutorHermes:
		model := e.implementerModel()
		provider, err := e.implementerProvider()
		if err != nil {
			return nil, err
		}
		// OMP has no published image to default to. Without one, docker run
		// failed with "invalid reference format" -- after the planner's model
		// call had already been paid for.
		if e.cfg.Executor.Image == "" && executor.DefaultAgentImage(string(e.cfg.Executor.Kind)) == "" {
			return nil, fmt.Errorf("executor: %s has no default image; set one with idea executors setup --kind %s --image <image> --model %s --yes",
				e.cfg.Executor.Kind, e.cfg.Executor.Kind, model)
		}
		// A key variable that resolves to nothing is refused here, not left to
		// the agent. Hermes exits 0 after a 401, so a keyless start of a keyed
		// provider produced an empty change set with no reason given. A
		// provider with no key variable (Ollama) needs none and is not refused.
		if provider.KeyEnv != "" && e.lookupKey(provider.KeyEnv) == "" {
			return nil, fmt.Errorf("executor: provider %q needs %s, which is set neither in the daemon's environment nor in the project's .env; "+
				"export it and restart the daemon (`idea daemon stop`), or add it to .env", provider.Name, provider.KeyEnv)
		}
		// Known-incompatible combination (docs/agents.md, "Known executor/model
		// compatibility"): refused here, before the container starts, rather
		// than after a paid model call that was always going to fail the same
		// way (ICP walkthrough: hermes + gpt-4.1-mini failed deterministically,
		// every run, with no signal until the agent produced nothing). An
		// untested combination is only logged -- Execute is not the place to
		// block on "nobody has tried this yet".
		if compat := executor.CheckModelCompat(string(e.cfg.Executor.Kind), model); compat.Message != "" {
			if compat.Blocked {
				return nil, fmt.Errorf("executor: %s", compat.Message)
			}
			// e.log is nil for an Engine built as a bare struct literal (several
			// selectExecutor tests do this rather than going through New(),
			// which is the only place that defaults it) -- nil-checked rather
			// than requiring every such test to wire a logger for an advisory
			// log line.
			if e.log != nil {
				e.log.Warn("untested executor/model combination", "kind", e.cfg.Executor.Kind, "model", model, "detail", compat.Message)
			}
		}
		var ex *executor.SandboxedAgentExecutor
		if e.cfg.Executor.Kind == config.ExecutorOMP {
			ex = executor.NewOMPExecutor(e.contentBlobs(), model, e.cfg.Executor.Image, provider)
		} else {
			ex = executor.NewHermesExecutor(e.contentBlobs(), model, e.cfg.Executor.Image, provider)
		}
		ex.SetKeyLookup(e.lookupKey)
		return ex, nil
	default:
		return executor.NewInternalExecutor(e.contentBlobs()), nil
	}
}

// ErrNoReadyTask is what nextReadyTask returns when the initiative has nothing
// runnable. A sentinel rather than a bare error so callers can tell it from the
// storage failures the same function can return -- NextAction has to make
// exactly that distinction.
var ErrNoReadyTask = errors.New("workflow: no ready tasks")

// nextReadyTask returns the first task that is ready or proposed (i.e. not yet
// done) for an initiative, in creation order.
func (e *Engine) nextReadyTask(ctx context.Context, initiativeID string) (domain.Task, error) {
	tasks, err := e.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return domain.Task{}, err
	}
	for _, t := range tasks {
		if t.Status != domain.TaskReady && t.Status != domain.TaskProposed {
			continue
		}
		ready, err := e.taskDependenciesApplied(ctx, initiativeID, t.ID)
		if err != nil {
			return domain.Task{}, err
		}
		if ready {
			return t, nil
		}
	}
	return domain.Task{}, fmt.Errorf("%w for initiative %q (dependencies not satisfied)", ErrNoReadyTask, initiativeID)
}

// taskDependenciesApplied reports whether every depends_on predecessor of taskID
// has an applied change set (full-platform review: task-dependency ordering —
// a task is executable only once its upstream work has actually landed).
func (e *Engine) taskDependenciesApplied(ctx context.Context, initiativeID, taskID string) (bool, error) {
	edges, err := e.db.ListGraphEdgesFrom(ctx, initiativeID, "task", taskID)
	if err != nil {
		return false, err
	}
	for _, edge := range edges {
		if edge.EdgeType != domain.EdgeDependsOn {
			continue
		}
		applied, err := e.taskHasAppliedChangeSet(ctx, edge.ToID)
		if err != nil {
			return false, err
		}
		if !applied {
			return false, nil
		}
	}
	return true, nil
}

// taskHasAppliedChangeSet reports whether a task has any applied change set
// that actually implements it -- an applied NoOp change set (zero file
// changes) does not count (ICP re-run defect #2; see
// domain.ChangeSet.ImplementsTask).
func (e *Engine) taskHasAppliedChangeSet(ctx context.Context, taskID string) (bool, error) {
	css, err := e.db.ListChangeSetsByTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, cs := range css {
		if cs.ImplementsTask() {
			return true, nil
		}
	}
	return false, nil
}

// runPlanner runs the engineering_lead planner to produce an implementation brief
// for a task, returned as rendered text for the implementer (§50). The brief is a
// durable Run (role engineering_lead, stage implementation_brief) via runModel.
//
// The rendered brief is what the implementer's ENTIRE prompt is (req.Brief goes
// to the coding agent CLI verbatim, executor.ompArgs/hermesArgs) -- so the
// detected stack and a hard "write files, don't paste code" instruction are
// appended here, by the orchestrator, rather than left to the planner model's
// own judgment to include or not (review #6, ICP re-run: the implementer
// generated Python for a Go project because nothing in its brief said what
// stack it was working in at all, or even that it was joining an existing
// project rather than starting from nothing).
func (e *Engine) runPlanner(ctx context.Context, initiativeID string, task domain.Task) (string, error) {
	stack := detectStack(e.workspaceRoot)
	scale := projectScaleOf(ctx, e.workspaceRoot)
	projectContext := proportionateContext(scale, stack, projectConventions(e.workspaceRoot))

	// ICP re-run defect H: a founder-journey run answered "keys in an env
	// var" and the implementer built a JSON config file instead, because
	// nothing in the planner's inputs -- or the brief it produces -- ever
	// named a resolved decision or an answered question at all. Fed here
	// exactly as architecture/product design/the execution plan already feed
	// decisions (advance.go), plus questions (renderQuestionsWithAnswers,
	// which none of those stages read either, but this one -- the last stop
	// before code gets written -- most needs to).
	allDecs, _ := e.db.ListDecisions(ctx, initiativeID)
	decs := activeDecisions(allDecs)
	questions, _ := e.db.ListQuestions(ctx, initiativeID)

	_, obj, err := e.runModel(ctx, initiativeID, StageImplementationBrief, RoleEngineeringLead, "implementation_brief", schema.ImplementationBrief,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Produce a short implementation brief for the task, for an implementer who will write directly into this project's existing files. Name concrete files to add or change, in the project's own detected stack -- never propose a different language or framework than the one the project already uses. A decided human decision or an answered question is authoritative: do not propose or imply anything that contradicts one."},
			{Selector: "project_scale", Content: projectContext},
			{Selector: "task", Content: task.Statement},
			{Selector: "decisions", Content: renderDecisions(decs)},
			{Selector: "questions", Content: e.renderQuestionsWithAnswers(questions)},
		},
		"Task:\n"+task.Statement,
	)
	if err != nil {
		return "", fmt.Errorf("workflow: planner: %w", err)
	}

	var b strings.Builder
	b.WriteString("Implementation brief\n")
	b.WriteString("Summary: " + str(obj["summary"]) + "\n")
	if files := strSlice(obj["files"]); len(files) > 0 {
		b.WriteString("Files: " + strings.Join(files, ", ") + "\n")
	}
	if changes := strSlice(obj["changes"]); len(changes) > 0 {
		b.WriteString("Changes: " + strings.Join(changes, "; ") + "\n")
	}
	if req := str(obj["target_requirement"]); req != "" {
		b.WriteString("Target requirement: " + req + "\n")
	}
	// Deterministic, orchestrator-authored, and NOT contingent on the planner
	// model choosing to carry them into its own summary (ICP re-run defect
	// H): a decided decision or an answered question the human already gave
	// must reach the coding agent verbatim, not survive only if an
	// intermediate model call happened to repeat it.
	if resolved := e.resolvedDecisionsAndAnswers(decs, questions); resolved != "" {
		b.WriteString("\nResolved decisions and answered questions (authoritative -- follow these exactly):\n" + resolved)
	}
	// Deterministic, orchestrator-authored -- always present regardless of
	// what the planner model chose to say.
	if len(stack) > 0 {
		fmt.Fprintf(&b, "Project stack (detected): %s. Write this task's code in this stack; never introduce a different language or framework.\n", strings.Join(stack, ", "))
	} else if scale.Files == 0 {
		b.WriteString("This is a new project with no existing code yet: choose a stack appropriate to the task and be consistent about it.\n")
	} else {
		b.WriteString("This project's stack could not be auto-detected from its root-level files: match whatever language/framework its existing files already use; do not introduce a new one.\n")
	}
	b.WriteString("Write your changes directly to files in the workspace. Do not paste code, diffs, or file contents in your chat response -- only files actually written to disk are collected as this task's change set.\n")
	// Review defect #3: an agent with network egress (needed to reach its own
	// model API) but no toolchain in its container downloaded a full Go
	// toolchain into the workspace to run `go build`/`go test` itself,
	// producing ~14,300 extra files (review defect #2). The harness already
	// runs these same checks itself, in an isolated sandbox, right after this
	// run -- naming the actual commands here removes any reason to install
	// one. This is deliberately the only addition this defect makes to the
	// brief; it does not touch the rest of runPlanner's assembly above.
	b.WriteString(checkCommandsBriefNote(e.cfg, e.workspaceRoot))
	return b.String(), nil
}

// checkCommandsBriefNote tells the implementer which build/test commands the
// harness will run itself after this task, so it never has a reason to
// install a toolchain or download an SDK to verify its own work (review
// defect #3). It mirrors runDeterministicChecks' own config-then-detected
// precedence so the commands named here are the ones that actually run.
func checkCommandsBriefNote(cfg config.HarnessConfig, workspaceRoot string) string {
	detectedBuild, detectedTest := detectCheckCommands(workspaceRoot)
	build := effectiveCheckCommand(cfg.BuildCmd, detectedBuild)
	test := effectiveCheckCommand(cfg.TestCmd, detectedTest)
	if build == "" && test == "" {
		return "The harness runs its own build/test checks after this task, in an isolated sandbox with no network access; " +
			"it could not determine this project's build/test commands, but do not install a toolchain, compiler, or SDK yourself to verify your work -- write the code and let the checks run.\n"
	}
	var b strings.Builder
	b.WriteString("The harness runs its own deterministic checks after this task, in an isolated sandbox with no network access:")
	if build != "" {
		fmt.Fprintf(&b, " build: `%s`", build)
	}
	if test != "" {
		if build != "" {
			b.WriteString(";")
		}
		fmt.Fprintf(&b, " tests: `%s`", test)
	}
	b.WriteString(". Do not install a toolchain, compiler, or SDK yourself to build or test your work -- write the code and let the harness's own checks run afterward.\n")
	return b.String()
}

// effectiveCheckCommand returns the command that actually runs: the
// configured one if set, otherwise the detected one, otherwise "".
func effectiveCheckCommand(configured string, detected detectedCommand) string {
	if configured != "" {
		return configured
	}
	return detected.Cmd
}

// Execute runs the multi-role pipeline for the next ready task — planner
// (engineering_lead brief) → implementer (executor writes files) → deterministic
// checks → review_required/rejected. The adversarial reviewer runs at review time
// (§50).
func (e *Engine) Execute(ctx context.Context, initiativeID string) (domain.ChangeSet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// D-a: a rerun note applies to this one Execute only (see currentNoteOverride
	// below), the same lifetime as SetModelOverride/clearModelOverride for retry.
	defer e.clearNoteOverride(initiativeID)

	task, err := e.nextReadyTask(ctx, initiativeID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if err := e.refuseWhileAnotherChangeSetIsOpen(ctx, initiativeID, task.ID); err != nil {
		return domain.ChangeSet{}, err
	}
	// Visible to the running card from here, as "starting", until the agent
	// runs (runstate.go).
	e.setLiveStarting(initiativeID)
	defer e.clearLive(initiativeID)

	// Select the executor before anything is spent or changed. It is where a
	// missing provider key or an unresolvable provider is refused, and it ran
	// after the planner's model call and after superseding the previous change
	// set -- so a refusal cost money and discarded the change set being redone.
	exec, err := e.selectExecutor()
	if err != nil {
		return domain.ChangeSet{}, err
	}
	// The capability gate is the deterministic trust-tier policy, not the
	// executor's self-declared capability list: the engine authorizes the
	// selected executor's engine-assigned trust tier (§49).
	// The engine assigns the trust tier from the selected executor's kind, never
	// from the executor's self-declared Capabilities().Trust (system-deny).
	trust := executor.TrustTierFor(exec.Name())
	if !executor.TrustAllows(trust, "write_file") {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: trust tier %q may not write_file", trust)
	}

	// Supersede any prior changes_requested change set for this task so a
	// re-execute cleanly replaces it (full-platform review: changes_requested
	// was a terminal dead end with no producer of the superseded transition).
	prior, err := e.db.ListChangeSetsByTask(ctx, task.ID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	for _, p := range prior {
		if p.Status == domain.ChangeSetChangesRequested {
			if err := e.db.TransitionChangeSet(ctx, p.ID, domain.ChangeSetChangesRequested, domain.ChangeSetSuperseded, store.ActorOrchestrator); err != nil {
				return domain.ChangeSet{}, err
			}
		}
		// ICP re-run defect #1: a change set can sit in review_required with
		// the task still `proposed` only when Execute's own call to the
		// adversarial reviewer errored (e.g. the operation was cancelled
		// mid-review) after the checks-running -> review_required transition
		// already happened -- the normal path always moves the task to
		// done/proposed the moment the change set leaves checks_running (see
		// below). refuseWhileAnotherChangeSetIsOpen does not catch this: it
		// deliberately skips the task's OWN change sets, because a fresh
		// Execute is expected to supersede/replace them. Here it is the SAME
		// task, and re-running the agent would spend another implementer run
		// and produce a second change set while the first -- unreviewed --
		// one is still sitting there, exactly the "re-picks it forever"
		// defect. The way forward is `idea review <id> rerun`, not another
		// Execute.
		if p.Status == domain.ChangeSetReviewRequired && !p.ReviewCompleted() {
			return domain.ChangeSet{}, fmt.Errorf(
				"workflow: execute: %s never finished its adversarial review (%s) and is still awaiting one; "+
					"run `idea review %s rerun` to finish reviewing it before running this task again", p.HumanID, p.ReviewNeverCompletedCause(), p.HumanID)
		}
		// C4: a NoOp change set leaves its task `proposed` (not `done`,
		// which would deadlock it forever -- see the switch below), so it
		// looks re-pickable to nextReadyTask the instant it is created. The
		// naive fix stops there and passes the rest of this suite, but lets
		// a fresh Execute immediately re-pick the task and spend a second
		// agent run while the first no-op change set is still sitting open
		// (unreviewed, unapproved, or approved-but-not-yet-applied). Refuse
		// until it reaches a terminal disposition: rejected, superseded (by
		// this same loop, above, for changes_requested -- not reachable for
		// a NoOp still awaiting review), applied, reverted or conflicted.
		if p.NoOp && changeSetIsOpen(p.Status) {
			return domain.ChangeSet{}, fmt.Errorf(
				"workflow: execute: %s is a no-op change set for %s and is still %s; "+
					"reject it, or approve and apply it, before running this task again",
				p.HumanID, task.Code, strings.ReplaceAll(string(p.Status), "_", " "))
		}
	}

	// Planner: engineering_lead produces an implementation brief the implementer
	// follows. The brief is a durable Run (role engineering_lead).
	e.setStage(ctx, initiativeID, "Planning "+task.Code)
	brief, err := e.runPlanner(ctx, initiativeID, task)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	// D-a: an optional human note on `idea execute`/`POST .../execute`, steering
	// this one retry of the task (ICP finding "Retries. There was no way to
	// steer a retry."). Appended to the implementer's brief, exactly as the
	// wave 3 contract specifies, and cleared once Execute returns so it never
	// bleeds into a later, unrelated run of this initiative.
	note := strings.TrimSpace(e.currentNoteOverride(initiativeID))
	if note != "" {
		brief += "\n\nNote from the human, for this run only:\n" + note
	}

	root := e.workRoot
	if root == "" {
		root = os.TempDir()
	}
	req := executor.Request{
		InitiativeID:  initiativeID,
		TaskID:        task.ID,
		TaskTitle:     task.Title,
		TaskStatement: task.Statement,
		Brief:         brief,
		WorkRoot:      filepath.Join(root, "work", initiativeID),
	}

	// Record a durable run for the implementer step (review B7). The run is
	// marked failed on any error path and succeeded once the change set is
	// produced, so the execution is traceable end to end.
	e.setStage(ctx, initiativeID, "Agent running "+task.Code)
	agentRunID, err := e.db.CreateRun(ctx, domain.RunAgent, "", "implementer", initiativeID, "agent:"+task.Code)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	if note != "" {
		// D-a: "recorded on the run" (wave 3 contract) -- best-effort; a failure
		// here must never fail the execute itself.
		if err := e.db.SetRunNote(ctx, agentRunID, note); err != nil {
			e.log.Warn("could not record the rerun note on the run", "initiative", initiativeID, "run", agentRunID, "error", err)
		}
	}
	agentStart := time.Now()
	agentSucceeded := false
	defer func() {
		if !agentSucceeded {
			_ = e.db.SetRunStatus(context.Background(), agentRunID, domain.RunFailed, "implementer")
		}
	}()

	// Snapshot the isolated work root BEFORE the executor runs, so the
	// orchestrator computes the real diff itself — never trusting the executor's
	// self-declaration (review B3).
	// The agent works on a fresh copy of the workspace (see seed.go).
	seedSkipped, err := seedWorkRoot(ctx, e.workspaceRoot, req.WorkRoot, e.detectCredential)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	before, err := fsdiff.Snapshot(req.WorkRoot)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: snapshot before: %w", err)
	}
	// Continue a stopped run of this task, if one left files (runstate.go).
	// After the before snapshot, so what is carried is part of the diff.
	carried := e.resumePartial(initiativeID, task.ID, req.WorkRoot)

	attempt, err := exec.Start(ctx, req)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: %w", err)
	}
	if t, ok := exec.(executor.OutputTailer); ok {
		e.setLive(initiativeID, t, attempt.ID)
	}
	// Cancel the attempt on any early-exit path so a hung agent never leaks a
	// running container/subprocess (review B6). Collect already cleans up on the
	// success path, so this is a harmless no-op there.
	completed := false
	defer func() {
		if !completed {
			_ = exec.Cancel(context.Background(), attempt.ID)
		}
	}()
	if err := waitForCompletion(ctx, exec, attempt.ID); err != nil {
		err = e.withAgentOutput(ctx, exec, attempt.ID, err)
		// Keep what the agent wrote, so Run again continues from it -- but
		// only once its container is confirmed gone, so nothing is still
		// writing. waitForCompletion has usually removed it already, and
		// then Cancel reports the attempt unknown; any other Cancel error
		// means it may still be running, and nothing is kept.
		cerr := exec.Cancel(context.WithoutCancel(ctx), attempt.ID)
		completed = cerr == nil || errors.Is(cerr, executor.ErrUnknownAttempt)
		if completed {
			e.capturePartial(ctx, initiativeID, task.ID, req.WorkRoot, before)
		} else {
			// review defect F: without this, a Stop whose cleanup could not
			// confirm the container was gone left NO log line at all -- not
			// even a warning -- making "did it try to keep my files" as
			// unanswerable from the log as capturePartial's own "nothing to
			// keep" case (fixed in capturePartial itself). cerr is the real
			// reason nothing was attempted: the container may still be
			// running and writing, so it would be unsafe to snapshot it.
			e.log.Warn("stopped run: could not confirm the agent's container is gone; its files were not kept",
				"initiative", initiativeID, "task", task.ID, "err", cerr)
		}
		return domain.ChangeSet{}, err
	}
	// waitForCompletion succeeded: the agent's process has exited, and
	// req.WorkRoot (a bind mount, not something Collect touches) already
	// holds everything it will ever write, whatever happens next --
	// Collect's own docker-rm cleanup, the snapshot/sweep, or CreateChangeSet
	// below. Before this fix, capturePartial ran ONLY on the
	// waitForCompletion-failed branch above; a Stop (or a bare Collect
	// failure, or any ctx-bound call erroring on the way to CreateChangeSet)
	// anywhere in the rest of this function returned with nothing captured,
	// and the NEXT Execute's seedWorkRoot unconditionally
	// os.RemoveAll(workRoot)s before copying in a fresh copy -- silently
	// discarding exactly the files this exists to save. Deferred, so every
	// return path below is covered without hand-picking each one; the flag
	// skips it once CreateChangeSet actually succeeds, since the content is
	// durably persisted from that point on (blob store + the change_set row
	// itself, recoverable via ReconcileChangeSets if IT is then interrupted).
	changeSetPersisted := false
	defer func() {
		if !changeSetPersisted {
			e.capturePartial(ctx, initiativeID, task.ID, req.WorkRoot, before)
		}
	}()

	// Read before Collect, which removes the container and its output with it.
	// Hermes exits 0 even when every model call failed, so a clean exit that
	// changed nothing is the common failure, and this is the only record of why.
	// The same 100 lines the live view showed, kept on the change set.
	agentOutput := e.agentOutputLines(ctx, exec, attempt.ID, liveOutputLines)
	if _, err := exec.Collect(ctx, attempt.ID); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: %w", err)
	}
	completed = true
	e.markLiveDone(initiativeID, agentOutput)

	// Snapshot AFTER and compute the orchestrator-authored diff. The agent's
	// container had network egress, so what it wrote back is untrusted:
	// afterRunSnapshotOptions applies the workspace's own .gitignore and a
	// sanity cap on top of fsdiff's always-on default exclusions (review
	// defect #2).
	after, err := fsdiff.Snapshot(req.WorkRoot, e.afterRunSnapshotOptions()...)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: snapshot after: %w", err)
	}
	files := fsdiff.Diff(before, after)

	// Sweep the produced content BEFORE persisting it: if it carries a real
	// credential, we reject without ever writing the raw bytes to the
	// redaction-free raw store (review: raw-blob GC for rejected patches).
	//
	// Vocabulary markers (auth, password, token, secret, network, payment, pii
	// -- word-boundary matches, not value inspection) no longer block on their
	// own (review D2/A1): risk.RiskSignals still runs and its result still
	// surfaces as a non-blocking "sensitive area" finding below, but only a
	// detected credential VALUE gates persistence and rejection now.
	diffText := e.renderDiffTextFromWorkRoot(req.WorkRoot, files)
	signals := risk.RiskSignals(ctx, "", diffText)
	allowlist, err := e.secretAllowlist(ctx)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	credMatches := filterAllowlisted(e.scanWorkRootForCredentials(req.WorkRoot, files), allowlist)
	// blockingMatches gates rejection; advisoryMatches (a generic
	// "credential assignment" match found in a test/fixture path) never does
	// (ICP re-run defect #1) but is still recorded once the change set
	// exists, below.
	blockingMatches, advisoryMatches := splitCredentialMatches(credMatches)
	hasCredential := len(blockingMatches) > 0

	// Persist each produced file's content and the patch manifest to the blob
	// store, so apply/revert can reconstruct the diff from digests alone. Skip
	// the raw content write when a credential was detected, so rejected content
	// is never written to the raw store. hasBlockingRiskSignal is always false
	// (review D2/A1) but stays in this expression, not inlined away, so the one
	// guard that used to hard-reject on a bare word is still one place to
	// mutation-check: restore its old body and TestExecuteDoesNotRejectOnSecretMarkerAlone fails.
	sweepBlocked := hasCredential || hasBlockingRiskSignal(signals)
	if !sweepBlocked {
		if err := e.persistDiff(req.WorkRoot, files); err != nil {
			return domain.ChangeSet{}, fmt.Errorf("workflow: execute: persist diff: %w", err)
		}
	} else {
		// The raw bytes are never written (the whole point of the gate), but the
		// human reviewing the denial still needs to see what was blocked -- with
		// every detected secret span masked, never the raw value. Without this,
		// the diff viewer had nothing at all to show for a denied change set, even
		// after the human dispositioned the finding "not a secret" (review:
		// secret-sweep diff visibility). Best-effort: a masking failure must not
		// turn a correctly-denied change set into a hard error, so it only logs.
		if err := e.persistRedactedDiff(req.WorkRoot, files); err != nil {
			e.log.Warn("could not persist masked diff for denied change set", "error", err)
		}
	}
	patchDigest, err := e.persistPatch(fsdiff.Patch{BaseSnapshotDigest: before.Digest(), Files: files})
	if err != nil {
		return domain.ChangeSet{}, err
	}

	// Computed here (config-derived, known before the agent ever ran) so it can
	// be stored on the change set at creation, not just on the run manifest --
	// CreatedByRunID above is the EXECUTOR's attempt id, not the domain Run id
	// CreateRunManifest keys its RunID on, so the two cannot be joined to
	// recover it later.
	modelSpec := e.implementerModel()
	cs := &domain.ChangeSet{
		InitiativeID:   initiativeID,
		TaskID:         task.ID,
		BaseSnapshotID: before.Digest(),
		PatchDigest:    patchDigest,
		CreatedByRunID: attempt.ID,
		Model:          displayModel(modelSpec),
		Executor:       exec.Name(),
		AgentOutput:    agentOutput,
		// ICP re-run defect #2: `files` is the full before/after diff, so a
		// zero-length one means this change set touches nothing at all --
		// whatever else it goes on to do (checks/review/approve/apply are all
		// still allowed, unchanged), it must never be counted as implementing
		// task.ID. See domain.ChangeSet.ImplementsTask.
		NoOp: len(files) == 0,
	}
	// CreateChangeSet forces status=draft and assigns CHG-N.
	if err := e.db.CreateChangeSet(ctx, cs); err != nil {
		return domain.ChangeSet{}, err
	}
	// The work is now durably on the change set (and the blob store, above):
	// capturePartial's on-disk fallback is no longer the only record of it.
	changeSetPersisted = true
	if err := e.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, store.ActorOrchestrator); err != nil {
		return domain.ChangeSet{}, err
	}

	// Run the real deterministic checks (B5) and record their Findings. A
	// failing check is a blocking finding and will reject the change set below.
	// Skipped when a credential was already detected (the raw content was not
	// persisted, so patch-applies would spuriously fail, and the change set is
	// rejected on the secret sweep regardless).
	checksBlocked := false
	if !sweepBlocked {
		e.setStage(ctx, initiativeID, "Running checks")
		checks, blocked := e.runDeterministicChecks(ctx, cs, req.WorkRoot, files)
		checksBlocked = blocked
		for _, f := range checks {
			f := f
			// The build/test command output is attacker-influenced (agent-produced
			// code runs in the sandbox). Sweep the RAW output FIRST so a secret
			// printed by the agent's code is rejected, then redact for persistence.
			// review M2: this now runs the SAME credential catalog + allowlist
			// filter file content gets (e.scanTextForCredentials + filterAllowlisted),
			// not a bare detectCredential boolean with no override path -- so a
			// value a human already allowlisted (e.g. a test fixture the test
			// runner prints back) does not deny the change set here even though it
			// would still deny on first sight. A bare vocabulary marker in it does
			// not block either (review D2/A1); hasBlockingRiskSignal is always
			// false, kept for the same mutation-check reason as its other call
			// sites.
			checkOutputPath := "output of " + f.Category
			rawMatches := filterAllowlisted(e.scanTextForCredentials(checkOutputPath, f.Evidence), allowlist)
			rawBlocking, rawAdvisory := splitCredentialMatches(rawMatches)
			rawCred := len(rawBlocking) > 0
			rawSignals := risk.RiskSignals(ctx, "", f.Evidence)
			if rawCred || hasBlockingRiskSignal(rawSignals) {
				f.Code = sweepDenyCode(rawCred)
				f.Severity = domain.SeverityHigh
				checksBlocked = true
			}
			f.Finding = e.redact(f.Finding)
			f.Evidence = e.redact(f.Evidence)
			f.Impact = e.redact(f.Impact)
			f.Recommendation = e.redact(f.Recommendation)
			if err := e.db.CreateFinding(ctx, &f); err != nil {
				return domain.ChangeSet{}, err
			}
			// One dedicated secret_sweep finding per surviving match, named
			// "in the output of <check>" (review M2), mirroring the execute-time
			// and apply-time sweeps: never the value, a masked-line Evidence, and
			// SecretPath/SecretFingerprint/SecretOverridable/SecretPreview set so
			// the finding is independently dispositionable (and allowlistable)
			// the same way a file-content match is.
			for _, m := range rawBlocking {
				if err := e.db.CreateFinding(ctx, &domain.Finding{
					InitiativeID:      cs.InitiativeID,
					TargetType:        "change_set",
					TargetID:          cs.ID,
					Severity:          domain.SeverityCritical,
					Confidence:        domain.ConfidenceHigh,
					Category:          "secret_sweep",
					Code:              sweepDenyCode(true),
					Finding:           fmt.Sprintf("in the output of %s: looks like %s", f.Category, m.Kind),
					Evidence:          e.redact(secretSweepEvidence(m)),
					Impact:            "change set rejected before review",
					Recommendation:    secretSweepRecommendation(m),
					Disposition:       domain.DispositionRejected,
					SecretFingerprint: m.Fingerprint,
					SecretKind:        m.Kind,
					SecretPath:        checkOutputPath,
					SecretOverridable: m.Overridable,
					SecretPreview:     m.Preview,
				}); err != nil {
					return domain.ChangeSet{}, err
				}
			}
			for _, m := range rawAdvisory {
				if err := e.createAdvisorySecretFinding(ctx, cs.InitiativeID, cs.ID, m); err != nil {
					return domain.ChangeSet{}, err
				}
			}
		}
	}

	// A coding agent that changed nothing is reported, not failed. Hermes exits
	// 0 after a failed model call, so this is the common failure, and its own
	// output is the only record of why -- so it goes on the change set, where
	// the review shows it. A hard failure here was tried and stranded
	// initiatives: the work root carries every earlier attempt, so an agent
	// that correctly finds a task already done has nothing to change, and
	// failing it made the task impossible to get past.
	// What the agent itself changed, not counting files carried from a
	// stopped run and left as carried: a Continue whose model call failed
	// would otherwise show only the carried files, with no warning, and read
	// as a finished task.
	if _, isAgent := exec.(executor.OutputTailer); isAgent && len(carried.agentChanged(files)) == 0 {
		evidence := agentOutput
		if evidence == "" {
			evidence = "(the agent printed nothing)"
		}
		f := domain.Finding{
			InitiativeID:   cs.InitiativeID,
			TargetType:     "change_set",
			TargetID:       cs.ID,
			Severity:       domain.SeverityMedium,
			Confidence:     domain.ConfidenceHigh,
			Category:       "agent_output",
			Finding:        "The coding agent finished without changing any file.",
			Evidence:       evidence,
			Impact:         "This change set is empty. If the task was not already done, the agent's model call most likely failed.",
			Recommendation: "Read the agent output. A 401 means the provider key is wrong; a 404 means the model id is retired. Fix it and retry, or approve the empty change set if the task was already done.",
		}
		if err := e.db.CreateFinding(ctx, &f); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// The change set now carries the kept files, so the record can go.
	if carried != nil {
		_ = os.Remove(e.partialPath(initiativeID))
	}
	if f := e.carriedFinding(cs, carried); f != nil {
		if err := e.db.CreateFinding(ctx, f); err != nil {
			return domain.ChangeSet{}, err
		}
	}
	if len(seedSkipped) > 0 {
		f := domain.Finding{
			InitiativeID:   cs.InitiativeID,
			TargetType:     "change_set",
			TargetID:       cs.ID,
			Severity:       domain.SeverityObservation,
			Confidence:     domain.ConfidenceHigh,
			Category:       "workspace_copy",
			Finding:        fmt.Sprintf("%d project file(s) were not given to the coding agent.", len(seedSkipped)),
			Evidence:       seedSkipSummary(seedSkipped),
			Impact:         "The agent could not read or build against these files.",
			Recommendation: "If the task needed one of them, move the secret it holds into .env (which is never copied) and retry.",
		}
		if err := e.db.CreateFinding(ctx, &f); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// The vocabulary marker scan is advisory only now (review D2/A1): it never
	// blocks, but it still surfaces to the reviewer as a non-blocking "sensitive
	// area" finding -- the same signals that used to hard-reject on a bare word
	// match now just flag the change set for a closer look.
	if err := e.createSensitiveAreaFinding(ctx, cs.InitiativeID, cs.ID, signals, "deterministic marker scan of the produced patch"); err != nil {
		return domain.ChangeSet{}, err
	}

	// A generic "credential assignment" match in a test/fixture path is
	// downgraded to advisory (ICP re-run defect #1): visible to the reviewer,
	// never gating rejection, whatever else the change set does below.
	for _, m := range advisoryMatches {
		if err := e.createAdvisorySecretFinding(ctx, cs.InitiativeID, cs.ID, m); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// A real credential sweep gates rejection now, not a bare vocabulary word
	// (review D2/A1). blockingMatches was computed above from the work-root
	// content, BEFORE the raw store write, and already excludes any fingerprint
	// a human dispositioned "not a secret" for this project (and any
	// test/fixture-path advisory match, split out above).
	rejected := false
	if checksBlocked || hasCredential || hasBlockingRiskSignal(signals) {
		rejected = true
		// One finding per match, each naming path:line and the kind of secret --
		// never the value (full-platform review: secret leak) -- so the denial
		// says where, instead of a bare "blocking risk signal detected".
		for _, m := range blockingMatches {
			if err := e.db.CreateFinding(ctx, &domain.Finding{
				InitiativeID:      cs.InitiativeID,
				TargetType:        "change_set",
				TargetID:          cs.ID,
				Severity:          domain.SeverityCritical,
				Confidence:        domain.ConfidenceHigh,
				Category:          "secret_sweep",
				Code:              sweepDenyCode(true),
				Finding:           fmt.Sprintf("%s:%d: looks like %s", m.Path, m.Line, m.Kind),
				Evidence:          e.redact(secretSweepEvidence(m)),
				Impact:            "change set rejected before review",
				Recommendation:    secretSweepRecommendation(m),
				Disposition:       domain.DispositionRejected,
				SecretFingerprint: m.Fingerprint,
				SecretKind:        m.Kind,
				SecretPath:        m.Path,
				SecretOverridable: m.Overridable,
				SecretPreview:     m.Preview,
			}); err != nil {
				return domain.ChangeSet{}, err
			}
		}
		if err := e.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
			return domain.ChangeSet{}, err
		}
	} else {
		if err := e.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, store.ActorOrchestrator); err != nil {
			return domain.ChangeSet{}, err
		}
		// ICP re-run defect A: the adversarial reviewer used to run again on
		// every `idea review <CHG-n> approve` call -- a fresh model call (15s
		// - 2.5min, real spend: 64% of model spend on the run that found
		// this) that persisted a fresh set of findings every time, with no
		// memory of what a human had already dispositioned. A finding a
		// human marked rejected was immediately replaced by a reworded
		// near-duplicate of the same severity, so approval never converged
		// without --accept-findings. A change set's content is fixed the
		// moment it is created here -- PatchDigest is never rewritten after
		// this call (see runtime invariant: CreateChangeSet forces draft,
		// and nothing in this package assigns a new patch to an existing
		// change set id) -- so there is exactly one point where reviewing it
		// is meaningful: right here, once, before a human ever sees it.
		// reviewChangeSet's "approved" path (below) now only evaluates these
		// findings plus whatever the human has since dispositioned; it never
		// calls the model again.
		runID, findings, _, err := e.runChangeSetAdversarialReview(ctx, cs)
		if err != nil {
			return domain.ChangeSet{}, err
		}
		// Carry a human's disposition of a near-duplicate PRIOR finding (same
		// target type, path and near-identical text) forward onto this fresh
		// batch, so a finding the model merely rewords does not silently
		// escape a judgment call the human already made on an earlier change
		// set of this same initiative (dedup.go).
		if err := e.inheritDispositionFromPrior(ctx, initiativeID, findings); err != nil {
			return domain.ChangeSet{}, err
		}
		for i := range findings {
			findings[i].InitiativeID = initiativeID
			findings[i].TargetType = "change_set"
			findings[i].TargetID = cs.ID
			if err := e.db.CreateFinding(ctx, &findings[i]); err != nil {
				return domain.ChangeSet{}, err
			}
		}
		if err := e.db.SetChangeSetReview(ctx, cs.ID, runID, store.ActorOrchestrator); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	switch {
	case rejected:
		// Re-open the task so a re-execute produces a fresh change set; leaving it
		// done with only a rejected change set would permanently block its
		// dependents (full-platform review: task-dependency deadlock).
		if err := e.db.SetTaskStatus(ctx, task.ID, domain.TaskProposed); err != nil {
			return domain.ChangeSet{}, err
		}
	case cs.NoOp:
		// C4: domain.ChangeSet.ImplementsTask() is "applied AND !NoOp" -- an
		// applied NoOp change set NEVER satisfies it. Marking the task done
		// here, as the non-NoOp case below does, would deadlock it (and every
		// dependent behind it) forever: nextReadyTask only re-picks
		// ready/proposed tasks, taskDependenciesApplied requires an
		// implementing change set from every predecessor, and there would be
		// no way back short of editing the database directly (the exact
		// defect this comment's sibling already guards against for the
		// `rejected` case, above). Leave it proposed instead, same as
		// rejected. This alone would let a fresh Execute immediately re-pick
		// the task while THIS change set is still open (unreviewed/
		// unapplied) -- the same-task guard at the top of Execute (see
		// "still open" below) refuses that until the change set reaches a
		// terminal disposition (rejected, superseded, applied, reverted or
		// conflicted).
		if err := e.db.SetTaskStatus(ctx, task.ID, domain.TaskProposed); err != nil {
			return domain.ChangeSet{}, err
		}
	default:
		// Mark done so a repeated Execute does not re-pick the same task and
		// create a duplicate change set (it is now awaiting review/apply).
		if err := e.db.SetTaskStatus(ctx, task.ID, domain.TaskDone); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// Record the implementer run's provenance + success (review B7). modelSpec
	// was already resolved above, when the change set itself was created.
	//
	// context.WithoutCancel: by this point the agent has already run --
	// real spend already incurred with the provider, whatever this run's
	// cost accounting has counted against it -- and its change set already
	// exists in the DB (CreateChangeSet, above). A Stop landing exactly here
	// used to fail this write with the live, cancellable ctx: Execute
	// returned an error, the deferred "on any error, if !agentSucceeded, mark
	// the run failed" fired (via its own context.Background(), so IT always
	// lands), and the result was a run left permanently `failed` with no
	// manifest/cost record at all for an attempt that, in fact, completed and
	// already produced a real change set -- the spend record lost even
	// though the spend already happened. A fresh, uncancellable context
	// mirrors MarkChangeSetApplied's own "cannot strand a fully-written
	// [result]" reasoning a few calls away.
	runCtx := context.WithoutCancel(ctx)
	if err := e.db.CreateRunManifest(runCtx, &domain.RunManifest{
		RunID:          agentRunID,
		RequestedModel: modelSpec,
		ActualModel:    modelSpec,
		Usage:          domain.RunUsage{DurationMS: time.Since(agentStart).Milliseconds()},
		ResultDigest:   patchDigest,
		StartedAt:      agentStart,
		FinishedAt:     time.Now().UTC(),
	}); err != nil {
		return domain.ChangeSet{}, err
	}
	if err := e.db.SetRunStatus(runCtx, agentRunID, domain.RunSucceeded, "implementer"); err != nil {
		return domain.ChangeSet{}, err
	}
	agentSucceeded = true

	got, err := e.db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	return *got, nil
}

// persistDiff persists each produced file's content to the blob store (keyed by
// digest), so apply/revert can reconstruct the diff from digests alone.
func (e *Engine) persistDiff(workRoot string, files []domain.FileChange) error {
	if e.contentBlobs() == nil {
		return fmt.Errorf("workflow: no blob store")
	}
	for _, fc := range files {
		if fc.IsDelete() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workRoot, filepath.FromSlash(fc.Path)))
		if err != nil {
			return fmt.Errorf("read %q: %w", fc.Path, err)
		}
		if _, err := e.contentBlobs().Put(data); err != nil {
			return fmt.Errorf("persist %q: %w", fc.Path, err)
		}
	}
	return nil
}

// persistRedactedDiff stores each produced file's content with every detected
// secret span masked (never the raw value), keyed by the digest of the MASKED
// bytes, and records that digest on the FileChange as RedactedDigest -- so a
// change set the secret sweep denies still has something honest to show in
// the diff viewer instead of nothing at all. files is mutated in place: it is
// the same slice that goes on to be persisted as the patch manifest, so the
// RedactedDigest set here survives on the record the diff endpoint reads back.
func (e *Engine) persistRedactedDiff(workRoot string, files []domain.FileChange) error {
	if e.contentBlobs() == nil {
		return fmt.Errorf("workflow: no blob store")
	}
	for i, fc := range files {
		if fc.IsDelete() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workRoot, filepath.FromSlash(fc.Path)))
		if err != nil {
			return fmt.Errorf("read %q: %w", fc.Path, err)
		}
		masked := e.redactOrFallback(string(data))
		digest, err := e.contentBlobs().Put([]byte(masked))
		if err != nil {
			return fmt.Errorf("persist masked %q: %w", fc.Path, err)
		}
		files[i].RedactedDigest = digest
	}
	return nil
}

// redactOrFallback masks known secret values and every credential-pattern
// match in s, using the engine's per-project redactor when one is configured
// and the package-level (unkeyed) fallback otherwise -- the same pattern
// RenderPatch and StructuredDiff already use at their display surface.
func (e *Engine) redactOrFallback(s string) string {
	if e.redactor != nil {
		return e.redactor.RedactString(s)
	}
	return security.Redact(s)
}

// persistPatch stores the patch manifest JSON and returns its content digest.
func (e *Engine) persistPatch(patch fsdiff.Patch) (string, error) {
	data, err := json.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("workflow: marshal patch: %w", err)
	}
	if e.contentBlobs() == nil {
		return fsdiff.Patch{BaseSnapshotDigest: patch.BaseSnapshotDigest, Files: patch.Files}.Digest(), nil
	}
	return e.contentBlobs().Put(data)
}

// renderDiffText renders the file contents of a diff as one readable string (for
// the deterministic secret sweep).
// detectCredential reports whether s contains a known secret value or a
// credential pattern, using the engine's redactor (nil-safe fallback to the
// pattern-only detector). The matched value is intentionally NOT returned so it
// cannot leak into a finding.
func (e *Engine) detectCredential(s string) bool {
	if e.redactor != nil {
		return e.redactor.ContainsCredential(s)
	}
	return security.FindCredential(s) != ""
}

// secretAllowlist loads the project's "not a secret" fingerprints (review
// changes #1), so a sweep can ignore a value a human already dispositioned.
// Detection runs per file from the work root or the persisted patch, so unlike
// the pre-fix whole-blob scan, the location survives into the finding it denies
// with (§ D2, review #7).
func (e *Engine) secretAllowlist(ctx context.Context) (map[string]bool, error) {
	if e.db == nil {
		return nil, nil
	}
	return e.db.SecretAllowlistFingerprints(ctx)
}

// scanForCredentials runs every file in files through the credential detector
// (the redactor's configured secret values plus the pattern catalog),
// returning each match located by path and line -- never the value. read
// supplies one file's content; a file that cannot be read (or a delete) is
// skipped, matching the sweep's existing "read what was produced" behaviour.
func (e *Engine) scanForCredentials(files []domain.FileChange, read func(domain.FileChange) (string, bool)) []security.CredentialFinding {
	var out []security.CredentialFinding
	for _, fc := range files {
		if fc.IsDelete() {
			continue
		}
		content, ok := read(fc)
		if !ok {
			continue
		}
		if e.redactor != nil {
			out = append(out, e.redactor.FindKnownInFile(fc.Path, content)...)
		}
		// e.redactor.FindPatternsInFile is nil-safe and, when e.redactor is set,
		// fingerprints with this project's own HMAC key (review H3) rather than
		// the package-level FindCredentialsInFile's unkeyed fallback.
		out = append(out, e.redactor.FindPatternsInFile(fc.Path, content)...)
	}
	return dedupeCredentialFindings(out)
}

// dedupeCredentialFindings collapses matches to at most one per (path, line,
// fingerprint) (review low-priority item): the SAME value at the SAME
// location can match more than one pattern in the catalog (or both a known
// value and a pattern -- e.g. a configured secret whose value also happens
// to look like an AWS key), and showing it twice is pure noise for the
// reviewer. The first occurrence wins: FindKnownInFile's known-value matches
// (Overridable=false) are always appended before FindPatternsInFile's, so a
// known-value match is kept over a pattern match for the identical
// (path, line, fingerprint) rather than the other way around.
func dedupeCredentialFindings(matches []security.CredentialFinding) []security.CredentialFinding {
	if len(matches) < 2 {
		return matches
	}
	seen := make(map[string]bool, len(matches))
	out := make([]security.CredentialFinding, 0, len(matches))
	for _, m := range matches {
		key := fmt.Sprintf("%s\x00%d\x00%s", m.Path, m.Line, m.Fingerprint)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return out
}

// scanWorkRootForCredentials is scanForCredentials reading from the isolated
// work root, for the execute-time sweep (before anything is persisted).
func (e *Engine) scanWorkRootForCredentials(workRoot string, files []domain.FileChange) []security.CredentialFinding {
	return e.scanForCredentials(files, func(fc domain.FileChange) (string, bool) {
		content, err := os.ReadFile(filepath.Join(workRoot, filepath.FromSlash(fc.Path)))
		if err != nil {
			return "", false
		}
		return string(content), true
	})
}

// scanTextForCredentials runs the same credential detector scanForCredentials
// uses (known values + pattern catalog, keyed by this project's redactor) over
// a single block of raw text under a synthetic "path" -- for content that
// never was a file, such as a build/test command's captured output (review
// M2). Using the same detector and the same allowlist scoping as file content
// means a value a human already allowlisted at this same synthetic path
// (i.e. dispositioned not_a_secret on a prior "in the output of <check>"
// finding) is recognized here exactly as it would be for a real file.
func (e *Engine) scanTextForCredentials(path, content string) []security.CredentialFinding {
	var out []security.CredentialFinding
	if e.redactor != nil {
		out = append(out, e.redactor.FindKnownInFile(path, content)...)
	}
	out = append(out, e.redactor.FindPatternsInFile(path, content)...)
	return dedupeCredentialFindings(out)
}

// scanBlobsForCredentials is scanForCredentials reading each file's content
// from the content-addressed blob store, for the apply-time re-sweep (the
// work root is long gone by then; only the persisted patch remains).
func (e *Engine) scanBlobsForCredentials(files []domain.FileChange) []security.CredentialFinding {
	return e.scanForCredentials(files, func(fc domain.FileChange) (string, bool) {
		if e.contentBlobs() == nil {
			return "", false
		}
		content, err := e.contentBlobs().Get(fc.Digest)
		if err != nil {
			return "", false
		}
		return string(content), true
	})
}

// splitCredentialMatches separates a credential scan's matches into the ones
// that gate rejection (blocking) and the ones downgraded to advisory-only
// (ICP re-run defect #1): a generic "credential assignment" match found in a
// test/fixture path (security.CredentialFinding.Advisory) is shown to the
// reviewer but never blocks -- a provider-specific format or a configured
// secret value is never Advisory, so it always ends up in blocking, in a
// test path or not.
func splitCredentialMatches(matches []security.CredentialFinding) (blocking, advisory []security.CredentialFinding) {
	for _, m := range matches {
		if m.Advisory {
			advisory = append(advisory, m)
			continue
		}
		blocking = append(blocking, m)
	}
	return blocking, advisory
}

// createAdvisorySecretFinding records a non-blocking secret_sweep finding for
// a match downgraded to advisory (ICP re-run defect #1): visible to the
// reviewer -- Finding.Advisory makes review.EvaluateApproval and
// denyReviewOpenBlocking skip it outright, whatever its severity -- and still
// dispositionable not_a_secret like any other overridable pattern match.
func (e *Engine) createAdvisorySecretFinding(ctx context.Context, initiativeID, targetID string, m security.CredentialFinding) error {
	return e.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID:   initiativeID,
		TargetType:     "change_set",
		TargetID:       targetID,
		Severity:       domain.SeverityHigh,
		Confidence:     domain.ConfidenceHigh,
		Category:       "secret_sweep",
		Finding:        fmt.Sprintf("%s: looks like %s (advisory: test/fixture code)", m.Path, m.Kind),
		Evidence:       e.redact(secretSweepEvidence(m)),
		Impact:         "shown for review; does not block a test/fixture-only match",
		Recommendation: secretSweepRecommendation(m),
		// Advisory (not a lower Severity alone) is what actually keeps this
		// from blocking (review.EvaluateApproval / denyReviewOpenBlocking
		// both skip an Advisory finding outright, §11/§15) -- Severity stays
		// High so the reviewer still sees it as a real "looks like a secret"
		// signal, just a non-gating one.
		Advisory:          true,
		SecretFingerprint: m.Fingerprint,
		SecretKind:        m.Kind,
		SecretPath:        m.Path,
		SecretOverridable: m.Overridable,
		SecretPreview:     m.Preview,
	})
}

// filterAllowlisted drops every Overridable match whose (fingerprint, path)
// a human already dispositioned "not a secret" for this project (review H3:
// scoped to path too, not fingerprint alone, so the same value allowlisted
// in one file -- e.g. a test fixture -- still blocks in another). A match
// drawn from the project's own configured secrets (Overridable=false) is
// never filtered: a real, resolved credential is not eligible for the
// override regardless of what fingerprint+path someone else allowlisted, so
// even a coincidental fingerprint collision with an allowlisted pattern match
// can never let a configured secret value through.
func filterAllowlisted(matches []security.CredentialFinding, allowed map[string]bool) []security.CredentialFinding {
	if len(allowed) == 0 {
		return matches
	}
	out := make([]security.CredentialFinding, 0, len(matches))
	for _, m := range matches {
		if m.Overridable && allowed[store.AllowlistKey(m.Fingerprint, m.Path)] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// secretSweepEvidence is the Evidence text for a secret_sweep finding (review
// H3): the source line with the secret value itself replaced by its masked
// preview, so a reviewer can see WHERE and roughly WHAT without the raw
// value ever being persisted or returned. Falls back to a generic note when
// MaskedLine could not be computed (should not normally happen).
func secretSweepEvidence(m security.CredentialFinding) string {
	if m.MaskedLine != "" {
		return m.MaskedLine
	}
	return "deterministic scan of the produced patch"
}

// secretSweepRecommendation is the Recommendation text for a credential
// finding: an overridable (pattern-shaped) match gets a route out for a false
// positive, a configured-secret-value match does not (review changes #1 --
// that override exists for shapes that might be false positives, never for a
// value the project itself resolved as a real secret).
func secretSweepRecommendation(m security.CredentialFinding) string {
	if m.Overridable {
		return "remove the sensitive content and re-execute, or, if this is not really a secret, " +
			"disposition this finding \"not a secret\" to allow it from now on"
	}
	return "remove the sensitive content and re-execute"
}

// renderDiffTextFromWorkRoot renders the produced file contents directly from
// the work root (before they are persisted to the blob store), so the secret
// sweep can run BEFORE persistDiff and rejected secret content is never written
// to the redaction-free raw store (review: raw-blob GC).
func (e *Engine) renderDiffTextFromWorkRoot(workRoot string, files []domain.FileChange) string {
	var b strings.Builder
	for _, fc := range files {
		if fc.IsDelete() {
			fmt.Fprintf(&b, "delete %s\n", fc.Path)
			continue
		}
		content, err := os.ReadFile(filepath.Join(workRoot, filepath.FromSlash(fc.Path)))
		if err != nil {
			fmt.Fprintf(&b, "%s (unreadable)\n", fc.Path)
			continue
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n", fc.Path, string(content))
	}
	return b.String()
}

func (e *Engine) renderDiffText(files []domain.FileChange) string {
	if e.contentBlobs() == nil {
		return ""
	}
	var b strings.Builder
	for _, fc := range files {
		if fc.IsDelete() {
			fmt.Fprintf(&b, "delete %s\n", fc.Path)
			continue
		}
		content, err := e.contentBlobs().Get(fc.Digest)
		if err != nil {
			if fc.RedactedDigest != "" {
				if masked, merr := e.contentBlobs().Get(fc.RedactedDigest); merr == nil {
					fmt.Fprintf(&b, "--- %s (content withheld because it contained a secret; showing masked) ---\n%s\n", fc.Path, string(masked))
					continue
				}
			}
			fmt.Fprintf(&b, "%s (unreadable)\n", fc.Path)
			continue
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n", fc.Path, string(content))
	}
	return b.String()
}

// loadPatch reads and decodes a change set's patch manifest.
func (e *Engine) loadPatch(cs *domain.ChangeSet) (*fsdiff.Patch, error) {
	if cs.PatchDigest == "" || e.contentBlobs() == nil {
		return &fsdiff.Patch{}, nil
	}
	data, err := e.contentBlobs().Get(cs.PatchDigest)
	if err != nil {
		return nil, fmt.Errorf("workflow: load patch: %w", err)
	}
	var p fsdiff.Patch
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("workflow: decode patch: %w", err)
	}
	return &p, nil
}

// BackfillNoOpChangeSets recomputes no_op for every change set migration 34
// left at its conservative default of false (review MEDIUM #1). The
// migration's comment argued file counts "aren't recomputable" -- they are:
// the patch manifest was persisted unconditionally before no_op existed, so
// len(loadPatch(cs).Files) == 0 is one blob read per row. The recompute
// doesn't happen IN the migration because a migration is plain SQL text
// (store.migrations) with no access to the blob store; this runs once, at
// daemon start, after both the DB and the raw blob store exist (see
// runtime.go's EnsureProject), gated by store.ClaimNoOpBackfill's persisted
// marker so it costs one query per project on every later start rather than
// a full table scan.
//
// A change set whose patch can no longer be read (a blob evicted or
// corrupted independently of this fix) is left at no_op=false -- exactly
// migration 34's own fallback for a row it cannot recompute -- rather than
// failing the whole backfill.
func (e *Engine) BackfillNoOpChangeSets(ctx context.Context) (int, error) {
	if e.db == nil {
		return 0, nil
	}
	claimed, err := e.db.ClaimNoOpBackfill(ctx)
	if err != nil {
		return 0, fmt.Errorf("workflow: claim no_op backfill: %w", err)
	}
	if !claimed {
		return 0, nil
	}
	refs, err := e.db.ListChangeSetsForNoOpBackfill(ctx)
	if err != nil {
		return 0, fmt.Errorf("workflow: list no_op backfill candidates: %w", err)
	}
	fixed := 0
	for _, ref := range refs {
		patch, err := e.loadPatch(&domain.ChangeSet{PatchDigest: ref.PatchDigest})
		if err != nil {
			if e.log != nil {
				e.log.Warn("no_op backfill: could not load patch, leaving no_op=false", "change_set", ref.ID, "err", err)
			}
			continue
		}
		if len(patch.Files) != 0 {
			continue
		}
		if err := e.db.SetChangeSetNoOp(ctx, ref.ID, true); err != nil {
			return fixed, fmt.Errorf("workflow: backfill no_op for %s: %w", ref.ID, err)
		}
		fixed++
	}
	return fixed, nil
}

// RenderPatch renders a change set's patch as a readable diff (path + content
// per file) for the adversarial reviewer and the diff endpoint.
// RenderPatch renders a change set's patch as a readable diff (path + content
// per file) for the adversarial reviewer and the diff endpoint. It is REDACTED
// at this display surface so raw file content never reaches a network-egress
// model (review SRE regression); the raw store stays redaction-free for
// byte-for-byte apply/revert.
func (e *Engine) RenderPatch(cs *domain.ChangeSet) string {
	patch, err := e.loadPatch(cs)
	if err != nil {
		return fmt.Sprintf("(patch unreadable: %v)", err)
	}
	raw := e.renderDiffText(patch.Files)
	if e.redactor != nil {
		return e.redactor.RedactString(raw)
	}
	return security.Redact(raw)
}

// RenderPatchPage is RenderPatch bounded to at most maxDiffEndpointFiles
// files (the diff endpoint's variant; see maxDiffEndpointFiles). total is the
// change set's true file count, so a truncated response can say so.
func (e *Engine) RenderPatchPage(cs *domain.ChangeSet) (rendered string, total int) {
	patch, err := e.loadPatch(cs)
	if err != nil {
		return fmt.Sprintf("(patch unreadable: %v)", err), 0
	}
	total = len(patch.Files)
	files := patch.Files
	var note string
	if total > maxDiffEndpointFiles {
		files = files[:maxDiffEndpointFiles]
		note = fmt.Sprintf("\n… showing %d of %d files; the rest are omitted from this rendering.\n",
			maxDiffEndpointFiles, total)
	}
	raw := e.renderDiffText(files) + note
	if e.redactor != nil {
		return e.redactor.RedactString(raw), total
	}
	return security.Redact(raw), total
}

// StructuredFile is one file of a change set for per-file display: its path,
// add/modify/delete kind, byte size, and REDACTED content. Content is redacted
// at this display surface exactly like RenderPatch, so raw bytes never reach a
// network-egress model (review SRE regression); the raw store stays
// redaction-free for byte-for-byte apply/revert. The post-image digest is
// deliberately NOT exposed here — a digest is not needed for display and would
// be pure hash-confirmation surface.
type StructuredFile struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`
	Size       int    `json:"size"`
	Content    string `json:"content"`
	// Patch is a unified diff of the file against its base (B6): an added file
	// is all '+', a deleted file is all '-', a modified file is base vs new.
	// Omitted when there is nothing to diff (both sides empty). This is what
	// lets the diff view render a change instead of a full-file dump for a
	// whole-file rewrite of an existing file.
	Patch string `json:"patch,omitempty"`
	// PatchNote says why Patch is empty when there was a change to show: the
	// file is binary, too large to diff, its content was never kept, or its
	// base can no longer be recovered. The UI shows it and falls back to
	// Content.
	PatchNote string `json:"patch_note,omitempty"`
	// Redacted is true when Content holds MASKED content instead of the file's
	// real post-image bytes, because the secret sweep denied this change set
	// and the raw bytes were never persisted -- only their masked form was.
	// The UI shows a distinct, honest note instead of treating this like an
	// ordinary (or missing) diff. Review: secret-sweep diff visibility.
	Redacted bool `json:"redacted,omitempty"`

	// contentKept is whether the post-image was read from the store; an empty
	// file that was kept is not the same as content that never was.
	contentKept bool
}

// baseContent returns fc's REDACTED pre-change content. ok is false when the
// base cannot be recovered exactly.
//
// The base is read from the content blob store first -- fsdiff.Prepare
// persists the workspace's pre-image there keyed by digest, at apply time, for
// revert -- and falls back to the live workspace file otherwise, which is what
// makes a diff available at review time. The fallback is used only if the file
// still hashes to BaseDigest: a file the user edited after the agent ran would
// otherwise be diffed as the base, and the user's own edits would appear,
// reversed, as the agent's.
func (e *Engine) baseContent(fc domain.FileChange) (string, bool) {
	if fc.BaseDigest == "" {
		return "", true
	}
	var data []byte
	if e.contentBlobs() != nil {
		if d, err := e.contentBlobs().Get(fc.BaseDigest); err == nil {
			data = d
		}
	}
	if data == nil && e.workspaceRoot != "" {
		if d, err := os.ReadFile(filepath.Join(e.workspaceRoot, filepath.FromSlash(fc.Path))); err == nil && blob.Digest(d) == fc.BaseDigest {
			data = d
		}
	}
	if data == nil {
		return "", false
	}
	raw := string(data)
	if e.redactor != nil {
		return e.redactor.RedactString(raw), true
	}
	return security.Redact(raw), true
}

// patchFor renders fc's diff against its base as hunks, or explains why it
// cannot. sf carries fc's already-redacted post-image content and its size
// (Size is 0 when the content was never persisted, e.g. after the secret
// sweep).
func (e *Engine) patchFor(fc domain.FileChange, sf StructuredFile) (patch, note string) {
	if sf.Redacted {
		// Diffing masked content against a real base would show a wall of
		// spurious changes (or, worse, read as though the base itself changed) --
		// so this is display-only content, not a diff, and says so.
		return "", "content withheld because it contained a secret; showing masked"
	}
	newContent := sf.Content
	if fc.IsDelete() {
		newContent = ""
	} else if !sf.contentKept {
		return "", "The new content of this file was not kept, so there is no diff to show."
	}
	old, ok := e.baseContent(fc)
	if !ok {
		return "", "The file has changed in the workspace since the agent ran, so its original version is not available to diff; showing the new content."
	}
	if strings.ContainsRune(old, 0) || strings.ContainsRune(newContent, 0) {
		return "", "Binary file; no text diff."
	}
	if old == "" && newContent == "" {
		return "", ""
	}
	p, ok := textdiff.Hunks(old, newContent)
	if ok && p == "" && old != newContent {
		// splitLines ignores line endings and the final newline, so a change
		// to only those has no hunk -- and without a note the UI showed the
		// whole file with nothing to say why.
		return "", "Only line endings or the final newline changed."
	}
	if !ok {
		return "", fmt.Sprintf("Too large to diff (%d lines); showing the new content.",
			strings.Count(newContent, "\n")+1)
	}
	return p, ""
}

// StructuredDiff renders a change set as a per-file list (path + kind + redacted
// content). Deleted files carry no content. A change set rejected by the
// secret sweep never had its raw bytes persisted (persistDiff is skipped), but
// carries a MASKED version instead (RedactedDigest, persistRedactedDiff) --
// that is served here with Redacted=true rather than leaving Content empty,
// so the human can see what was blocked. Content is genuinely absent (Size 0,
// Redacted false) only when neither the raw nor the masked bytes could be
// found -- e.g. a patch persisted before this fix, or a masking failure. A
// patch-manifest read/decode failure yields an empty (non-nil) list so the
// `files` wire field is always a JSON array; the read error is surfaced via
// RenderPatch's "(patch unreadable: …)" in the sibling `patch` field.
func (e *Engine) StructuredDiff(cs *domain.ChangeSet) []StructuredFile {
	patch, err := e.loadPatch(cs)
	if err != nil {
		return []StructuredFile{}
	}
	return e.structuredFiles(patch.Files)
}

// maxDiffEndpointFiles bounds how many files the diff endpoint (GET
// .../change-sets/{id}/diff) computes a per-file redacted rendering and
// unified hunk for in one request. Review defect #2: an ICP run's agent
// wrote ~14,300 files into its work root, and this endpoint tried to
// redact-and-diff every one of them synchronously and never responded within
// 100s. The real fix is upstream -- Snapshot's sanity cap (afterRunSnapshotOptions)
// now refuses to turn that many files into a change set in the first place --
// but a change set persisted before that fix, or one that is simply large,
// must still not be able to hang this endpoint. StructuredDiff/RenderPatch's
// unpaged form is untouched: they also render the adversarial reviewer's and
// the risk endpoint's input, and neither is this endpoint.
const maxDiffEndpointFiles = 500

// StructuredDiffPage is StructuredDiff bounded to at most maxDiffEndpointFiles
// files, returning the change set's true total file count alongside the
// (possibly truncated) list so a caller can report "showing 500 of 14300".
func (e *Engine) StructuredDiffPage(cs *domain.ChangeSet) (files []StructuredFile, total int) {
	patch, err := e.loadPatch(cs)
	if err != nil {
		return []StructuredFile{}, 0
	}
	total = len(patch.Files)
	limited := patch.Files
	if len(limited) > maxDiffEndpointFiles {
		limited = limited[:maxDiffEndpointFiles]
	}
	return e.structuredFiles(limited), total
}

// structuredFiles is StructuredDiff/StructuredDiffPage's shared per-file
// rendering: path + kind + redacted content. Deleted files carry no content.
// A change set rejected by the secret sweep never had its raw bytes persisted
// (persistDiff is skipped), but carries a MASKED version instead
// (RedactedDigest, persistRedactedDiff) -- that is served here with
// Redacted=true rather than leaving Content empty, so the human can see what
// was blocked. Content is genuinely absent (Size 0, Redacted false) only when
// neither the raw nor the masked bytes could be found -- e.g. a patch
// persisted before this fix, or a masking failure.
func (e *Engine) structuredFiles(files []domain.FileChange) []StructuredFile {
	out := make([]StructuredFile, 0, len(files))
	for _, fc := range files {
		ct := string(fc.ChangeType)
		if ct == "" {
			// Patches persisted before ChangeType existed only carried a digest;
			// an empty digest is the only distinction they encoded (delete).
			if fc.IsDelete() {
				ct = string(domain.FileDeleted)
			} else {
				ct = string(domain.FileModified)
			}
		}
		sf := StructuredFile{Path: fc.Path, ChangeType: ct}
		if !fc.IsDelete() && e.contentBlobs() != nil {
			if content, err := e.contentBlobs().Get(fc.Digest); err == nil {
				sf.contentKept = true
				sf.Size = len(content)
				raw := string(content)
				if e.redactor != nil {
					raw = e.redactor.RedactString(raw)
				} else {
					raw = security.Redact(raw)
				}
				sf.Content = raw
			} else if fc.RedactedDigest != "" {
				if masked, merr := e.contentBlobs().Get(fc.RedactedDigest); merr == nil {
					sf.Redacted = true
					sf.Content = string(masked)
					sf.Size = len(masked)
				}
			}
		}
		sf.Patch, sf.PatchNote = e.patchFor(fc, sf)
		out = append(out, sf)
	}
	return out
}

// persistManifest stores a snapshot manifest as JSON and returns its digest.
func (e *Engine) persistManifest(m fsdiff.Manifest) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("workflow: marshal manifest: %w", err)
	}
	if e.contentBlobs() == nil {
		return m.Digest(), nil
	}
	return e.contentBlobs().Put(data)
}

// loadManifest reads and decodes a snapshot manifest by digest.
func (e *Engine) loadManifest(digest string) (fsdiff.Manifest, error) {
	if digest == "" {
		return fsdiff.Manifest{}, nil
	}
	if e.contentBlobs() == nil {
		return nil, fmt.Errorf("workflow: no blob store")
	}
	data, err := e.contentBlobs().Get(digest)
	if err != nil {
		return nil, fmt.Errorf("workflow: load manifest: %w", err)
	}
	var m fsdiff.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("workflow: decode manifest: %w", err)
	}
	return m, nil
}

// RestoreWorkspace restores the canonical workspace from a persisted pre-image
// manifest digest, for a change set stuck mid-apply. Used by startup
// reconciliation to unwind an interrupted apply.
//
// H3: RevertChangeSet refuses to restore a pre-image over an edit made to the
// workspace AFTER the mutation it is unwinding (fsdiff.StalePost) -- without
// the same guard here, a crash-recovery restore could silently overwrite
// whatever the user (or a later, successful operation) wrote to one of these
// same paths between the crash and the next daemon startup. changeSetID
// identifies the change set whose patch describes exactly the paths (and
// post-image digests) this restore would otherwise blindly overwrite.
func (e *Engine) RestoreWorkspace(ctx context.Context, changeSetID, manifestDigest string) error {
	pre, err := e.loadManifest(manifestDigest)
	if err != nil {
		return err
	}
	if e.workspaceRoot == "" {
		return fmt.Errorf("workflow: no canonical workspace root configured")
	}
	if cs, err := e.db.GetChangeSet(ctx, changeSetID); err == nil {
		if patch, err := e.loadPatch(cs); err == nil {
			if stale := fsdiff.StalePost(e.workspaceRoot, patch.Files); len(stale) > 0 {
				return fmt.Errorf("workflow: refusing to restore %s: the workspace changed since the interrupted apply: %s",
					cs.HumanID, strings.Join(stale, "; "))
			}
		}
	}
	return fsdiff.Restore(e.workspaceRoot, pre, e.contentBlobs().Get)
}

// waitForCompletion polls the executor until the attempt reaches a terminal
// state. It never exposes chain-of-thought: only the terminal status is used.
// External executors (container, OMP) can take minutes, so it polls with
// exponential backoff up to a generous ceiling, still honoring ctx cancellation.
func waitForCompletion(ctx context.Context, exec executor.Executor, attemptID string) error {
	const (
		maxDuration = 15 * time.Minute
		maxSleep    = time.Second
	)
	deadline := time.NewTimer(maxDuration)
	defer deadline.Stop()
	sleep := 5 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			_ = exec.Cancel(context.Background(), attemptID)
			return ctx.Err()
		case <-deadline.C:
			_ = exec.Cancel(context.Background(), attemptID)
			return fmt.Errorf("workflow: execute: attempt did not complete within %s", maxDuration)
		default:
		}
		st, err := exec.Status(ctx, attemptID)
		if err != nil {
			_ = exec.Cancel(context.Background(), attemptID)
			return fmt.Errorf("workflow: execute: %w", err)
		}
		switch st.Status {
		case "succeeded":
			return nil
		case "failed", "cancelled":
			if st.Action != "" {
				return fmt.Errorf("workflow: execute: attempt %s (%s)", st.Status, st.Action)
			}
			return fmt.Errorf("workflow: execute: attempt %s", st.Status)
		}
		time.Sleep(sleep)
		if sleep < maxSleep {
			sleep *= 2
		}
	}
}

// refuseWhileAnotherChangeSetIsOpen stops a task from running while another
// task's change set in the same initiative is still open.
//
// The agent works on a copy of the workspace. That is only the right base if
// the workspace already holds every earlier task's work: run task 2 while task
// 1's change set is still awaiting review and task 2's agent never sees task
// 1's changes, and the two collide at apply. The task's own earlier attempt is
// not in the way -- it is superseded as before.
func (e *Engine) refuseWhileAnotherChangeSetIsOpen(ctx context.Context, initiativeID, taskID string) error {
	sets, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, cs := range sets {
		if cs.TaskID == taskID {
			continue
		}
		if changeSetIsOpen(cs.Status) {
			return fmt.Errorf("workflow: execute: %s is still %s; apply or reject it before running the next task, "+
				"so the next task starts from its changes", cs.HumanID, strings.ReplaceAll(string(cs.Status), "_", " "))
		}
	}
	return nil
}

// changeSetIsOpen reports whether a change set in this status is still
// awaiting resolution -- review, approval or apply -- rather than at a
// terminal disposition (draft is included for completeness; Execute holds
// e.mu for its entire body, so no caller ever observes another in-progress
// Execute's change set at draft between the two of them). Shared by
// refuseWhileAnotherChangeSetIsOpen (another task's change set must be
// resolved before this task may run) and Execute's own-task NoOp guard (C4:
// a NoOp change set leaves its task `proposed`, not `done`, specifically so
// it can be re-picked -- but only once ITS OWN change set stops being open,
// or a fresh Execute would re-pick the task and spend a second agent run
// while the first, unresolved, no-op change set is still sitting there).
func changeSetIsOpen(status domain.ChangeSetStatus) bool {
	switch status {
	case domain.ChangeSetDraft, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired,
		domain.ChangeSetApproved, domain.ChangeSetApplying, domain.ChangeSetChangesRequested,
		domain.ChangeSetConflicted:
		return true
	}
	return false
}

// agentOutput returns the end of an external agent's own output, redacted, or
// "" for an executor that has none.
func (e *Engine) agentOutput(ctx context.Context, exec executor.Executor, attemptID string) string {
	return e.agentOutputLines(ctx, exec, attemptID, 0)
}

// agentOutputLines is agentOutput with a line count (0 = the default 40).
func (e *Engine) agentOutputLines(ctx context.Context, exec executor.Executor, attemptID string, lines int) string {
	t, ok := exec.(executor.OutputTailer)
	if !ok {
		return ""
	}
	return e.redact(t.OutputTail(ctx, attemptID, lines))
}

// withAgentOutput appends the agent's own output to a failed attempt's error.
// "attempt failed" was all `idea execute` reported; `--smoke` already showed the
// output, and a real run is where it matters more.
func (e *Engine) withAgentOutput(ctx context.Context, exec executor.Executor, attemptID string, err error) error {
	// A cancelled run's context cannot read anything, and a timeout says why
	// already; both were cancelled by waitForCompletion, so the output is gone.
	if ctx.Err() != nil {
		return err
	}
	if out := e.agentOutput(ctx, exec, attemptID); out != "" {
		return fmt.Errorf("%w; agent output:\n%s", err, out)
	}
	return err
}

// runDeterministicChecks runs the real B5 deterministic checks against the
// produced diff: patch-applies (content integrity), build, and tests (when a
// command is configured). Each produces a Finding; a fail is a blocking
// (critical/high) finding that flips `blocked`. Commands are config-supplied,
// never model/agent output.
func (e *Engine) runDeterministicChecks(ctx context.Context, cs *domain.ChangeSet, workRoot string, files []domain.FileChange) ([]domain.Finding, bool) {
	out := make([]domain.Finding, 0, 3)
	blocked := false
	appendFinding := func(f domain.Finding) {
		out = append(out, f)
		if f.Severity.Blocking() {
			blocked = true
		}
	}

	// Detected once per call, from the canonical workspace (the same root
	// detectToolchainImage reads), never the ephemeral work root: a project's
	// toolchain markers live in the real checkout, and workRoot is a fresh
	// copy per run that may not even exist yet the first time this runs
	// (review #5, ICP re-run).
	detectedBuild, detectedTest := detectCheckCommands(e.workspaceRoot)
	appendFinding(e.checkPatchApplies(cs, files))
	appendFinding(e.checkCommand(ctx, cs, workRoot, "build", "build_cmd", "build/type-check passes", e.cfg.BuildCmd, detectedBuild))
	appendFinding(e.checkCommand(ctx, cs, workRoot, "tests", "test_cmd", "tests pass", e.cfg.TestCmd, detectedTest))
	return out, blocked
}

// checkPatchApplies verifies every collected file blob is retrievable and
// digest-consistent — the file-replacement patch applies cleanly by construction.
func (e *Engine) checkPatchApplies(cs *domain.ChangeSet, files []domain.FileChange) domain.Finding {
	sev := domain.SeverityObservation
	status := "pass"
	evidence := fmt.Sprintf("%d file(s) in patch", len(files))
	code := domain.DenyCode("")
	if e.contentBlobs() == nil {
		sev = domain.SeverityHigh
		status = "FAIL"
		evidence = "no blob store available"
		code = domain.DenyCodeBlobDigestMismatch
	} else {
		for _, fc := range files {
			if fc.IsDelete() {
				continue
			}
			if _, err := e.contentBlobs().Get(fc.Digest); err != nil {
				sev = domain.SeverityHigh
				status = "FAIL"
				evidence = fmt.Sprintf("%s: content missing or corrupt: %v", fc.Path, err)
				code = domain.DenyCodeBlobDigestMismatch
				break
			}
		}
	}
	return domain.Finding{
		InitiativeID:   cs.InitiativeID,
		TargetType:     "change_set",
		TargetID:       cs.ID,
		Severity:       sev,
		Confidence:     domain.ConfidenceHigh,
		Category:       "patch_applies",
		Code:           code,
		Finding:        "patch applies cleanly to base snapshot — " + status,
		Evidence:       evidence,
		Impact:         "deterministic check result",
		Recommendation: "regenerate the patch if content is missing",
	}
}

// checkCommand runs a build/test command -- config-supplied, or, when none is
// configured, auto-detected from the project's own toolchain markers
// (detected, review #5, ICP re-run) -- in the isolated work root and reports
// pass/fail. Only when NEITHER is available is the check recorded
// not_applicable; a fabricated pass never happens either way.
// configKey is the harness.yaml field name, which is NOT derivable from the
// category: the category is "tests" (its value is pinned by the /checks
// endpoint and by execute_test.go) while the field is `test_cmd`. Deriving one
// from the other printed `set tests_cmd in harness.yaml`, and following that
// advice literally makes `idea config validate` exit 1 with
// `additional properties 'tests_cmd' not allowed`.
func (e *Engine) checkCommand(ctx context.Context, cs *domain.ChangeSet, workRoot, category, configKey, description, cmd string, detected detectedCommand) domain.Finding {
	base := domain.Finding{
		InitiativeID: cs.InitiativeID,
		TargetType:   "change_set",
		TargetID:     cs.ID,
		Confidence:   domain.ConfidenceHigh,
		Category:     category,
	}
	cmdNote := ""
	if cmd == "" {
		if detected.Cmd == "" {
			base.Severity = domain.SeverityObservation
			base.Finding = description + " — not applicable"
			base.Evidence = "no " + category + " command configured"
			base.Impact = "deterministic check skipped"
			base.Recommendation = "set " + configKey + " in harness.yaml (top level)"
			base.Disposition = domain.DispositionNotApplicable
			return base
		}
		cmd = detected.Cmd
		cmdNote = configKey + " not configured; detected `" + cmd + "` from " + detected.Source +
			" (set " + configKey + " in harness.yaml to override)"
	}

	// Fail closed: the build/test command executes the agent's code, so it must
	// never run on the host. It requires a toolchain image, configured
	// explicitly or derived from the project's own toolchain markers when
	// unambiguous (review C5): an ICP journey's check ran Node 24 against a
	// Node 26 project and wasted the whole run, because the configured image
	// never had anything to do with the project it was checking.
	image := e.cfg.Check.Image
	imageNote := ""
	if image == "" {
		derived, evidence := detectToolchainImage(e.workspaceRoot)
		if derived != "" {
			image = derived
			imageNote = "check.image not configured; derived " + evidence
		} else {
			base.Severity = domain.SeverityHigh
			base.Code = domain.DenyCodeHostExec
			base.Finding = description + " — FAIL (no sandbox image)"
			base.Disposition = domain.DispositionRejected
			base.Evidence = "check.image is not configured; refusing to run " + category + " on the host (" + evidence + ")"
			base.Impact = "deterministic check could not run safely"
			base.Recommendation = "set check.image to a toolchain image (e.g. golang:1.25, node:22)"
			return base
		}
	}

	timeout := 10 * time.Minute
	if e.cfg.CheckTimeoutSeconds > 0 {
		timeout = time.Duration(e.cfg.CheckTimeoutSeconds) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A "tests" check run as a literal `go test` invocation is re-run with
	// `-json` appended (C1/C2, review fix): `go test`'s own test2json stream
	// gives an exact Package/Test identity per result instead of one scraped
	// from `-v` text, which is what let two same-named tests in different
	// packages collide. Build keeps its own command untouched -- this is
	// scoped to the tests evidence path only.
	runCmd := cmd
	if category == "tests" && isGoTestCommand(cmd) {
		runCmd = cmd + " -json"
	}

	// Run inside an isolated container (no network, read-only root, dropped
	// capabilities) with only the work root mounted.
	sandbox := executor.NewContainerSandbox(image)
	stdout, stderr, exitCode, runErr := sandbox.Run(checkCtx, executor.RunOptions{
		WorkRoot: workRoot,
		RepoRoot: e.workspaceRoot, // read-only /repo mount so checks can read the real project
		Cmd:      []string{"sh", "-c", runCmd},
	})
	// imagePrefix shows the image every non-skipped finding below carries
	// (review C5: "show the derived image in the check finding"), and notes
	// when it was derived rather than configured. cmdNote does the same for a
	// detected command (review #5): a human reading a "detected" finding must
	// not have to guess what command actually ran or where it came from.
	imagePrefix := "image: " + image
	if imageNote != "" {
		imagePrefix += " (" + imageNote + ")"
	}
	imagePrefix += "\n"
	if cmdNote != "" {
		imagePrefix += cmdNote + "\n"
	}
	if runErr != nil {
		// Docker unavailable or launch failed → fail closed, never host-exec.
		base.Severity = domain.SeverityHigh
		base.Code = domain.DenyCodeHostExec
		base.Finding = description + " — FAIL (sandbox unavailable)"
		base.Disposition = domain.DispositionRejected
		base.Evidence = imagePrefix + fmt.Sprintf("sandbox launch failed: %v\n%s", runErr, truncateOutput(stderr))
		base.Impact = "deterministic check could not run safely"
		base.Recommendation = "ensure Docker is available and " + image + " is pullable"
		return base
	}
	out := stdout + stderr
	// cap is the byte cap applied to this check's stored evidence. "tests"
	// gets a far larger cap than the general 2000 bytes (review C1 fix:
	// "parse before truncating output" -- truncating a real suite's output
	// at 2000 bytes silently dropped tests the plan named, which
	// confirm_passing_tests then read as "never ran" rather than "failed").
	// Build and every other check keep the original, tighter cap.
	outputCap := 2000
	if category == "tests" {
		outputCap = testsOutputCap
	}
	if exitCode != 0 {
		base.Severity = domain.SeverityHigh
		base.Finding = description + " — FAIL"
		base.Disposition = domain.DispositionRejected // review M5: a rejected change set's findings are closed, not left open
		if checkCtx.Err() == context.DeadlineExceeded {
			base.Evidence = imagePrefix + fmt.Sprintf("timed out after %s\n%s", timeout, truncateOutputN(out, outputCap))
			base.Recommendation = "the " + category + " command hung; fix it or raise check_timeout_seconds"
		} else {
			base.Evidence = imagePrefix + fmt.Sprintf("exit code %d\n%s", exitCode, truncateOutputN(out, outputCap))
			base.Recommendation = "fix the failure and re-execute"
		}
		base.Impact = "deterministic check failed"
		return base
	}
	base.Severity = domain.SeverityObservation
	base.Finding = description + " — pass"
	base.Evidence = imagePrefix + truncateOutputN(out, outputCap)
	base.Impact = "deterministic check passed"
	return base
}

// testsOutputCap is the byte cap for a "tests" check's stored evidence --
// see the comment above its use in checkCommand.
const testsOutputCap = 2 << 20 // 2 MiB

// isGoTestCommand reports whether cmd is a literal `go test ...` invocation
// (its first two whitespace-separated fields), the case checkCommand
// appends `-json` for (review C1/C2 fix). A wrapper script or a different
// test runner that merely contains the substring "go test" elsewhere is
// deliberately not matched.
func isGoTestCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	return len(fields) >= 2 && fields[0] == "go" && fields[1] == "test"
}

// truncateOutput caps command output for findings at the general 2000-byte
// limit.
func truncateOutput(s string) string {
	return truncateOutputN(s, 2000)
}

// truncateOutputN is truncateOutput with an explicit cap.
func truncateOutputN(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// hasBlockingRiskSignal reports whether a detected vocabulary-marker signal
// alone should hard-reject a change set at deterministic checks (§45).
//
// It always returns false (review D2/A1, "words stop blocking"). Every marker
// in risk.markerCatalog -- auth, secret, credential, network, payment, pii --
// is a whole-word match against the diff text, not an inspection of any actual
// value, so a patch that legitimately discusses authentication, adds an HTTPS
// URL, or stores a per-user rate limit under the word "token" used to be
// refused outright with no override and no file or line (the ICP finding this
// fixes). The scan still runs and its result still surfaces as a non-blocking
// "sensitive area" finding for the reviewer -- see the signals-driven finding
// built in Execute -- but nothing here alone denies a change set anymore.
// Only a detected credential VALUE blocks now; see sweepDenyCode and each
// hasCredential call site.
//
// Kept as a named function, not inlined to `false`, so the invariant is one
// place to mutation-check: flip this back to the old marker-based switch and
// TestExecuteDoesNotRejectOnSecretMarkerAlone (and its siblings) fail.
func hasBlockingRiskSignal(signals []domain.RiskSignal) bool {
	return false
}

// sweepDenyCode is the machine-readable system-deny code for a credential-
// blocked change set. Vocabulary markers no longer block on their own (review
// D2/A1: hasBlockingRiskSignal above), so the only remaining reason a sweep
// denies is a detected credential value, and the code is always secret_sweep.
func sweepDenyCode(hasCredential bool) domain.DenyCode {
	if hasCredential {
		return domain.DenyCodeSecretSweep
	}
	return ""
}

// detectedRiskNames joins the names of the detected risk signals for a readable
// finding message.
func detectedRiskNames(signals []domain.RiskSignal) string {
	var names []string
	for _, s := range signals {
		if s.Detected {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, ", ")
}

// createSensitiveAreaFinding creates at most one "sensitive_area" finding per
// change set (review low-priority item). Execute's own sweep and the
// apply-time re-sweep both call this for the same change set (and a retried
// apply calls the latter again), and without this check a change set could
// accumulate several near-identical advisory findings restating the same
// signals -- noise on top of noise, on a finding that is already advisory
// and never blocks anything.
func (e *Engine) createSensitiveAreaFinding(ctx context.Context, initiativeID, targetID string, signals []domain.RiskSignal, evidence string) error {
	if len(signals) == 0 {
		return nil
	}
	existing, err := e.db.ListFindingsByTarget(ctx, "change_set", targetID)
	if err != nil {
		return err
	}
	for _, f := range existing {
		if f.Category == "sensitive_area" {
			return nil
		}
	}
	return e.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID:   initiativeID,
		TargetType:     "change_set",
		TargetID:       targetID,
		Severity:       domain.SeverityMedium,
		Confidence:     domain.ConfidenceHigh,
		Category:       "sensitive_area",
		Finding:        "sensitive area: " + detectedRiskNames(signals),
		Evidence:       evidence,
		Impact:         "advisory only; look closer before approving",
		Recommendation: "review the change for real secrets or unsafe handling before approving",
	})
}

// ApplyChangeSet applies an approved change set: approved→applying→applied.
// It writes the orchestrator-computed diff into the canonical workspace via
// atomic per-file temp+rename, capturing the pre-image into the journal so
// revert can restore it byte-for-byte (§39, review B1).
func (e *Engine) ApplyChangeSet(ctx context.Context, initiativeID, changeSetID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return err
	}
	if cs.InitiativeID != initiativeID {
		return fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	// ICP re-run defect #3: a reverted change set may be re-applied, subject
	// to the same staleness/secret-sweep guards below as a fresh approved one
	// -- recovering from a revert used to always cost a full agent run, even
	// when the reverted content was still exactly what was wanted.
	// fromStatus is the state every recovery transition below returns to on
	// failure: it must never be hardcoded to Approved once Reverted can reach
	// here too, or a failed re-apply attempt would wrongly relabel an
	// already-reverted change set as merely "approved".
	fromStatus := cs.Status
	if fromStatus != domain.ChangeSetApproved && fromStatus != domain.ChangeSetReverted {
		return fmt.Errorf("workflow: change set %q is not approved (status=%s)", cs.HumanID, cs.Status)
	}
	workspaceRoot := e.workspaceRoot
	if workspaceRoot == "" {
		return fmt.Errorf("workflow: no canonical workspace root configured")
	}
	patch, err := e.loadPatch(cs)
	if err != nil {
		return err
	}

	// Re-sweep the patch content at the apply boundary (defense-in-depth): the
	// credential catalog may have grown since Execute swept it. Never mutate the
	// workspace if a credential is now detected (system-deny). Consistent with
	// the execute-time sweep (review D2/A1): vocabulary markers are advisory
	// only here too, and an allowlisted fingerprint is ignored here too.
	diffText := e.renderDiffText(patch.Files)
	signals := risk.RiskSignals(ctx, cs.PatchDigest, diffText)
	allowlist, err := e.secretAllowlist(ctx)
	if err != nil {
		return err
	}
	credMatches := filterAllowlisted(e.scanBlobsForCredentials(patch.Files), allowlist)
	blockingMatches, advisoryMatches := splitCredentialMatches(credMatches)
	if err := e.createSensitiveAreaFinding(ctx, cs.InitiativeID, changeSetID, signals, "deterministic marker re-scan of the persisted patch"); err != nil {
		return err
	}
	// A test/fixture-path advisory match is recorded regardless of whether
	// this apply otherwise proceeds (ICP re-run defect #1): visible, never
	// gating.
	for _, m := range advisoryMatches {
		if err := e.createAdvisorySecretFinding(ctx, cs.InitiativeID, changeSetID, m); err != nil {
			return err
		}
	}
	if len(blockingMatches) > 0 || hasBlockingRiskSignal(signals) {
		for _, m := range blockingMatches {
			if err := e.db.CreateFinding(ctx, &domain.Finding{
				InitiativeID:      cs.InitiativeID,
				TargetType:        "change_set",
				TargetID:          changeSetID,
				Severity:          domain.SeverityCritical,
				Confidence:        domain.ConfidenceHigh,
				Category:          "secret_sweep",
				Code:              sweepDenyCode(true),
				Finding:           fmt.Sprintf("%s:%d: looks like %s (found at apply time)", m.Path, m.Line, m.Kind),
				Evidence:          e.redact(secretSweepEvidence(m)),
				Impact:            "apply denied before workspace mutation",
				Recommendation:    secretSweepRecommendation(m),
				Disposition:       domain.DispositionRejected,
				SecretFingerprint: m.Fingerprint,
				SecretKind:        m.Kind,
				SecretPath:        m.Path,
				SecretOverridable: m.Overridable,
				SecretPreview:     m.Preview,
			}); err != nil {
				return err
			}
		}
		// Recover the change set to a terminal state and re-open the task so a
		// corrected re-execute replaces it (review: system-deny deadlock fix).
		// Only meaningful starting from Approved -- a re-apply attempt on an
		// already-Reverted change set (ICP re-run defect #3) is already at a
		// resting, terminal-for-this-purpose state; rejecting it would
		// misrepresent a change set that WAS legitimately applied once, and
		// reopening its task would fight whatever already-correct state the
		// original revert left it in.
		if fromStatus == domain.ChangeSetApproved {
			_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApproved, domain.ChangeSetRejected, store.ActorOrchestrator)
			if cs.TaskID != "" {
				_ = e.db.SetTaskStatus(context.Background(), cs.TaskID, domain.TaskProposed)
			}
		}
		return fmt.Errorf("workflow: apply denied: system deny %s", domain.DenyCodeSecretSweep)
	}

	if err := e.db.TransitionChangeSet(ctx, changeSetID, fromStatus, domain.ChangeSetApplying, store.ActorOrchestrator); err != nil {
		return err
	}

	// Refuse to overwrite anything the change was not made against: a file
	// edited in the workspace after the agent ran, or one the change "adds"
	// that now exists. Nothing is written; the change set is conflicted and
	// the reason recorded, because retrying the same patch fails the same way
	// -- the way forward is to reject it and run the task again.
	if stale := fsdiff.StaleBase(workspaceRoot, patch.Files); len(stale) > 0 {
		_ = e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   cs.InitiativeID,
			TargetType:     "change_set",
			TargetID:       changeSetID,
			Severity:       domain.SeverityHigh,
			Confidence:     domain.ConfidenceHigh,
			Category:       "stale_base",
			Finding:        "The workspace changed since this change set was made, so applying it would overwrite work.",
			Evidence:       strings.Join(stale, "\n"),
			Impact:         "Nothing was written.",
			Recommendation: "Reject this change set and run the task again; retrying the same change set conflicts again.",
		})
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
		return fmt.Errorf("workflow: apply refused: the workspace changed since %s was made: %s", cs.HumanID, strings.Join(stale, "; "))
	}

	// Record a "prepared" journal row BEFORE mutating so an interrupted apply is
	// recoverable at startup (review H2). The digest is left EMPTY: the true
	// workspace pre-image is only persisted on the workspace_mutated row, so a
	// crash before Prepare completes must recover via the no-bytes-written path
	// (applying→approved), not attempt to restore a never-persisted manifest.
	if err := e.db.AppendJournal(ctx, &domain.ApplyJournalEntry{
		ChangeSetID: changeSetID,
		State:       domain.ApplyPrepared,
	}); err != nil {
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, fromStatus, store.ActorOrchestrator)
		return err
	}

	// Phase 1 — Prepare (non-mutating): capture and persist the pre-image of
	// every touched path. No byte is written here, so a failure is recoverable
	// by reverting applying→approved (full-platform review: apply atomicity).
	preImage, err := fsdiff.Prepare(workspaceRoot, patch.Files, e.contentBlobs().Put)
	if err != nil {
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, fromStatus, store.ActorOrchestrator)
		return fmt.Errorf("workflow: apply prepare: %w", err)
	}
	preImageDigest, err := e.persistManifest(preImage)
	if err != nil {
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, fromStatus, store.ActorOrchestrator)
		return err
	}

	// Record "workspace_mutated" (with the pre-image digest) BEFORE the first
	// write, so the workspace can be restored if Commit fails or the daemon
	// crashes mid-apply.
	if err := e.db.AppendJournal(ctx, &domain.ApplyJournalEntry{
		ChangeSetID:             changeSetID,
		State:                   domain.ApplyWorkspaceMutated,
		WorkspaceSnapshotDigest: preImageDigest,
	}); err != nil {
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, fromStatus, store.ActorOrchestrator)
		return err
	}

	// Phase 2 — Commit: write the diff. On failure, restore the pre-image
	// immediately and mark conflicted (do not rely on next-startup reconciliation
	// to repair the user's source tree).
	if err := fsdiff.Commit(workspaceRoot, patch.Files, e.contentBlobs().Get); err != nil {
		_ = fsdiff.Restore(workspaceRoot, preImage, e.contentBlobs().Get)
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
		return fmt.Errorf("workflow: apply files: %w", err)
	}

	// Opt-in git commit-on-apply (§M7): create a commit for exactly the touched
	// paths (hooks disabled) BEFORE the change set is marked applied. A commit
	// failure is non-fatal at the apply level — restore the pre-image and mark
	// conflicted, mirroring the fsdiff.Commit failure path. The byte-for-byte
	// workspace revert is orthogonal: no revert commit is created here.
	//
	// A guard refusal (detached HEAD, a rebase/merge/cherry-pick in progress,
	// or the default branch without git.allow_default_branch — review C4,
	// adopted #2) is NOT a commit failure: the change set still applies, and
	// the reason is recorded as a warning instead (below), never a half-applied
	// change set.
	//
	// git.branch_per_initiative IMPLIES committing (review defect D): it is
	// documented as opting into "creating and checking out idea/<slug> …, then
	// committing each of its change sets there" (GitConfig.BranchPerInitiative's
	// own doc comment), but nothing ever created that commit unless git_commit
	// was ALSO set -- a config combination the setting's own name never
	// suggested was necessary, and `idea pr`'s own error told a user to
	// "enable git.branch_per_initiative and apply a change set first" with no
	// mention that git_commit was a second, separate requirement. A real run
	// set only branch_per_initiative, applied change sets that silently never
	// committed anything, and `idea pr` refused with no dedicated branch ever
	// having been created. Rather than fix the error message to describe two
	// flags, the semantics are made to match the one flag's own promise.
	var commitSHA, commitWarning string
	if e.cfg.GitCommit || e.cfg.Git.BranchPerInitiative {
		sha, warning, err := e.commitChangeSet(ctx, cs, patch)
		if err != nil {
			_ = fsdiff.Restore(workspaceRoot, preImage, e.contentBlobs().Get)
			_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
			return fmt.Errorf("workflow: git commit: %w", err)
		}
		commitSHA = sha
		commitWarning = warning
	}

	// applying→applied + committed journal (carrying the pre-image digest) happen
	// in ONE transaction so status and journal never diverge (§39). Use a fresh
	// context so a cancellation cannot strand a fully-written workspace; on
	// failure, restore the pre-image immediately (review: apply failure window).
	if err := e.db.MarkChangeSetApplied(context.Background(), changeSetID, preImageDigest, store.ActorOrchestrator); err != nil {
		_ = fsdiff.Restore(workspaceRoot, preImage, e.contentBlobs().Get)
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
		return fmt.Errorf("workflow: mark applied: %w", err)
	}
	if commitSHA != "" {
		if err := e.db.SetChangeSetCommit(ctx, changeSetID, commitSHA, store.ActorOrchestrator); err != nil {
			return err
		}
	}
	if commitWarning != "" {
		if err := e.db.SetChangeSetGitWarning(ctx, changeSetID, commitWarning, store.ActorOrchestrator); err != nil {
			return err
		}
	}
	// Track B: the verification_engineer records deterministic evidence. The
	// checks passed (otherwise the change set would have been rejected), but the
	// acceptance criterion itself needs a human/real measurement (§70).
	if err := e.recordAutomatedEvidence(ctx, cs); err != nil {
		return err
	}
	// H2: a successful apply implements the task -- Execute already marks a
	// fresh (non-NoOp) change set's task done at creation time, so this is a
	// no-op there, but it is the ONLY place that does so for a RE-apply of a
	// previously reverted change set (fromStatus == Reverted, above).
	// RevertChangeSet reopens the task to `proposed` on revert (ICP re-run
	// defect E); before this, nothing ever moved it back to `done` on
	// re-apply, so it stayed re-executable and a second Execute spent a
	// phantom paid agent run on a task that was, in fact, already
	// implemented again. cs.NoOp is invariant across apply/revert/re-apply,
	// so this single check also correctly does nothing for a NoOp change
	// set (C4): it must never count as done, applied or not.
	if cs.TaskID != "" && !cs.NoOp {
		if err := e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskDone); err != nil {
			return err
		}
	}
	return nil
}

// commitChangeSet creates the opt-in git commit for an applied change set and
// returns the commit SHA ("" when the workspace is not a git repository, the
// diff was empty, or a guard refused). The subject is "<HumanID>: <task
// title>" (HumanID is already CHG-N), with the task title sanitized so it can
// never inject extra commit lines or a forged header.
//
// Two things can skip the commit outright, each reported back as a warning
// rather than an error: the change set still applies, only uncommitted, and
// ApplyChangeSet records the reason on it instead of failing.
//
//   - git.branch_per_initiative (review C4 piece 2): every change set for one
//     initiative must land on that initiative's own branch. If the branch
//     cannot be ensured (a name collision aside, chiefly an unrelated dirty
//     file that would ride along onto a freshly created branch), the commit
//     is skipped rather than landing on whatever branch happened to be
//     checked out.
//   - git.CommitGuardReason (review C4 piece 1): a detached HEAD, a
//     rebase/merge/cherry-pick in progress, or (unless
//     git.allow_default_branch) the repository's default branch. Checked
//     after the branch switch above, so it is the *initiative's* branch being
//     judged, not whatever was checked out before.
//
// A title-lookup failure is non-fatal: the commit still proceeds with an empty
// title rather than failing the whole apply.
func (e *Engine) commitChangeSet(ctx context.Context, cs *domain.ChangeSet, patch *fsdiff.Patch) (sha, warning string, err error) {
	paths := make([]string, 0, len(patch.Files))
	for _, f := range patch.Files {
		paths = append(paths, f.Path)
	}

	// review MEDIUM #4: captured before EnsureInitiativeBranch below can
	// switch branches, so a genuine git failure AFTER that switch actually
	// completed (the default-branch guard's own git error, or -- the case
	// the review names -- the change set's own commit failing) restores the
	// workspace to what was checked out before, rather than leaving it
	// stranded on the initiative's branch on top of reporting the failure.
	// switchedBranch only arms once EnsureInitiativeBranch itself reports a
	// completed switch (branch != "", no brErr, no soft reason); a soft
	// refusal or warning returns err == nil below, so the defer never fires
	// for those -- staying on the initiative's branch there is the
	// intended, working outcome, not a failure to recover from.
	originalBranch := git.CurrentBranch(ctx, e.workspaceRoot)
	var switchedBranch bool
	defer func() {
		if err != nil && switchedBranch {
			if restoreErr := git.CheckoutBranch(context.Background(), e.workspaceRoot, originalBranch); restoreErr != nil && e.log != nil {
				e.log.Warn("commitChangeSet: could not restore the original branch after a commit failure",
					"change_set", cs.HumanID, "branch", originalBranch, "err", restoreErr)
			}
		}
	}()

	// Merge/rebase/cherry-pick/detached-HEAD are checked BEFORE any checkout
	// (review H2): switching branches (below) while one of those is in
	// progress is exactly the hazard the guard exists to prevent, so it must
	// run first, not after branch-per-initiative has already moved HEAD.
	if reason, guardErr := git.PreCommitGuardReason(ctx, e.workspaceRoot); guardErr != nil {
		return "", "", guardErr
	} else if reason != "" {
		return "", "not committed because " + reason, nil
	}

	var branchWarning string
	if e.cfg.Git.BranchPerInitiative {
		ini, iniErr := e.db.GetInitiative(ctx, cs.InitiativeID)
		if iniErr != nil {
			return "", "not committed: could not look up the initiative's branch: " + iniErr.Error(), nil
		}
		// catchUpFiles (review defect D, review MEDIUM #4): files this same
		// initiative's own earlier applied change sets touched but never got
		// committed -- typically because git_commit was off for those
		// applies and turned on later. EnsureInitiativeBranch commits
		// exactly these (when their content still matches what that change
		// set wrote) onto the initiative's branch rather than refusing
		// forever over the harness's own prior writes.
		catchUpFiles, catchUpErr := e.uncommittedAppliedPaths(ctx, cs.InitiativeID, cs.ID)
		if catchUpErr != nil {
			return "", "not committed: could not look up earlier applied change sets: " + catchUpErr.Error(), nil
		}
		branch, reason, warning, brErr := git.EnsureInitiativeBranch(ctx, e.workspaceRoot, ini.GitBranch, ini.Title, paths, catchUpFiles)
		if brErr != nil {
			return "", "not committed: could not switch to the initiative's branch: " + brErr.Error(), nil
		}
		if reason != "" {
			return "", "not committed: " + reason, nil
		}
		switchedBranch = branch != ""
		// H6: the branch was created from current HEAD rather than the
		// resolved default branch (HEAD wasn't at the default's commit).
		// The change set still applies and commits; the caller surfaces
		// this as the change set's git_commit_warning below.
		branchWarning = warning
		if branch != "" && branch != ini.GitBranch {
			if setErr := e.db.SetInitiativeGitBranch(ctx, cs.InitiativeID, branch, store.ActorOrchestrator); setErr != nil {
				return "", "checked out " + branch + " but could not record it on the initiative: " + setErr.Error(), nil
			}
		}
	}

	// The default-branch check runs AFTER the branch switch above (review
	// H2): it must judge the initiative's own branch, once
	// branch-per-initiative has switched to it, not whatever HEAD was before.
	if reason, guardErr := git.DefaultBranchGuardReason(ctx, e.workspaceRoot, e.cfg.Git.AllowDefaultBranch); guardErr != nil {
		return "", "", guardErr
	} else if reason != "" {
		return "", "not committed because " + reason, nil
	}
	title := ""
	if cs.TaskID != "" {
		if tasks, err := e.db.ListTasks(ctx, cs.InitiativeID); err == nil {
			for _, t := range tasks {
				if t.ID == cs.TaskID {
					title = t.Title
					break
				}
			}
		}
	}
	message := fmt.Sprintf("%s: %s", cs.HumanID, sanitizeCommitTitle(title))
	sha, err = git.Commit(ctx, e.workspaceRoot, paths, message)
	return sha, branchWarning, err
}

// uncommittedAppliedPaths returns the de-duplicated files touched by
// initiativeID's own OTHER applied change sets that have never been
// committed (GitCommitSHA == ""), excluding excludeChangeSetID (the one
// commitChangeSet is about to commit itself). This is exactly the set of
// files a real run left sitting uncommitted in the workspace because
// git_commit was off for those earlier applies (review defect D) --
// EnsureInitiativeBranch treats a dirty path in this set as safe to catch up
// onto the initiative's branch, rather than refusing forever over the
// harness's own prior writes -- PROVIDED the workspace still holds what that
// change set actually wrote (git.CatchUpFile.Digest, checked by
// EnsureInitiativeBranch/splitCatchUp: review MEDIUM #4 -- a catch-up commit
// must never sweep in a user's own hand-edit made to the same path).
//
// changeSets is iterated oldest-first (ListChangeSets' order) and later
// occurrences of the same path OVERWRITE earlier ones, so a path touched by
// more than one uncommitted change set is keyed on the LATEST write -- the
// one the workspace should actually still hold -- not whichever happened to
// run first. Best-effort: an earlier change set whose patch can no longer be
// read contributes nothing rather than failing this apply.
func (e *Engine) uncommittedAppliedPaths(ctx context.Context, initiativeID, excludeChangeSetID string) ([]git.CatchUpFile, error) {
	changeSets, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	byPath := map[string]git.CatchUpFile{}
	var order []string
	for _, cs := range changeSets {
		if cs.ID == excludeChangeSetID || cs.Status != domain.ChangeSetApplied || cs.GitCommitSHA != "" {
			continue
		}
		patch, err := e.loadPatch(&cs)
		if err != nil || patch == nil {
			continue // best-effort: an unreadable earlier patch contributes nothing to catch up
		}
		for _, fc := range patch.Files {
			if _, ok := byPath[fc.Path]; !ok {
				order = append(order, fc.Path)
			}
			byPath[fc.Path] = git.CatchUpFile{Path: fc.Path, Digest: fc.Digest}
		}
	}
	files := make([]git.CatchUpFile, 0, len(order))
	for _, p := range order {
		files = append(files, byPath[p])
	}
	return files, nil
}

// sanitizeCommitTitle collapses newlines and strips leading dashes from a task
// title so a model-authored title can never smuggle extra commit-body lines or
// an option-looking subject into the commit message.
func sanitizeCommitTitle(title string) string {
	title = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(title)
	title = strings.TrimLeft(title, "-")
	return strings.TrimSpace(title)
}

// RevertChangeSet rolls back an applied change set: it restores the pre-image
// snapshot recorded at apply time (byte-for-byte), then advances
// applied→reverted plus a rolled_back journal entry atomically (§39, review B1).
func (e *Engine) RevertChangeSet(ctx context.Context, initiativeID, changeSetID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return err
	}
	if cs.InitiativeID != initiativeID {
		return fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	if cs.Status != domain.ChangeSetApplied {
		return fmt.Errorf("workflow: cannot revert change set in status %q", cs.Status)
	}
	workspaceRoot := e.workspaceRoot
	if workspaceRoot == "" {
		return fmt.Errorf("workflow: no canonical workspace root configured")
	}

	// Refuse to erase an edit made in the workspace after apply: an ICP user
	// added a comment to src/app.js after apply, and a revert that only ever
	// looked at its own pre-image silently erased it. Mirror of
	// ApplyChangeSet's fsdiff.StaleBase guard at the opposite end of the
	// change set's life -- apply protects an edit made BEFORE it ran; this
	// protects one made AFTER (review A3). Checked before any write, so a
	// refusal here writes nothing. No force option: the recovery is for the
	// human to commit or discard their own edit, then revert again --
	// RevertBlockedReason lets the API/CLI say so before an operation even
	// starts, and this is the authoritative recheck under the engine lock.
	patch, err := e.loadPatch(cs)
	if err != nil {
		return err
	}
	if stale := fsdiff.StalePost(workspaceRoot, patch.Files); len(stale) > 0 {
		laterByFile, lerr := e.laterChangeSetsTouchingByFile(ctx, cs, staleFilePaths(stale))
		if lerr != nil {
			laterByFile = nil // best-effort naming; a lookup failure must not block the refusal itself
		}
		msg := revertRefusedMessage(cs.HumanID, stale, laterByFile)
		_ = e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID: cs.InitiativeID,
			TargetType:   "change_set",
			TargetID:     changeSetID,
			// review M4: stale_post is advisory, not a blocker of `complete` --
			// hasOpenBlockingFindings only ever looks at Severity.Blocking()
			// (critical/high), and there is nothing to accept-risk or override
			// here: it is resolved by acting on the Recommendation, not by a
			// disposition. Mutation-check: set this back to SeverityHigh and a
			// pending stale_post finding blocks complete again.
			Severity:       domain.SeverityObservation,
			Confidence:     domain.ConfidenceHigh,
			Category:       "stale_post",
			Finding:        "The workspace changed after this change set was applied, so reverting it now would erase that work.",
			Evidence:       strings.Join(stale, "\n"),
			Impact:         "Nothing was written.",
			Recommendation: revertAdvice(staleFilePaths(stale), laterByFile),
		})
		return fmt.Errorf("workflow: %s", msg)
	}

	preDigest, err := e.db.JournalSnapshotDigest(ctx, changeSetID)
	if err != nil {
		return err
	}
	preImage, err := e.loadManifest(preDigest)
	if err != nil {
		return err
	}
	if err := fsdiff.Restore(workspaceRoot, preImage, e.contentBlobs().Get); err != nil {
		return fmt.Errorf("workflow: revert files: %w", err)
	}
	if err := e.db.MarkChangeSetReverted(ctx, changeSetID, store.ActorOrchestrator); err != nil {
		return err
	}
	// Review M2: rolling back the implementation must also roll back the
	// verification verdict — the requirement returns to approved and the
	// initiative leaves complete.
	if cs.TaskID != "" {
		edges, err := e.db.ListGraphEdgesFrom(ctx, initiativeID, "task", cs.TaskID)
		if err != nil {
			return err
		}
		for _, edge := range edges {
			if edge.EdgeType == domain.EdgeImplements && edge.ToType == "requirement" {
				_ = e.db.UnverifyRequirement(ctx, edge.ToID, store.ActorOrchestrator)
			}
		}
		// ICP re-run defect E: reverting a change set used to leave its task
		// `done` forever. Guidance says "re-run the affected tasks", but a
		// `done` task is never picked by nextReadyTask (it only considers
		// ready/proposed) -- so `idea execute` printed "started" and then
		// failed "no ready tasks", with no way back short of editing the
		// database directly. Reverting is the reopen: put the task back in a
		// runnable state, but only when no OTHER applied change set still
		// implements it (a task can have more than one change set across
		// retries; reverting an old, already-superseded one must not re-open
		// a task a newer change set already carries). Dependents need no
		// extra handling here: taskDependenciesApplied is checked at pick
		// time (nextReadyTask), not cached, so a dependent that has not yet
		// executed is automatically re-blocked the moment this task is no
		// longer applied; one that already executed on top of it is a deeper
		// cascading-revert scenario this does not attempt to unwind.
		stillApplied, err := e.taskHasAppliedChangeSet(ctx, cs.TaskID)
		if err != nil {
			return err
		}
		if !stillApplied {
			if err := e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskProposed); err != nil {
				return err
			}
		}
	}
	_ = e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativePlanApproved, store.ActorOrchestrator)
	return nil
}

// staleFilePaths extracts the bare workspace-relative path from each of
// fsdiff.StalePost's "path: reason" lines.
func staleFilePaths(stale []string) []string {
	files := make([]string, 0, len(stale))
	for _, s := range stale {
		if i := strings.Index(s, ": "); i >= 0 {
			files = append(files, s[:i])
		} else {
			files = append(files, s)
		}
	}
	return files
}

// revertAdvice builds the actionable half of the revert-refusal message
// (review M4): for a file a LATER applied change set itself touched, the fix
// is "revert <that change set> first" -- discarding what looks like a "user
// edit" there would actually discard a legitimate later change. For every
// other file, the advice is "discard (or stash) your edits to X": committing
// does not change the file's bytes on disk, so it cannot make StalePost's
// byte-for-byte comparison pass -- only discarding (or moving the edit aside
// with a stash) does.
func revertAdvice(files []string, laterByFile map[string]string) string {
	var laterNames []string
	seenLater := map[string]bool{}
	var edited []string
	for _, f := range files {
		if human, ok := laterByFile[f]; ok && human != "" {
			if !seenLater[human] {
				seenLater[human] = true
				laterNames = append(laterNames, human)
			}
			continue
		}
		edited = append(edited, f)
	}
	var parts []string
	if len(laterNames) > 0 {
		parts = append(parts, fmt.Sprintf("revert %s first", strings.Join(laterNames, ", ")))
	}
	if len(edited) > 0 {
		parts = append(parts, fmt.Sprintf("discard (or stash) your edits to %s", strings.Join(edited, ", ")))
	}
	return strings.Join(parts, "; ") + ", then revert again"
}

// revertRefusedMessage explains, in terms of what to do next, why cs cannot
// be reverted while stale (fsdiff.StalePost's output, one line per file) is
// non-empty (review A3, sharpened by review M4: see revertAdvice).
// laterByFile maps a stale path to the HumanID of a later, still-applied
// change set that touched it, when one exists (nil or a missing entry means
// none did, for that path).
func revertRefusedMessage(humanID string, stale []string, laterByFile map[string]string) string {
	files := staleFilePaths(stale)
	return fmt.Sprintf(
		"revert refused: the workspace changed since %s was applied: %s; %s",
		humanID, strings.Join(stale, "; "), revertAdvice(files, laterByFile))
}

// laterChangeSetsTouchingByFile maps each of files to the HumanID of the
// LATEST OTHER applied change set for cs's initiative, created after cs,
// whose own patch touches that path (review M4, low-priority item). Used so
// a revert refusal can tell the human which later change set to revert
// first instead of wrongly suggesting they "discard" what is actually a
// legitimate later change.
//
// The latest, not the earliest: if a file was touched by CHG-2 and then
// again by CHG-3 (both after the change set being reverted), CHG-3's
// content is what currently sits on top -- reverting CHG-2 first, as the
// earliest-wins version of this used to advise, would fail the same
// stale-workspace check for the same reason (CHG-3 still sits on top of
// it), producing a second refusal instead of unwinding anything.
// ListChangeSets returns oldest-first, so always overwriting rather than
// keeping only the first match naturally leaves each file's LATEST toucher
// once the loop finishes.
//
// Best-effort: a patch that fails to load for one candidate change set is
// skipped rather than failing the whole lookup, since this only sharpens
// advice text and must never itself block or fail a revert attempt.
func (e *Engine) laterChangeSetsTouchingByFile(ctx context.Context, cs *domain.ChangeSet, files []string) (map[string]string, error) {
	all, err := e.db.ListChangeSets(ctx, cs.InitiativeID)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(files))
	for _, f := range files {
		want[f] = true
	}
	result := make(map[string]string)
	for i := range all {
		other := all[i]
		if other.ID == cs.ID || other.Status != domain.ChangeSetApplied || !other.CreatedAt.After(cs.CreatedAt) {
			continue
		}
		patch, err := e.loadPatch(&other)
		if err != nil {
			continue
		}
		for _, fc := range patch.Files {
			if want[fc.Path] {
				result[fc.Path] = other.HumanID
			}
		}
	}
	return result, nil
}

// RevertBlockedReason reports why RevertChangeSet would refuse to revert
// changeSetID right now, or "" when it would not. It mirrors RevertChangeSet's
// own stale-workspace guard read-only and lock-free (see RunBlockedReason), so
// Runtime can answer a synchronous 409 instead of starting an operation that
// can only fail in the background, and so a client can grey out "Revert" and
// say why before the click.
//
// A race between this read and RevertChangeSet's own authoritative recheck
// (taken under the engine lock, immediately before any byte is restored) is
// still safe: a file edited in that narrow window is caught there instead,
// and nothing is written either way.
func (e *Engine) RevertBlockedReason(ctx context.Context, initiativeID, changeSetID string) (string, error) {
	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return "", err
	}
	if cs.InitiativeID != initiativeID {
		return "", fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	// Only an applied change set has a post-image to protect; every other
	// status is refused elsewhere (handleRevertChangeSet's own state check) for
	// its own reason, not this one.
	if cs.Status != domain.ChangeSetApplied {
		return "", nil
	}
	workspaceRoot := e.workspaceRoot
	if workspaceRoot == "" {
		return "", nil
	}
	patch, err := e.loadPatch(cs)
	if err != nil {
		return "", err
	}
	if stale := fsdiff.StalePost(workspaceRoot, patch.Files); len(stale) > 0 {
		laterByFile, lerr := e.laterChangeSetsTouchingByFile(ctx, cs, staleFilePaths(stale))
		if lerr != nil {
			laterByFile = nil
		}
		return revertRefusedMessage(cs.HumanID, stale, laterByFile), nil
	}
	return "", nil
}

// ErrNoInitiativeBranch is returned by CreatePR when the initiative has no
// dedicated branch yet AND git.branch_per_initiative is off -- the message
// names the actual fix in that case: turn the setting on, then apply a
// change set. It must not be returned verbatim when the setting is already
// on (see noInitiativeBranchError): a real founder-journey run hit `idea pr`
// with git.branch_per_initiative already enabled and no change set applied
// yet, and got told to "enable git.branch_per_initiative" -- advice for a
// state the initiative was not in.
var ErrNoInitiativeBranch = errors.New("workflow: this initiative has no dedicated branch yet; " +
	"enable git.branch_per_initiative and apply a change set first")

// noInitiativeBranchError is CreatePR's refusal when git.branch_per_initiative
// is already on but no branch exists yet, for either reason: nothing has been
// applied, or a specific commit attempt was refused (mostRecentGitCommitWarning).
// Review defect D's fix named the specific reason but still wrapped
// ErrNoInitiativeBranch with %w, which reproduces its FULL text -- "enable
// git.branch_per_initiative and apply a change set first" -- regardless, since
// %w's formatted output is the wrapped error's own Error() string. This type
// gives the message its own text while Unwrap keeps
// errors.Is(err, ErrNoInitiativeBranch) true, so daemon/runtime.go's
// CreatePRForInitiative still classifies it as the same 409 precondition.
type noInitiativeBranchError struct{ msg string }

func (e *noInitiativeBranchError) Error() string { return e.msg }
func (e *noInitiativeBranchError) Unwrap() error { return ErrNoInitiativeBranch }

// mostRecentGitCommitWarning returns the most recent non-empty
// GitCommitWarning among initiativeID's change sets, "" when none exists
// (either no change sets yet, or every commit that was attempted succeeded).
// Best-effort: a lookup failure yields "" rather than failing CreatePR over a
// nicer error message.
func (e *Engine) mostRecentGitCommitWarning(ctx context.Context, initiativeID string) string {
	changeSets, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return ""
	}
	var latest *domain.ChangeSet
	for i := range changeSets {
		cs := &changeSets[i]
		if cs.GitCommitWarning == "" {
			continue
		}
		// MEDIUM fix: single-sourced via domain.NewerChangeSetInScanOrder,
		// so this and every other "latest change set" reducer (evidence
		// eligibility, /queue, /guidance) agree on which one wins a
		// same-millisecond tie.
		if latest == nil || domain.NewerChangeSetInScanOrder(*cs, *latest) {
			latest = cs
		}
	}
	if latest == nil {
		return ""
	}
	return latest.GitCommitWarning
}

// CreatePR pushes an initiative's dedicated branch and opens a pull request
// via `gh pr create` (review C4 piece 3), with a body built from the
// initiative's title, description, change sets and verification progress.
// Idempotent: a repeat call returns the already-recorded PR URL rather than
// opening a second PR for the same branch.
func (e *Engine) CreatePR(ctx context.Context, initiativeID string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.PRURL != "" {
		return ini.PRURL, nil
	}
	if ini.GitBranch == "" {
		// The generic ErrNoInitiativeBranch message ("enable
		// git.branch_per_initiative and apply a change set first") is right
		// when branch_per_initiative was simply never turned on -- the
		// e.cfg.Git.BranchPerInitiative == false case, below, unconditionally.
		// When it is already ON, that advice is wrong regardless of why the
		// branch does not exist yet: telling a user to "enable" a setting
		// they already enabled reads as though nothing they did took effect.
		// noInitiativeBranchError names the real precondition instead --
		// review defect D's specific-reason case (a guard refused the last
		// apply's own commit; that reason is on the change set as
		// GitCommitWarning) and the plainer case (nothing has been applied
		// yet) both get their own text here rather than %w-wrapping
		// ErrNoInitiativeBranch, which would reproduce its full "enable
		// git.branch_per_initiative" text regardless of what came after it.
		if e.cfg.Git.BranchPerInitiative {
			if reason := e.mostRecentGitCommitWarning(ctx, initiativeID); reason != "" {
				return "", &noInitiativeBranchError{msg: fmt.Sprintf(
					"workflow: this initiative has no dedicated branch yet; git.branch_per_initiative is already "+
						"enabled -- the last apply's own commit was skipped (%s); resolve that and re-apply a change set",
					reason)}
			}
			return "", &noInitiativeBranchError{msg: "workflow: this initiative has no dedicated branch yet; " +
				"git.branch_per_initiative is already enabled -- apply a change set first to create it"}
		}
		return "", ErrNoInitiativeBranch
	}
	if e.workspaceRoot == "" {
		return "", fmt.Errorf("workflow: no canonical workspace root configured")
	}
	body, err := e.prBody(ctx, ini)
	if err != nil {
		return "", err
	}
	url, err := git.CreatePR(ctx, e.workspaceRoot, git.PRRequest{
		Branch: ini.GitBranch,
		Title:  ini.Title,
		Body:   body,
	})
	if err != nil {
		return "", err
	}
	if err := e.db.SetInitiativePRURL(ctx, initiativeID, url, store.ActorOrchestrator); err != nil {
		return "", err
	}
	return url, nil
}

// prBody composes a pull request body from the initiative's own record: its
// PRD summary (title + description), the change sets it produced, and how
// many of its requirements are verified.
func (e *Engine) prBody(ctx context.Context, ini domain.Initiative) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", ini.Title)
	if ini.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", ini.Description)
	}
	sets, err := e.db.ListChangeSets(ctx, ini.ID)
	if err != nil {
		return "", err
	}
	if len(sets) > 0 {
		b.WriteString("### Change sets\n\n")
		for _, cs := range sets {
			fmt.Fprintf(&b, "- `%s` — %s\n", cs.HumanID, cs.Status)
		}
		b.WriteString("\n")
	}
	reqs, err := e.db.ListRequirements(ctx, ini.ID)
	if err != nil {
		return "", err
	}
	if len(reqs) > 0 {
		verified := 0
		for _, r := range reqs {
			if r.VerifiedAt != nil {
				verified++
			}
		}
		fmt.Fprintf(&b, "### Verification\n\n%d/%d requirements verified.\n", verified, len(reqs))
	}
	return b.String(), nil
}

// ErrNoPR is returned by PRChecks when the initiative has no pull request yet.
var ErrNoPR = errors.New("workflow: this initiative has no pull request yet; run `idea pr` first")

// PRChecks reports the latest CI status of changeSetID's initiative's pull
// request via `gh pr checks` (review C4 piece 3). Every change set of an
// initiative shares the same PR, so this is the same result whichever of the
// initiative's change sets it is asked through.
func (e *Engine) PRChecks(ctx context.Context, changeSetID string) (git.PRChecksResult, error) {
	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return git.PRChecksResult{}, err
	}
	ini, err := e.db.GetInitiative(ctx, cs.InitiativeID)
	if err != nil {
		return git.PRChecksResult{}, err
	}
	if ini.PRURL == "" {
		return git.PRChecksResult{}, ErrNoPR
	}
	return git.PRChecks(ctx, e.workspaceRoot, ini.GitBranch)
}

// ReviewChangeSet applies a human review verdict. "approved" runs the
// adversarial reviewer and gates approval on its findings; "changes_requested"
// and "rejected" apply the corresponding state transition.
// denyReviewOpenBlocking returns a system-deny error if any undisposed blocking
// finding targets the change set (review: system-deny tier — "system deny beats
// human approve").
func (e *Engine) denyReviewOpenBlocking(ctx context.Context, changeSetID string) error {
	findings, err := e.db.ListFindingsByTarget(ctx, "change_set", changeSetID)
	if err != nil {
		return err
	}
	for _, f := range findings {
		// Mirror §11 EvaluateApproval via Finding.ResolvesBlocking (ICP re-run
		// defect #2): fixed/accepted_risk always resolve; a human's rejected
		// resolves too, but ONLY on a reviewer's own judgment call (no system
		// deny code) -- deferred/duplicate/mitigate/not_applicable, and a
		// SYSTEM-set rejected (a failing check, a deny-coded finding), do NOT
		// clear a system-deny (review: non-overridable means non-overridable).
		if f.Advisory {
			continue
		}
		if !f.ResolvesBlocking() && f.Severity.Blocking() {
			if f.Code == "" {
				// A reviewer's judgment. A human may accept it, and until this
				// message said so the only command `idea next` names was refused
				// and named nothing that works.
				return fmt.Errorf("workflow: approval blocked: system deny %s (open %s finding); "+
					"`idea reviews` lists them, and `idea review <CHG-n> approve --accept-findings` "+
					"accepts the change set findings and all", domain.DenyCodeReviewOpenBlocking, f.Severity)
			}
			return fmt.Errorf("workflow: approval blocked: system deny %s (open %s finding, %s); "+
				"a system deny is not something a human can accept — `idea reviews` shows it",
				domain.DenyCodeReviewOpenBlocking, f.Severity, f.Code)
		}
	}
	return nil
}

// ReviewChangeSet with acceptFindings=true is the human accept: it signs off the
// reviewer's open findings on this change set and approves, without running a
// fresh review.
//
// That last clause is the whole point, and it is the third time this project has
// needed it. `idea approve` had to stop re-running the PRD review; `idea
// approve-plan` had to stop re-running the plan review; and this had to stop
// re-running the change-set review. A model reviewer is not a fixed point, so
// "disposition, then review again" is a retry, not an accept -- and here it was
// worse than a coin flip: the first attempt persists its findings, and every
// later attempt fails denyReviewOpenBlocking on them before the review even
// runs. The keyed magic path hit this every run and had no way out.
func (e *Engine) ReviewChangeSet(ctx context.Context, initiativeID, changeSetID, verdict string) error {
	return e.reviewChangeSet(ctx, initiativeID, changeSetID, verdict, false)
}

// ReviewChangeSetAccepting is ReviewChangeSet with the human accept.
func (e *Engine) ReviewChangeSetAccepting(ctx context.Context, initiativeID, changeSetID, verdict string) error {
	return e.reviewChangeSet(ctx, initiativeID, changeSetID, verdict, true)
}

func (e *Engine) reviewChangeSet(ctx context.Context, initiativeID, changeSetID, verdict string, acceptFindings bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return err
	}
	if cs.InitiativeID != initiativeID {
		return fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}

	switch verdict {
	case "approved":
		if cs.Status != domain.ChangeSetReviewRequired {
			return fmt.Errorf("workflow: cannot approve change set in status %q", cs.Status)
		}
		// ICP re-run defect #1: without this, a change set whose adversarial
		// review never completed (Execute's own review call errored -- most
		// often the operation was cancelled mid-review) was indistinguishable
		// from one that had genuinely converged with zero findings: it
		// carried only mechanical-check observations (build/tests/
		// patch-applies/workspace-copy), approve made no model call by
		// design (see the no-fresh-review comment below), and it went
		// straight to approved/appliable with no adversarial pass ever having
		// happened. `idea review <id> rerun` is the explicit, paid way
		// forward; this refusal names it rather than silently letting the
		// change set through.
		if !cs.ReviewCompleted() {
			return fmt.Errorf("workflow: cannot approve %s: its adversarial review never completed (%s), "+
				"so it carries only mechanical check observations, no adversarial findings; run `idea review %s rerun` first",
				cs.HumanID, cs.ReviewNeverCompletedCause(), cs.HumanID)
		}
		// The human accept: sign off the reviewer's own findings on this change
		// set, then re-run the gate. NOT skip it -- anything the disposition
		// declined to touch still refuses, which is what keeps the two predicates
		// in DispositionUnclaimedFindingsByTarget load-bearing rather than
		// decorative. A secret_sweep or host_exec finding carries a deny code and
		// is untouched; a failing build check carries `rejected` and is untouched;
		// both then block here, naming the deny.
		if acceptFindings {
			if err := e.db.DispositionUnclaimedFindingsByTarget(ctx, initiativeID,
				"change_set", changeSetID, domain.DispositionAcceptedRisk, store.HumanActor); err != nil {
				return err
			}
			if err := e.denyReviewOpenBlocking(ctx, changeSetID); err != nil {
				return err
			}
			// No fresh review, and no EvaluateApproval. See the doc comment.
			//
			// review_id is deliberately left unstamped: there is no run, and no
			// review passed. The provenance is the event log -- a
			// change_set.transitioned carrying HumanActor -- which is what the
			// audit trail rests on.
			return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, store.HumanActor)
		}
		// ICP re-run defect A: this used to run a FRESH adversarial review
		// right here, on every plain `approve` call -- real model spend (15s
		// to 2.5min; 64% of model spend on the run that found this), and a
		// model reviewer is not a fixed point: a finding a human had just
		// marked rejected was immediately replaced by a reworded
		// near-duplicate of the same severity, so approval never converged
		// without --accept-findings, and the client-side HTTP timeout (item
		// C) made it worse. The review now runs exactly once, in Execute,
		// the moment the change set's content is fixed (execute.go, right
		// after the checks-running -> review_required transition) --
		// PatchDigest never changes after that point, so there is nothing
		// left to re-review here. Approve now only evaluates what is already
		// on the change set: the reviewer's own findings plus whatever the
		// human has since dispositioned via `idea finding` -- exactly what
		// denyReviewOpenBlocking already checks, and exactly the same gate
		// --accept-findings runs through below. "System deny beats human
		// approve" (review: system-deny tier) is unchanged.
		if err := e.denyReviewOpenBlocking(ctx, changeSetID); err != nil {
			return err
		}
		return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, store.HumanActor)

	case "changes_requested":
		if err := e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetChangesRequested, store.HumanActor); err != nil {
			return err
		}
		// Re-open the task so a subsequent Execute re-picks it and supersedes this
		// change set (full-platform review: changes_requested was a dead end).
		return e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskProposed)

	case "rejected":
		// Rejection is accepted from either checks_running or review_required.
		switch cs.Status {
		case domain.ChangeSetChecksRunning:
			if err := e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.HumanActor); err != nil {
				return err
			}
			return e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskProposed)
		case domain.ChangeSetReviewRequired:
			if err := e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetRejected, store.HumanActor); err != nil {
				return err
			}
			return e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskProposed)
		default:
			return fmt.Errorf("workflow: cannot reject change set in status %q", cs.Status)
		}

	default:
		return fmt.Errorf("workflow: unknown review verdict %q", verdict)
	}
}

// RerunChangeSetReview runs the adversarial reviewer for a change set whose
// review never completed (ICP re-run defect #1): Execute's own call to it
// errored -- most often the operation was cancelled mid-review -- after the
// change set had already left checks_running for review_required, so it sits
// there carrying only mechanical-check observations, with the task never
// marked done and `idea execute` free to re-pick it and duplicate the work.
//
// This is an explicit, paid, user-initiated action (a real model call), never
// triggered implicitly by approve: the round-3 fix that made a plain approve
// free of model calls is unconditional, and this is the one, named escape
// hatch for the one case that needs a review to actually happen. It refuses
// outright once a review has already completed for this change set --
// nothing to rerun, and rerunning would reintroduce exactly the "a rejected
// finding comes back reworded" non-convergence problem that fix removed.
func (e *Engine) RerunChangeSetReview(ctx context.Context, initiativeID, changeSetID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return err
	}
	if cs.InitiativeID != initiativeID {
		return fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		return fmt.Errorf("workflow: cannot rerun review for change set in status %q", cs.Status)
	}
	if cs.ReviewCompleted() {
		return fmt.Errorf("workflow: %s already has a completed review; nothing to rerun -- `idea review %s approve` or `reject` instead", cs.HumanID, cs.HumanID)
	}

	runID, findings, _, err := e.runChangeSetAdversarialReview(ctx, cs)
	if err != nil {
		return err
	}
	if err := e.inheritDispositionFromPrior(ctx, initiativeID, findings); err != nil {
		return err
	}
	for i := range findings {
		findings[i].InitiativeID = initiativeID
		findings[i].TargetType = "change_set"
		findings[i].TargetID = cs.ID
		if err := e.db.CreateFinding(ctx, &findings[i]); err != nil {
			return err
		}
	}
	if err := e.db.SetChangeSetReview(ctx, cs.ID, runID, store.HumanActor); err != nil {
		return err
	}
	// Mirror Execute's own not-rejected path: the checks that gate rejection
	// (secret sweep, deterministic checks) already ran and passed before this
	// change set ever reached review_required, so a completed review -- same
	// as a fresh Execute -- marks the task done rather than leaving it
	// `proposed` forever. Best-effort and idempotent: if the task moved on
	// (e.g. a human already reopened/superseded it) this is a no-op observer,
	// not a correctness requirement for the review itself completing.
	if cs.TaskID != "" {
		_ = e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskDone)
	}
	return nil
}

// ResetConflictedForRetry resets a conflicted change set back to approved so the
// existing apply path can be re-run unchanged (approved→applying→applied).
func (e *Engine) ResetConflictedForRetry(ctx context.Context, initiativeID, changeSetID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return err
	}
	if cs.InitiativeID != initiativeID {
		return fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	if cs.Status != domain.ChangeSetConflicted {
		return fmt.Errorf("workflow: cannot retry change set in status %q", cs.Status)
	}
	return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetConflicted, domain.ChangeSetApproved, store.ActorOrchestrator)
}

// RejectConflicted resolves a conflicted change set by rejecting it and
// re-opening its task (mirrors the existing rejected recovery path).
func (e *Engine) RejectConflicted(ctx context.Context, initiativeID, changeSetID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, err := e.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return err
	}
	if cs.InitiativeID != initiativeID {
		return fmt.Errorf("workflow: change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	if cs.Status != domain.ChangeSetConflicted {
		return fmt.Errorf("workflow: cannot reject change set in status %q", cs.Status)
	}
	if err := e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetConflicted, domain.ChangeSetRejected, store.HumanActor); err != nil {
		return err
	}
	// Best-effort task re-open (mirrors the system-deny recovery path): the
	// rejection itself is the durable outcome; a missing/terminal task must not
	// wedge it.
	if cs.TaskID != "" {
		_ = e.db.SetTaskStatus(ctx, cs.TaskID, domain.TaskProposed)
	}
	return nil
}

// ChangeSetReviewSchemaName is the fake provider's queue key for the change-set
// adversarial review. Exported so demoProvider can prime it and tests can steer
// it; it never reaches a real provider.
const ChangeSetReviewSchemaName = "change_set_review"

// runChangeSetAdversarialReview runs the adversarial reviewer over a change set
// and converts its structured output into findings. The returned RigorLevel is
// the one this review actually ran at (review B1), so the caller can decide
// whether a blocking finding should still block (quick converges in one pass
// and accepts with advisories instead).
func (e *Engine) runChangeSetAdversarialReview(ctx context.Context, cs *domain.ChangeSet) (string, []domain.Finding, RigorLevel, error) {
	// H2: a patch that fails to load must fail the review, not silently
	// review against an empty file set. An empty changeFiles set makes
	// EVERY path-bearing finding "advisory" (see classifyDiffEvidence),
	// which is the unsafe direction for a real blocker -- the comment this
	// replaced called that "the safe direction"; it is not, for a deny.
	patch, err := e.loadPatch(cs)
	if err != nil {
		return "", nil, "", err
	}
	changeFiles := changeSetFilePaths(patch)

	scale := projectScaleOf(ctx, e.workspaceRoot)
	chg := changeSize{Files: len(changeFiles), Lines: e.changeSetLines(patch)}
	rigor := calibrateRigor(RigorLevel(e.cfg.Review.Rigor), scale, chg)
	conventions := projectConventions(e.workspaceRoot)

	// "change_set_review", not "review". The name is a demo/test discriminator
	// only -- JSONSchemaName is read by nothing but the fake provider, and real
	// providers never see it, because runModel inlines the schema text into the
	// system prompt. Sharing the key with the PRD review meant a queued demo
	// response could outlive the review it was meant for and land here, which is
	// the hazard the plan review's key was split to avoid; and it meant there
	// was no way to make THIS review block on demand, which is why five smoke
	// passes and every audit missed the live-lock behind it.
	runID, obj, err := e.runModel(ctx, cs.InitiativeID, StageReview, RoleAdversarialReviewer, ChangeSetReviewSchemaName, schema.Review,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Independently challenge the change set. Emit structured findings. Never set accepted_risk. " +
				"For each finding, set `path` to the file it is about when it names one, and `in_diff` to whether it is about code this change set actually touches. " +
				"Every finding must cite the specific line or path in the change set and a CONCRETE failure input or scenario that would trigger it -- not a generic category of risk. " +
				"A speculative concern with no concrete failure input (e.g. \"could theoretically be misused\", or flagging a value already validated/normalized earlier in the same diff) is at most low severity and advisory; " +
				"reserve critical/high for a finding you can show actually breaks with a specific input or actually contradicts a stated requirement."},
			{Selector: "project_scale", Content: scaleAndConventions(scale, chg, rigor, conventions)},
			{Selector: "change_set", Content: renderChangeSet(cs, e.RenderPatch(cs))},
		},
		"Adversarial review of change set "+cs.HumanID+".",
	)
	if err != nil {
		return "", nil, "", err
	}
	findings, err := review.ParseFindings(runID, "change_set", cs.ID, obj)
	if err != nil {
		return "", nil, "", err
	}
	for i := range findings {
		classifyDiffEvidence(&findings[i], changeFiles, e.workspaceRoot)
	}
	findings = capFindings(findings, findingCap(rigor), rigor)
	return runID, findings, rigor, nil
}

// changeSetLines estimates the number of lines a change set's diff touched
// (review H3's change-size calibration input): the total line count of every
// added/modified file's post-image content. fsdiff's patches are
// content-addressed at the file level, not the line level, so this is an
// upper bound on "added lines" rather than a precise line diff -- but it is
// enough to catch what the review found: a small file count (3 files) hiding
// a huge change (10,000 lines), which file count alone calibrated to
// `quick`.
func (e *Engine) changeSetLines(patch *fsdiff.Patch) int {
	if patch == nil || e.contentBlobs() == nil {
		return 0
	}
	lines := 0
	for _, fc := range patch.Files {
		if fc.IsDelete() {
			continue
		}
		data, err := e.contentBlobs().Get(fc.Digest)
		if err != nil || len(data) == 0 {
			continue
		}
		lines += strings.Count(string(data), "\n") + 1
	}
	return lines
}

// changeSetFilePaths returns the set of normalized file paths a change set's
// patch touches, for review B2's deterministic "is this finding about a file
// the change set actually touches" check. A nil/unreadable patch yields an
// empty set (never a match), which is the safe direction: a finding that
// names a path outside a set we could not determine falls back to the
// self-report rule (classifyDiffEvidence), not a silent demotion.
//
// Every path is run through normalizeFindingPath (H2) so it compares equal to
// a reviewer-supplied path in a different but equivalent form.
func changeSetFilePaths(patch *fsdiff.Patch) map[string]bool {
	out := map[string]bool{}
	if patch == nil {
		return out
	}
	for _, fc := range patch.Files {
		if norm := normalizeFindingPath(fc.Path, ""); norm != "" {
			out[norm] = true
		}
	}
	return out
}

// normalizeFindingPath normalizes a path for the in-diff membership check
// (review H2): the reviewer, the patch, and the workspace can each spell the
// same file differently. It:
//
//   - strips a trailing ":N" line suffix ("src/a.go:42");
//   - converts to slash-separated form;
//   - strips a workspace-absolute prefix, when workspace is given and the
//     path is rooted under it;
//   - strips a leading "./", "a/" or "b/" (git-diff-style prefixes);
//   - runs path.Clean.
//
// The comparison this feeds is case-sensitive: no case-folding is applied,
// since normalizeFindingPath's job is structural equivalence, not
// platform-specific case-insensitivity.
func normalizeFindingPath(p, workspace string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if idx := strings.LastIndex(p, ":"); idx > 0 {
		if _, err := strconv.Atoi(p[idx+1:]); err == nil {
			p = p[:idx]
		}
	}
	p = filepath.ToSlash(p)
	if workspace != "" {
		ws := filepath.ToSlash(workspace)
		ws = strings.TrimSuffix(ws, "/")
		if p == ws {
			return ""
		}
		if strings.HasPrefix(p, ws+"/") {
			p = strings.TrimPrefix(p, ws+"/")
		}
	}
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return p
}

// classifyDiffEvidence applies review B2's deterministic in-diff rule to one
// change-set review finding, mutating it in place:
//
//   - Path given, and its normalized form (H2) is one of the change set's
//     (also normalized) files: left alone -- the deterministic check
//     confirmed it.
//   - Path given, but it normalizes to no match: this does NOT prove the
//     finding is out-of-diff -- an unanticipated spelling normalizeFindingPath
//     doesn't yet strip looks identical to a genuinely out-of-diff path. H2:
//     fall back to the self-report rule below rather than demoting a possible
//     real blocker.
//   - Path omitted (or unmatched, per above): the reviewer's own in_diff
//     self-report decides Advisory instead, and the note says the finding's
//     diff-membership is unverified -- self-report, not the deterministic
//     path check.
func classifyDiffEvidence(f *domain.Finding, changeFiles map[string]bool, workspace string) {
	if f.Path != "" {
		if norm := normalizeFindingPath(f.Path, workspace); norm != "" && changeFiles[norm] {
			return
		}
	}
	f.Note = appendNote(f.Note, "unverified")
	if !f.InDiff {
		f.Advisory = true
	}
}

// appendNote appends a note fragment, keeping any note already present
// (e.g. contradictsDecision's heuristic note) rather than clobbering it.
func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// renderChangeSet renders a change set for the adversarial reviewer's context,
// including the produced patch text so the review is content-aware rather than
// digest-only.
func renderChangeSet(cs *domain.ChangeSet, patch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Change set: %s\n", cs.HumanID)
	if cs.TaskID != "" {
		fmt.Fprintf(&b, "Task: %s\n", cs.TaskID)
	}
	if cs.PatchDigest != "" {
		fmt.Fprintf(&b, "Patch digest: %s\n", cs.PatchDigest)
	}
	if patch != "" {
		b.WriteString("Patch:\n")
		b.WriteString(patch)
		if !strings.HasSuffix(patch, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
