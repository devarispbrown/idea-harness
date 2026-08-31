package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ccontext "github.com/idea-harness/idea-harness/internal/context"
	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/executor"
	"github.com/idea-harness/idea-harness/internal/fsdiff"
	"github.com/idea-harness/idea-harness/internal/review"
	"github.com/idea-harness/idea-harness/internal/risk"
	"github.com/idea-harness/idea-harness/internal/schema"
	"github.com/idea-harness/idea-harness/internal/store"
)

// SetWorkRoot sets the directory that Execute derives per-initiative isolated
// work roots from. The daemon points this at its per-project state directory so
// the executor's WorkRoot mirrors "<state-root>/work/<initiative>".
func (e *Engine) SetWorkRoot(dir string) { e.workRoot = dir }

// SetWorkspaceRoot sets the canonical workspace that ApplyChangeSet writes into
// and RevertChangeSet restores. The daemon points this at the project root.
func (e *Engine) SetWorkspaceRoot(dir string) { e.workspaceRoot = dir }

// selectExecutor returns the executor selected by harness.executor.kind. The
// kind is a config field (deterministic), never model output; an unknown kind
// fails closed to the internal executor.
func (e *Engine) selectExecutor() executor.Executor {
	switch e.cfg.Executor.Kind {
	case config.ExecutorContainer:
		return executor.NewContainerExecutor(e.blobs, e.cfg.Executor.Image)
	case config.ExecutorOMP:
		return executor.NewOMPExecutor(e.blobs, e.implementerModel(), e.cfg.Executor.Image)
	case config.ExecutorHermes:
		return executor.NewHermesExecutor(e.blobs, e.implementerModel(), e.cfg.Executor.Image)
	default:
		return executor.NewInternalExecutor(e.blobs)
	}
}

// nextReadyTask returns the first task that is ready or proposed (i.e. not yet
// done) for an initiative, in creation order.
func (e *Engine) nextReadyTask(ctx context.Context, initiativeID string) (domain.Task, error) {
	tasks, err := e.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return domain.Task{}, err
	}
	for _, t := range tasks {
		if t.Status == domain.TaskReady || t.Status == domain.TaskProposed {
			return t, nil
		}
	}
	return domain.Task{}, fmt.Errorf("workflow: no ready tasks for initiative %q", initiativeID)
}

// Execute runs the next ready task through the configured executor and records
// the resulting change set, advancing it through deterministic checks to either
// review_required (clean) or rejected (a detected secret/credential/pii marker).
func (e *Engine) Execute(ctx context.Context, initiativeID string) (domain.ChangeSet, error) {
	task, err := e.nextReadyTask(ctx, initiativeID)
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
		WorkRoot:      filepath.Join(root, "work", initiativeID),
	}

	exec := e.selectExecutor()
	// The capability gate is the deterministic trust-tier policy, not the
	// executor's self-declared capability list: the engine authorizes the
	// selected executor's engine-assigned trust tier (§49).
	trust := exec.Capabilities(ctx).Trust
	if !executor.TrustAllows(trust, "write_file") {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: trust tier %q may not write_file", trust)
	}

	// Snapshot the isolated work root BEFORE the executor runs, so the
	// orchestrator computes the real diff itself — never trusting the executor's
	// self-declaration (review B3).
	before, err := fsdiff.Snapshot(req.WorkRoot)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: snapshot before: %w", err)
	}

	attempt, err := exec.Start(ctx, req)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: %w", err)
	}
	if err := waitForCompletion(ctx, exec, attempt.ID); err != nil {
		return domain.ChangeSet{}, err
	}
	if _, err := exec.Collect(ctx, attempt.ID); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: %w", err)
	}

	// Snapshot AFTER and compute the orchestrator-authored diff.
	after, err := fsdiff.Snapshot(req.WorkRoot)
	if err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: snapshot after: %w", err)
	}
	files := fsdiff.Diff(before, after)

	// Persist each produced file's content and the patch manifest to the blob
	// store, so apply/revert can reconstruct the diff from digests alone.
	if err := e.persistDiff(req.WorkRoot, files); err != nil {
		return domain.ChangeSet{}, fmt.Errorf("workflow: execute: persist diff: %w", err)
	}
	patchDigest, err := e.persistPatch(fsdiff.Patch{BaseSnapshotDigest: before.Digest(), Files: files})
	if err != nil {
		return domain.ChangeSet{}, err
	}

	cs := &domain.ChangeSet{
		InitiativeID:   initiativeID,
		TaskID:         task.ID,
		BaseSnapshotID: before.Digest(),
		PatchDigest:    patchDigest,
		CreatedByRunID: attempt.ID,
	}
	// CreateChangeSet forces status=draft and assigns CHG-N.
	if err := e.db.CreateChangeSet(ctx, cs); err != nil {
		return domain.ChangeSet{}, err
	}
	if err := e.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, store.ActorOrchestrator); err != nil {
		return domain.ChangeSet{}, err
	}

	// Record the diff-dependent deterministic checks (B6) as Findings. They are
	// not_applicable for a changelog-only patch, so the pipeline is honest about
	// which checks did and did not run.
	for _, f := range e.runDeterministicChecks(cs) {
		f := f
		if err := e.db.CreateFinding(ctx, &f); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// Secret/credential/pii sweep gates rejection. The scan runs over the actual
	// produced file contents (rendered from the persisted diff), so it reflects
	// what apply will write.
	signals := risk.RiskSignals(ctx, patchDigest, e.renderDiffText(files))
	if hasBlockingRiskSignal(signals) {
		// Record a blocking finding so the rejection is traceable.
		if err := e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   cs.InitiativeID,
			TargetType:     "change_set",
			TargetID:       cs.ID,
			Severity:       domain.SeverityCritical,
			Confidence:     domain.ConfidenceHigh,
			Category:       "secret_sweep",
			Finding:        "blocking risk signal detected: " + detectedRiskNames(signals),
			Evidence:       "deterministic marker scan of the produced patch",
			Impact:         "change set rejected before review",
			Recommendation: "remove the sensitive content and re-execute",
			Disposition:    domain.DispositionRejected,
		}); err != nil {
			return domain.ChangeSet{}, err
		}
		if err := e.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
			return domain.ChangeSet{}, err
		}
	} else {
		if err := e.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, store.ActorOrchestrator); err != nil {
			return domain.ChangeSet{}, err
		}
	}

	// Mark the task done so a repeated Execute does not re-pick the same task and
	// create a duplicate change set.
	if err := e.db.SetTaskStatus(ctx, task.ID, domain.TaskDone); err != nil {
		return domain.ChangeSet{}, err
	}

	got, err := e.db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	return *got, nil
}

// persistDiff persists each produced file's content to the blob store (keyed by
// digest), so apply/revert can reconstruct the diff from digests alone.
func (e *Engine) persistDiff(workRoot string, files []domain.FileChange) error {
	if e.blobs == nil {
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
		if _, err := e.blobs.Put(data); err != nil {
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
	if e.blobs == nil {
		return fsdiff.Patch{BaseSnapshotDigest: patch.BaseSnapshotDigest, Files: patch.Files}.Digest(), nil
	}
	return e.blobs.Put(data)
}

// renderDiffText renders the file contents of a diff as one readable string (for
// the deterministic secret sweep).
func (e *Engine) renderDiffText(files []domain.FileChange) string {
	if e.blobs == nil {
		return ""
	}
	var b strings.Builder
	for _, fc := range files {
		if fc.IsDelete() {
			fmt.Fprintf(&b, "delete %s\n", fc.Path)
			continue
		}
		content, err := e.blobs.Get(fc.Digest)
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
	if cs.PatchDigest == "" || e.blobs == nil {
		return &fsdiff.Patch{}, nil
	}
	data, err := e.blobs.Get(cs.PatchDigest)
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
func (e *Engine) RenderPatch(cs *domain.ChangeSet) string {
	patch, err := e.loadPatch(cs)
	if err != nil {
		return fmt.Sprintf("(patch unreadable: %v)", err)
	}
	return e.renderDiffText(patch.Files)
}

// persistManifest stores a snapshot manifest as JSON and returns its digest.
func (e *Engine) persistManifest(m fsdiff.Manifest) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("workflow: marshal manifest: %w", err)
	}
	if e.blobs == nil {
		return m.Digest(), nil
	}
	return e.blobs.Put(data)
}

// loadManifest reads and decodes a snapshot manifest by digest.
func (e *Engine) loadManifest(digest string) (fsdiff.Manifest, error) {
	if digest == "" {
		return fsdiff.Manifest{}, nil
	}
	if e.blobs == nil {
		return nil, fmt.Errorf("workflow: no blob store")
	}
	data, err := e.blobs.Get(digest)
	if err != nil {
		return nil, fmt.Errorf("workflow: load manifest: %w", err)
	}
	var m fsdiff.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("workflow: decode manifest: %w", err)
	}
	return m, nil
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
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("workflow: execute: attempt did not complete within %s", maxDuration)
		default:
		}
		st, err := exec.Status(ctx, attemptID)
		if err != nil {
			return fmt.Errorf("workflow: execute: %w", err)
		}
		switch st.Status {
		case "succeeded":
			return nil
		case "failed", "cancelled":
			return fmt.Errorf("workflow: execute: attempt %s", st.Status)
		}
		time.Sleep(sleep)
		if sleep < maxSleep {
			sleep *= 2
		}
	}
}

// deterministicChecks enumerates the diff-dependent checks from the M3 plan
// (B6): patch-applies, build/type-check, and tests. They cannot run against a
// changelog-only patch, so each is recorded as an explicit not_applicable
// Finding rather than being silently skipped. Once an executor captures a base
// snapshot and produces a real file diff, these switch from "not applicable" to
// their runner path.
var deterministicChecks = []struct {
	category       string
	check          string
	notApplicable  string
	recommendation string
}{
	{"patch_applies", "patch applies cleanly to base snapshot", "no base snapshot captured (changelog-only patch)", "capture a base snapshot and produce a real file diff"},
	{"build", "build/type-check passes", "no buildable artifacts (changelog-only patch)", "produce real source changes to build and type-check"},
	{"tests", "tests pass", "no tests to run (changelog-only patch)", "produce real source changes with tests"},
}

// runDeterministicChecks emits one Finding per diff-dependent deterministic
// check. A change set with no base snapshot is a changelog-only patch, so those
// checks are recorded as not_applicable; a base snapshot flips them to a
// deferred "runner not wired" state until the build/test runners are implemented.
func (e *Engine) runDeterministicChecks(cs *domain.ChangeSet) []domain.Finding {
	out := make([]domain.Finding, 0, len(deterministicChecks))
	for _, c := range deterministicChecks {
		f := domain.Finding{
			InitiativeID: cs.InitiativeID,
			TargetType:   "change_set",
			TargetID:     cs.ID,
			Severity:     domain.SeverityObservation,
			Confidence:   domain.ConfidenceHigh,
			Category:     c.category,
		}
		if cs.BaseSnapshotID == "" {
			f.Finding = c.check + " — not applicable"
			f.Evidence = c.notApplicable
			f.Impact = "no file diff to verify; recorded for traceability"
			f.Recommendation = c.recommendation
			f.Disposition = domain.DispositionNotApplicable
		} else {
			f.Finding = c.check + " — runner not wired"
			f.Evidence = "base snapshot present (" + cs.BaseSnapshotID + "), but the " + c.check + " runner is not yet implemented"
			f.Impact = "cannot yet verify this check for a diff patch"
			f.Recommendation = c.recommendation
			f.Disposition = domain.DispositionDeferred
		}
		out = append(out, f)
	}
	return out
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

	if err := e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetApproved, domain.ChangeSetApplying, store.ActorOrchestrator); err != nil {
		return err
	}

	// Write the diff, capturing (and persisting) the pre-image of every touched
	// path so revert can restore it byte-for-byte.
	preImage, err := fsdiff.Apply(workspaceRoot, patch.Files, e.blobs.Get, e.blobs.Put)
	if err != nil {
		return fmt.Errorf("workflow: apply files: %w", err)
	}
	preImageDigest, err := e.persistManifest(preImage)
	if err != nil {
		return err
	}

	// applying→applied + committed journal (carrying the pre-image digest) happen
	// in ONE transaction so status and journal never diverge (§39).
	if err := e.db.MarkChangeSetApplied(ctx, changeSetID, preImageDigest, store.ActorOrchestrator); err != nil {
		return err
	}
	if cs.GitCommitSHA != "" {
		if err := e.db.SetChangeSetCommit(ctx, changeSetID, cs.GitCommitSHA, store.ActorOrchestrator); err != nil {
			return err
		}
	}
	return nil
}

// RevertChangeSet rolls back an applied change set: it restores the pre-image
// snapshot recorded at apply time (byte-for-byte), then advances
// applied→reverted plus a rolled_back journal entry atomically (§39, review B1).
func (e *Engine) RevertChangeSet(ctx context.Context, initiativeID, changeSetID string) error {
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
	if err := fsdiff.Restore(workspaceRoot, preImage, e.blobs.Get); err != nil {
		return fmt.Errorf("workflow: revert files: %w", err)
	}
	return e.db.MarkChangeSetReverted(ctx, changeSetID, store.ActorOrchestrator)
}

// ReviewChangeSet applies a human review verdict. "approved" runs the
// adversarial reviewer and gates approval on its findings; "changes_requested"
// and "rejected" apply the corresponding state transition.
func (e *Engine) ReviewChangeSet(ctx context.Context, initiativeID, changeSetID, verdict string) error {
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
		runID, findings, err := e.runChangeSetAdversarialReview(ctx, cs)
		if err != nil {
			return err
		}
		for i := range findings {
			findings[i].InitiativeID = initiativeID
			if err := e.db.CreateFinding(ctx, &findings[i]); err != nil {
				return err
			}
		}
		if blocked, reason := review.EvaluateApproval(findings); blocked {
			return fmt.Errorf("workflow: approval blocked: %s", reason)
		}
		if err := e.db.SetChangeSetReview(ctx, changeSetID, runID, store.HumanActor); err != nil {
			return err
		}
		return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, store.HumanActor)

	case "changes_requested":
		return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetChangesRequested, store.HumanActor)

	case "rejected":
		// Rejection is accepted from either checks_running or review_required.
		switch cs.Status {
		case domain.ChangeSetChecksRunning:
			return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.HumanActor)
		case domain.ChangeSetReviewRequired:
			return e.db.TransitionChangeSet(ctx, changeSetID, domain.ChangeSetReviewRequired, domain.ChangeSetRejected, store.HumanActor)
		default:
			return fmt.Errorf("workflow: cannot reject change set in status %q", cs.Status)
		}

	default:
		return fmt.Errorf("workflow: unknown review verdict %q", verdict)
	}
}

// runChangeSetAdversarialReview runs the adversarial reviewer over a change set
// and converts its structured output into findings.
func (e *Engine) runChangeSetAdversarialReview(ctx context.Context, cs *domain.ChangeSet) (string, []domain.Finding, error) {
	runID, obj, err := e.runModel(ctx, cs.InitiativeID, StageReview, RoleAdversarialReviewer, "review", schema.Review,
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
