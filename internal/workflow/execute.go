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
	return executor.ResolveAgentProvider(e.implementerModel(), e.cfg.DefaultModel, e.cfg.Providers)
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

	attempt, err := exec.Start(ctx, req)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: %w", err)
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
		return domain.ChangeSet{}, e.withAgentOutput(ctx, exec, attempt.ID, err)
	}
	// Read before Collect, which removes the container and its output with it.
	// Hermes exits 0 even when every model call failed, so a clean exit that
	// changed nothing is the common failure, and this is the only record of why.
	agentOutput := e.agentOutput(ctx, exec, attempt.ID)
	if _, err := exec.Collect(ctx, attempt.ID); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: %w", err)
	}
	completed = true

	// Snapshot AFTER and compute the orchestrator-authored diff.
	after, err := fsdiff.Snapshot(req.WorkRoot)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: snapshot after: %w", err)
	}
	files := fsdiff.Diff(before, after)

	// Sweep the produced content BEFORE persisting it: if it carries a known
	// secret or credential, we reject without ever writing the raw bytes to the
	// redaction-free raw store (review: raw-blob GC for rejected patches).
	diffText := e.renderDiffTextFromWorkRoot(req.WorkRoot, files)
	hasCredential := e.detectCredential(diffText)
	signals := risk.RiskSignals(ctx, "", diffText)

	// Persist each produced file's content and the patch manifest to the blob
	// store, so apply/revert can reconstruct the diff from digests alone. Skip
	// the raw content write when ANY blocking signal was detected (credential OR
	// risk marker), so rejected content is never written to the raw store.
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
			rawCred := e.detectCredential(f.Evidence)
			rawSignals := risk.RiskSignals(ctx, "", f.Evidence)
			if rawCred || hasBlockingRiskSignal(rawSignals) {
				f.Code = sweepDenyCode(rawCred, rawSignals)
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
		}
	}

	// A coding agent that changed nothing is reported, not failed. Hermes exits
	// 0 after a failed model call, so this is the common failure, and its own
	// output is the only record of why -- so it goes on the change set, where
	// the review shows it. A hard failure here was tried and stranded
	// initiatives: the work root carries every earlier attempt, so an agent
	// that correctly finds a task already done has nothing to change, and
	// failing it made the task impossible to get past.
	if _, isAgent := exec.(executor.OutputTailer); isAgent && len(files) == 0 {
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

	// Secret/credential/pii sweep gates rejection. diffText/signals/hasCredential
	// were computed above from the work-root content, BEFORE the raw store write.
	rejected := false
	if checksBlocked || hasBlockingRiskSignal(signals) || hasCredential {
		rejected = true
		// Record a blocking finding so the rejection is traceable. The finding
		// never echoes the credential value (full-platform review: secret leak).
		if hasBlockingRiskSignal(signals) || hasCredential {
			reason := detectedRiskNames(signals)
			code := sweepDenyCode(hasCredential, signals)
			if hasCredential {
				reason = "credential detected"
			}
			if err := e.db.CreateFinding(ctx, &domain.Finding{
				InitiativeID:   cs.InitiativeID,
				TargetType:     "change_set",
				TargetID:       cs.ID,
				Severity:       domain.SeverityCritical,
				Confidence:     domain.ConfidenceHigh,
				Category:       "secret_sweep",
				Code:           code,
				Finding:        "blocking risk signal detected: " + reason,
				Evidence:       "deterministic scan of the produced patch",
				Impact:         "change set rejected before review",
				Recommendation: "remove the sensitive content and re-execute",
				Disposition:    domain.DispositionRejected,
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
}

// baseContent returns fc's REDACTED pre-change content, or "" when there is
// none (an added file, or a base that cannot be recovered).
//
// The base is read from the content blob store first -- fsdiff.Prepare
// persists the workspace's pre-image there keyed by digest, at apply time, for
// revert -- and falls back to the live workspace file otherwise. That fallback
// is what makes a diff available at REVIEW time, before any apply has
// happened: the work root the agent edited is a fresh copy (Track A), so the
// file at this path in the actual workspace is still exactly what the agent
// started from.
func (e *Engine) baseContent(fc domain.FileChange) string {
	if fc.BaseDigest == "" {
		return ""
	}
	var data []byte
	if e.contentBlobs() != nil {
		if d, err := e.contentBlobs().Get(fc.BaseDigest); err == nil {
			data = d
		}
	}
	if data == nil && e.workspaceRoot != "" {
		if d, err := os.ReadFile(filepath.Join(e.workspaceRoot, filepath.FromSlash(fc.Path))); err == nil {
			data = d
		}
	}
	if data == nil {
		return ""
	}
	raw := string(data)
	if e.redactor != nil {
		return e.redactor.RedactString(raw)
	}
	return security.Redact(raw)
}

// patchFor renders fc's unified diff against its base. newContent is fc's
// already-redacted post-image content (StructuredDiff's sf.Content); a delete
// has none.
func (e *Engine) patchFor(fc domain.FileChange, newContent string) string {
	old := e.baseContent(fc)
	if fc.IsDelete() {
		newContent = ""
	}
	if old == "" && newContent == "" {
		return ""
	}
	return textdiff.Unified(old, newContent)
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
		sf.Patch = e.patchFor(fc, sf.Content)
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
	t, ok := exec.(executor.OutputTailer)
	if !ok {
		return ""
	}
	return e.redact(t.OutputTail(ctx, attemptID))
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
	// never run on the host. It requires a configured toolchain image (review H3).
	if e.cfg.Check.Image == "" {
		base.Severity = domain.SeverityHigh
		base.Code = domain.DenyCodeHostExec
		base.Finding = description + " — FAIL (no sandbox image)"
		base.Disposition = domain.DispositionRejected
		base.Evidence = "check.image is not configured; refusing to run " + category + " on the host"
		base.Impact = "deterministic check could not run safely"
		base.Recommendation = "set check.image to a toolchain image (e.g. golang:1.25, node:22)"
		return base
	}

	timeout := 10 * time.Minute
	if e.cfg.CheckTimeoutSeconds > 0 {
		timeout = time.Duration(e.cfg.CheckTimeoutSeconds) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Run inside an isolated container (no network, read-only root, dropped
	// capabilities) with only the work root mounted.
	sandbox := executor.NewContainerSandbox(e.cfg.Check.Image)
	stdout, stderr, exitCode, runErr := sandbox.Run(checkCtx, executor.RunOptions{
		WorkRoot: workRoot,
		RepoRoot: e.workspaceRoot, // read-only /repo mount so checks can read the real project
		Cmd:      []string{"sh", "-c", cmd},
	})
	if runErr != nil {
		// Docker unavailable or launch failed → fail closed, never host-exec.
		base.Severity = domain.SeverityHigh
		base.Code = domain.DenyCodeHostExec
		base.Finding = description + " — FAIL (sandbox unavailable)"
		base.Disposition = domain.DispositionRejected
		base.Evidence = fmt.Sprintf("sandbox launch failed: %v\n%s", runErr, truncateOutput(stderr))
		base.Impact = "deterministic check could not run safely"
		base.Recommendation = "ensure Docker is available and check.image is pullable"
		return base
	}
	out := stdout + stderr
	if exitCode != 0 {
		base.Severity = domain.SeverityHigh
		base.Finding = description + " — FAIL"
		base.Disposition = domain.DispositionRejected // review M5: a rejected change set's findings are closed, not left open
		if checkCtx.Err() == context.DeadlineExceeded {
			base.Evidence = fmt.Sprintf("timed out after %s\n%s", timeout, truncateOutput(out))
			base.Recommendation = "the " + category + " command hung; fix it or raise check_timeout_seconds"
		} else {
			base.Evidence = fmt.Sprintf("exit code %d\n%s", exitCode, truncateOutput(out))
			base.Recommendation = "fix the failure and re-execute"
		}
		base.Impact = "deterministic check failed"
		return base
	}
	base.Severity = domain.SeverityObservation
	base.Finding = description + " — pass"
	base.Evidence = truncateOutput(out)
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

// hasBlockingRiskSignal reports whether any detected signal hard-rejects the
// change set at deterministic checks (§45). The hard-reject set is limited to
// markers that are always sensitive in source regardless of context:
//
//   - secret / credential (api keys, passwords, tokens) — never legitimate.
//   - pii (SSN) — personal data in a patch is always a bug.
//
// auth, network, and payment are intentionally review-gated, not hard-rejected:
// a legitimate patch may add an auth endpoint, an HTTPS URL, or a payment field,
// so the adversarial reviewer decides instead of a naive word scan.
func hasBlockingRiskSignal(signals []domain.RiskSignal) bool {
	for _, s := range signals {
		if !s.Detected {
			continue
		}
		switch s.Name {
		case risk.SignalSecret, risk.SignalCredential, risk.SignalPII:
			return true
		}
	}
	return false
}

// sweepDenyCode derives the machine-readable system-deny code from what the
// sweep actually detected: secret/credential (marker OR credential pattern) →
// secret_sweep; PII-only → pii. This keeps Finding.Code consistent with the
// human-readable reason.
func sweepDenyCode(hasCredential bool, signals []domain.RiskSignal) domain.DenyCode {
	for _, s := range signals {
		if !s.Detected {
			continue
		}
		if s.Name == risk.SignalSecret || s.Name == risk.SignalCredential {
			return domain.DenyCodeSecretSweep
		}
	}
	if hasCredential {
		return domain.DenyCodeSecretSweep
	}
	return domain.DenyCodePII
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
	// secret/credential/PII catalog may have grown since Execute swept it. Never
	// mutate the workspace if a signal is now detected (system-deny).
	diffText := e.renderDiffText(patch.Files)
	signals := risk.RiskSignals(ctx, cs.PatchDigest, diffText)
	hasCredential := e.detectCredential(diffText)
	if hasCredential || hasBlockingRiskSignal(signals) {
		code := sweepDenyCode(hasCredential, signals)
		if err := e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   cs.InitiativeID,
			TargetType:     "change_set",
			TargetID:       changeSetID,
			Severity:       domain.SeverityCritical,
			Confidence:     domain.ConfidenceHigh,
			Category:       "secret_sweep",
			Code:           code,
			Finding:        "blocking risk signal detected in patch at apply time",
			Evidence:       "deterministic re-sweep of the persisted patch",
			Impact:         "apply denied before workspace mutation",
			Recommendation: "reject the change set and re-execute without the sensitive content",
			Disposition:    domain.DispositionRejected,
		}); err != nil {
			return err
		}
		// Recover the change set to a terminal state and re-open the task so a
		// corrected re-execute replaces it (review: system-deny deadlock fix).
		_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApproved, domain.ChangeSetRejected, store.ActorOrchestrator)
		if cs.TaskID != "" {
			_ = e.db.SetTaskStatus(context.Background(), cs.TaskID, domain.TaskProposed)
		}
		return fmt.Errorf("workflow: apply denied: system deny %s", code)
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
	var commitSHA string
	if e.cfg.GitCommit {
		sha, err := e.commitChangeSet(ctx, cs, patch)
		if err != nil {
			_ = fsdiff.Restore(workspaceRoot, preImage, e.contentBlobs().Get)
			_ = e.db.TransitionChangeSet(context.Background(), changeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
			return fmt.Errorf("workflow: git commit: %w", err)
		}
		commitSHA = sha
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
	// Track B: the verification_engineer records deterministic evidence. The
	// checks passed (otherwise the change set would have been rejected), but the
	// acceptance criterion itself needs a human/real measurement (§70).
	if err := e.recordAutomatedEvidence(ctx, cs); err != nil {
		return err
	}
	return nil
}

// commitChangeSet creates the opt-in git commit for an applied change set and
// returns the commit SHA ("" when the workspace is not a git repository or the
// diff was empty). The subject is "<HumanID>: <task title>" (HumanID is already
// CHG-N), with the task title sanitized so it can never inject extra commit
// lines or a forged header.
// A title-lookup failure is non-fatal: the commit still proceeds with an empty
// title rather than failing the whole apply.
func (e *Engine) commitChangeSet(ctx context.Context, cs *domain.ChangeSet, patch *fsdiff.Patch) (string, error) {
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
	paths := make([]string, 0, len(patch.Files))
	for _, f := range patch.Files {
		paths = append(paths, f.Path)
	}
	message := fmt.Sprintf("%s: %s", cs.HumanID, sanitizeCommitTitle(title))
	return git.Commit(ctx, e.workspaceRoot, paths, message)
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
		runID, findings, err := e.runChangeSetAdversarialReview(ctx, cs)
		if err != nil {
			return err
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
// and converts its structured output into findings.
func (e *Engine) runChangeSetAdversarialReview(ctx context.Context, cs *domain.ChangeSet) (string, []domain.Finding, error) {
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
			{Selector: "immutable_policy", Content: "Independently challenge the change set. Emit structured findings. Never set accepted_risk."},
			{Selector: "change_set", Content: renderChangeSet(cs, e.RenderPatch(cs))},
		},
		"Adversarial review of change set "+cs.HumanID+".",
	)
	if err != nil {
		return "", nil, err
	}
	findings, err := review.ParseFindings(runID, "change_set", cs.ID, obj)
	if err != nil {
		return "", nil, err
	}
	return runID, findings, nil
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
