// Package daemon implements the localhost control-plane HTTP server (§28–§29):
// token-authenticated, project-scoped REST + SSE over one user-level daemon.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/project"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// Runtime owns the global registry, the per-install token, and lazily-opened
// per-project runtimes.
type Runtime struct {
	stateRoot string
	token     string
	global    *store.DB
	log       *slog.Logger

	mu       sync.Mutex
	projects map[string]*projectRuntime
	// reconciled records which projects have already had startup reconciliation
	// run in this daemon lifetime, so a reload never re-reconciles (and thereby
	// fails) a live operation (full-platform review: project-runtime race).
	reconciled map[string]bool

	// cancelMu guards cancels: live cancellation handles for in-flight
	// asynchronous operations, keyed by operation id (M3 cancel, §16).
	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc
}

type projectRuntime struct {
	db       *store.DB
	blobs    *blob.Store // redacted store: model artifacts/context
	rawBlobs *blob.Store // redaction-free store: diff/apply + image bytes
	cfg      config.Config
	rootPath string
	dotEnv   map[string]string
	registry *model.Registry
	engine   *workflow.Engine
	redactor *security.Redactor
}

// NewRuntime opens (creating if needed) the global state and token.
func NewRuntime(stateRoot string, log *slog.Logger) (*Runtime, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(filepath.Join(stateRoot, "projects"), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: state dir: %w", err)
	}
	global, err := store.OpenGlobal(filepath.Join(stateRoot, "global.db"))
	if err != nil {
		return nil, err
	}
	token, err := security.LoadOrCreateToken(filepath.Join(stateRoot, "token"))
	if err != nil {
		global.Close()
		return nil, err
	}
	return &Runtime{
		stateRoot:  stateRoot,
		token:      token,
		global:     global,
		log:        log,
		projects:   map[string]*projectRuntime{},
		reconciled: map[string]bool{},
		cancels:    map[string]context.CancelFunc{},
	}, nil
}

// Token returns the per-install API token.
func (r *Runtime) Token() string { return r.token }

// Close releases global resources.
func (r *Runtime) Close() error { return r.global.Close() }

// StateRoot returns the global state directory.
func (r *Runtime) StateRoot() string { return r.stateRoot }

// CreateProject initializes .idea-harness/ in rootPath and registers it.
func (r *Runtime) CreateProject(ctx context.Context, rootPath string) (string, error) {
	if err := project.InitConfig(rootPath); err != nil {
		return "", err
	}
	name := filepath.Base(filepath.Clean(rootPath))
	projectID, err := r.global.RegisterProject(ctx, name, rootPath, "")
	if err != nil {
		return "", err
	}
	// Open the per-project runtime eagerly to surface config errors now.
	if _, err := r.EnsureProject(ctx, projectID); err != nil {
		return "", err
	}
	return projectID, nil
}

// EnsureProject returns (opening if needed) the runtime for a project id.
// Construction is single-flight: r.mu is held across the whole build so two
// concurrent first-opens cannot open two *store.DB handles for one project
// (full-platform review: project-runtime race).
func (r *Runtime) EnsureProject(ctx context.Context, projectID string) (*projectRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pr, ok := r.projects[projectID]; ok {
		return pr, nil
	}

	rec, err := r.global.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("daemon: unknown project %q: %w", projectID, err)
	}

	projDir := filepath.Join(r.stateRoot, "projects", projectID)
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: project dir: %w", err)
	}
	db, err := store.Open(filepath.Join(projDir, "state.db"))
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(filepath.Join(rec.RootPath, config.Dir))
	if err != nil {
		db.Close()
		return nil, err
	}
	// Load the project's .env as a fallback for secret resolution (§47).
	dotEnv := config.LoadDotEnv(rec.RootPath)

	// Redaction choke point: the blob store redacts known secrets + patterns
	// before any content is persisted (§47–§48). This store is for model
	// artifacts/context only.
	redactor := security.NewRedactor(collectSecrets(cfg, dotEnv)...)
	blobs, err := blob.New(filepath.Join(projDir, "blobs"), redactor.Redact)
	if err != nil {
		db.Close()
		return nil, err
	}
	// File content (diff/apply/revert) must round-trip byte-for-byte, so it uses a
	// redaction-free store keyed by the raw digest (review C1/C2).
	rawBlobs, err := blob.New(filepath.Join(projDir, "raw-blobs"), nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	registry := buildRegistry(cfg, r.log, dotEnv)
	engine := workflow.New(db, blobs, registry, cfg.Harness, r.log)
	engine.SetRawBlobs(rawBlobs)
	engine.SetRedactor(redactor)
	// Point the execute pipeline's isolated work roots at this project's state
	// directory so WorkRoot mirrors "<state-root>/work/<initiative>".
	engine.SetWorkRoot(projDir)
	// The canonical workspace that apply writes into and revert restores is the
	// project root (the user's real source tree).
	engine.SetWorkspaceRoot(rec.RootPath)

	pr := &projectRuntime{db: db, blobs: blobs, rawBlobs: rawBlobs, cfg: cfg, rootPath: rec.RootPath, dotEnv: dotEnv, registry: registry, engine: engine, redactor: redactor}

	r.projects[projectID] = pr

	// Reconcile once per daemon lifetime (never on a reload), so a live
	// operation is never spuriously failed by a re-open. Runs after the engine +
	// raw blob store exist so an interrupted apply can be restored.
	if !r.reconciled[projectID] {
		r.reconciled[projectID] = true
		r.reconcileStaleOperations(ctx, pr)
	}
	return pr, nil
}

// ReloadProject drops the cached runtime so the next EnsureProject re-reads
// config and rebuilds the provider registry (§40). It rebuilds the
// and rebuilds the provider registry (§40). It builds a NEW *projectRuntime
// reusing the same *store.DB (no second DB handle) and swaps the pointer under
// r.mu, so a reload never mutates fields that a concurrent reader/operation is
// using (review: project-runtime / field-swap race). Reconciliation does not
// re-run (reconciled flag stays set).
func (r *Runtime) ReloadProject(ctx context.Context, projectID string) (*projectRuntime, error) {
	r.mu.Lock()
	pr, ok := r.projects[projectID]
	r.mu.Unlock()
	if !ok {
		return r.EnsureProject(ctx, projectID)
	}

	// Refuse to swap config while an operation is in flight: a pipeline goroutine
	// (Run/Advance/SuggestAnswers) reads the engine's config/registry/redactor
	// without holding e.mu, so a concurrent reload would race those reads. This
	// keeps the swap safe without serializing every pipeline (review: v1).
	if ops, err := pr.db.ListUnfinishedOperations(ctx); err == nil && len(ops) > 0 {
		return nil, fmt.Errorf("daemon: cannot reload project while %d operation(s) are running", len(ops))
	}

	cfg, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		return nil, err
	}
	dotEnv := config.LoadDotEnv(pr.rootPath)
	redactor := security.NewRedactor(collectSecrets(cfg, dotEnv)...)
	blobs, err := blob.New(filepath.Join(r.stateRoot, "projects", projectID, "blobs"), redactor.Redact)
	if err != nil {
		return nil, err
	}
	registry := buildRegistry(cfg, r.log, dotEnv)

	// Rebuild config-derived components in place on the SAME engine (its mutex
	// keeps serializing execute/apply/revert across the reload); then publish the
	// pr fields under r.mu.
	pr.engine.Reload(cfg.Harness, registry, redactor, blobs)
	r.mu.Lock()
	pr.cfg = cfg
	pr.dotEnv = dotEnv
	pr.registry = registry
	pr.blobs = blobs
	pr.redactor = redactor
	r.mu.Unlock()
	return pr, nil
}

// registerCancel records a live cancellation handle for opID.
func (r *Runtime) registerCancel(opID string, cancel context.CancelFunc) {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	r.cancels[opID] = cancel
}

// unregisterCancel drops a cancellation handle once its operation settles.
func (r *Runtime) unregisterCancel(opID string) {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	delete(r.cancels, opID)
}

// launchOperation starts fn in a goroutine with a cancellable context that is
// registered under opID so CancelOperation can interrupt it. fn is expected to
// transition the operation to a terminal status; the handle is deregistered
// and released when fn returns.
func (r *Runtime) launchOperation(opID string, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	r.registerCancel(opID, cancel)
	go func() {
		defer cancel()
		defer r.unregisterCancel(opID)
		// These goroutines are detached, so net/http's per-request recovery does
		// not reach them: one panic in a pipeline took the whole daemon down,
		// including any other operation mid-apply between committing files and
		// marking the change set applied.
		//
		// It also stranded the initiative claim. Recovering here lets the
		// operation be recorded as failed and the claim released, which is the
		// difference between one broken initiative and a dead process.
		defer func() {
			if p := recover(); p != nil {
				r.log.Error("operation panicked",
					"operation_id", opID, "panic", p, "stack", string(debug.Stack()))
				r.failOperationAfterPanic(opID, p)
			}
		}()
		fn(ctx)
	}()
}

// failOperationAfterPanic records a panicked operation as failed and releases
// the initiative claim, searching the registry because launchOperation is not
// told which project owns the id.
func (r *Runtime) failOperationAfterPanic(opID string, p any) {
	ctx := context.Background()
	projects, err := r.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, proj := range projects {
		pr, err := r.EnsureProject(ctx, proj.ProjectID)
		if err != nil {
			continue
		}
		op, err := pr.db.GetOperation(ctx, opID)
		if err != nil {
			continue
		}
		// Both writes are gated on the operation still being live, and on the
		// SAME outcome.
		//
		// A panic can happen after the goroutine's own terminal write -- in a
		// trailing log line, say. Rewriting a succeeded operation to failed is
		// bad; rewriting only the initiative is worse, because it leaves
		// operation=succeeded with initiative=failed and sends a user to
		// `idea retry` on a run that actually worked, re-running the whole
		// pipeline and spending real model budget.
		//
		// So the initiative release happens only when this call is the one that
		// actually moved the operation to failed.
		if op.Status != domain.OperationPending && op.Status != domain.OperationRunning {
			r.log.Warn("operation panicked after it had already finished; leaving the record alone",
				"operation_id", opID, "status", op.Status, "panic", p)
			return
		}
		if err := pr.db.SetOperationStatus(ctx, opID, domain.OperationFailed, ""); err != nil {
			r.log.Error("recording a panicked operation failed", "operation_id", opID, "error", err)
			return
		}
		if op.InitiativeID != "" {
			if err := pr.db.FailInitiative(ctx, op.InitiativeID,
				fmt.Sprintf("internal error while running %s", op.Type)); err != nil {
				r.log.Error("releasing the initiative after a panic failed",
					"initiative", op.InitiativeID, "error", err)
			}
		}
		return
	}
}

// failOperation transitions opID to a terminal failed (or cancelled) status and
// optionally fails the initiative. It uses a fresh context so the durable status
// write survives cancellation of the operation's own context.
func (r *Runtime) failOperation(opID, initiativeID, logMsg string, opCtx context.Context, pr *projectRuntime, runErr error, failInitiative bool) {
	r.log.Error(logMsg, "initiative", initiativeID, "op", opID, "err", runErr)
	sctx := context.Background()
	status := domain.OperationFailed
	if opCtx.Err() != nil {
		status = domain.OperationCancelled
	}
	_ = pr.db.SetOperationStatus(sctx, opID, status, "")

	// Release the initiative on cancellation too, not only on failure.
	//
	// The claim added this round marks the initiative running before the work
	// starts, and only a terminal transition releases it. This branch used to
	// require status == OperationFailed, so cancelling left the operation
	// correctly cancelled and the initiative running forever: every later
	// advance/execute/revise answered "already running", reconcileStaleOperations
	// could not see it (a cancelled operation is terminal, and it only scans
	// pending/running), and no CLI command could recover it. That is a worse bug
	// than the double-click race the claim closes, introduced by closing it.
	//
	// `failed` is the right resting state for both: it is honest about the work
	// not completing, and `idea retry` runs from exactly there.
	if failInitiative && (status == domain.OperationFailed || status == domain.OperationCancelled) {
		reason := runErr.Error()
		if status == domain.OperationCancelled {
			reason = "cancelled: " + reason
		}
		if err := pr.db.FailInitiative(sctx, initiativeID, reason); err != nil {
			r.log.Error("could not release the initiative after a terminal operation",
				"initiative", initiativeID, "op", opID, "error", err)
		}
	}
}

// CancelOperation interrupts a running asynchronous operation (M3, §16): it
// cancels the operation's context so the in-flight model/command call unwinds,
// and marks the operation cancelled so callers observe the terminal state
// immediately. Returns an error when no live handle exists (already settled or
// orphaned after a daemon restart).
func (r *Runtime) CancelOperation(ctx context.Context, opID string) error {
	r.cancelMu.Lock()
	cancel, ok := r.cancels[opID]
	r.cancelMu.Unlock()
	if !ok {
		return fmt.Errorf("operation %q is not running (no live cancellation handle)", opID)
	}
	cancel()

	// Find the owning project DB to persist the terminal status. Search the
	// registry (same strategy as handleGetOperation) because the cancel route
	// is addressed by operation id alone.
	projects, err := r.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		pr, err := r.EnsureProject(ctx, p.ProjectID)
		if err != nil {
			continue
		}
		if _, err := pr.db.GetOperation(ctx, opID); err == nil {
			// Guarded: a cancel that arrives after the operation finished is a
			// no-op rather than overwriting the real outcome. The goroutine's own
			// terminal write stays unconditional, because it knows what actually
			// happened -- an apply can complete after cancellation, and the log
			// has to say so.
			err := pr.db.CancelOperationStatus(ctx, opID)
			if errors.Is(err, store.ErrOperationNotLive) {
				return preconditionf("operation %q has already finished and cannot be cancelled", opID)
			}
			return err
		}
	}
	return fmt.Errorf("operation %q not found in any project", opID)
}

// InitiativeDocInput is one context doc supplied atomically at initiative
// creation (idea new --doc).
type InitiativeDocInput struct {
	Content string
	Kind    string
	Title   string
}

// StartInitiative creates an initiative + operation and runs the workflow
// asynchronously (§34). Returns the initiative and operation ids.
func (r *Runtime) StartInitiative(ctx context.Context, projectID, title, description string) (string, string, error) {
	return r.StartInitiativeWithDocs(ctx, projectID, title, description, nil)
}

// StartInitiativeWithDocs creates an initiative, ingests its context docs, and
// only THEN launches the M1 run — so `idea new --doc` grounds framing/research/
// PRD in the supplied material. Docs are credential-swept/validated BEFORE the
// initiative is created, so a rejected doc never leaves a half-created row.
func (r *Runtime) StartInitiativeWithDocs(ctx context.Context, projectID, title, description string, docs []InitiativeDocInput) (string, string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	if len(docs) > maxContextDocsPerInitiative {
		return "", "", ingestError{http.StatusBadRequest, "too many context docs"}
	}
	// Pre-validate every doc (non-empty + credential sweep) before persisting
	// anything, so a rejected upload cannot leave a dangling initiative.
	for _, d := range docs {
		content := strings.TrimSpace(d.Content)
		if content == "" {
			return "", "", ingestError{http.StatusBadRequest, "content is required"}
		}
		if pr.redactor.ContainsCredential(content) {
			return "", "", ingestError{http.StatusBadRequest, "document contains a credential and was rejected"}
		}
	}

	ini := &domain.Initiative{Title: pr.redactor.RedactString(title), Description: pr.redactor.RedactString(description), Status: domain.InitiativeRunning}
	if err := pr.db.CreateInitiative(ctx, ini); err != nil {
		return "", "", err
	}
	for _, d := range docs {
		if _, err := pr.ingestTextDoc(ctx, ini.ID, d.Content, d.Kind, d.Title); err != nil {
			return "", "", err
		}
	}

	opID, err := pr.db.CreateOperation(ctx, projectID, ini.ID, "run_initiative")
	if err != nil {
		return "", "", err
	}

	r.launchOperation(opID, func(bg context.Context) {
		// ini.Description, not the raw parameter: the redactor already swept it
		// on the line that created the initiative, and the engine sends this
		// value straight to the model provider.
		//
		// Passing the raw text scrubbed a pasted credential from local storage
		// and then egressed it to a third party anyway -- strictly worse than
		// keeping it locally, and invisible because every retry took the
		// redacted path (workflow/retry.go passes ini.Description), so the leak
		// only ever happened on a first run.
		status, runErr := pr.engine.Run(bg, ini.ID, ini.Description)
		if runErr != nil {
			r.failOperation(opID, ini.ID, "initiative run failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, ini.ID)
		r.log.Info("initiative run complete", "initiative", ini.ID, "status", status)
	})

	return ini.ID, opID, nil
}

// ApproveInitiative records human approval of the reviewed PRD.
//
// A Runtime wrapper rather than a direct engine call from the handler, and that
// is deliberate: preconditionf is what writeErr turns into a 409, and it is also
// what `make api-check`'s conflict axis scans for. Calling the engine straight
// from the handler and mapping the error to 409 there would have produced the
// right status code and a spec that silently never declared it -- the checker
// only sees preconditionf inside Runtime methods.
func (r *Runtime) ApproveInitiative(ctx context.Context, projectID, initiativeID string) (domain.InitiativeStatus, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	if err := pr.engine.Approve(ctx, initiativeID); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			ini, gerr := pr.db.GetInitiative(ctx, initiativeID)
			if gerr == nil {
				return "", preconditionf(
					"approve accepts a PRD that is waiting on your judgment (status=%s); `idea next` names the step this initiative is actually ready for",
					ini.Status)
			}
			return "", preconditionf("initiative is not in a state that can be approved")
		}
		return "", err
	}
	return domain.InitiativeApproved, nil
}

// ResumeInitiative re-runs the review loop for an initiative parked on a human.
func (r *Runtime) ResumeInitiative(ctx context.Context, projectID, initiativeID string) (domain.InitiativeStatus, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	status, err := pr.engine.Resume(ctx, initiativeID)
	if err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("resume re-runs the review for an initiative waiting on your judgment; this one is not")
		}
		return "", err
	}
	return status, nil
}

// AdvanceInitiative runs the M2 slice (Design → Execution Plan → plan review)
// asynchronously. Returns the operation id.
func (r *Runtime) AdvanceInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	if ini.Status != domain.InitiativeApproved {
		return "", preconditionf("advance requires an approved PRD (status=%s)", ini.Status)
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeApproved); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to advance")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "advance_initiative")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.Advance(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "advance failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("advance complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// ExecuteInitiative runs the M3 execute pipeline for the next ready task
// asynchronously, producing a change set. Returns the operation id.
func (r *Runtime) ExecuteInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	// Execute is asynchronous, so a state that cannot execute has to be caught
	// here or the caller gets "✓ execute started" and exit 0 with the failure
	// only in the log -- the same defect apply was fixed for (audit finding b).
	//
	// Scoped deliberately to the status. The stronger check, "a ready task
	// exists", needs nextReadyTask, which walks tasks, graph edges and each
	// dependency's change sets; Execute holds the engine mutex across a model
	// call and a container run, so a synchronous ready-task check here either
	// blocks this handler for minutes or races the pipeline it guards.
	// NextAction does that check instead, so `idea next` never proposes an
	// execute that cannot run, and the async failure path stays the backstop for
	// the narrow window between this check and the task being claimed.
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	// plan_approved or needs_human_judgment, matching RevisePlanInitiative,
	// ApprovePlanInitiative and the task-edit guard. Accepting only the former
	// would have been a fourth spelling of the same precondition.
	if ini.Status != domain.InitiativePlanApproved && ini.Status != domain.InitiativeNeedsHumanJudgment {
		return "", preconditionf(
			"execute requires an approved plan (status=%s); `idea next` names the step this initiative is actually ready for",
			ini.Status)
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativePlanApproved, domain.InitiativeNeedsHumanJudgment); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to execute")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "execute_initiative")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	// The status to return to when the run ends, captured before the claim.
	//
	// Execute is the one claiming entry point whose engine call never sets the
	// initiative status itself: Advance, Revise, Retry, RevisePlan, ReopenPhase
	// and ApprovePlan all reach a terminal status inside internal/workflow, so
	// the claim is released for them as a side effect. Execute's closure wrote
	// only the operation status on success and passed failInitiative=false on
	// error, so after the claim was introduced a *successful* execute left the
	// initiative running forever -- `idea next` answered "workflow running"
	// indefinitely and every later command answered "already running".
	//
	// scripts/smoke.sh and the audit journey both missed it because they revert
	// the change set afterwards, and RevertChangeSet happens to set the status
	// back. A real multi-task initiative that applies without reverting is stuck.
	priorStatus := ini.Status
	r.launchOperation(opID, func(bg context.Context) {
		release := func() {
			if err := pr.db.SetInitiativeStatus(context.WithoutCancel(bg), initiativeID,
				priorStatus, store.ActorOrchestrator); err != nil {
				r.log.Error("could not release the initiative after execute",
					"initiative", initiativeID, "op", opID, "error", err)
			}
		}
		cs, runErr := pr.engine.Execute(bg, initiativeID)
		if runErr != nil {
			// Released to where it started, not failed: an execute failure is
			// recoverable by running execute again, which is what this command
			// did before the claim existed.
			release()
			r.failOperation(opID, initiativeID, "execute failed", bg, pr, runErr, false)
			return
		}
		release()
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, cs.ID)
		r.log.Info("execute complete", "initiative", initiativeID, "change_set", cs.HumanID)
	})
	return opID, nil
}

// ApplyChangeSetInitiative applies an approved change set asynchronously.
func (r *Runtime) ApplyChangeSetInitiative(ctx context.Context, projectID, initiativeID, changeSetID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	// Claim from every non-running state.
	//
	// The list is deliberately permissive, and derived by probing rather than by
	// inspection: apply and revert had no guard at all, so they succeeded from
	// every status, and a narrower list would have refused flows that work
	// today. What this excludes is the one case that matters -- starting an
	// apply while another operation holds the initiative.
	//
	// The hazard is narrower than the other claims': apply, revert and execute
	// already share the engine mutex, so they were mutually exclusive already.
	// The gap is Revise, which takes no mutex and can supersede requirements
	// while an apply lands -- a provenance race rather than a corrupted tree.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID,
		domain.InitiativeApproved, domain.InitiativePlanApproved,
		domain.InitiativeNeedsHumanJudgment, domain.InitiativeComplete,
		domain.InitiativeFailed); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is busy; wait for the running operation to finish")
		}
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "apply_change_set")
	if err != nil {
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	priorStatus := ini.Status
	r.launchOperation(opID, func(bg context.Context) {
		release := func() {
			if err := pr.db.SetInitiativeStatus(context.WithoutCancel(bg), initiativeID,
				priorStatus, store.ActorOrchestrator); err != nil {
				r.log.Error("could not release the initiative after apply",
					"initiative", initiativeID, "op", opID, "error", err)
			}
		}
		if runErr := pr.engine.ApplyChangeSet(bg, initiativeID, changeSetID); runErr != nil {
			release()
			r.failOperation(opID, initiativeID, "apply failed", bg, pr, runErr, false)
			return
		}
		release()
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, changeSetID)
		r.log.Info("apply complete", "change_set", changeSetID)
	})
	return opID, nil
}

// RevertChangeSetInitiative records a rolled-back journal entry for a change
// set asynchronously.
func (r *Runtime) RevertChangeSetInitiative(ctx context.Context, projectID, initiativeID, changeSetID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	// Claim from every non-running state.
	//
	// The list is deliberately permissive, and derived by probing rather than by
	// inspection: apply and revert had no guard at all, so they succeeded from
	// every status, and a narrower list would have refused flows that work
	// today. What this excludes is the one case that matters -- starting an
	// apply while another operation holds the initiative.
	//
	// The hazard is narrower than the other claims': apply, revert and execute
	// already share the engine mutex, so they were mutually exclusive already.
	// The gap is Revise, which takes no mutex and can supersede requirements
	// while an apply lands -- a provenance race rather than a corrupted tree.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID,
		domain.InitiativeApproved, domain.InitiativePlanApproved,
		domain.InitiativeNeedsHumanJudgment, domain.InitiativeComplete,
		domain.InitiativeFailed); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is busy; wait for the running operation to finish")
		}
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "revert_change_set")
	if err != nil {
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	priorStatus := ini.Status
	r.launchOperation(opID, func(bg context.Context) {
		release := func(to domain.InitiativeStatus) {
			if err := pr.db.SetInitiativeStatus(context.WithoutCancel(bg), initiativeID,
				to, store.ActorOrchestrator); err != nil {
				r.log.Error("could not release the initiative after revert",
					"initiative", initiativeID, "op", opID, "error", err)
			}
		}
		if runErr := pr.engine.RevertChangeSet(bg, initiativeID, changeSetID); runErr != nil {
			// Nothing landed, so the initiative goes back where it was.
			release(priorStatus)
			r.failOperation(opID, initiativeID, "revert failed", bg, pr, runErr, false)
			return
		}
		// Not priorStatus. RevertChangeSet has just rolled the workspace back
		// and unverified the requirement the change set implemented, and ends
		// by setting plan_approved itself -- so the initiative is no longer
		// wherever it was before the claim. Writing priorStatus back here
		// reverted from `complete` to `complete`: files rolled back, change set
		// `reverted`, requirement unverified, and `idea next` still reporting
		// the initiative finished. Releasing to the status the engine chose is
		// idempotent with what it just wrote, and still guarantees the claim is
		// released even if the engine's own write failed (it discards its
		// error).
		release(domain.InitiativePlanApproved)
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, changeSetID)
		r.log.Info("revert complete", "change_set", changeSetID)
	})
	return opID, nil
}

// ReviseInitiative applies the human's decisions by re-running product
// definition + review asynchronously (§8, §11).
func (r *Runtime) ReviseInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is running; wait for it to finish before revising")
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeApproved, domain.InitiativeNeedsHumanJudgment, domain.InitiativePlanApproved, domain.InitiativeComplete, domain.InitiativeFailed); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to revise")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "revise_initiative")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.Revise(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "revise failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("revise complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// RetryInitiative re-runs a failed initiative's workflow under a chosen model
// (retry-with-a-different-model). Returns the operation id.
func (r *Runtime) RetryInitiative(ctx context.Context, projectID, initiativeID, model string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	if ini.Status != domain.InitiativeFailed {
		return "", fmt.Errorf("retry requires a failed initiative (status=%s)", ini.Status)
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeFailed); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to retry")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "retry_initiative")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		pr.engine.SetModelOverride(initiativeID, model)
		status, runErr := pr.engine.Retry(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "retry failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("retry complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// ReopenPhaseInitiative re-runs a design phase and everything downstream
// (cascade) asynchronously. Returns the operation id.
func (r *Runtime) ReopenPhaseInitiative(ctx context.Context, projectID, initiativeID, phase string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	// Refuse synchronously once execution has begun (a stale UI click should get
	// a clean 409, not a 202 that fails asynchronously).
	cses, err := pr.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if len(cses) > 0 {
		return "", fmt.Errorf("cannot reopen a design phase after execution has begun")
	}
	// Reopen re-runs design stages, so the design must already exist (the PRD was
	// approved and design attempted); refuse before that to avoid running design
	// stages against an empty requirement set.
	arts, err := pr.db.ListArtifacts(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	hasDesign := false
	for _, a := range arts {
		switch a.Kind {
		case domain.ArtifactTechnicalDesign, domain.ArtifactDesignSpec, domain.ArtifactVerificationPlan, domain.ArtifactThreatModel, domain.ArtifactExecutionPlan:
			hasDesign = true
		}
	}
	if !hasDesign {
		return "", fmt.Errorf("no design or plan to reopen — advance the initiative first")
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeApproved, domain.InitiativeNeedsHumanJudgment, domain.InitiativePlanApproved, domain.InitiativeComplete, domain.InitiativeFailed); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to reopen-phase")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "reopen_phase")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.ReopenPhase(bg, initiativeID, phase)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "reopen phase failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("reopen phase complete", "initiative", initiativeID, "phase", phase, "status", status)
	})
	return opID, nil
}

// RevisePlanInitiative regenerates the execution plan (and its review)
// asynchronously, keeping the PRD/design artifacts. Returns the operation id.
func (r *Runtime) RevisePlanInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", fmt.Errorf("initiative is running; wait for it to finish")
	}
	if ini.Status != domain.InitiativePlanApproved && ini.Status != domain.InitiativeNeedsHumanJudgment {
		return "", fmt.Errorf("no plan to revise (status=%s)", ini.Status)
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativePlanApproved, domain.InitiativeNeedsHumanJudgment); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to revise-plan")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "revise_plan")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.RevisePlan(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "plan revision failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("plan revision complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// ApprovePlanInitiative re-runs the execution-plan review asynchronously.
func (r *Runtime) ApprovePlanInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", fmt.Errorf("initiative is running; wait for it to finish")
	}
	if ini.Status != domain.InitiativeNeedsHumanJudgment && ini.Status != domain.InitiativePlanApproved {
		return "", fmt.Errorf("no plan to approve (status=%s)", ini.Status)
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment, domain.InitiativePlanApproved); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is already running or not ready to approve-plan")
		}
		return "", err
	}
	// Nothing between the claim and launchOperation may leave the
	// initiative claimed. A CreateOperation failure here would strand it in
	// "running" with no operation for reconcileStaleOperations to find --
	// permanently stuck, which is worse than the race the claim closes.
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "approve_plan")
	if err != nil {
		// context.WithoutCancel: this is a compensating write, and ctx is the
		// HTTP request's. If the client disconnected in the window between the
		// claim and CreateOperation, the create fails on ctx.Err() and a release
		// sharing that context fails too -- deterministically, not just on the
		// "DB gone" case. That would strand the initiative with no operation row
		// for the sweep to find.
		if relErr := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), initiativeID, ini.Status, store.ActorOrchestrator); relErr != nil {
			// Both failing usually means one shared cause (DB gone, disk full), and
			// the initiative is then stuck running with no operation for
			// reconcileStaleOperations to find. Nothing here can fix that, so it
			// is logged loudly rather than swallowed.
			r.log.Error("could not release the initiative claim after a failed operation create",
				"initiative", initiativeID, "create_error", err, "release_error", relErr)
		}
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.ApprovePlan(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "plan approval failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("plan approval complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// RetryConflictedApply resets a conflicted change set to approved and re-runs the
// existing async apply path. Returns the operation id.
func (r *Runtime) RetryConflictedApply(ctx context.Context, projectID, initiativeID, changeSetID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	if err := pr.engine.ResetConflictedForRetry(ctx, initiativeID, changeSetID); err != nil {
		return "", err
	}
	return r.ApplyChangeSetInitiative(ctx, projectID, initiativeID, changeSetID)
}

// SuggestAnswersInitiative generates AI answers for open questions
// asynchronously and persists them. Returns the operation id.
func (r *Runtime) SuggestAnswersInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	// The existence check every sibling entry point has and this one did not.
	// Without it, POST on an initiative that does not exist answered 202 with a
	// real operation id — a pollable handle for work that cannot succeed, and an
	// operations row with a dangling initiative_id — while advance, execute,
	// revise and retry all answered 404. It was the only 202 endpoint that lied
	// about accepting work.
	//
	// Recorded as C4 in docs/plans/deferred.md since audit 3 and found again by
	// audit 5, this time with the client-visible consequence attached.
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "suggest_answers")
	if err != nil {
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		// Clear prior answers, then append each chunk as it completes so the UI
		// can observe real progress via the suggested-answers list.
		_ = pr.db.ReplaceSuggestedAnswers(bg, initiativeID, nil)
		total := 0
		runErr := pr.engine.SuggestAnswers(bg, initiativeID, func(answers []domain.SuggestedAnswer) {
			for i := range answers {
				answers[i].Question = pr.redactor.RedactString(answers[i].Question)
				answers[i].Answer = pr.redactor.RedactString(answers[i].Answer)
			}
			if err := pr.db.AppendSuggestedAnswers(bg, initiativeID, answers); err != nil {
				r.log.Error("suggest answers append failed", "initiative", initiativeID, "err", err)
				return
			}
			total += len(answers)
		})
		if runErr != nil {
			r.failOperation(opID, initiativeID, "suggest answers failed", bg, pr, runErr, false)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("suggest answers complete", "initiative", initiativeID, "count", total)
	})
	return opID, nil
}

// ListProjects returns the registry entries.
func (r *Runtime) ListProjects(ctx context.Context) ([]store.ProjectRecord, error) {
	return r.global.ListProjects(ctx)
}

// reconcileStaleOperations marks any unfinished operations (and their
// initiatives) as failed, and recovers change sets stuck mid-apply or
// mid-checks (full-platform review: apply atomicity + change-set reconciliation).
// It runs after the engine + raw blob store exist so the workspace can be
// restored from a persisted pre-image.
func (r *Runtime) reconcileStaleOperations(ctx context.Context, pr *projectRuntime) {
	ops, err := pr.db.ListUnfinishedOperations(ctx)
	if err == nil {
		for _, op := range ops {
			_ = pr.db.SetOperationStatus(ctx, op.ID, domain.OperationFailed, "")
			if op.InitiativeID == "" {
				continue
			}
			// Only an initiative this operation actually claimed. Not every
			// operation claims one -- suggest_answers creates a row and runs
			// against a `failed` or `complete` initiative without claiming --
			// so failing unconditionally reaches initiatives that were never
			// running. releaseOrphanedClaims fifty lines below carries the same
			// guard, for the same reason.
			ini, err := pr.db.GetInitiative(ctx, op.InitiativeID)
			if err != nil || ini.Status != domain.InitiativeRunning {
				continue
			}
			// FailInitiative, not SetInitiativeStatus: the latter is the one
			// transition that deliberately leaves failure_reason alone
			// (store/repo.go:195), so an initiative failed by a daemon restart
			// mid-operation ended up `failed` with an empty reason -- `idea
			// status` reported a failure it could not explain, and `idea next`
			// had nothing to name.
			//
			// The guard above is what makes writing the reason safe. Unguarded,
			// this overwrites a *real* reason with this generic one: a failed
			// initiative carrying "openai: status 401: ..." plus a stale
			// suggest_answers row would come back from a restart blaming the
			// restart, and `idea next` would report that as the cause and send
			// the user to `idea retry` against a 401 it no longer mentioned.
			_ = pr.db.FailInitiative(ctx, op.InitiativeID,
				"the daemon restarted while this operation was running, so it could not finish")
		}
	}

	// Sweep initiatives left claimed with nothing running.
	//
	// The loop above only sees operations that are pending or running, so it
	// cannot release an initiative whose operation reached a terminal state
	// without releasing it -- a cancellation, or a daemon killed between the two
	// writes. Those initiatives answer "already running" to every later request
	// with no way back, so this is the backstop for the claim mechanism rather
	// than a nicety.
	r.releaseOrphanedClaims(ctx, pr)

	recoveries, err := pr.db.ReconcileChangeSets(ctx)
	if err != nil {
		return
	}
	for _, rec := range recoveries {
		switch rec.Status {
		case domain.ChangeSetApplying:
			if rec.PreImageDigest != "" {
				// A byte may have been written: restore the pre-image, and only
				// then mark conflicted. If the restore fails, leave the change set
				// in applying and log loudly so a later reconcile/operator can
				// retry (never persist a terminal status over a mutated workspace).
				if err := pr.engine.RestoreWorkspace(ctx, rec.PreImageDigest); err != nil {
					r.log.Error("reconcile: restore failed, leaving change set applying", "change_set", rec.ChangeSetID, "err", err)
					continue
				}
				_ = pr.db.TransitionChangeSet(ctx, rec.ChangeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
			} else {
				// No bytes were written (prepared/absent): revert applying→approved
				// so a human can re-apply.
				_ = pr.db.TransitionChangeSet(ctx, rec.ChangeSetID, domain.ChangeSetApplying, domain.ChangeSetApproved, store.ActorOrchestrator)
			}
		case domain.ChangeSetChecksRunning:
			_ = pr.db.TransitionChangeSet(ctx, rec.ChangeSetID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator)
			if rec.TaskID != "" {
				_ = pr.db.SetTaskStatus(ctx, rec.TaskID, domain.TaskProposed)
			}
		}
	}
}

// lookupEnv resolves a secret from the process environment first, then the
// project's .env file (§47).
func lookupEnv(key string, dotEnv map[string]string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if dotEnv != nil {
		return dotEnv[key]
	}
	return ""
}

// collectSecrets returns resolved secret values (provider API keys) for
// redaction, from the environment and .env (§47). It gathers both explicitly
// configured providers AND the built-in presets, so a preset-only key (e.g.
// GOOGLE_API_KEY via the gemini preset) is redacted too (full-platform review).
func collectSecrets(cfg config.Config, dotEnv map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	// Built-in presets (used even before a provider is explicitly configured).
	for _, p := range config.Presets {
		add(lookupEnv(p.DefaultKeyEnv, dotEnv))
	}
	// Explicitly configured providers.
	for name, p := range cfg.Harness.Providers {
		_, _, keyEnv := config.ResolveProvider(name, p)
		if keyEnv != "" {
			add(lookupEnv(keyEnv, dotEnv))
		}
	}
	// Arbitrary secret-bearing .env entries (e.g. AWS_SECRET_ACCESS_KEY, a
	// DATABASE_URL with an embedded password) are redacted too (review).
	for k, v := range dotEnv {
		if isSecretKey(k) || looksHighEntropy(v) {
			add(v)
		}
	}
	return out
}

// isSecretKey reports whether a .env key names a secret-like value.
func isSecretKey(k string) bool {
	u := strings.ToUpper(k)
	for _, tok := range []string{"SECRET", "PASSWORD", "PASSWD", "TOKEN", "API_KEY", "APIKEY", "CREDENTIAL", "CRED", "PRIVATE_KEY", "AUTH"} {
		if strings.Contains(u, tok) {
			return true
		}
	}
	return false
}

// looksHighEntropy reports whether a value looks like a high-entropy secret
// (length >= 16 and at least three character classes), so short/common values
// like "development" are not added as known secrets.
func looksHighEntropy(v string) bool {
	if len(v) < 16 {
		return false
	}
	var lower, upper, digit, special bool
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			special = true
		}
	}
	classes := 0
	for _, b := range []bool{lower, upper, digit, special} {
		if b {
			classes++
		}
	}
	return classes >= 3
}

// buildRegistry constructs a multi-provider registry from config (§40). It
// supports OpenAI, Anthropic, Gemini, DeepSeek, OpenRouter, Ollama, and any
// custom OpenAI-compatible endpoint. A deterministic demo provider is always
// present as the last resort.
func buildRegistry(cfg config.Config, log *slog.Logger, dotEnv map[string]string) *model.Registry {
	reg := model.NewRegistry(defaultProviderName(cfg))

	// Always register the built-in presets (with env/.env-resolved keys) so
	// model listing works even before a provider is explicitly configured.
	for _, p := range config.Presets {
		key := lookupEnv(p.DefaultKeyEnv, dotEnv)
		reg.Add(p.Key, providerFor(p.Key, p.Type, key, p.BaseURL))
	}

	// Configured providers override preset defaults (custom base URLs/types) —
	// but only after the same validation the write path applies. handleUpdateProviders
	// is not the only way a provider entry arrives: harness.yaml is portable and
	// committable, so it also comes from a cloned repo, a hand edit, or a restored
	// backup. Validating only on write left the whole point of the check open,
	// because this function is what turns
	// `providers.openai.base_url: https://attacker.tld/v1` into a live client that
	// still inherits OPENAI_API_KEY and receives the full prompt context.
	accepted, refused := resolveProviderEntries(cfg.Harness.Providers)
	for _, e := range refused {
		// Refuse the entry, not the project. Failing the open would make one bad
		// line in a portable file unopenable (and `idea config validate`, the
		// command that explains it, unreachable); silently honouring the entry is
		// the exfiltration. So the entry is dropped, loudly: a preset name falls
		// back to its preset registration above, and a custom name resolves to
		// "model: unknown provider" at first use rather than to the attacker.
		log.Error("refusing to register provider from harness.yaml; run `idea config validate` for details",
			"provider", e.Name, "reason", e.Err)
	}
	for _, e := range accepted {
		reg.Add(e.Name, providerFor(e.Name, e.Type, lookupEnv(e.KeyEnv, dotEnv), e.BaseURL))
	}

	if isDemo(cfg, dotEnv) {
		log.Warn("no model provider configured; using deterministic demo provider")
	}
	reg.Add(demoProviderName, model.NewFakeProvider())
	return reg
}

// resolvedProvider is one configured provider entry that passed validation,
// with the type/base URL/key-env it will register under.
type resolvedProvider struct {
	Name    string
	Type    config.ProviderType
	BaseURL string
	KeyEnv  string
}

// refusedProvider is one configured provider entry that must NOT be registered,
// with the reason to log.
type refusedProvider struct {
	Name string
	Err  error
}

// resolveProviderEntries partitions configured provider entries into the ones
// safe to register and the ones refused, applying config.ValidateProvider —
// the same check handleUpdateProviders applies on the write path. Sorted so the
// accepted set and the log lines are deterministic.
func resolveProviderEntries(providers map[string]config.ProviderConfig) ([]resolvedProvider, []refusedProvider) {
	var accepted []resolvedProvider
	var refused []refusedProvider
	for _, name := range sortedProviderNames(providers) {
		pc := providers[name]
		if err := config.ValidateProvider(name, pc); err != nil {
			refused = append(refused, refusedProvider{Name: name, Err: err})
			continue
		}
		typ, baseURL, keyEnv := config.ResolveProvider(name, pc)
		accepted = append(accepted, resolvedProvider{Name: name, Type: typ, BaseURL: baseURL, KeyEnv: keyEnv})
	}
	return accepted, refused
}

// providerFor constructs a provider from a resolved type/key/base URL.
func providerFor(name string, typ config.ProviderType, key, baseURL string) model.Provider {
	switch typ {
	case config.ProviderAnthropic:
		return model.NewAnthropic(key, baseURL)
	case config.ProviderGemini:
		return model.NewGemini(key, baseURL)
	default: // openai-compatible
		if name == "ollama" {
			return model.NewOllama(baseURL)
		}
		return model.NewOpenAICompat(name, key, baseURL)
	}
}

// defaultProviderName returns the provider portion of default_model, else the
// first (alphabetically) configured provider, else the demo default.
func defaultProviderName(cfg config.Config) string {
	if p, _ := config.ParseModelSpec(cfg.Harness.DefaultModel); p != "" {
		return p
	}
	names := make([]string, 0, len(cfg.Harness.Providers))
	for n := range cfg.Harness.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return demoProviderName
}

// releaseOrphanedClaims fails any initiative sitting in "running" with no
// unfinished operation to explain it.
//
// `failed` rather than a guessed previous status: the work genuinely did not
// finish, and `idea retry` runs from there, so the initiative is recoverable by
// a documented command instead of a database edit.
func (r *Runtime) releaseOrphanedClaims(ctx context.Context, pr *projectRuntime) {
	inis, err := pr.db.ListInitiatives(ctx)
	if err != nil {
		return
	}
	live := map[string]bool{}
	if ops, err := pr.db.ListUnfinishedOperations(ctx); err == nil {
		for _, op := range ops {
			if op.InitiativeID != "" {
				live[op.InitiativeID] = true
			}
		}
	}
	for _, ini := range inis {
		if ini.Status != domain.InitiativeRunning || live[ini.ID] {
			continue
		}
		r.log.Warn("releasing an initiative left running with no live operation",
			"initiative", ini.ID)
		if err := pr.db.FailInitiative(ctx, ini.ID,
			"the operation that claimed this initiative ended without releasing it"); err != nil {
			r.log.Error("could not release an orphaned initiative claim",
				"initiative", ini.ID, "error", err)
		}
	}
}
