// Package daemon implements the localhost control-plane HTTP server (§28–§29):
// token-authenticated, project-scoped REST + SSE over one user-level daemon.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
		fn(ctx)
	}()
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
	if failInitiative && status == domain.OperationFailed {
		_ = pr.db.FailInitiative(sctx, initiativeID, runErr.Error())
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
			return pr.db.SetOperationStatus(ctx, opID, domain.OperationCancelled, "")
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
		status, runErr := pr.engine.Run(bg, ini.ID, description)
		if runErr != nil {
			r.failOperation(opID, ini.ID, "initiative run failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, ini.ID)
		r.log.Info("initiative run complete", "initiative", ini.ID, "status", status)
	})

	return ini.ID, opID, nil
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
		return "", fmt.Errorf("initiative is already running")
	}
	if ini.Status != domain.InitiativeApproved {
		return "", fmt.Errorf("advance requires an approved PRD (status=%s)", ini.Status)
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "advance_initiative")
	if err != nil {
		return "", err
	}
	// Mark the initiative running so the UI reflects "designing & planning"
	// rather than a stale "approved" during the M2 pipeline.
	_ = pr.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeRunning, store.ActorOrchestrator)
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
	if _, err := pr.db.GetInitiative(ctx, initiativeID); err != nil {
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "execute_initiative")
	if err != nil {
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		cs, runErr := pr.engine.Execute(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "execute failed", bg, pr, runErr, false)
			return
		}
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
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "apply_change_set")
	if err != nil {
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		if runErr := pr.engine.ApplyChangeSet(bg, initiativeID, changeSetID); runErr != nil {
			r.failOperation(opID, initiativeID, "apply failed", bg, pr, runErr, false)
			return
		}
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
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "revert_change_set")
	if err != nil {
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		if runErr := pr.engine.RevertChangeSet(bg, initiativeID, changeSetID); runErr != nil {
			r.failOperation(opID, initiativeID, "revert failed", bg, pr, runErr, false)
			return
		}
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
		return "", fmt.Errorf("initiative is running; wait for it to finish before revising")
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "revise_initiative")
	if err != nil {
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
		return "", fmt.Errorf("initiative is already running")
	}
	if ini.Status != domain.InitiativeFailed {
		return "", fmt.Errorf("retry requires a failed initiative (status=%s)", ini.Status)
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "retry_initiative")
	if err != nil {
		return "", err
	}
	_ = pr.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeRunning, store.ActorOrchestrator)
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
		return "", fmt.Errorf("initiative is already running")
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
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "reopen_phase")
	if err != nil {
		return "", err
	}
	_ = pr.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeRunning, store.ActorOrchestrator)
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
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "revise_plan")
	if err != nil {
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
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "approve_plan")
	if err != nil {
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
			if op.InitiativeID != "" {
				_ = pr.db.SetInitiativeStatus(ctx, op.InitiativeID, domain.InitiativeFailed, store.ActorOrchestrator)
			}
		}
	}

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
	reg.Add("idea-harness-default", model.NewFakeProvider())
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
	return "idea-harness-default"
}
