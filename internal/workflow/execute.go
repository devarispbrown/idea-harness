package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// taskHasAppliedChangeSet reports whether a task has any applied change set.
func (e *Engine) taskHasAppliedChangeSet(ctx context.Context, taskID string) (bool, error) {
	css, err := e.db.ListChangeSetsByTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, cs := range css {
		if cs.Status == domain.ChangeSetApplied {
			return true, nil
		}
	}
	return false, nil
}

// runPlanner runs the engineering_lead planner to produce an implementation brief
// for a task, returned as rendered text for the implementer (§50). The brief is a
// durable Run (role engineering_lead, stage implementation_brief) via runModel.
func (e *Engine) runPlanner(ctx context.Context, initiativeID string, task domain.Task) (string, error) {
	_, obj, err := e.runModel(ctx, initiativeID, StageImplementationBrief, RoleEngineeringLead, "implementation_brief", schema.ImplementationBrief,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Produce a short implementation brief for the task."},
			{Selector: "task", Content: task.Statement},
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
	return b.String(), nil
}

// Execute runs the multi-role pipeline for the next ready task — planner
// (engineering_lead brief) → implementer (executor writes files) → deterministic
// checks → review_required/rejected. The adversarial reviewer runs at review time
// (§50).
func (e *Engine) Execute(ctx context.Context, initiativeID string) (domain.ChangeSet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

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
	}

	// Planner: engineering_lead produces an implementation brief the implementer
	// follows. The brief is a durable Run (role engineering_lead).
	brief, err := e.runPlanner(ctx, initiativeID, task)
	if err != nil {
		return domain.ChangeSet{}, err
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
	agentRunID, err := e.db.CreateRun(ctx, domain.RunAgent, "", "implementer")
	if err != nil {
		return domain.ChangeSet{}, err
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
		}
		return domain.ChangeSet{}, err
	}
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

	// Snapshot AFTER and compute the orchestrator-authored diff.
	after, err := fsdiff.Snapshot(req.WorkRoot)
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
	hasCredential := len(credMatches) > 0

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
	}
	// CreateChangeSet forces status=draft and assigns CHG-N.
	if err := e.db.CreateChangeSet(ctx, cs); err != nil {
		return domain.ChangeSet{}, err
	}
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
			rawCred := len(rawMatches) > 0
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
			for _, m := range rawMatches {
				if err := e.db.CreateFinding(ctx, &domain.Finding{
					InitiativeID:      cs.InitiativeID,
					TargetType:        "change_set",
					TargetID:          cs.ID,
					Severity:          domain.SeverityCritical,
					Confidence:        domain.ConfidenceHigh,
					Category:          "secret_sweep",
					Code:              sweepDenyCode(true),
					Finding:           fmt.Sprintf("in the output of %s: looks like %s", f.Category, m.Kind),
					Evidence:          secretSweepEvidence(m),
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

	// A real credential sweep gates rejection now, not a bare vocabulary word
	// (review D2/A1). credMatches was computed above from the work-root
	// content, BEFORE the raw store write, and already excludes any fingerprint
	// a human dispositioned "not a secret" for this project.
	rejected := false
	if checksBlocked || hasCredential || hasBlockingRiskSignal(signals) {
		rejected = true
		// One finding per match, each naming path:line and the kind of secret --
		// never the value (full-platform review: secret leak) -- so the denial
		// says where, instead of a bare "blocking risk signal detected".
		for _, m := range credMatches {
			if err := e.db.CreateFinding(ctx, &domain.Finding{
				InitiativeID:      cs.InitiativeID,
				TargetType:        "change_set",
				TargetID:          cs.ID,
				Severity:          domain.SeverityCritical,
				Confidence:        domain.ConfidenceHigh,
				Category:          "secret_sweep",
				Code:              sweepDenyCode(true),
				Finding:           fmt.Sprintf("%s:%d: looks like %s", m.Path, m.Line, m.Kind),
				Evidence:          secretSweepEvidence(m),
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
	}

	if rejected {
		// Re-open the task so a re-execute produces a fresh change set; leaving it
		// done with only a rejected change set would permanently block its
		// dependents (full-platform review: task-dependency deadlock).
		if err := e.db.SetTaskStatus(ctx, task.ID, domain.TaskProposed); err != nil {
			return domain.ChangeSet{}, err
		}
	} else {
		// Mark done so a repeated Execute does not re-pick the same task and
		// create a duplicate change set (it is now awaiting review/apply).
		if err := e.db.SetTaskStatus(ctx, task.ID, domain.TaskDone); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// Record the implementer run's provenance + success (review B7). modelSpec
	// was already resolved above, when the change set itself was created.
	if err := e.db.CreateRunManifest(ctx, &domain.RunManifest{
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
	if err := e.db.SetRunStatus(ctx, agentRunID, domain.RunSucceeded, "implementer"); err != nil {
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
// content). Deleted files carry no content. Content is omitted (Size 0) when a
// file's bytes were never persisted — e.g. a change set rejected by the secret
// sweep skips persistDiff, so its produced bytes are intentionally absent. A
// patch-manifest read/decode failure yields an empty (non-nil) list so the
// `files` wire field is always a JSON array; the read error is surfaced via
// RenderPatch's "(patch unreadable: …)" in the sibling `patch` field.
func (e *Engine) StructuredDiff(cs *domain.ChangeSet) []StructuredFile {
	patch, err := e.loadPatch(cs)
	if err != nil {
		return []StructuredFile{}
	}
	out := make([]StructuredFile, 0, len(patch.Files))
	for _, fc := range patch.Files {
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
// manifest digest. Used by startup reconciliation to unwind an interrupted apply.
func (e *Engine) RestoreWorkspace(ctx context.Context, manifestDigest string) error {
	pre, err := e.loadManifest(manifestDigest)
	if err != nil {
		return err
	}
	if e.workspaceRoot == "" {
		return fmt.Errorf("workflow: no canonical workspace root configured")
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
		switch cs.Status {
		case domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, domain.ChangeSetApproved,
			domain.ChangeSetApplying, domain.ChangeSetChangesRequested, domain.ChangeSetConflicted:
			return fmt.Errorf("workflow: execute: %s is still %s; apply or reject it before running the next task, "+
				"so the next task starts from its changes", cs.HumanID, strings.ReplaceAll(string(cs.Status), "_", " "))
		}
	}
	return nil
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

	appendFinding(e.checkPatchApplies(cs, files))
	appendFinding(e.checkCommand(ctx, cs, workRoot, "build", "build_cmd", "build/type-check passes", e.cfg.BuildCmd))
	appendFinding(e.checkCommand(ctx, cs, workRoot, "tests", "test_cmd", "tests pass", e.cfg.TestCmd))
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

// checkCommand runs a config-supplied build/test command in the isolated work
// root and reports pass/fail. A missing command is not_applicable, never a
// fabricated pass.
// configKey is the harness.yaml field name, which is NOT derivable from the
// category: the category is "tests" (its value is pinned by the /checks
// endpoint and by execute_test.go) while the field is `test_cmd`. Deriving one
// from the other printed `set tests_cmd in harness.yaml`, and following that
// advice literally makes `idea config validate` exit 1 with
// `additional properties 'tests_cmd' not allowed`.
func (e *Engine) checkCommand(ctx context.Context, cs *domain.ChangeSet, workRoot, category, configKey, description, cmd string) domain.Finding {
	base := domain.Finding{
		InitiativeID: cs.InitiativeID,
		TargetType:   "change_set",
		TargetID:     cs.ID,
		Confidence:   domain.ConfidenceHigh,
		Category:     category,
	}
	if cmd == "" {
		base.Severity = domain.SeverityObservation
		base.Finding = description + " — not applicable"
		base.Evidence = "no " + category + " command configured"
		base.Impact = "deterministic check skipped"
		base.Recommendation = "set " + configKey + " in harness.yaml (top level)"
		base.Disposition = domain.DispositionNotApplicable
		return base
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

	// Run inside an isolated container (no network, read-only root, dropped
	// capabilities) with only the work root mounted.
	sandbox := executor.NewContainerSandbox(image)
	stdout, stderr, exitCode, runErr := sandbox.Run(checkCtx, executor.RunOptions{
		WorkRoot: workRoot,
		RepoRoot: e.workspaceRoot, // read-only /repo mount so checks can read the real project
		Cmd:      []string{"sh", "-c", cmd},
	})
	// imagePrefix shows the image every non-skipped finding below carries
	// (review C5: "show the derived image in the check finding"), and notes
	// when it was derived rather than configured.
	imagePrefix := "image: " + image
	if imageNote != "" {
		imagePrefix += " (" + imageNote + ")"
	}
	imagePrefix += "\n"
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
	if exitCode != 0 {
		base.Severity = domain.SeverityHigh
		base.Finding = description + " — FAIL"
		base.Disposition = domain.DispositionRejected // review M5: a rejected change set's findings are closed, not left open
		if checkCtx.Err() == context.DeadlineExceeded {
			base.Evidence = imagePrefix + fmt.Sprintf("timed out after %s\n%s", timeout, truncateOutput(out))
			base.Recommendation = "the " + category + " command hung; fix it or raise check_timeout_seconds"
		} else {
			base.Evidence = imagePrefix + fmt.Sprintf("exit code %d\n%s", exitCode, truncateOutput(out))
			base.Recommendation = "fix the failure and re-execute"
		}
		base.Impact = "deterministic check failed"
		return base
	}
	base.Severity = domain.SeverityObservation
	base.Finding = description + " — pass"
	base.Evidence = imagePrefix + truncateOutput(out)
	base.Impact = "deterministic check passed"
	return base
}

// truncateOutput caps command output for findings.
func truncateOutput(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
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
	if cs.Status != domain.ChangeSetApproved {
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
	if err := e.createSensitiveAreaFinding(ctx, cs.InitiativeID, changeSetID, signals, "deterministic marker re-scan of the persisted patch"); err != nil {
		return err
	}
	if len(credMatches) > 0 || hasBlockingRiskSignal(signals) {
		for _, m := range credMatches {
			if err := e.db.CreateFinding(ctx, &domain.Finding{
				InitiativeID:      cs.InitiativeID,
				TargetType:        "change_set",
				TargetID:          changeSetID,
				Severity:          domain.SeverityCritical,
				Confidence:        domain.ConfidenceHigh,
				Category:          "secret_sweep",
				Code:              sweepDenyCode(true),
				Finding:           fmt.Sprintf("%s:%d: looks like %s (found at apply time)", m.Path, m.Line, m.Kind),
				Evidence:          secretSweepEvidence(m),
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
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApproved, domain.ChangeSetRejected, store.ActorOrchestrator)
		if cs.TaskID != "" {
			_ = e.db.SetTaskStatus(context.Background(), cs.TaskID, domain.TaskProposed)
		}
		return fmt.Errorf("workflow: apply denied: system deny %s", domain.DenyCodeSecretSweep)
	}

	if err := e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetApproved, domain.ChangeSetApplying, store.ActorOrchestrator); err != nil {
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
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetApproved, store.ActorOrchestrator)
		return err
	}

	// Phase 1 — Prepare (non-mutating): capture and persist the pre-image of
	// every touched path. No byte is written here, so a failure is recoverable
	// by reverting applying→approved (full-platform review: apply atomicity).
	preImage, err := fsdiff.Prepare(workspaceRoot, patch.Files, e.contentBlobs().Put)
	if err != nil {
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetApproved, store.ActorOrchestrator)
		return fmt.Errorf("workflow: apply prepare: %w", err)
	}
	preImageDigest, err := e.persistManifest(preImage)
	if err != nil {
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetApproved, store.ActorOrchestrator)
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
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetApproved, store.ActorOrchestrator)
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
	var commitSHA, commitWarning string
	if e.cfg.GitCommit {
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

	// Merge/rebase/cherry-pick/detached-HEAD are checked BEFORE any checkout
	// (review H2): switching branches (below) while one of those is in
	// progress is exactly the hazard the guard exists to prevent, so it must
	// run first, not after branch-per-initiative has already moved HEAD.
	if reason, err := git.PreCommitGuardReason(ctx, e.workspaceRoot); err != nil {
		return "", "", err
	} else if reason != "" {
		return "", "not committed because " + reason, nil
	}

	if e.cfg.Git.BranchPerInitiative {
		ini, iniErr := e.db.GetInitiative(ctx, cs.InitiativeID)
		if iniErr != nil {
			return "", "not committed: could not look up the initiative's branch: " + iniErr.Error(), nil
		}
		branch, reason, brErr := git.EnsureInitiativeBranch(ctx, e.workspaceRoot, ini.GitBranch, ini.Title, paths)
		if brErr != nil {
			return "", "not committed: could not switch to the initiative's branch: " + brErr.Error(), nil
		}
		if reason != "" {
			return "", "not committed: " + reason, nil
		}
		if branch != "" && branch != ini.GitBranch {
			if setErr := e.db.SetInitiativeGitBranch(ctx, cs.InitiativeID, branch, store.ActorOrchestrator); setErr != nil {
				return "", "checked out " + branch + " but could not record it on the initiative: " + setErr.Error(), nil
			}
		}
	}

	// The default-branch check runs AFTER the branch switch above (review
	// H2): it must judge the initiative's own branch, once
	// branch-per-initiative has switched to it, not whatever HEAD was before.
	if reason, err := git.DefaultBranchGuardReason(ctx, e.workspaceRoot, e.cfg.Git.AllowDefaultBranch); err != nil {
		return "", "", err
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
	return sha, "", err
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
// earliest OTHER applied change set for cs's initiative, created after cs,
// whose own patch touches that path (review M4). Used so a revert refusal
// can tell the human which later change set to revert first instead of
// wrongly suggesting they "discard" what is actually a legitimate later
// change. Best-effort: a patch that fails to load for one candidate change
// set is skipped rather than failing the whole lookup, since this only
// sharpens advice text and must never itself block or fail a revert attempt.
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
				if _, already := result[fc.Path]; !already {
					result[fc.Path] = other.HumanID
				}
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
// dedicated branch yet (git.branch_per_initiative has not created one --
// either the setting is off, or the initiative has not been applied yet).
var ErrNoInitiativeBranch = errors.New("workflow: this initiative has no dedicated branch yet; " +
	"enable git.branch_per_initiative and apply a change set first")

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
		// Mirror §11 EvaluateApproval: only fixed/accepted_risk resolve a blocking
		// finding; deferred/duplicate/mitigate/not_applicable/rejected do NOT clear
		// a system-deny (review: non-overridable means non-overridable).
		if f.Advisory {
			continue
		}
		resolved := f.Disposition == domain.DispositionFixed || f.Disposition == domain.DispositionAcceptedRisk
		if !resolved && f.Severity.Blocking() {
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
		// System-deny: refuse to approve while an undisposed blocking finding
		// targets this change set (e.g. a prior adversarial review's critical/high
		// finding). "System deny beats human approve" (review: system-deny tier).
		if err := e.denyReviewOpenBlocking(ctx, changeSetID); err != nil {
			return err
		}
		runID, findings, rigor, err := e.runChangeSetAdversarialReview(ctx, cs)
		if err != nil {
			return err
		}
		if rigor == RigorQuick {
			// Quick converges in one pass and accepts with advisories (review
			// B1): the adversarial reviewer's own judgment no longer gates
			// approval, though the findings are still recorded, honestly
			// marked advisory rather than silently skipped. A system-deny
			// finding (denyReviewOpenBlocking, above) is unaffected -- that is
			// a security invariant, not a rigor knob.
			for i := range findings {
				if findings[i].Code == "" {
					findings[i].Advisory = true
				}
			}
		}
		for i := range findings {
			findings[i].InitiativeID = initiativeID
			findings[i].TargetType = "change_set"
			findings[i].TargetID = changeSetID
			if err := e.db.CreateFinding(ctx, &findings[i]); err != nil {
				return err
			}
		}
		if blocked, reason := review.EvaluateApproval(findings); blocked {
			// The FIRST refusal a user meets, and the one that persists the
			// findings every later attempt then fails the deny gate on. Naming
			// the route here matters more than naming it there: by the time the
			// deny gate speaks, the user has already been told "no" once with no
			// way forward.
			return fmt.Errorf("workflow: approval blocked: %s; "+
				"`idea reviews` lists them, and `idea review <CHG-n> approve --accept-findings` "+
				"accepts the change set findings and all", reason)
		}
		if err := e.db.SetChangeSetReview(ctx, changeSetID, runID, store.HumanActor); err != nil {
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
	patch, _ := e.loadPatch(cs)
	changeFiles := changeSetFilePaths(patch)

	scale := projectScaleOf(ctx, e.workspaceRoot)
	chg := changeSize{Files: len(changeFiles)}
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
				"For each finding, set `path` to the file it is about when it names one, and `in_diff` to whether it is about code this change set actually touches."},
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
		classifyDiffEvidence(&findings[i], changeFiles)
	}
	findings = capFindings(findings, findingCap(rigor), rigor)
	return runID, findings, rigor, nil
}

// changeSetFilePaths returns the set of file paths a change set's patch
// touches, for review B2's deterministic "is this finding about a file the
// change set actually touches" check. A nil/unreadable patch yields an empty
// set (never a match), which is the safe direction: a finding that names a
// path outside a set we could not determine is advisory, not silently
// blocking.
func changeSetFilePaths(patch *fsdiff.Patch) map[string]bool {
	out := map[string]bool{}
	if patch == nil {
		return out
	}
	for _, fc := range patch.Files {
		out[fc.Path] = true
	}
	return out
}

// classifyDiffEvidence applies review B2's deterministic in-diff rule to one
// change-set review finding, mutating it in place:
//
//   - Path given, and not one of the change set's files: Advisory, with a note
//     that it is about code this change set did not touch. Path given AND a
//     change-set file: left alone -- the deterministic check confirmed it.
//   - Path omitted: the reviewer's own in_diff self-report decides Advisory
//     instead, and the note says the finding's diff-membership is
//     unverified -- self-report, not the deterministic path check.
func classifyDiffEvidence(f *domain.Finding, changeFiles map[string]bool) {
	if f.Path != "" {
		if !changeFiles[f.Path] {
			f.Advisory = true
			f.Note = appendNote(f.Note, "about code this change set did not touch")
		}
		return
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
