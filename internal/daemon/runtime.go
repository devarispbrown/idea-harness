// Package daemon implements the localhost control-plane HTTP server (§28–§29):
// token-authenticated, project-scoped REST + SSE over one user-level daemon.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/project"
	"github.com/idea-harness/idea-harness/internal/security"
	"github.com/idea-harness/idea-harness/internal/store"
	"github.com/idea-harness/idea-harness/internal/workflow"
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
}

type projectRuntime struct {
	db       *store.DB
	blobs    *blob.Store
	cfg      config.Config
	rootPath string
	dotEnv   map[string]string
	registry *model.Registry
	engine   *workflow.Engine
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
		stateRoot: stateRoot,
		token:     token,
		global:    global,
		log:       log,
		projects:  map[string]*projectRuntime{},
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
func (r *Runtime) EnsureProject(ctx context.Context, projectID string) (*projectRuntime, error) {
	r.mu.Lock()
	if pr, ok := r.projects[projectID]; ok {
		r.mu.Unlock()
		return pr, nil
	}
	r.mu.Unlock()

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
	// First open in this daemon lifetime: any "running" operation is orphaned
	// (its goroutine died with a previous daemon process). Reconcile it so
	// initiatives never hang in a stuck "running" state (§34, §16).
	reconcileStaleOperations(ctx, db)

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
	// Point the execute pipeline's isolated work roots at this project's state
	// directory so WorkRoot mirrors "<state-root>/work/<initiative>".
	engine.SetWorkRoot(projDir)
	// The canonical workspace that apply writes into and revert restores is the
	// project root (the user's real source tree).
	engine.SetWorkspaceRoot(rec.RootPath)

	pr := &projectRuntime{db: db, blobs: blobs, cfg: cfg, rootPath: rec.RootPath, dotEnv: dotEnv, registry: registry, engine: engine}

	r.mu.Lock()
	r.projects[projectID] = pr
	r.mu.Unlock()
	return pr, nil
}

// ReloadProject drops the cached runtime so the next EnsureProject re-reads
// config and rebuilds the provider registry (§40).
func (r *Runtime) ReloadProject(ctx context.Context, projectID string) (*projectRuntime, error) {
	r.mu.Lock()
	delete(r.projects, projectID)
	r.mu.Unlock()
	return r.EnsureProject(ctx, projectID)
}

// StartInitiative creates an initiative + operation and runs the workflow
// asynchronously (§34). Returns the initiative and operation ids.
func (r *Runtime) StartInitiative(ctx context.Context, projectID, title, description string) (string, string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	ini := &domain.Initiative{Title: title, Description: description, Status: domain.InitiativeRunning}
	if err := pr.db.CreateInitiative(ctx, ini); err != nil {
		return "", "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, ini.ID, "run_initiative")
	if err != nil {
		return "", "", err
	}

	go func() {
		bg := context.Background()
		status, runErr := pr.engine.Run(bg, ini.ID, description)
		if runErr != nil {
			r.log.Error("initiative run failed", "initiative", ini.ID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			pr.db.FailInitiative(bg, ini.ID, runErr.Error())
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, ini.ID)
		r.log.Info("initiative run complete", "initiative", ini.ID, "status", status)
	}()

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
	go func() {
		bg := context.Background()
		status, runErr := pr.engine.Advance(bg, initiativeID)
		if runErr != nil {
			r.log.Error("advance failed", "initiative", initiativeID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			pr.db.FailInitiative(bg, initiativeID, runErr.Error())
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("advance complete", "initiative", initiativeID, "status", status)
	}()
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
	go func() {
		bg := context.Background()
		cs, runErr := pr.engine.Execute(bg, initiativeID)
		if runErr != nil {
			r.log.Error("execute failed", "initiative", initiativeID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, cs.ID)
		r.log.Info("execute complete", "initiative", initiativeID, "change_set", cs.HumanID)
	}()
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
	go func() {
		bg := context.Background()
		if runErr := pr.engine.ApplyChangeSet(bg, initiativeID, changeSetID); runErr != nil {
			r.log.Error("apply failed", "change_set", changeSetID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, changeSetID)
		r.log.Info("apply complete", "change_set", changeSetID)
	}()
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
	go func() {
		bg := context.Background()
		if runErr := pr.engine.RevertChangeSet(bg, initiativeID, changeSetID); runErr != nil {
			r.log.Error("revert failed", "change_set", changeSetID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, changeSetID)
		r.log.Info("revert complete", "change_set", changeSetID)
	}()
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
	go func() {
		bg := context.Background()
		status, runErr := pr.engine.Revise(bg, initiativeID)
		if runErr != nil {
			r.log.Error("revise failed", "initiative", initiativeID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			pr.db.FailInitiative(bg, initiativeID, runErr.Error())
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("revise complete", "initiative", initiativeID, "status", status)
	}()
	return opID, nil
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
	go func() {
		bg := context.Background()
		// Clear prior answers, then append each chunk as it completes so the UI
		// can observe real progress via the suggested-answers list.
		_ = pr.db.ReplaceSuggestedAnswers(bg, initiativeID, nil)
		total := 0
		runErr := pr.engine.SuggestAnswers(bg, initiativeID, func(answers []domain.SuggestedAnswer) {
			if err := pr.db.AppendSuggestedAnswers(bg, initiativeID, answers); err != nil {
				r.log.Error("suggest answers append failed", "initiative", initiativeID, "err", err)
				return
			}
			total += len(answers)
		})
		if runErr != nil {
			r.log.Error("suggest answers failed", "initiative", initiativeID, "err", runErr)
			pr.db.SetOperationStatus(bg, opID, domain.OperationFailed, "")
			return
		}
		pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("suggest answers complete", "initiative", initiativeID, "count", total)
	}()
	return opID, nil
}

// ListProjects returns the registry entries.
func (r *Runtime) ListProjects(ctx context.Context) ([]store.ProjectRecord, error) {
	return r.global.ListProjects(ctx)
}

// reconcileStaleOperations marks any unfinished operations (and their
// initiatives) as failed. Called once per project on daemon startup: an
// operation still "running"/"pending" means its driving goroutine was lost.
// It also reconciles interrupted applies: any non-terminal (prepared /
// workspace_mutated) journal row is marked recovery_required so a half-applied
// workspace is surfaced instead of silently left mutated (review H2).
func reconcileStaleOperations(ctx context.Context, db *store.DB) {
	ops, err := db.ListUnfinishedOperations(ctx)
	if err != nil {
		return
	}
	for _, op := range ops {
		_ = db.SetOperationStatus(ctx, op.ID, domain.OperationFailed, "")
		if op.InitiativeID != "" {
			_ = db.SetInitiativeStatus(ctx, op.InitiativeID, domain.InitiativeFailed, store.ActorOrchestrator)
		}
	}
	_, _ = db.ReconcileJournal(ctx)
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
// redaction, from the environment and .env (§47).
func collectSecrets(cfg config.Config, dotEnv map[string]string) []string {
	var out []string
	for name, p := range cfg.Harness.Providers {
		_, _, keyEnv := config.ResolveProvider(name, p)
		if keyEnv != "" {
			if v := lookupEnv(keyEnv, dotEnv); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
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

	// Configured providers override preset defaults (custom base URLs/types).
	for name, pc := range cfg.Harness.Providers {
		typ, baseURL, keyEnv := config.ResolveProvider(name, pc)
		reg.Add(name, providerFor(name, typ, lookupEnv(keyEnv, dotEnv), baseURL))
	}

	if isDemo(cfg, dotEnv) {
		log.Warn("no model provider configured; using deterministic demo provider")
	}
	reg.Add("idea-harness-default", model.NewFakeProvider())
	return reg
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
