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
	"time"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/git"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/project"
	"github.com/devarispbrown/idea-harness/internal/projection"
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
	// ops counts running operations, so Shutdown can wait for them to unwind
	// -- and remove their agent containers -- before the process exits.
	// closing (under cancelMu) is set once shutdown begins: an operation
	// launched after that starts already cancelled, and is still counted, so
	// nothing runs uncancelled past the stop and Add never races Wait.
	ops     sync.WaitGroup
	closing bool
	// swept is closed once the startup sweep of a dead daemon's leftovers has
	// finished. Operations wait for it, so the sweep cannot remove one of
	// this daemon's own agents.
	swept     chan struct{}
	sweptOnce sync.Once
	// lifetime is cancelled when shutdown begins. Work that must survive its
	// HTTP client going away, but not the daemon stopping, derives from it.
	lifetime    context.Context
	endLifetime context.CancelFunc

	// images caches docker image presence (C3), so GET /executors and the
	// Build page's run_blocked_reason poll never call Docker more than once
	// every 30s per image.
	images *imagePresenceCache
	// smokes is the per-project claim for smoke tests started from the UI.
	smokes smokeClaims

	// extraProviders are registered into every project's model registry on
	// top of what harness.yaml configures (RegisterProvider; eval_hooks.go).
	// Its own mutex, because EnsureProject builds a registry while holding mu.
	extraMu        sync.Mutex
	extraProviders map[string]model.Provider
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
	// cfgMTime is harness.yaml's mtime as of the last successful load
	// (construction or ReloadProject), used to auto-reload a hand edit
	// (review defect #4). Zero when the file did not exist at that time.
	cfgMTime time.Time

	// planEdit is shared across every *projectRuntime a reload ever produces
	// for this project (review H4: ReloadProject now builds a NEW
	// projectRuntime and swaps the pointer rather than mutating fields in
	// place, so anything that must outlive a reload -- this registry,
	// engine -- has to be a pointer copied onto the new value, not a plain
	// field. Two separate *sync.Mutex-per-initiative maps, one per
	// projectRuntime generation, would let two requests that landed on
	// different generations "hold the same lock" without excluding each
	// other at all).
	planEdit *planEditRegistry
}

// planEditRegistry holds one *sync.Mutex per initiative id, lazily created
// (review M5), shared by every generation of a project's *projectRuntime
// (review H4) so a reload can never hand out a second, unrelated lock for an
// initiative another generation is already editing.
type planEditRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lockPlanEdit serializes the Plan-editing endpoints (add/update/delete/
// cancel/reorder task, set depends_on, merge, split) per initiative (review
// M5): taskEditableGuard, the cycle check, and the write(s) that follow it
// are each individually correct but together form a check-then-act sequence
// with no lock. Two concurrent set-depends_on requests (A->B and B->A) each
// ran wouldCycle against the graph as it stood before either write landed,
// both saw no cycle, and together created one; an Execute could also start
// between the guard and the write. Call the returned func to release,
// typically via `defer lockPlanEdit(...)()` immediately after resolving the
// initiative, so the guard check through every write in the handler runs
// under the lock.
//
// review C2: it is not enough for the handlers alone to take this lock --
// they re-read the initiative under it (lockPlanEditFresh in handlers.go)
// precisely so this lock has something to serialize against. Every entry
// point that claims the initiative and then rewrites its tasks --
// AdvanceInitiative, ReopenPhaseInitiative, RevisePlanInitiative -- takes
// this same lock around its own read-check-claim sequence, so a plan-edit
// handler's fresh read always observes either the claim (and refuses) or a
// state from strictly before it (and its write is not later undone by
// DeleteTasksForInitiative). Before this, those three were the one path to
// DeleteTasksForInitiative that took no lock at all: a plan-edit request
// could read the initiative as editable, lose the race to one of them, and
// still answer 200 OK for a task that no longer existed.
//
// Locks are per-initiative, not per-project: two different initiatives in
// the same project may still edit their plans concurrently.
func (pr *projectRuntime) lockPlanEdit(initiativeID string) func() {
	pe := pr.planEdit
	pe.mu.Lock()
	m, ok := pe.locks[initiativeID]
	if !ok {
		if pe.locks == nil {
			pe.locks = map[string]*sync.Mutex{}
		}
		m = &sync.Mutex{}
		pe.locks[initiativeID] = m
	}
	pe.mu.Unlock()
	m.Lock()
	return m.Unlock
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
	lifetime, endLifetime := context.WithCancel(context.Background())
	return &Runtime{
		lifetime:    lifetime,
		endLifetime: endLifetime,
		stateRoot:   stateRoot,
		token:       token,
		global:      global,
		log:         log,
		projects:    map[string]*projectRuntime{},
		reconciled:  map[string]bool{},
		cancels:     map[string]context.CancelFunc{},
		swept:       closedChan(),
		images:      newImagePresenceCache(),
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
//
// Every HTTP handler resolves its project through here (Server.project), so
// this is also the single choke point for maybeAutoReload: a cached project
// whose harness.yaml has changed on disk since it was last loaded -- a hand
// edit, e.g. to test_cmd -- is reloaded before its (possibly stale) runtime
// is handed back (review defect #4).
func (r *Runtime) EnsureProject(ctx context.Context, projectID string) (*projectRuntime, error) {
	r.mu.Lock()
	pr, ok := r.projects[projectID]
	r.mu.Unlock()
	if ok {
		return r.maybeAutoReload(ctx, projectID, pr), nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-check under the lock: another goroutine may have finished building
	// this project's runtime between the unlock above and here.
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
	//
	// The redactor is keyed with this project's own HMAC key (review H3), so
	// every secret-sweep fingerprint it produces is scoped to the project
	// rather than a bare sha256 comparable across every installation.
	fpKey, err := db.SecretFingerprintKey(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("daemon: secret fingerprint key: %w", err)
	}
	redactor := security.NewRedactorWithKey(fpKey, collectSecrets(cfg, dotEnv)...)
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
	r.addExtraProviders(registry)
	engine := workflow.New(db, blobs, registry, cfg.Harness, r.log)
	engine.SetRawBlobs(rawBlobs)
	engine.SetRedactor(redactor)
	// Point the execute pipeline's isolated work roots at this project's state
	// directory so WorkRoot mirrors "<state-root>/work/<initiative>".
	engine.SetWorkRoot(projDir)
	// The canonical workspace that apply writes into and revert restores is the
	// project root (the user's real source tree).
	engine.SetWorkspaceRoot(rec.RootPath)
	engine.SetDotEnv(dotEnv)

	pr = &projectRuntime{
		db: db, blobs: blobs, rawBlobs: rawBlobs, cfg: cfg, rootPath: rec.RootPath, dotEnv: dotEnv,
		registry: registry, engine: engine, redactor: redactor, cfgMTime: harnessYAMLMTime(rec.RootPath),
		planEdit: &planEditRegistry{},
	}

	r.projects[projectID] = pr

	// Reconcile once per daemon lifetime (never on a reload), so a live
	// operation is never spuriously failed by a re-open. Runs after the engine +
	// raw blob store exist so an interrupted apply can be restored.
	if !r.reconciled[projectID] {
		r.reconciled[projectID] = true
		r.reconcileStaleOperations(ctx, pr)
		// review MEDIUM #1: migration 34 left every pre-existing change set's
		// no_op at its conservative default (false), even the ones whose
		// patch manifest actually touched zero files. Recomputing that needs
		// the blob store, which a migration (plain SQL text) cannot reach, so
		// it happens here instead, guarded by a persisted marker so it
		// actually runs once per project rather than once per daemon start.
		if n, err := pr.engine.BackfillNoOpChangeSets(ctx); err != nil {
			r.log.Warn("no_op backfill failed", "project", projectID, "err", err)
		} else if n > 0 {
			r.log.Info("no_op backfill: fixed pre-migration change sets", "project", projectID, "count", n)
		}
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
	fpKey, err := pr.db.SecretFingerprintKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("daemon: secret fingerprint key: %w", err)
	}
	redactor := security.NewRedactorWithKey(fpKey, collectSecrets(cfg, dotEnv)...)
	blobs, err := blob.New(filepath.Join(r.stateRoot, "projects", projectID, "blobs"), redactor.Redact)
	if err != nil {
		return nil, err
	}
	registry := buildRegistry(cfg, r.log, dotEnv)
	r.addExtraProviders(registry)

	// Rebuild config-derived components on the SAME engine (its own mutexes
	// keep serializing execute/apply/revert, and cfgMu -- see engine.go --
	// keep every entry point's config-derived reads consistent, across the
	// reload; review H4).
	pr.engine.Reload(cfg.Harness, registry, redactor, blobs, dotEnv)

	// review H4: build a FRESH *projectRuntime and swap the map entry under
	// r.mu, rather than mutating pr's fields in place as this used to do
	// (despite the doc comment above already promising a pointer swap).
	// EnsureProject hands out this pointer to every HTTP handler, and
	// roughly twenty of them (executor_status.go, providers, secrets,
	// queue/evidence projections, redaction call sites...) read
	// cfg/registry/redactor/blobs/dotEnv directly off it with no lock at
	// all -- proven by the race detector against the in-place mutation this
	// replaces. A handler that already holds the OLD pr (fetched from the
	// map before this swap) keeps a fully self-consistent snapshot for the
	// rest of its request; the next EnsureProject call gets the new one.
	// db, rawBlobs, rootPath, engine and planEdit are identical across every
	// generation by construction (see projectRuntime's doc comment) and so
	// are carried over unchanged, not reloaded.
	newPr := &projectRuntime{
		db:       pr.db,
		blobs:    blobs,
		rawBlobs: pr.rawBlobs,
		cfg:      cfg,
		rootPath: pr.rootPath,
		dotEnv:   dotEnv,
		registry: registry,
		engine:   pr.engine,
		redactor: redactor,
		cfgMTime: harnessYAMLMTime(pr.rootPath),
		planEdit: pr.planEdit,
	}
	r.mu.Lock()
	r.projects[projectID] = newPr
	r.mu.Unlock()
	return newPr, nil
}

// harnessYAMLMTime returns harness.yaml's modification time under rootPath's
// portable config directory, or the zero time if it cannot be stat'd (not
// yet written, or a transient error) -- callers treat the zero time as "no
// baseline to compare against" rather than an error.
func harnessYAMLMTime(rootPath string) time.Time {
	fi, err := os.Stat(filepath.Join(rootPath, config.Dir, "harness.yaml"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// maybeAutoReload reloads pr's config when harness.yaml has changed on disk
// since it was last loaded. Every HTTP handler resolves its project through
// EnsureProject, which is this function's only caller, so this is the single
// choke point that makes a hand edit -- e.g. to test_cmd -- take effect on
// the next request rather than requiring a daemon restart (review defect
// #4): before this, nothing ever re-read harness.yaml once a project's
// runtime was cached, and ReloadProject only ever ran when the daemon itself
// had just written the file (the provider/executor settings endpoints).
//
// Best-effort: a reload that cannot safely happen right now (an operation is
// in flight -- ReloadProject's own guard) or that fails to load (e.g. the
// file is mid-write and briefly invalid YAML) is logged and the previous,
// still-valid config keeps serving this request; the next request tries
// again. A passive background check must never turn an unrelated request
// into a hard failure.
func (r *Runtime) maybeAutoReload(ctx context.Context, projectID string, pr *projectRuntime) *projectRuntime {
	mtime := harnessYAMLMTime(pr.rootPath)
	if mtime.IsZero() {
		return pr // no harness.yaml (yet) to compare against
	}
	r.mu.Lock()
	stale := mtime.After(pr.cfgMTime)
	r.mu.Unlock()
	if !stale {
		return pr
	}
	reloaded, err := r.ReloadProject(ctx, projectID)
	if err != nil {
		r.log.Warn("harness.yaml changed on disk but could not be reloaded automatically; serving the previous config until it can be",
			"project", projectID, "error", err)
		return pr
	}
	return reloaded
}

// Shutdown cancels every running operation and waits, until ctx expires, for
// them to finish.
//
// Go exits at once on SIGTERM, which is what `idea daemon stop` sends, so no
// deferred cleanup ran: an agent container kept running with the provider key,
// network access and auto-approve, writing into the work root, until something
// happened to load the project again. Cancelling an operation's context is what
// makes the engine remove its container.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.beginShutdown()
	done := make(chan struct{})
	go func() {
		r.ops.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("daemon: operations still running at shutdown: %w", ctx.Err())
	}
}

// beginShutdown cancels every running operation and makes any launched from
// now on start cancelled. Safe to call more than once.
func (r *Runtime) beginShutdown() {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	r.closing = true
	if r.endLifetime != nil {
		r.endLifetime()
	}
	for _, cancel := range r.cancels {
		cancel()
	}
}

// holdOperationsUntilSwept makes operations wait for markSwept. Run calls it
// before serving; a Runtime built any other way (tests) never sweeps and does
// not wait.
func (r *Runtime) holdOperationsUntilSwept() { r.swept = make(chan struct{}) }

// lifetimeContext is cancelled when the daemon begins shutting down. A Runtime
// not built by NewRuntime has none, and gets a background context.
func (r *Runtime) lifetimeContext() context.Context {
	if r.lifetime == nil {
		return context.Background()
	}
	return r.lifetime
}

// detachedContext is for work a request starts that must finish even if the
// client goes away (approving a change set runs a model review), bounded, and
// cancelled when the daemon stops.
func (r *Runtime) detachedContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.lifetimeContext(), 5*time.Minute)
}

// markSwept records that the startup sweep is done.
func (r *Runtime) markSwept() { r.sweptOnce.Do(func() { close(r.swept) }) }

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// claimingOperationTypes are the CreateOperation `type` strings whose entry
// point takes ClaimInitiativeRunning -- either directly in the Runtime
// method that creates the operation, or (resume) inside the engine call it
// launches -- before the operation exists (review H7). Everything else
// (currently just suggest_answers) only reads the initiative to refuse an
// already-running request synchronously; it never claims it.
//
// This is an explicit allowlist, not its complement, deliberately: a new
// operation type added here that DOES claim and is left off this list only
// means failOperationAfterPanic leaves a stuck `running` claim for
// reconcileStaleOperations to sweep up on the next daemon restart -- a
// recoverable stall. The list being wrong in the other direction (a
// non-claiming type added here by mistake) is the actual hazard this review
// item is about: it lets a panic in that operation steal and fail a
// DIFFERENT, unrelated live claim. When adding a new claiming
// CreateOperation call site, add its type string here too.
var claimingOperationTypes = map[string]bool{
	"resume":             true,
	"advance_initiative": true,
	"execute_initiative": true,
	"apply_change_set":   true,
	"revert_change_set":  true,
	"revise_initiative":  true,
	"retry_initiative":   true,
	"reopen_phase":       true,
	"revise_plan":        true,
	"approve_plan":       true,
}

// operationClaimsInitiative reports whether opType's entry point takes
// ClaimInitiativeRunning before the operation exists (review H7). See
// claimingOperationTypes.
func operationClaimsInitiative(opType string) bool {
	return claimingOperationTypes[opType]
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
	r.cancelMu.Lock()
	r.cancels[opID] = cancel
	r.ops.Add(1)
	if r.closing {
		cancel()
	}
	r.cancelMu.Unlock()
	go func() {
		defer r.ops.Done()
		defer cancel()
		select {
		case <-r.swept:
		case <-ctx.Done():
		}
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

// operationErrorMessage redacts msg with the project's own redactor (nil-safe:
// falls back to pattern-only redaction) before it is persisted on the
// operation (B2), so a credential the agent's own output happened to echo
// never lands in the operations table. store.SetOperationFailed caps the
// length; this only redacts.
func operationErrorMessage(pr *projectRuntime, msg string) string {
	var redactor *security.Redactor
	if pr != nil {
		redactor = pr.redactor
	}
	if redactor != nil {
		msg = redactor.RedactString(msg)
	} else {
		msg = security.Redact(msg)
	}
	return msg
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
		if err := pr.db.SetOperationFailed(ctx, opID, domain.OperationFailed,
			operationErrorMessage(pr, fmt.Sprintf("internal error: %v", p))); err != nil {
			r.log.Error("recording a panicked operation failed", "operation_id", opID, "error", err)
			return
		}
		// review H7: only release the initiative when this operation type is
		// one that actually claims it (ClaimInitiativeRunning, directly here
		// or inside the engine call this launched). SuggestAnswersInitiative
		// notably does not: it only reads the initiative to refuse a
		// synchronous "already running" case and never claims anything, so a
		// panic in it has no claim of its own to release. Before this,
		// FailInitiative ran unconditionally here regardless of operation
		// type; if some OTHER, unrelated operation legitimately held the
		// initiative `running` at that exact moment, this overwrote that
		// live claim with `failed` -- the user then ran `idea retry`, and a
		// second pipeline started concurrently with the first, which kept
		// running to completion none the wiser. FailInitiative's own CAS
		// (status must be `running`) is defense-in-depth for the same
		// hazard, but cannot by itself tell "my claim" from "someone else's
		// claim" -- both look like `running` -- so this check is what
		// actually closes it for the case this function handles.
		if op.InitiativeID != "" && operationClaimsInitiative(op.Type) {
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
	sctx := context.Background()
	status := domain.OperationFailed
	if opCtx.Err() != nil {
		status = domain.OperationCancelled
	}
	// Every user Stop takes this same path -- CancelOperation cancels opCtx,
	// which unwinds the in-flight call as a plain `context canceled` error --
	// and it was logged at Error alongside a real failure, so a normal click
	// on Stop showed up as `level=ERROR msg="execute failed" err="context
	// canceled"` in the daemon log. That reads as something having gone
	// wrong; nothing did. A cancellation the user asked for is expected
	// behaviour worth a line (an operator following the log should still see
	// it happened), not an error worth paging over -- real failures (the
	// OperationFailed branch) keep the Error level and the original message.
	if status == domain.OperationCancelled {
		r.log.Info("stopped by you", "initiative", initiativeID, "op", opID)
	} else {
		r.log.Error(logMsg, "initiative", initiativeID, "op", opID, "err", runErr)
	}
	_ = pr.db.SetOperationFailed(sctx, opID, status, operationErrorMessage(pr, runErr.Error()))

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
		// Redacted like the operation's own error: both are shown in the UI,
		// and a provider error can echo request material.
		reason := operationErrorMessage(pr, runErr.Error())
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
	// The map first, and the interrupt immediately. A live handle implies the
	// row exists -- the operation is created before launchOperation registers
	// the handle -- so nothing on this branch needs a lookup, and cancel is the
	// one endpoint whose contract is promptness. Looking up first would make a
	// cancel in the newest project wait while EnsureProject cold-starts and
	// reconciles every older one, which can include restoring a workspace.
	r.cancelMu.Lock()
	cancel, ok := r.cancels[opID]
	r.cancelMu.Unlock()

	if !ok {
		// No handle: now the two causes have to be told apart. This probe used
		// to be the whole function, so an id that matched nothing answered 409
		// "is not running" -- a claim about an operation that does not exist --
		// and the registry search below was unreachable for that case.
		//
		// preconditionf stays here and not in findOperation: docsgen's conflict
		// axis marks any daemon function containing preconditionf and then
		// demands a 409 on every operation whose handler calls a method of that
		// name, so putting it in the shared lookup would make
		// `GET /operations/{id}` declare a conflict it cannot answer.
		if _, _, err := r.findOperation(ctx, opID); err != nil {
			return err
		}
		return preconditionf("operation %q is not running (no live cancellation handle)", opID)
	}
	cancel()

	pr, _, err := r.findOperation(ctx, opID)
	if err != nil {
		return err
	}

	// Guarded: a cancel that arrives after the operation finished is a no-op
	// rather than overwriting the real outcome. The goroutine's own terminal
	// write stays unconditional, because it knows what actually happened -- an
	// apply can complete after cancellation, and the log has to say so.
	if err := pr.db.CancelOperationStatus(ctx, opID); err != nil {
		if errors.Is(err, store.ErrOperationNotLive) {
			return preconditionf("operation %q has already finished and cannot be cancelled", opID)
		}
		return err
	}
	return nil
}

// findOperation locates the project owning an operation. The cancel and read
// routes are addressed by operation id alone, so both have to search the
// registry; they each did it inline, and each invented its own "not found"
// text.
func (r *Runtime) findOperation(ctx context.Context, opID string) (*projectRuntime, domain.Operation, error) {
	projects, err := r.ListProjects(ctx)
	if err != nil {
		return nil, domain.Operation{}, err
	}
	for _, p := range projects {
		pr, err := r.EnsureProject(ctx, p.ProjectID)
		if err != nil {
			continue
		}
		if op, err := pr.db.GetOperation(ctx, opID); err == nil {
			return pr, op, nil
		}
	}
	return nil, domain.Operation{}, &store.NotFoundError{Kind: "operation", Ref: opID}
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
// PRD in the supplied material. Docs are size-checked, credential-swept and
// validated BEFORE the initiative is created, so a rejected doc never leaves a
// half-created row; anything that fails after creation marks the initiative
// failed rather than stranding it in `running`.
func (r *Runtime) StartInitiativeWithDocs(ctx context.Context, projectID, title, description string, docs []InitiativeDocInput) (string, string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	if len(docs) > maxContextDocsPerInitiative {
		return "", "", ingestError{http.StatusBadRequest, "too many context docs"}
	}
	// Pre-validate every doc (non-empty + size + credential sweep) before
	// persisting anything, so a rejected upload cannot leave a dangling
	// initiative.
	//
	// The size cap used to be missing from this loop -- it lives in
	// ingestTextDoc, which runs AFTER CreateInitiative below. So an oversized
	// doc answered 413 with the initiative row already committed as `running`
	// and no operation ever created. reconcileStaleOperations sweeps
	// operations, so it never saw that row; `retry` and `resume` both refused
	// it, `next` answered `wait` forever, and there is no DELETE route -- the
	// project's attention list was wedged until someone hand-edited SQLite.
	// The comment above this loop had promised the opposite since it was
	// written.
	for _, d := range docs {
		content := strings.TrimSpace(d.Content)
		if content == "" {
			return "", "", ingestError{http.StatusBadRequest, "content is required"}
		}
		if len(content) > maxDocFileBytes {
			return "", "", ingestError{http.StatusRequestEntityTooLarge, "document too large"}
		}
		if pr.redactor.ContainsCredential(content) {
			return "", "", ingestError{http.StatusBadRequest, "document contains a credential and was rejected"}
		}
	}

	ini := &domain.Initiative{Title: pr.redactor.RedactString(title), Description: pr.redactor.RedactString(description), Status: domain.InitiativeRunning}
	if err := pr.db.CreateInitiative(ctx, ini); err != nil {
		return "", "", err
	}

	// Anything that fails from here to launchOperation leaves a row already
	// committed as `running`. Ordering fixes the known case (the size cap); this
	// covers the rest -- a blob write, or CreateOperation itself -- by marking
	// the initiative failed, which is a first-class recoverable state: `idea
	// retry` requires exactly that status. Without it the row is unreachable by
	// any route the product offers.
	// context.WithoutCancel for the same reason the release at the bottom of
	// ClaimInitiativeRunning gives: this is a compensating write and ctx is the
	// HTTP request's. The likeliest trigger in this window is the client
	// disconnecting -- `idea new --doc <big file>` then Ctrl-C -- in which case
	// ingestTextDoc fails on ctx.Err() and a repair sharing that context fails
	// too, deterministically, leaving exactly the stranded row this function was
	// changed to prevent.
	fail := func(err error) (string, string, error) {
		if ferr := pr.db.FailInitiative(context.WithoutCancel(ctx), ini.ID, operationErrorMessage(pr, err.Error())); ferr != nil {
			r.log.Error("could not fail a half-created initiative", "initiative", ini.ID, "err", ferr)
		}
		return "", "", err
	}

	for _, d := range docs {
		if _, err := pr.ingestTextDoc(ctx, ini.ID, d.Content, d.Kind, d.Title); err != nil {
			return fail(err)
		}
	}

	opID, err := pr.db.CreateOperation(ctx, projectID, ini.ID, "run_initiative")
	if err != nil {
		return fail(err)
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, ini.ID)
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
					"approve accepts a PRD that is waiting on your judgment (initiative is %s); `idea next` names the step this initiative is actually ready for",
					ini.Status)
			}
			return "", preconditionf("initiative is not in a state that can be approved")
		}
		return "", err
	}
	return domain.InitiativeApproved, nil
}

// ResumeInitiative re-runs the review loop for an initiative parked on a
// human. It stays synchronous (unlike Advance/RevisePlan/ReopenPhase below):
// callers get the final status directly, as before.
//
// review H4: Resume used to run Engine.Resume with no operation row at all --
// the only claiming entry point that didn't create one. That made it
// invisible to ListUnfinishedOperations, which both ReloadProject's "refuse
// to swap config while an operation is running" guard and
// reconcileStaleOperations key off: a reload could swap in a new
// redactor/registry/cfg mid-review-loop (a secret just added to harness.yaml
// would not be swept from the second half of the run), and a daemon crash
// mid-Resume stranded Engine.Resume's own ClaimInitiativeRunning claim with
// nothing for the sweep to find -- exactly what that claim's own comment
// already flagged. The operation row here is bookkeeping only: Resume still
// runs synchronously in this goroutine and still self-claims/self-releases
// inside Engine.Resume exactly as before (its own defer already restores
// needs_human_judgment on error); only ops visibility changes, so this
// operation's own status is recorded here rather than via failOperation
// (which would also fail the initiative -- Engine.Resume already decided
// that outcome).
func (r *Runtime) ResumeInitiative(ctx context.Context, projectID, initiativeID string) (domain.InitiativeStatus, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "resume")
	if err != nil {
		return "", err
	}
	r.ops.Add(1)
	defer r.ops.Done()
	status, err := pr.engine.Resume(ctx, initiativeID)
	if err != nil {
		opStatus := domain.OperationFailed
		if ctx.Err() != nil {
			opStatus = domain.OperationCancelled
		}
		if serr := pr.db.SetOperationFailed(context.WithoutCancel(ctx), opID, opStatus, operationErrorMessage(pr, err.Error())); serr != nil {
			r.log.Error("could not record resume's operation failure", "initiative", initiativeID, "op", opID, "error", serr)
		}
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("resume re-runs the review for an initiative waiting on your judgment; this one is not")
		}
		return "", err
	}
	if serr := pr.db.SetOperationStatus(context.WithoutCancel(ctx), opID, domain.OperationSucceeded, initiativeID); serr != nil {
		r.log.Error("could not record resume's operation success", "initiative", initiativeID, "op", opID, "error", serr)
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
	// Held across the read-check-claim-CreateOperation sequence below,
	// mutually exclusive with the same lock held by every Plan-editing HTTP
	// handler around its own read-check-write (review C2): without this, a
	// plan-edit request could read the initiative as still editable a moment
	// before this claims it `running` and rewrites its tasks (Advance builds
	// the execution plan), and the plan-edit's later write would land on --
	// or be silently undone by -- that rewrite with no error to the caller.
	defer pr.lockPlanEdit(initiativeID)()
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	if ini.Status != domain.InitiativeApproved {
		return "", preconditionf("advance requires an approved PRD (initiative is %s); `idea next` names the step this initiative is actually ready for", ini.Status)
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("advance complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// ExecuteInitiative runs the M3 execute pipeline for the next ready task
// asynchronously, producing a change set. Returns the operation id.
func (r *Runtime) ExecuteInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	return r.ExecuteInitiativeFresh(ctx, projectID, initiativeID, false)
}

// ExecuteInitiativeFresh is ExecuteInitiative; with fresh, a stopped run's kept
// files are discarded once the run is accepted, so it starts from the
// workspace ("Start over").
func (r *Runtime) ExecuteInitiativeFresh(ctx context.Context, projectID, initiativeID string, fresh bool) (string, error) {
	return r.ExecuteInitiativeFreshWithNote(ctx, projectID, initiativeID, fresh, "")
}

// ExecuteInitiativeFreshWithNote is ExecuteInitiativeFresh, with an optional
// human note (D-a, wave 3 contract) that steers this one execute: appended to
// the implementer's brief and recorded on the agent run. Set on the engine
// BEFORE launching, and cleared by Execute itself once that run finishes, so
// it can never bleed into a later, unrelated execute of this initiative.
func (r *Runtime) ExecuteInitiativeFreshWithNote(ctx context.Context, projectID, initiativeID string, fresh bool, note string) (string, error) {
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
	// ICP re-run defect E: this used to be scoped deliberately to the status
	// alone, reasoning that "a ready task exists" needs nextReadyTask, which
	// walks tasks, graph edges and each dependency's change sets, and that
	// doing so here would block this handler or race the pipeline it guards.
	// That conflated the READ (cheap: table scans, no lock, no model call --
	// engine.NextReadyTask is documented lock-free precisely so a poll-driven
	// UI can call it on every refresh) with Execute itself (which holds the
	// engine mutex across a model call and a container run). The read alone
	// costs nothing to do here, and NOT doing it is what let `idea execute`
	// print "✓ execute started" and then fail in the background with "no
	// ready tasks" on a plan with nothing left to run -- a fully deterministic
	// outcome, not a narrow race, e.g. right after reverting the change set
	// for the only remaining task and before its status caught up, or once
	// every task is simply done. The async failure path (Execute's own
	// nextReadyTask call) remains the backstop for the genuine race window
	// between this check and the task actually being claimed.
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is already running")
	}
	// plan_approved or needs_human_judgment, matching RevisePlanInitiative,
	// ApprovePlanInitiative and the task-edit guard. Accepting only the former
	// would have been a fourth spelling of the same precondition.
	if ini.Status != domain.InitiativePlanApproved && ini.Status != domain.InitiativeNeedsHumanJudgment {
		return "", preconditionf(
			"execute requires an approved plan (initiative is %s); `idea next` names the step this initiative is actually ready for",
			ini.Status)
	}
	if reason := workflow.PlanNotReadyForExecution(ini); reason != "" {
		return "", preconditionf("%s", reason)
	}
	if nt, err := pr.engine.NextReadyTask(ctx, initiativeID); err != nil {
		return "", err
	} else if nt == nil {
		return "", preconditionf(
			"no ready tasks for this initiative (every task is done, cancelled, or blocked on an unsatisfied dependency); " +
				"if every task is done, run `idea verify` instead; if a task should run again (e.g. after reverting its change " +
				"set), it must be ready/proposed -- `idea status` shows why it is not")
	}
	// ICP re-run D5: Execute's own guards (another open change set, executor
	// resolution, the trust-tier gate, a prior change set of this task left
	// mid-review or as an unresolved no-op) used to run only inside the async
	// launchOperation goroutine below, so a refusal there -- typically under a
	// millisecond, no model call -- landed as an operation failure strictly
	// after this handler had already answered 202 and the CLI had already
	// printed "✓ execute started". Running the same checks here, before that
	// response goes out, means a run this fast/cheap guard would refuse
	// anyway is refused synchronously instead, with nothing printed as having
	// started at all.
	if err := pr.engine.ExecutePrecondition(ctx, initiativeID); err != nil {
		return "", preconditionf("%s", err)
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
	// Start over: discard a stopped run's kept files only now that this run is
	// accepted. Discarding first and then being refused lost them for nothing.
	if fresh {
		_ = pr.engine.DiscardPartial(initiativeID)
	}
	if strings.TrimSpace(note) != "" {
		pr.engine.SetNoteOverride(initiativeID, note)
	}
	r.launchOperation(opID, func(bg context.Context) {
		// ReleaseInitiativeClaim, not SetInitiativeStatus (review H7): a CAS
		// on `running`, so this cannot stomp a legitimate write (e.g.
		// verify's CompleteInitiative) that landed in the gap between
		// Execute's engine mutex being released and this closure running.
		release := func() {
			if err := pr.db.ReleaseInitiativeClaim(context.WithoutCancel(bg), initiativeID,
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, cs.ID)
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
		// ReleaseInitiativeClaim, not SetInitiativeStatus (review H7): a CAS
		// on `running`. Proven repro: verify's CompleteInitiative landing in
		// the gap between ApplyChangeSet's engine mutex being released and
		// this closure running, discarding a real completion. See
		// ReleaseInitiativeClaim's own doc comment.
		release := func() {
			if err := pr.db.ReleaseInitiativeClaim(context.WithoutCancel(bg), initiativeID,
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, changeSetID)
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
	// Read-only and lock-free (review A3): a file the change set touched has
	// changed in the workspace since apply, so reverting now would erase that
	// edit. Checked before the claim so a stale revert answers 409 synchronously
	// instead of starting an operation whose only failure path is the log --
	// the same defect apply and revert's own status guard were fixed for
	// (handleApplyChangeSet/handleRevertChangeSet). RevertChangeSet re-checks
	// this itself, under the engine lock, immediately before it restores
	// anything, so a race in the gap between this read and the claim below is
	// still refused safely.
	if reason, err := pr.engine.RevertBlockedReason(ctx, initiativeID, changeSetID); err != nil {
		return "", err
	} else if reason != "" {
		return "", preconditionf("%s", reason)
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
		// ReleaseInitiativeClaim, not SetInitiativeStatus (review H7): see
		// the identical comment on apply's release() above.
		release := func(to domain.InitiativeStatus) {
			if err := pr.db.ReleaseInitiativeClaim(context.WithoutCancel(bg), initiativeID,
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, changeSetID)
		r.log.Info("revert complete", "change_set", changeSetID)
	})
	return opID, nil
}

// CreatePRForInitiative pushes the initiative's dedicated branch and opens a
// pull request via `gh pr create` (review C4 piece 3), synchronously: unlike
// apply/execute/revert it neither claims the initiative nor mutates the
// workspace tree, only git/GitHub state, so there is no long-running pipeline
// to run as a background operation. The three ways this can be refused --
// no dedicated branch yet, `gh` not installed, or (PRChecksForChangeSet only)
// no PR yet -- are classified here as preconditions, never inside
// internal/workflow (see PreconditionError's doc comment), so a caller
// answers 409 with a message naming exactly which of the three it hit.
func (r *Runtime) CreatePRForInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	url, err := pr.engine.CreatePR(ctx, initiativeID)
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrNoInitiativeBranch):
			return "", preconditionf("%s", err.Error())
		case errors.Is(err, git.ErrGHNotFound):
			return "", preconditionf("%s", err.Error())
		case errors.Is(err, git.ErrNoRemote):
			// ICP re-run gap: `idea pr` on a repository with no remote used
			// to dump git's own raw push error verbatim ("'origin' does not
			// appear to be a git repository") instead of saying plainly what
			// was wrong and what to do about it.
			return "", preconditionf("%s", err.Error())
		}
		return "", err
	}
	return url, nil
}

// PRChecksForChangeSet reports the latest CI status of changeSetID's
// initiative's pull request via `gh pr checks`. See CreatePRForInitiative's
// doc comment for why "no PR yet" and "no gh" are classified here.
func (r *Runtime) PRChecksForChangeSet(ctx context.Context, projectID, changeSetID string) (git.PRChecksResult, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return git.PRChecksResult{}, err
	}
	res, err := pr.engine.PRChecks(ctx, changeSetID)
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrNoPR):
			return git.PRChecksResult{}, preconditionf("%s", err.Error())
		case errors.Is(err, git.ErrGHNotFound):
			return git.PRChecksResult{}, preconditionf("%s", err.Error())
		}
		return git.PRChecksResult{}, err
	}
	return res, nil
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("revise complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// RetryInitiative re-runs a failed initiative's workflow under a chosen model
// (retry-with-a-different-model). Returns the operation id.
func (r *Runtime) RetryInitiative(ctx context.Context, projectID, initiativeID, model string) (string, error) {
	return r.RetryInitiativeWithNote(ctx, projectID, initiativeID, model, "")
}

// RetryInitiativeWithNote is RetryInitiative, with an optional human note
// (D-a, wave 3 contract) that steers this one retry. Retry re-runs the
// framing→research→PRD→review pipeline rather than the implementer, so the
// note joins the framing prompt (see Engine.runFraming) instead of "the
// implementer's brief" the wave 3 contract names -- there is no implementer
// brief at this stage of the pipeline for it to join.
func (r *Runtime) RetryInitiativeWithNote(ctx context.Context, projectID, initiativeID, model, note string) (string, error) {
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
		return "", preconditionf("retry requires a failed initiative (initiative is %s); `idea next` names the step this initiative is actually ready for", ini.Status)
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
		if strings.TrimSpace(note) != "" {
			pr.engine.SetNoteOverride(initiativeID, note)
		}
		status, runErr := pr.engine.Retry(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "retry failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
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
	// See the identical lock in AdvanceInitiative (review C2): this claims
	// the initiative and, once running, ReopenPhase calls
	// DeleteTasksForInitiative -- the exact write a concurrent plan-edit
	// handler's stale pre-lock read let through as a spurious 200 OK.
	defer pr.lockPlanEdit(initiativeID)()
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
	if changeSetsHoldingWork(cses) > 0 {
		return "", preconditionf("cannot reopen a design phase after execution has begun")
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
		return "", preconditionf("no design or plan to reopen — advance the initiative first")
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
	// The plan this changes may change the task a stopped run's kept files
	// belong to; carrying them into a rewritten task would be wrong.
	_ = pr.engine.DiscardPartial(initiativeID)
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.ReopenPhase(bg, initiativeID, phase)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "reopen phase failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
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
	// See the identical lock in AdvanceInitiative (review C2): this claims
	// the initiative and, once running, RevisePlan calls
	// DeleteTasksForInitiative -- the exact write a concurrent plan-edit
	// handler's stale pre-lock read let through as a spurious 200 OK.
	defer pr.lockPlanEdit(initiativeID)()
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "", preconditionf("initiative is running; wait for it to finish")
	}
	if ini.Status != domain.InitiativePlanApproved && ini.Status != domain.InitiativeNeedsHumanJudgment {
		return "", preconditionf("no plan to revise (initiative is %s, so no execution plan exists yet); `idea next` names the step this initiative is actually ready for", ini.Status)
	}
	// The change-set guard, which lived only in Engine.RevisePlan -- and that
	// runs inside the launched operation, returning InitiativeFailed, so the
	// refusal marked the initiative FAILED instead of declining the request. A
	// healthy plan_approved initiative with one unreviewed change set became
	// `failed` because of a refusal the API answers synchronously on the sibling
	// endpoint one function away. ApprovePlanInitiative's own guard carries the
	// comment "Matching RevisePlan and ReopenPhase"; that was false of the
	// Runtime sibling until now.
	//
	// The engine check stays: it is the invariant, this is the precondition.
	//
	// Before the claim, matching ApprovePlanInitiative. After it would close the
	// window where a concurrent `execute` creates CHG-1 between this read and the
	// claim, but would need a compensating release -- and a failed release
	// strands the initiative `running` with no operation for
	// reconcileStaleOperations, which is worse than the race. The race degrades
	// to exactly the old behaviour, with the engine check as the backstop.
	//
	// The message names the route that does exist. With a change set present and
	// the initiative parked on plan_review, `idea next` offers revise-plan and
	// approve-plan and BOTH are now refused (deferred.md, audit 6 / J1: the
	// change-set walk in NextAction sits under plan_approved only). Saying
	// nothing there is how J1 survived a round.
	if cses, err := pr.db.ListChangeSets(ctx, initiativeID); err != nil {
		return "", err
	} else if changeSetsHoldingWork(cses) > 0 {
		return "", preconditionf("cannot revise a plan that already has change sets; " +
			"`idea changes` lists them, and `idea review <CHG-n> approve` — or " +
			"`--accept-findings` if the reviewer blocks — then `idea apply` is the way on")
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
	// The plan this changes may change the task a stopped run's kept files
	// belong to; carrying them into a rewritten task would be wrong.
	_ = pr.engine.DiscardPartial(initiativeID)
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.RevisePlan(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "plan revision failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("plan revision complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// changeSetsHoldingWork counts the change sets a plan-level command must not
// disturb. See domain.ChangeSetStatus.HoldsWork: `rejected` and `superseded`
// are absorbing states minted on ordinary paths, so counting them made these
// guards refuse forever.
func changeSetsHoldingWork(cses []domain.ChangeSet) int {
	n := 0
	for _, cs := range cses {
		if cs.Status.HoldsWork() {
			n++
		}
	}
	return n
}

// hasOpenStructuralPlanFinding reports whether the initiative carries an
// undisposed `critical` finding from the deterministic plan checks -- today,
// exactly a task-dependency cycle.
//
// ReviewRunID is the discriminator because runExecutionPlanReview stamps
// "deterministic-plan-check" on every finding the checks produced and leaves the
// adversarial reviewer's own run id on the rest. The PRD loop uses a different
// literal ("deterministic-check"), so the two cannot collide.
func hasOpenStructuralPlanFinding(ctx context.Context, pr *projectRuntime, initiativeID string) (bool, error) {
	findings, err := pr.db.ListFindingsByInitiative(ctx, initiativeID)
	if err != nil {
		return false, err
	}
	for _, f := range findings {
		if f.Disposition == "" &&
			f.ReviewRunID == workflow.DeterministicPlanCheckRun &&
			f.Severity == domain.SeverityCritical {
			return true, nil
		}
	}
	return false, nil
}

// RunVerificationGate records any human evidence in the request and then runs
// the verification gate, refusing an initiative that has already failed.
//
// Verify was the one state-writing path that never went through the Runtime:
// handleVerify called the engine directly, with no running check, no claim and
// no state precondition -- which is why `verifyInitiative` declared no 409 and
// the conflict axis correctly agreed there was nothing to declare.
//
// `failed` only. `running` is deliberately NOT refused: `idea apply` does not
// wait for its operation, and the initiative reads `running` until the release
// that follows the change set being marked applied -- so the documented
// apply-then-verify sequence (docs/demo-greenfield.md, and scripts/smoke.sh,
// which polls the change set and then verifies) runs inside that window.
// Refusing it there would break the flow this fix is not about.
//
// The residual that leaves: a `complete` written during an in-flight apply can
// be reverted by that apply's release, which writes priorStatus unconditionally.
// Pre-existing, recorded in docs/plans/deferred.md, and not closed here.
func (r *Runtime) RunVerificationGate(ctx context.Context, projectID, initiativeID, requirement string,
	disposition domain.VerificationStatus, note string) (domain.InitiativeStatus, []workflow.Coverage, error) {
	status, coverage, _, err := r.RunVerificationGateConfirmingTests(ctx, projectID, initiativeID, requirement, disposition, note, false)
	return status, coverage, err
}

// ConfirmResult reports what confirmPassingTests did (C3/H2, review fix):
// which requirement codes it actually confirmed, and which candidates it
// looked at but skipped, and why -- so a bad requirement never aborts the
// whole loop silently, and the caller (the HTTP handler, in turn the UI's
// confirm dialog) can show exactly what happened instead of a bare status.
type ConfirmResult struct {
	Confirmed []string         `json:"confirmed"`
	Skipped   []ConfirmSkipped `json:"skipped"`
}

// ConfirmSkipped names one requirement confirmPassingTests looked at but did
// not confirm, and why.
type ConfirmSkipped struct {
	Requirement string `json:"requirement"`
	Reason      string `json:"reason"`
}

// RunVerificationGateConfirmingTests is RunVerificationGate, with
// confirmPassingTests (C2, wire contract: `POST .../verify
// {"confirm_passing_tests": true}`) additionally confirming, through the same
// human-evidence path (actor "human"), every requirement whose mapped tests
// all passed. Never runs without this explicit request -- nothing here is
// invoked from any other path. The returned *ConfirmResult is nil unless
// confirmPassingTests was requested.
func (r *Runtime) RunVerificationGateConfirmingTests(ctx context.Context, projectID, initiativeID, requirement string,
	disposition domain.VerificationStatus, note string, confirmPassingTests bool) (domain.InitiativeStatus, []workflow.Coverage, *ConfirmResult, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", nil, nil, err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", nil, nil, err
	}
	// Resolved here rather than in the handler, so a missing code and a refused
	// state are decided in one place. With the lookup outside, the same failed
	// initiative answered 404 for `{"requirement":"REQ-99"}` and 409 for
	// `{"requirement":"REQ-99","pass":true}` -- the same request, two precedences.
	if requirement != "" {
		if _, err := pr.db.GetRequirementByRef(ctx, initiativeID, requirement); err != nil {
			return "", nil, nil, err
		}
	}
	// Evidence is recorded BEFORE the failed check, and that ordering is the
	// point. A human's pass/fail/waive on a requirement is a judgment about that
	// requirement, not about the workflow, and refusing it because a later stage
	// failed strands work with no way back: `idea retry` -- the escape named
	// below -- calls SupersedeRequirements before re-running framing, so it
	// regenerates the whole set and takes any evidence with it. The first version
	// of this fix gated both and a commit review caught the over-reach.
	if requirement != "" && disposition != "" {
		if err := pr.engine.RecordHumanEvidence(ctx, initiativeID, requirement, disposition, note, ""); err != nil {
			return "", nil, nil, err
		}
	}
	var confirmResult *ConfirmResult
	if confirmPassingTests {
		result, err := r.confirmPassingTests(ctx, pr, initiativeID)
		if err != nil {
			return "", nil, nil, err
		}
		confirmResult = &result
	}
	// The GATE is what a failed initiative may not run -- it is the only thing
	// here that can write `complete`. The status alone is not the test: apply,
	// revert, revise, retry and reopen-phase all claim `running` from `failed`
	// and leave failure_reason intact, so a failed initiative reads `running`
	// for the length of those operations. The recorded reason is the durable
	// fact; store.CompleteInitiative guards on the same predicate.
	if ini.Status == domain.InitiativeFailed || ini.FailureReason != "" {
		return "", nil, confirmResult, preconditionf(
			"the verification gate does not run on a failed initiative; `idea retry` re-runs it")
	}
	if err := pr.engine.VerifyInitiative(ctx, initiativeID); err != nil {
		// The engine cannot build a *PreconditionError -- internal/daemon imports
		// internal/workflow, not the reverse -- so the store's raw sentinel is
		// translated here. Unmapped it reached the client as
		// `400: store: initiative is not in a claimable state`.
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", nil, confirmResult, preconditionf("the initiative changed state while the gate ran; re-run `idea verify`")
		}
		return "", nil, confirmResult, err
	}
	ini, err = pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", nil, confirmResult, err
	}
	coverage, err := pr.engine.Coverage(ctx, initiativeID)
	if err != nil {
		return "", nil, confirmResult, err
	}
	return ini.Status, coverage, confirmResult, nil
}

// confirmPassingTests records human "pass" evidence, via the same path a
// manual `idea verify REQ-n --pass` uses, for every requirement that is
// `approved` (C3/H2/H4, review fix: never `draft`, `removed`, `verified`,
// `failed` or `waived` -- confirming a failed/waived requirement would
// silently overwrite a human's own explicit judgment, and the others are not
// awaiting one), has acceptance criteria, and whose mapped tests
// (projection.ComputeEvidenceTests) all passed. It requires at least one
// applied change set for the initiative -- otherwise there is nothing built
// to have produced real evidence from, whatever a stray finding might say.
//
// One bad requirement never aborts the loop and leaves earlier writes
// stranded (H2): every candidate is attempted independently, and a failure
// is recorded as skipped rather than returned, so the caller always gets a
// complete, honest account of what happened to every requirement it looked
// at instead of a partial confirm hidden behind a 500.
func (r *Runtime) confirmPassingTests(ctx context.Context, pr *projectRuntime, initiativeID string) (ConfirmResult, error) {
	// Never nil (renders as JSON `null` rather than `[]`): every other
	// wave 3 array-shaped response is documented to decode to an array, even
	// empty, and this is no exception.
	result := ConfirmResult{Confirmed: []string{}, Skipped: []ConfirmSkipped{}}
	changeSets, err := pr.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return result, err
	}
	hasApplied := false
	for _, cs := range changeSets {
		// A NoOp applied change set touched nothing (ICP re-run defect #2):
		// it is not "something built" to have produced real evidence from.
		if cs.ImplementsTask() {
			hasApplied = true
			break
		}
	}
	reqs, err := pr.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return result, err
	}
	if !hasApplied {
		for _, req := range reqs {
			if req.SupersededBy == "" && req.Status == domain.RequirementApproved {
				result.Skipped = append(result.Skipped, ConfirmSkipped{
					Requirement: req.Code, Reason: "the initiative has no applied change set yet",
				})
			}
		}
		return result, nil
	}
	evidence, err := projection.ComputeEvidenceTests(ctx, pr.db, pr.blobs, pr.rawBlobs, initiativeID)
	if err != nil {
		return result, err
	}
	for _, req := range reqs {
		if req.SupersededBy != "" || req.Status != domain.RequirementApproved {
			// Not a candidate at all (verified/failed/waived/draft/removed, or
			// superseded by a later revision) -- never reported as "skipped",
			// same as before this fix: skipped names only what looked
			// confirmable and was not.
			continue
		}
		if len(req.AcceptanceCriteria) == 0 {
			result.Skipped = append(result.Skipped, ConfirmSkipped{Requirement: req.Code, Reason: "no acceptance criteria"})
			continue
		}
		reqEvidence := evidence[req.ID]
		if len(reqEvidence) == 0 {
			// ICP re-run defect #3: "not every mapped test passed" is wrong
			// when NOTHING is mapped at all -- it reads as "some tests failed"
			// when the real problem is nothing was ever linked to this
			// requirement, and it gives no hint how to fix that.
			result.Skipped = append(result.Skipped, ConfirmSkipped{
				Requirement: req.Code,
				Reason: fmt.Sprintf(
					"no tests are mapped to %s; name it in the verification plan's hooks, "+
						"name a test's function so it contains %q, or implement it from a task "+
						"that `implements` this requirement (its change set's own test files are "+
						"mapped automatically once applied and passing)",
					req.Code, req.Code),
			})
			continue
		}
		if !projection.AllTestsPassed(reqEvidence) {
			result.Skipped = append(result.Skipped, ConfirmSkipped{Requirement: req.Code, Reason: "not every mapped test passed"})
			continue
		}
		// actor "": RecordHumanEvidence defaults it to store.HumanActor, so this
		// is recorded exactly as if a human had typed the pass themselves (C2:
		// "using the existing human-evidence path with the actor set to human").
		if err := pr.engine.RecordHumanEvidence(ctx, initiativeID, req.Code, domain.VerificationPass,
			"confirmed: every mapped test passed", ""); err != nil {
			result.Skipped = append(result.Skipped, ConfirmSkipped{Requirement: req.Code, Reason: err.Error()})
			continue
		}
		result.Confirmed = append(result.Confirmed, req.Code)
	}
	return result, nil
}

// ApprovePlanInitiative accepts the execution plan, findings and all.
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
		return "", preconditionf("initiative is running; wait for it to finish")
	}
	// needs_human_judgment only. `plan_approved` used to be accepted too, and
	// once ApprovePlan became a real accept that was a hole: SetInitiativeStatus
	// clears judgment_reason, so a plan_approved initiative carries "", which the
	// reason gate below allows -- and the web UI's commit bar renders in every
	// status (PlanPage.tsx). A stray click would then have dispositioned every
	// open finding as a human's accepted_risk, with no review and no
	// confirmation. Refusing here means the button is inert rather than
	// destructive, and it makes the idempotency explicit instead of silent.
	if ini.Status == domain.InitiativePlanApproved {
		return "", preconditionf("this plan is already approved")
	}
	if ini.Status != domain.InitiativeNeedsHumanJudgment {
		return "", preconditionf("no plan to approve (initiative is %s, so no plan is waiting on your judgment); `idea next` names the step this initiative is actually ready for", ini.Status)
	}
	// The reason, not just the status. All four JudgmentReasons share one, so a
	// status-only gate let this accept a plan parked on `plan_dependencies` --
	// where the undisposed `plan_dependency` finding IS the guard keeping a
	// dependency graph known to be incomplete out of `complete`. Approving there
	// dispositioned the guard and shipped the incomplete graph. `idea next`
	// already refuses to offer approve-plan in that state; this is the API
	// refusing to accept it. Empty is allowed for initiatives parked before the
	// reason was recorded.
	switch ini.JudgmentReason {
	case "", domain.JudgmentPlanReview:
	default:
		return "", preconditionf("approve-plan accepts a plan whose review raised findings; "+
			"this initiative is waiting on %s, which only `idea revise-plan` resolves", ini.JudgmentReason)
	}
	// Matching RevisePlan and ReopenPhase. Without it a plan-level command swept
	// the findings of an initiative that was already building, including a
	// blocking change-set finding -- the one nothing else can clear.
	if cses, err := pr.db.ListChangeSets(ctx, initiativeID); err != nil {
		return "", err
	} else if changeSetsHoldingWork(cses) > 0 {
		return "", preconditionf("cannot approve a plan that already has change sets")
	}
	// A human can accept an adversarial reviewer's judgment. A human cannot
	// accept a dependency cycle: detectCycle's own finding says "the plan cannot
	// be scheduled", and nextReadyTask will never yield a task whose
	// predecessors can never apply. Approving one would trade audit 7's
	// live-lock for a Build that no command can advance -- `idea next` answers
	// "verify" forever and only revise-plan recovers, which it will not name.
	//
	// critical ONLY, deliberately. deterministicPlanChecks also emits a `high`
	// traceability finding per untraced task, and a task is untraced whenever
	// the model's `requirement` string does not resolve: reqByCode returns "" and
	// createTask skips the edge with no error. That field is a bare string with
	// no enum, so an untraced task is ordinary, not exceptional. Sweeping `high`
	// in here would refuse approve-plan on the common case and send the user to
	// revise-plan, which re-runs the same unconstrained call -- reconstructing
	// the live-lock this fix exists to remove, with a different message.
	// Untraced work schedules and applies fine; it is a judgment call, and
	// judgment calls are exactly what a human may accept.
	if blocked, err := hasOpenStructuralPlanFinding(ctx, pr, initiativeID); err != nil {
		return "", err
	} else if blocked {
		return "", preconditionf("this plan has a structural defect a human cannot sign off " +
			"(see `idea reviews`); `idea revise-plan` regenerates it")
	}
	// Claim the initiative atomically. The read-only check above gives the
	// specific message; this is what actually excludes a second request --
	// four of these entry points asked "is it running?" and never set it, so
	// two concurrent callers both passed.
	// Narrowed with the read gate above: both must move together, or the request
	// passes one and fails the other with the wrong message.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment); err != nil {
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("plan approval complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// RerunChangeSetReviewInitiative runs the adversarial reviewer again for a
// change set whose review never completed, as an async operation (ICP re-run
// D2). The review itself is a real model call -- observed 1m12s-1m38s across
// four founder-journey runs -- which used to run SYNCHRONOUSLY inside the
// HTTP handler (detached from the request's own cancellation, but not from
// the CLI's 60s http.Client timeout): the daemon always finished the work,
// but the CLI reported a timeout error on every single invocation of a
// brand-new command, because it was still blocked waiting for the response
// when its own client gave up. It now returns an operation id immediately,
// the same shape `idea apply`/`idea execute` use, so `idea review <id> rerun`
// can be waited on/watched/cancelled like any other long-running action
// instead of racing a fixed client timeout against a model call.
//
// The two precondition checks that used to fail fast, synchronously, inside
// Engine.RerunChangeSetReview are duplicated here (read-only, no model call)
// so a wrong-state rerun still refuses immediately with a normal 4xx rather
// than an operation the caller must poll to learn failed instantly.
// Engine.RerunChangeSetReview re-checks both under its own lock regardless,
// which is the correctness boundary; this is purely for a fast, synchronous
// answer on the common misuse.
func (r *Runtime) RerunChangeSetReviewInitiative(ctx context.Context, projectID, initiativeID, changeSetID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	cs, err := pr.db.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return "", err
	}
	if cs.InitiativeID != initiativeID {
		return "", fmt.Errorf("change set %q does not belong to initiative %q", changeSetID, initiativeID)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		return "", preconditionf("cannot rerun review for %s: it is %s, not awaiting review; `idea changes` shows its current state", cs.HumanID, cs.Status)
	}
	if cs.ReviewCompleted() {
		return "", preconditionf("%s already has a completed review; nothing to rerun -- `idea review %s approve` or `reject` instead", cs.HumanID, cs.HumanID)
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	// Permissive, matching ApplyChangeSetInitiative (a change set's review
	// state is independent of exactly which settled status its initiative is
	// in): what this excludes is the one case that matters, starting a rerun
	// while another operation already holds the initiative.
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID,
		domain.InitiativeApproved, domain.InitiativePlanApproved,
		domain.InitiativeNeedsHumanJudgment, domain.InitiativeComplete,
		domain.InitiativeFailed); err != nil {
		if errors.Is(err, store.ErrInitiativeNotClaimable) {
			return "", preconditionf("initiative is busy; wait for the running operation to finish")
		}
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "rerun_review")
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
			if err := pr.db.ReleaseInitiativeClaim(context.WithoutCancel(bg), initiativeID,
				priorStatus, store.ActorOrchestrator); err != nil {
				r.log.Error("could not release the initiative after a review rerun",
					"initiative", initiativeID, "op", opID, "error", err)
			}
		}
		if runErr := pr.engine.RerunChangeSetReview(bg, initiativeID, changeSetID); runErr != nil {
			release()
			r.failOperation(opID, initiativeID, "review rerun failed", bg, pr, runErr, false)
			return
		}
		release()
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, changeSetID)
		r.log.Info("review rerun complete", "change_set", changeSetID)
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
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
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
			_ = pr.db.SetOperationFailed(ctx, op.ID, domain.OperationFailed,
				"the daemon restarted while this operation was running, so it could not finish")
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
			// WithoutCancel: EnsureProject runs this sweep on the first request
			// that touches a project, so ctx is that request's. A client that
			// hangs up mid-reconcile would otherwise leave the very rows this
			// sweep exists to clean.
			_ = pr.db.FailInitiative(context.WithoutCancel(ctx), op.InitiativeID,
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
			// H3: recover to where this attempt STARTED, not always Approved.
			// reverted->applying is a legal, documented edge (changeset.go's
			// legalChangeSetTransition, added for re-apply crash recovery;
			// CHANGELOG.md's "a failed re-apply attempt recovers back to
			// `reverted`, not `approved`") -- hardcoding Approved here
			// mislabeled a crashed re-apply of an already-reverted change
			// set, and a later `idea apply` on it re-ran the fresh-apply path
			// against content that was, in fact, already once applied and
			// reverted.
			fromStatus := rec.FromStatus
			if fromStatus != domain.ChangeSetApproved && fromStatus != domain.ChangeSetReverted {
				fromStatus = domain.ChangeSetApproved // unknown (e.g. pre-upgrade event history): the only possibility before re-apply existed
			}
			if rec.PreImageDigest != "" {
				// A byte may have been written (scoped to THIS attempt only --
				// see ChangeSetRecovery.PreImageDigest): restore the pre-image,
				// and only then mark conflicted. RestoreWorkspace itself refuses
				// (StalePost) rather than overwrite an edit made to the same
				// paths since the crash. Either way, if the restore fails, leave
				// the change set in applying and log loudly so a later
				// reconcile/operator can retry (never persist a terminal status
				// over a mutated-or-refused workspace).
				if err := pr.engine.RestoreWorkspace(ctx, rec.ChangeSetID, rec.PreImageDigest); err != nil {
					r.log.Error("reconcile: restore failed, leaving change set applying", "change_set", rec.ChangeSetID, "err", err)
					continue
				}
				_ = pr.db.TransitionChangeSet(ctx, rec.ChangeSetID, domain.ChangeSetApplying, domain.ChangeSetConflicted, store.ActorOrchestrator)
			} else {
				// No bytes were written by this attempt (prepared/absent):
				// recover to fromStatus so a human can (re-)apply from there.
				_ = pr.db.TransitionChangeSet(ctx, rec.ChangeSetID, domain.ChangeSetApplying, fromStatus, store.ActorOrchestrator)
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
		reg.Add(p.Key, providerFor(p.Key, p.Type, key, p.BaseURL, p.DefaultKeyEnv))
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
		reg.Add(e.Name, providerFor(e.Name, e.Type, lookupEnv(e.KeyEnv, dotEnv), e.BaseURL, e.KeyEnv))
	}

	if isDemo(cfg, dotEnv) {
		log.Warn("no model provider configured; using deterministic demo provider")
	}
	reg.Add(demoProviderName, demoProvider(cfg.Harness))
	return reg
}

// demoProvider builds the built-in deterministic provider, optionally primed to
// make the PRD review not converge.
//
// The queue is the mechanism, not a flag on the provider: all three adversarial
// reviews used the schema name `review`, so a flag would have blocked all three.
// Each has its own key now -- `review`, `plan_review`, `change_set_review` -- so
// a queue primed for one cannot land on another.
//
// A blocking finding on a change set used to be unclearable by anything the CLI
// could do, which is why that sharing was a hazard rather than a nuisance. It is
// clearable now: `idea review <CHG-n> approve --accept-findings` signs off the
// reviewer's own findings. A finding carrying a deny code still is not.
func demoProvider(h config.HarnessConfig) *model.FakeProvider {
	fp := model.NewFakeProvider()
	// Clamped to max_cycles. The queue is safe only while it drains on M1's
	// review calls; a surplus entry outlives them and lands on the plan review,
	// or -- via `idea execute`, which the API accepts from a plan-blocked
	// initiative -- on the change-set review, where it parks the initiative on a
	// review the run did not intend to block. That is the exact state this
	// design chose a queue over a flag to avoid, so it is enforced rather than
	// trusted. (It is no longer unrecoverable: `idea review <CHG-n> approve
	// --accept-findings` clears the reviewer's own findings, as the comment
	// above demoProvider says. This sentence used to claim otherwise and
	// survived the fix that made it false.)
	n := h.Demo.BlockReviewCycles
	// review MEDIUM #7: max_cycles == 0 does NOT mean "unclamped" -- config's
	// own applyDefaults only defaults it to 3 when EVERY review.* field is
	// unset (max_cycles/max_tokens/max_cost_usd all zero), so a harness.yaml
	// that sets, say, review.max_tokens alongside demo.block_review_cycles
	// left max_cycles at its raw zero here even though the engine (New/
	// Reload) coerces any value <= 0 to 3 before ever running a review loop.
	// The old `max > 0 &&` guard read that raw zero as "no clamp", handing a
	// surplus block_review_cycles queue entry to the plan or change-set
	// review -- exactly the state a queue (over a flag) exists to avoid.
	// Effective(), not the raw field, matches what the engine will actually
	// enforce.
	if max := h.Review.EffectiveMaxCycles(); n > max {
		n = max
	}
	for i := 0; i < n; i++ {
		fp.EnqueueResponse("review", model.DemoBlockingReviewJSON)
	}
	// Separate queues, so a surplus in one cannot reach the other -- and neither
	// can reach the change-set review, which still answers on "review" only
	// after M1's queue is drained.
	for i := 0; i < h.Demo.BlockPlanReviewCycles; i++ {
		fp.EnqueueResponse("plan_review", model.DemoBlockingReviewJSON)
	}
	for i := 0; i < h.Demo.BlockChangeSetReviewCycles; i++ {
		fp.EnqueueResponse(workflow.ChangeSetReviewSchemaName, model.DemoBlockingReviewJSON)
	}
	if h.Demo.DanglePlanDependency {
		fp.EnqueueResponse("execution_plan", model.DemoDanglingPlanJSON)
	}
	return fp
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
//
// keyEnv is the variable name key was (or should have been) read from. When
// the provider is keyed (keyEnv != "") and no value was found, this registers
// a MissingKeyProvider instead of the real client (F1): the real client would
// otherwise run and let the provider's own 401 JSON reach the UI verbatim,
// with no daemon-side indication that the actual problem is a missing key
// rather than a live provider failure. Ollama and other keyless providers
// pass keyEnv == "" and are never affected.
func providerFor(name string, typ config.ProviderType, key, baseURL, keyEnv string) model.Provider {
	if keyEnv != "" && key == "" {
		return model.NewMissingKeyProvider(name, keyEnv)
	}
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
		// WithoutCancel, for the reason reconcileStaleOperations gives: this
		// runs under the first request to touch the project.
		if err := pr.db.FailInitiative(context.WithoutCancel(ctx), ini.ID,
			"the operation that claimed this initiative ended without releasing it"); err != nil {
			r.log.Error("could not release an orphaned initiative claim",
				"initiative", ini.ID, "error", err)
		}
	}
}
