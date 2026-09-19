// Package workflow implements the deterministic M1 stage graph and the
// orchestrator that drives it (§6, §11). The orchestrator is constrained by
// workflow policy: it runs stages in order and may only rank/explain next moves
// within that policy.
package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	ccontext "github.com/devarispbrown/idea-harness/internal/context"
	"github.com/devarispbrown/idea-harness/internal/docparse"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/review"
	"github.com/devarispbrown/idea-harness/internal/roles"
	"github.com/devarispbrown/idea-harness/internal/schema"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/tools"
)

// Stage keys for the M1 graph.
const (
	StageFraming           = "framing"
	StageResearch          = "research"
	StageProductDefinition = "product_definition"
	StageReview            = "review"
)

// Stage keys for the M2 graph (§12).
const (
	StageArchitecture        = "architecture"
	StageProductDesign       = "product_design"
	StageVerificationDesign  = "verification_design"
	StageConvergence         = "convergence"
	StageExecutionPlan       = "execution_plan"
	StageExecutionPlanReview = "execution_plan_review"
	StageImplementationBrief = "implementation_brief"
)

// Role names (must match contracts/*.yaml).
const (
	RoleOrchestrator         = "product_orchestrator"
	RoleResearchAnalyst      = "research_analyst"
	RoleProductManager       = "product_manager"
	RoleAdversarialReviewer  = "adversarial_reviewer"
	RoleArchitect            = "architect"
	RoleProductDesigner      = "product_designer"
	RoleVerificationEngineer = "verification_engineer"
	RoleEngineeringLead      = "engineering_lead"
)

// Engine drives the M1 workflow.
type Engine struct {
	db       *store.DB
	blobs    *blob.Store // redacted store: model artifacts/context
	rawBlobs *blob.Store // redaction-free store: diff/apply/revert content
	redactor *security.Redactor
	registry *model.Registry
	cfg      config.HarnessConfig
	log      *slog.Logger
	// liveMu guards live: the running agent of each initiative, readable
	// while Execute holds mu (see runstate.go).
	liveMu        sync.Mutex
	live          map[string]*liveAttempt
	workRoot      string
	workspaceRoot string
	dotEnv        map[string]string // the project's .env, for provider keys

	// mu serializes the state-mutating operations (execute/apply/revert) so two
	// concurrent invocations cannot race on the same task or workspace.
	mu sync.Mutex

	// costMu guards the per-pipeline cost budget (review M4 cost pricing).
	costMu    sync.Mutex
	budgetUSD float64 // 0 = no budget
	spentUSD  float64

	// describeMu single-flights image description so concurrent uploads cannot
	// re-describe the same image or amplify model calls (vision review).
	describeMu sync.Mutex

	// modelMu guards modelOverride, a per-initiative "provider/model" override set
	// for a retry-with-a-different-model and cleared once that run finishes. It is
	// keyed by initiativeID so a retry of one initiative can never bleed into a
	// concurrent run of another.
	modelMu       sync.Mutex
	modelOverride map[string]string

	// noteMu guards noteOverride, a per-initiative human note set for the next
	// Execute/Retry run only (D-a, "Rerun with a note to the agent"). Modelled
	// on modelOverride/modelMu above.
	noteMu       sync.Mutex
	noteOverride map[string]string
}

// SetNoteOverride records a human note to steer the given initiative's next
// Execute or Retry (D-a, wave 3 contract). Cleared once that run finishes.
func (e *Engine) SetNoteOverride(initiativeID, note string) {
	e.noteMu.Lock()
	if e.noteOverride == nil {
		e.noteOverride = map[string]string{}
	}
	e.noteOverride[initiativeID] = note
	e.noteMu.Unlock()
}

func (e *Engine) currentNoteOverride(initiativeID string) string {
	e.noteMu.Lock()
	defer e.noteMu.Unlock()
	return e.noteOverride[initiativeID]
}

func (e *Engine) clearNoteOverride(initiativeID string) {
	e.noteMu.Lock()
	delete(e.noteOverride, initiativeID)
	e.noteMu.Unlock()
}

// setStage records the current step of initiativeID's running operation (C3,
// wave 3 contract), for the CLI/UI wait loop to show instead of a bare "still
// working…". Best-effort: a failure to persist a cosmetic progress hint must
// never fail the workflow step it describes, and it is a no-op (not an error)
// when there is no running operation to attach to -- e.g. a synchronous call
// outside launchOperation, such as change-set review.
func (e *Engine) setStage(ctx context.Context, initiativeID, stage string) {
	if initiativeID == "" || stage == "" {
		return
	}
	if err := e.db.SetOperationStage(ctx, initiativeID, stage); err != nil {
		e.log.Warn("could not record the operation stage", "initiative", initiativeID, "stage", stage, "error", err)
	}
}

// redact applies the engine's known-values redactor to a free-text string at the
// write boundary, so a secret that reached the model in any context is not
// persisted/re-served in structured columns (full-platform review: secret-catalog
// completion). Nil-safe: without a redactor it returns s unchanged.
func (e *Engine) redact(s string) string {
	if s == "" || e.redactor == nil {
		return s
	}
	return e.redactor.RedactString(s)
}

// redactSlice redacts every element of a string slice in place.
func (e *Engine) redactSlice(ss []string) []string {
	for i := range ss {
		ss[i] = e.redact(ss[i])
	}
	return ss
}

// New builds an engine.
func New(db *store.DB, blobs *blob.Store, registry *model.Registry, cfg config.HarnessConfig, log *slog.Logger) *Engine {
	if cfg.Review.MaxCycles <= 0 {
		cfg.Review.MaxCycles = 3
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Engine{db: db, blobs: blobs, registry: registry, cfg: cfg, log: log, budgetUSD: cfg.Review.MaxCostUSD}
}

// SetRawBlobs sets the redaction-free content store used by the diff/apply/revert
// path (review C1/C2). File content must round-trip byte-for-byte, so it is never
// redacted; the redacted `blobs` store is reserved for model artifacts.
func (e *Engine) SetRawBlobs(b *blob.Store) { e.rawBlobs = b }

// SetRedactor sets the redactor applied at the DISPLAY/model surface (the
// adversarial review context and the diff endpoint), so raw file content never
// reaches a network-egress model unredacted (review SRE regression).
func (e *Engine) SetRedactor(r *security.Redactor) { e.redactor = r }

// chargeUSD accumulates a model call's cost and fails once the running total
// exceeds the configured max_cost_usd budget (a cumulative ceiling per project,
// not a per-call cap — review M4). budgetUSD is set at construction/reload and
// never reset mid-flight, so concurrent pipelines cannot zero each other.
func (e *Engine) chargeUSD(cost float64) error {
	e.costMu.Lock()
	defer e.costMu.Unlock()
	if e.budgetUSD <= 0 {
		return nil
	}
	e.spentUSD += cost
	if e.spentUSD > e.budgetUSD {
		return fmt.Errorf("workflow: exceeded max_cost_usd budget ($%.4f > $%.4f)", e.spentUSD, e.budgetUSD)
	}
	return nil
}

// Reload swaps the config-derived components in place on the SAME engine, so the
// engine's mutex continues to serialize execute/apply/revert across a reload. It
// is guarded by e.mu, which ALL entry points now hold (see below), so no field
// read races the swap.
func (e *Engine) Reload(cfg config.HarnessConfig, registry *model.Registry, redactor *security.Redactor, blobs *blob.Store, dotEnv map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Swapped under the same lock as cfg, so an Execute never pairs a new
	// config with the old .env.
	e.dotEnv = dotEnv
	if cfg.Review.MaxCycles <= 0 {
		cfg.Review.MaxCycles = 3
	}
	e.cfg = cfg
	e.registry = registry
	e.redactor = redactor
	e.blobs = blobs
	e.costMu.Lock()
	e.budgetUSD = cfg.Review.MaxCostUSD
	e.costMu.Unlock()
}

// contentBlobs returns the redaction-free store for file content. It fails
// closed: when no raw store was configured (SetRawBlobs), it returns nil and
// callers surface an error rather than silently round-tripping content through
// the redacting store (which would corrupt file bytes — review C1/C2).
func (e *Engine) contentBlobs() *blob.Store {
	return e.rawBlobs
}

// Run executes the full vertical slice for an initiative and returns the final
// initiative status.
func (e *Engine) Run(ctx context.Context, initiativeID, idea string) (domain.InitiativeStatus, error) {
	start := time.Now()
	e.log.Info("workflow started", "initiative", initiativeID)
	if err := e.runFraming(ctx, initiativeID, idea); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runResearch(ctx, initiativeID, idea); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runProductDefinition(ctx, initiativeID, nil); err != nil {
		return domain.InitiativeFailed, err
	}
	status, err := e.runReviewLoop(ctx, initiativeID)
	e.log.Info("workflow finished", "initiative", initiativeID, "status", status, "duration", time.Since(start).Round(time.Millisecond))
	return status, err
}

// effectiveMaxOutput is the budget actually sent: what the stage wants, capped
// by what the provider says it can produce.
//
// The cap is the fix for audit 5's B1, and the defect it closes had been in the
// tree since the initial commit and shipped in v0.1.0: maxOutputForStage asks
// 32000 for the PRD, every real provider declares less (OpenAI and Anthropic
// 16384, Gemini 8192), and nothing compared the two. `Capabilities.MaxOutput`
// was declared per provider and never read, so the ceiling was decorative. The
// result was a hard 400 on every real OpenAI run -- the README's own worked
// example -- with the upstream explanation discarded (see model.httpError).
//
// A provider that declares 0 has not stated a ceiling, so the stage budget
// stands.
//
// Capping alone would have been a bad trade, and a review caught the claim that
// first sat here. "A truncated structured reply fails schema validation loudly"
// is false: model.repairJSON closes unterminated strings and containers, and
// schema.Review has no minItems, so an adversarial review cut from 15 findings
// to 1 validates clean. Turning a loud 400 into a quietly shorter review is
// worse than the 400.
//
// So the cap is only safe because the providers now detect truncation directly,
// from their own finish reason, and return model.ErrTruncated (see
// model.truncatedError). Do not remove that check and leave this one.
func effectiveMaxOutput(stageKey string, prov model.Provider) int {
	return minOutput(maxOutputForStage(stageKey), prov)
}

// minOutput caps a wanted budget by the provider's declared ceiling. A provider
// declaring 0 has stated no ceiling, so the want stands.
func minOutput(want int, prov model.Provider) int {
	if prov == nil {
		return want
	}
	if ceiling := prov.Capabilities().MaxOutput; ceiling > 0 && want > ceiling {
		return ceiling
	}
	return want
}

// maxOutputForStage returns the completion token budget a stage would like. The
// PRD is the largest output (full requirement set + acceptance criteria) and was
// previously truncated at 4096 tokens; framing is a short brief.
//
// These are wants, not promises: several exceed the ceilings real providers
// declare, and effectiveMaxOutput is what reconciles them. Do not read a value
// here as one that will be sent.
func maxOutputForStage(stageKey string) int {
	switch stageKey {
	case StageProductDefinition:
		return 32000
	case StageReview, StageExecutionPlanReview:
		// The adversarial reviewer can emit many findings (15+); a truncated
		// findings list silently fails schema validation.
		return 16384
	case StageResearch, StageArchitecture, StageProductDesign,
		StageVerificationDesign, StageConvergence, StageExecutionPlan:
		return 8192
	case "suggest_answers":
		return 8192
	default:
		return 4096
	}
}

// isTransientModelError reports whether a provider error is a transient
// infrastructure condition (rate limit / overload / timeout) that should be
// retried with backoff rather than failing the run (§51).
func isTransientModelError(err error) bool {
	s := strings.ToLower(err.Error())
	for _, c := range []string{"429", "500", "502", "503", "504", "529", "rate limit", "overloaded", "timeout", "temporarily", "unavailable"} {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

// runModel runs one structured-output model call and persists run provenance.
func (e *Engine) runModel(ctx context.Context, initiativeID, stageKey, roleName, schemaName, schemaJSON string, inputs []ccontext.Input, userPrompt string) (string, map[string]any, error) {
	runID, err := e.db.CreateRun(ctx, domain.RunModel, "", roleName, initiativeID, stageKey)
	if err != nil {
		return "", nil, err
	}
	e.wireDocConsumesIfAny(ctx, initiativeID, runID, inputs)

	bundle := ccontext.Compile(inputs)
	modelSpec := e.modelSpecFor(initiativeID, roleName)
	prov, modelName, err := e.registry.Resolve(modelSpec)
	if err != nil {
		e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
		return runID, nil, fmt.Errorf("workflow: %s: %w", stageKey, err)
	}
	// Include the JSON schema in the system prompt so the model knows the exact
	// required fields (cross-provider structured-output guidance, §39).
	system := roleSystemPrompt(roleName) + bundle.System +
		"\n\nReturn a single JSON object matching this schema (no markdown fences):\n" + schemaJSON
	req := model.Request{
		Model:          modelName,
		System:         system,
		Messages:       []model.Message{{Role: "user", Content: userPrompt}},
		JSONSchema:     []byte(schemaJSON),
		JSONSchemaName: schemaName,
		MaxOutput:      effectiveMaxOutput(stageKey, prov),
	}

	reqStart := time.Now()
	e.log.Info("model request", "stage", stageKey, "role", roleName, "model", modelSpec)
	// Bounded retry (3 attempts): transient infra errors (503/429/…), empty
	// output, and schema-validation failure are each retried before failing, so
	// a transient provider error or malformed JSON does not sink the run.
	//
	// totalCostUSD/totalInputTokens/totalOutputTokens accumulate EVERY billed
	// attempt (M5, review fix): chargeUSD already bills every retry against
	// the budget, but the manifest used to record only the last attempt's
	// own usage -- a real cost incurred by an earlier attempt that then
	// failed validation and was retried simply vanished from GET .../costs.
	var resp model.Response
	var decoded *model.StructuredResult
	var lastErr error
	var totalCostUSD float64
	var totalInputTokens, totalOutputTokens int64
	const maxAttempts = 3
	// failureManifest records whatever was actually spent before a run that
	// ultimately fails (M5, review fix: "model runs that fail... never write
	// a manifest, so their spend and time are missing"). A run that fails
	// before any attempt is billed (e.g. the registry resolve above) has
	// nothing to record and is left alone -- an empty manifest would be
	// noise, not evidence.
	failureManifest := func() {
		if totalCostUSD == 0 && totalInputTokens == 0 && totalOutputTokens == 0 {
			return
		}
		_ = e.db.CreateRunManifest(ctx, &domain.RunManifest{
			RunID:          runID,
			RequestedModel: modelSpec,
			ActualModel:    resp.ActualModel,
			Usage: domain.RunUsage{
				InputTokens: totalInputTokens, OutputTokens: totalOutputTokens,
				CostUSD: totalCostUSD, DurationMS: time.Since(reqStart).Milliseconds(),
			},
			StartedAt:  reqStart,
			FinishedAt: time.Now().UTC(),
		})
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err = prov.Complete(ctx, req)
		if err != nil {
			if attempt < maxAttempts && isTransientModelError(err) {
				e.log.Warn("transient model error, retrying", "stage", stageKey, "role", roleName, "model", modelSpec, "attempt", attempt, "err", err)
				time.Sleep(time.Duration(attempt) * 1500 * time.Millisecond)
				continue
			}
			e.log.Error("model request failed", "stage", stageKey, "role", roleName, "model", modelSpec, "err", err, "attempt", attempt, "duration", time.Since(reqStart).Round(time.Millisecond))
			failureManifest()
			e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
			return runID, nil, fmt.Errorf("workflow: %s: %w", stageKey, err)
		}
		// Charge every billed attempt (including retries) against the budget,
		// and accumulate it for the manifest this run eventually writes.
		totalCostUSD += resp.Usage.CostUSD
		totalInputTokens += resp.Usage.InputTokens
		totalOutputTokens += resp.Usage.OutputTokens
		if err := e.chargeUSD(resp.Usage.CostUSD); err != nil {
			failureManifest()
			e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
			return runID, nil, fmt.Errorf("workflow: %s: %w", stageKey, err)
		}
		if strings.TrimSpace(resp.RawJSON) == "" {
			lastErr = fmt.Errorf("model returned empty output")
			if attempt < maxAttempts {
				e.log.Warn("model returned empty output, retrying", "stage", stageKey, "role", roleName, "model", modelSpec, "attempt", attempt)
				req.System += "\n\nReturn ONLY the JSON object."
				continue
			}
			break
		}
		decoded, err = model.DecodeStructured([]byte(resp.RawJSON), []byte(schemaJSON))
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		if attempt < maxAttempts {
			e.log.Warn("structured output invalid, retrying", "stage", stageKey, "role", roleName, "model", modelSpec, "attempt", attempt, "err", err)
			req.System += "\n\nReturn ONLY the JSON object, with properly escaped quotes and no trailing commas."
			continue
		}
		break
	}
	e.log.Info("model response", "stage", stageKey, "role", roleName, "model", modelSpec, "actual", resp.ActualModel, "duration", time.Since(reqStart).Round(time.Millisecond), "out_tokens", resp.Usage.OutputTokens, "cost_usd", resp.Usage.CostUSD)

	// Enforce the review token ceiling as a hard stop (review M4).
	if e.cfg.Review.MaxTokens > 0 && resp.Usage.OutputTokens > e.cfg.Review.MaxTokens {
		failureManifest()
		e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
		return runID, nil, fmt.Errorf("workflow: %s exceeded max_tokens (%d > %d)", stageKey, resp.Usage.OutputTokens, e.cfg.Review.MaxTokens)
	}

	if decoded == nil {
		e.log.Error("structured output invalid", "stage", stageKey, "role", roleName, "err", lastErr)
		failureManifest()
		e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
		return runID, nil, fmt.Errorf("workflow: %s structured output: %w", stageKey, lastErr)
	}

	contextDigest := e.storeBlob(bundle.System)
	resultDigest := e.storeBlob(string(decoded.Raw))

	rc, _ := roles.Load(roleName)
	if err := e.db.CreateRunManifest(ctx, &domain.RunManifest{
		RunID:          runID,
		RoleVersion:    rc.Version,
		RequestedModel: modelSpec,
		ActualModel:    resp.ActualModel,
		ContextDigest:  contextDigest,
		ResultDigest:   resultDigest,
		// The accumulated total across every attempt (M5, review fix), not
		// just the successful last one -- see totalCostUSD's own comment.
		Usage: domain.RunUsage{InputTokens: totalInputTokens, OutputTokens: totalOutputTokens, CostUSD: totalCostUSD, DurationMS: time.Since(reqStart).Milliseconds()},
		// reqStart, not time.Now(): both fields used to be stamped "now" at
		// this point, AFTER the request already completed, so the manifest's
		// own StartedAt/FinishedAt never reflected the call's actual duration
		// (C3, wave 3 contract needs it for GET .../costs `seconds`).
		StartedAt:  reqStart,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		return runID, nil, err
	}

	if err := e.db.SetRunStatus(ctx, runID, domain.RunSucceeded, roleName); err != nil {
		return runID, nil, err
	}
	return runID, decoded.Valid, nil
}

func (e *Engine) storeBlob(content string) string {
	if content == "" {
		return ""
	}
	d, err := e.blobs.Put([]byte(content))
	if err != nil {
		return ""
	}
	return d
}

// resolveModelSpec returns the "provider/model" spec for a role: the runtime
// per-role model map first, then the role contract's model, then the configured
// default_model, then the registry default with a catalog-default model, then
// the demo default.
func (e *Engine) resolveModelSpec(roleName string) string {
	return resolveModelSpecFor(e.cfg, e.registry, roleName)
}

func resolveModelSpecFor(cfg config.HarnessConfig, registry *model.Registry, roleName string) string {
	if m := cfg.Models[roleName]; m != "" {
		return m
	}
	if rc, err := roles.Load(roleName); err == nil && rc.Model != "" {
		return rc.Model
	}
	if cfg.DefaultModel != "" {
		return cfg.DefaultModel
	}
	if registry != nil {
		if d := registry.Default(); d != "" && d != "idea-harness-default" {
			return d + "/" + model.DefaultModelFor(d)
		}
	}
	return "idea-harness-default"
}

// SetModelOverride forces every role to the given "provider/model" spec for the
// given initiative's next workflow run (retry-with-a-different-model). The
// engine clears it once that run finishes.
func (e *Engine) SetModelOverride(initiativeID, spec string) {
	e.modelMu.Lock()
	if e.modelOverride == nil {
		e.modelOverride = map[string]string{}
	}
	e.modelOverride[initiativeID] = spec
	e.modelMu.Unlock()
}

func (e *Engine) currentModelOverride(initiativeID string) string {
	e.modelMu.Lock()
	defer e.modelMu.Unlock()
	return e.modelOverride[initiativeID]
}

func (e *Engine) clearModelOverride(initiativeID string) {
	e.modelMu.Lock()
	delete(e.modelOverride, initiativeID)
	e.modelMu.Unlock()
}

// modelSpecFor returns the "provider/model" spec for a role and initiative: the
// per-initiative retry override wins, then the normal role resolution chain.
func (e *Engine) modelSpecFor(initiativeID, roleName string) string {
	if m := e.currentModelOverride(initiativeID); m != "" {
		return m
	}
	return e.resolveModelSpec(roleName)
}

// implementerModel resolves the coding agent's model: the per-role map
// ("implementer") wins, then the executor-level model, then the default chain.
// This is how OMP and Hermes get different models per role.
func (e *Engine) implementerModel() string {
	return ImplementerModelSpec(e.cfg, e.registry)
}

// ImplementerModelSpec is the model the coding agent runs: models.implementer,
// then executor.model, then the implementer role's usual resolution. Exported
// so the daemon's "can a run start" check resolves exactly what Execute will;
// it checked executor.model alone and so blocked runs Execute would accept,
// and passed ones it would refuse.
func ImplementerModelSpec(cfg config.HarnessConfig, registry *model.Registry) string {
	if m := cfg.Models["implementer"]; m != "" {
		return m
	}
	if m := cfg.Executor.Model; m != "" {
		return m
	}
	return resolveModelSpecFor(cfg, registry, "implementer")
}

// ImplementerProvider resolves the provider the coding agent will use, as
// Execute does.
func ImplementerProvider(cfg config.HarnessConfig, registry *model.Registry) (executor.AgentProvider, error) {
	return executor.ResolveAgentProvider(ImplementerModelSpec(cfg, registry), cfg.DefaultModel, cfg.Providers)
}

// demoModelSpec is the internal registry key resolveModelSpec falls back to
// when no provider is configured -- the same key the daemon's isDemo check
// (internal/daemon/providers.go: demoProviderName) compares against. Kept as
// its own constant here rather than imported, since workflow does not (and
// should not) depend on daemon.
const demoModelSpec = "idea-harness-default"

// displayModel maps the internal demo-provider spec to the literal "demo" a
// client can key off (model == "demo") to label placeholder output, so the
// registry's internal implementation detail never leaks into the API surface
// as a fake model id. Used on ChangeSet.Model (B2/Track G: "Shipped (demo)" /
// "Placeholder change" vs a real model's output).
func displayModel(spec string) string {
	if spec == "" || spec == demoModelSpec {
		return "demo"
	}
	return spec
}

// ---------- Stages ----------

func (e *Engine) runFraming(ctx context.Context, initiativeID, idea string) error {
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a concise IdeaBrief."},
		{Selector: "project_scale", Content: proportionateContext(projectScaleOf(ctx, e.workspaceRoot), detectStack(e.workspaceRoot), projectConventions(e.workspaceRoot))},
		{Selector: "task", Content: idea},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	// D-a: a rerun note on `POST .../retry` (wave 3 contract). Retry re-runs
	// this framing→research→PRD→review pipeline, not the implementer, so there
	// is no "implementer's brief" for it to join yet -- the closest faithful
	// place is the framing prompt that starts that pipeline back up, and the
	// framing run is what records it.
	note := strings.TrimSpace(e.currentNoteOverride(initiativeID))
	if note != "" {
		inputs = append(inputs, ccontext.Input{Selector: "human_note", Content: "Note from the human, for this retry only: " + note})
	}
	e.setStage(ctx, initiativeID, "Framing the idea")
	runID, obj, err := e.runModel(ctx, initiativeID, StageFraming, RoleOrchestrator, "idea_brief", schema.IdeaBrief,
		inputs,
		"Framing task:\n"+idea,
	)
	if err != nil {
		return err
	}
	if note != "" {
		if err := e.db.SetRunNote(ctx, runID, note); err != nil {
			e.log.Warn("could not record the rerun note on the run", "initiative", initiativeID, "run", runID, "error", err)
		}
	}

	body := renderIdeaBrief(obj)
	bodyDigest := e.storeBlob(body)
	art := &domain.Artifact{InitiativeID: initiativeID, Kind: domain.ArtifactIdeaBrief}
	ver := &domain.ArtifactVersion{BodyDigest: bodyDigest, ProducedByRun: runID, Status: "draft"}
	if err := e.db.CreateArtifact(ctx, art, ver); err != nil {
		return err
	}

	// Record assumptions as assertions.
	for _, a := range strSlice(obj["assumptions"]) {
		if err := e.db.CreateAssertion(ctx, &domain.Assertion{
			InitiativeID:  initiativeID,
			Type:          domain.AssertionAssumption,
			Status:        domain.AssertionCandidate,
			Statement:     e.redact(a),
			Confidence:    domain.ConfidenceLow,
			Source:        "framing",
			ProducedByRun: runID,
		}); err != nil {
			return err
		}
	}
	// Record open questions from framing.
	for _, q := range strSlice(obj["open_questions"]) {
		if err := e.createQuestionIfNew(ctx, initiativeID, e.redact(q)); err != nil {
			return err
		}
	}
	return nil
}

// createQuestionIfNew adds a question unless an equivalent one already exists
// for the initiative (§4: no duplicate open questions), merging near-duplicates
// by token Jaccard similarity, not just exact text (review B4, adopted #8). A
// judgment already raised as a structured decision is not re-asked as a
// free-form question — decisions take precedence over questions.
//
// A question that matches one already RESOLVED is not re-created either
// (review B4: "a question already answered is not re-created in a later
// cycle"); a `question.reraised_answered` event links the re-raising to the
// answered original, so the audit trail shows the cycle asked again rather
// than looking like nothing happened.
func (e *Engine) createQuestionIfNew(ctx context.Context, initiativeID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	norm := normalizeJudgment(text)

	decisions, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		dn := normalizeJudgment(d.Question)
		if dn == norm || similarJudgment(dn, norm) {
			return nil
		}
	}

	questions, err := e.db.ListQuestions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, q := range questions {
		qn := normalizeJudgment(q.Question)
		if qn == norm || similarJudgment(qn, norm) {
			if q.Status == domain.QuestionResolved {
				_ = e.db.ApplyNoop(ctx, store.Event{
					AggregateType: "question",
					AggregateID:   q.ID,
					Actor:         store.ActorOrchestrator,
					EventType:     "question.reraised_answered",
				})
			}
			return nil
		}
	}
	return e.db.CreateQuestion(ctx, &domain.Question{InitiativeID: initiativeID, Question: text})
}

// createDecisionIfNew adds a decision unless an equivalent one of the SAME
// TYPE already exists, merging near-duplicates by token Jaccard similarity
// after normalization, not just exact text (review B4, adopted #8: "same type
// and a token Jaccard similarity of at least 0.85"). Two decisions of
// different types are never merged even if worded identically -- a product
// decision and an operations decision can legitimately ask the same words
// about different concerns.
//
// The model re-lists prior decisions (prefixed with "[Source: ...]") on every
// revise cycle, so this also collapses those re-listings onto the original
// row. It resolves any equivalent open question too — a structured decision
// takes precedence over a free-form question.
func (e *Engine) createDecisionIfNew(ctx context.Context, initiativeID string, d *domain.Decision) error {
	if strings.TrimSpace(d.Question) == "" {
		return nil
	}
	norm := normalizeJudgment(d.Question)
	existing, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, x := range existing {
		if x.Type == d.Type && (normalizeJudgment(x.Question) == norm || similarJudgment(x.Question, d.Question)) {
			return nil
		}
	}
	if err := e.db.CreateDecision(ctx, d); err != nil {
		return err
	}
	// Resolve any open question this decision now covers.
	questions, err := e.db.ListQuestions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if q.Status != domain.QuestionOpen {
			continue
		}
		if normalizeJudgment(q.Question) == norm {
			digest := e.storeBlob("covered by decision " + d.Code)
			_ = e.db.AnswerQuestion(ctx, q.ID, digest, store.ActorOrchestrator)
			_ = e.db.WireDecisionDerivedFromQuestion(ctx, initiativeID, d.ID, q.ID)
		}
	}
	return nil
}

func (e *Engine) runResearch(ctx context.Context, initiativeID, idea string) error {
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce an evidence-backed ResearchBrief. Mark facts vs assumptions vs hypotheses. No unattributed facts. Cite the provided sources."},
		{Selector: "task", Content: idea},
		{Selector: "untrusted", Content: e.researchEvidence(ctx)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	e.setStage(ctx, initiativeID, "Researching")
	runID, obj, err := e.runModel(ctx, initiativeID, StageResearch, RoleResearchAnalyst, "research_brief", schema.ResearchBrief,
		inputs,
		"Research task:\n"+idea,
	)
	if err != nil {
		return err
	}

	body := renderResearchBrief(obj)
	bodyDigest := e.storeBlob(body)
	art := &domain.Artifact{InitiativeID: initiativeID, Kind: domain.ArtifactResearchBrief}
	ver := &domain.ArtifactVersion{BodyDigest: bodyDigest, ProducedByRun: runID, Status: "draft"}
	if err := e.db.CreateArtifact(ctx, art, ver); err != nil {
		return err
	}

	// Offline/disabled network: findings must be assumptions, never facts.
	offline := e.cfg.Network == config.NetworkDisabled
	for _, f := range objSlice(obj["findings"]) {
		ftype := domain.AssertionType(str(f["type"]))
		if offline && ftype == domain.AssertionFact {
			ftype = domain.AssertionAssumption
		}
		if err := e.db.CreateAssertion(ctx, &domain.Assertion{
			InitiativeID:  initiativeID,
			Type:          ftype,
			Status:        domain.AssertionCandidate,
			Statement:     e.redact(str(f["statement"])),
			Confidence:    domain.Confidence(str(f["confidence"])),
			Source:        e.redact(str(f["source"])),
			ProducedByRun: runID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// runProductDefinition authors the PRD. priorReqs are the active requirements
// from the immediately preceding cycle (captured before supersede); passing them
// — instead of the full requirement history — prevents the PRD from growing
// unboundedly across revise cycles.
func (e *Engine) runProductDefinition(ctx context.Context, initiativeID string, priorReqs []domain.Requirement) error {
	reqs := priorReqs
	if reqs == nil {
		all, _ := e.db.ListRequirements(ctx, initiativeID)
		reqs = activeRequirements(all)
	}
	decisions, _ := e.db.ListDecisions(ctx, initiativeID)
	findings, _ := e.db.ListFindingsByInitiative(ctx, initiativeID)
	questions, _ := e.db.ListQuestions(ctx, initiativeID)
	ini, _ := e.db.GetInitiative(ctx, initiativeID)

	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Author a PRD with verifiable requirements and measurable acceptance criteria (metric + target + condition). Raise a decision only for a genuinely open, undecided choice; do not re-list decisions already marked DECIDED, and do not restate an open question as a decision. Do not re-raise a question that already exists (open or answered); only add genuinely new questions. Any review finding dispositioned 'mitigate' must be addressed directly: incorporate the mitigation into the requirements. When a requirement is shaped by one of the decisions you raise, set the requirement's 'decision' field to that decision's exact question text."},
		{Selector: "project_scale", Content: proportionateContext(projectScaleOf(ctx, e.workspaceRoot), detectStack(e.workspaceRoot), projectConventions(e.workspaceRoot))},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decisions)},
		{Selector: "questions", Content: renderQuestions(questions)},
		{Selector: "findings", Content: renderFindings(findings)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}

	e.setStage(ctx, initiativeID, "Writing the PRD")
	runID, obj, err := e.runModel(ctx, initiativeID, StageProductDefinition, RoleProductManager, "prd", schema.PRD, inputs, "Author the PRD.")
	if err != nil {
		return err
	}

	body := renderPRD(obj)
	bodyDigest := e.storeBlob(body)
	art := &domain.Artifact{InitiativeID: initiativeID, Kind: domain.ArtifactPRD}
	ver := &domain.ArtifactVersion{BodyDigest: bodyDigest, ProducedByRun: runID, Status: "draft"}
	if err := e.db.CreateArtifact(ctx, art, ver); err != nil {
		return err
	}

	var decisionRefByReqID map[string]string // requirementID -> decision question text
	for _, r := range objSlice(obj["requirements"]) {
		var acs []domain.AcceptanceCriterion
		for _, ac := range objSlice(r["acceptance_criteria"]) {
			acs = append(acs, domain.AcceptanceCriterion{
				Metric: e.redact(str(ac["metric"])), Target: e.redact(str(ac["target"])), Condition: e.redact(str(ac["condition"])),
			})
		}
		req := &domain.Requirement{
			InitiativeID:       initiativeID,
			Statement:          e.redact(str(r["statement"])),
			Priority:           domain.RequirementPriority(str(r["priority"])),
			AcceptanceCriteria: acs,
			ProducedByRun:      runID,
		}
		if err := e.db.CreateRequirement(ctx, req); err != nil {
			return err
		}
		if ref := strings.TrimSpace(e.redact(str(r["decision"]))); ref != "" {
			if decisionRefByReqID == nil {
				decisionRefByReqID = map[string]string{}
			}
			decisionRefByReqID[req.ID] = ref
		}
	}
	for _, d := range objSlice(obj["decisions"]) {
		if err := e.createDecisionIfNew(ctx, initiativeID, &domain.Decision{
			InitiativeID:   initiativeID,
			Type:           domain.DecisionProduct,
			Question:       e.redact(str(d["question"])),
			Options:        e.redactSlice(strSlice(d["options"])),
			Recommendation: e.redact(str(d["recommendation"])),
		}); err != nil {
			return err
		}
	}
	// Wire requirement→decision edges for requirements shaped by a decision,
	// matched by the decision's exact question text (normalized), so an open
	// decision's impact ("blocks N") is the set of requirements waiting on it.
	if len(decisionRefByReqID) > 0 {
		decs, err := e.db.ListDecisions(ctx, initiativeID)
		if err != nil {
			return err
		}
		decIDByNorm := make(map[string]string, len(decs))
		for _, dec := range decs {
			decIDByNorm[normalizeJudgment(dec.Question)] = dec.ID
		}
		for reqID, ref := range decisionRefByReqID {
			if decID, ok := decIDByNorm[normalizeJudgment(ref)]; ok {
				if err := e.db.WireRequirementDerivedFromDecision(ctx, initiativeID, reqID, decID); err != nil {
					return err
				}
			} else {
				e.log.Debug("requirement decision reference did not match a raised decision; link skipped (best-effort)", "initiative", initiativeID, "requirement", reqID)
			}
		}
	}
	for _, q := range strSlice(obj["open_questions"]) {
		if err := e.createQuestionIfNew(ctx, initiativeID, e.redact(q)); err != nil {
			return err
		}
	}
	return nil
}

// runReviewLoop runs deterministic checks + adversarial review with bounded
// revise cycles (§11). Rigor (review B1) is calibrated once, from the
// requirement count of the FIRST cycle: a revise cycle is the review asking
// for more of the same PRD, not a new, larger one, so recalibrating each
// cycle could flip level mid-review.
func (e *Engine) runReviewLoop(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	maxCycles := e.cfg.Review.MaxCycles
	var rigor RigorLevel

	for cycle := 1; ; cycle++ {
		reqs, err := e.db.ListRequirements(ctx, initiativeID)
		if err != nil {
			return domain.InitiativeFailed, err
		}
		active := activeRequirements(reqs)

		if rigor == "" {
			scale := projectScaleOf(ctx, e.workspaceRoot)
			rigor = calibrateRigor(RigorLevel(e.cfg.Review.Rigor), scale, changeSize{Requirements: len(active)})
			if rigor == RigorQuick {
				maxCycles = 1
			}
		}

		detFindings := review.DeterministicChecks(initiativeID, active)
		e.setStage(ctx, initiativeID, fmt.Sprintf("Reviewing the PRD (cycle %d)", cycle))
		advFindings, err := e.runAdversarialReview(ctx, initiativeID, active, rigor)
		if err != nil {
			return domain.InitiativeFailed, err
		}
		all := append(detFindings, advFindings...)
		all = capFindings(all, findingCap(rigor), rigor)

		for i := range all {
			all[i].InitiativeID = initiativeID
			if all[i].ReviewRunID == "" {
				all[i].ReviewRunID = "deterministic-check"
			}
			all[i].Finding = e.redact(all[i].Finding)
			all[i].Evidence = e.redact(all[i].Evidence)
			all[i].Impact = e.redact(all[i].Impact)
			all[i].Recommendation = e.redact(all[i].Recommendation)
			if err := e.db.CreateFinding(ctx, &all[i]); err != nil {
				return domain.InitiativeFailed, err
			}
		}

		blocked, reason := review.EvaluateApproval(all)
		if !blocked || rigor == RigorQuick {
			if blocked {
				// Quick converges in one pass and accepts with advisories
				// (review B1): the findings stand, but do not gate approval,
				// and are not falsely dispositioned "fixed" -- nothing was.
				e.log.Info("review passed (quick, advisory)", "cycle", cycle, "findings", len(all))
			} else {
				e.log.Info("review passed", "cycle", cycle, "findings", len(all))
				// Close prior cycles' still-open findings: the review has converged,
				// so their concerns are resolved. Without this, hasOpenBlockingFindings
				// (verify gate) would see stale critical/high findings from earlier
				// cycles and wedge completion (full-platform review: non-monotonic gate).
				if err := e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionFixed, RoleAdversarialReviewer); err != nil {
					return domain.InitiativeFailed, err
				}
			}
			if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeApproved, RoleAdversarialReviewer); err != nil {
				return domain.InitiativeFailed, err
			}
			// The reviewed PRD's requirements are now approved (§17, review B4).
			if err := e.db.ApproveRequirements(ctx, initiativeID, RoleAdversarialReviewer); err != nil {
				return domain.InitiativeFailed, err
			}
			return domain.InitiativeApproved, nil
		}

		e.log.Warn("review blocked", "cycle", cycle, "findings", len(all), "reason", reason)
		if cycle >= maxCycles {
			e.log.Warn("review non-convergence", "cycles", cycle)
			if err := e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPRDReview, RoleOrchestrator); err != nil {
				return domain.InitiativeFailed, err
			}
			return domain.InitiativeNeedsHumanJudgment, nil
		}

		// Revise: supersede old requirements and re-run product definition.
		e.log.Info("revising", "cycle", cycle)
		if err := e.db.SupersedeRequirements(ctx, initiativeID, "review-cycle-"+fmt.Sprint(cycle)); err != nil {
			return domain.InitiativeFailed, err
		}
		if err := e.runProductDefinition(ctx, initiativeID, active); err != nil {
			return domain.InitiativeFailed, err
		}
	}
}

// Resume re-runs the review loop after a human has resolved decisions/findings
// in needs_human_judgment (§11).
//
// The claim is what makes that comment true. Resume took no claim and checked no
// status, so it re-ran the review loop from any state and wrote the result over
// it: called on a `plan_approved` initiative it discarded the approved plan and
// left `approved`, silently undoing M2. Claiming from needs_human_judgment is
// both the guard and the exclusion -- runReviewLoop's terminal transition
// releases it.
func (e *Engine) Resume(ctx context.Context, initiativeID string) (status domain.InitiativeStatus, err error) {
	// The reason is captured before the claim so the compensating release can
	// put it back exactly as it was.
	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	reason := ini.JudgmentReason
	if err := e.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment); err != nil {
		return "", err
	}
	// runReviewLoop returns (failed, err) on every error path without writing
	// the status -- a provider 401 or timeout included, which is the likeliest
	// thing to happen on the endpoint you reach for after a model misbehaved.
	// Resume is the only claiming entry point with no operation row, so a
	// stranded claim here is invisible to reconcileStaleOperations and the
	// initiative answers "already running" to everything until a daemon
	// restart, which then sweeps it to `failed` -- past the point `idea approve`
	// can take it. This is the compensating release that keeps the claim honest.
	//
	// context.WithoutCancel: the caller's context is the HTTP request's, and a
	// client that disconnected is exactly when the release matters most.
	defer func() {
		if err != nil {
			if rerr := e.db.NeedsHumanJudgment(context.WithoutCancel(ctx), initiativeID, reason, RoleOrchestrator); rerr != nil {
				e.log.Error("could not release the claim resume took",
					"initiative", initiativeID, "error", rerr)
			}
		}
	}()
	return e.runReviewLoop(ctx, initiativeID)
}

// Revise applies the human's decisions/answers by re-running product definition
// (with the resolved decisions + answered questions in context), then re-runs
// review. This is the "apply my judgment → updated PRD" loop (§8, §11).
func (e *Engine) Revise(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	e.log.Info("revising after human decisions", "initiative", initiativeID)
	// Capture the current active requirements before superseding so the model
	// improves the immediately preceding set rather than re-reading the full
	// (unbounded) requirement history.
	all, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	prior := activeRequirements(all)
	if err := e.db.SupersedeRequirements(ctx, initiativeID, "human-revise"); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runProductDefinition(ctx, initiativeID, prior); err != nil {
		return domain.InitiativeFailed, err
	}
	return e.runReviewLoop(ctx, initiativeID)
}

// Approve records explicit human approval of the initiative (§11): the reviewed
// PRD is accepted as it stands, findings and all, and the initiative moves on to
// design.
//
// Compare-and-swap, not a read-then-write. This was an unconditional
// SetInitiativeStatus with no check whatsoever, so it overwrote whatever the
// initiative was doing -- including releasing the claim a running `advance` was
// holding, after which a second advance was accepted and two ran concurrently on
// one initiative. Reading the status first would have narrowed that window
// without closing it, which is the same check-then-act ClaimInitiativeRunning
// exists to avoid.
//
// `approved` is a legal source so a repeated approve is a no-op rather than an
// error. Every other status is refused: `running` because work is in flight,
// `plan_approved` and `complete` because approving there walks the state machine
// backwards, and `failed` because the recovery for that is `idea retry`.
func (e *Engine) Approve(ctx context.Context, initiativeID string) error {
	// The reason, not just the status. All four JudgmentReasons share
	// needs_human_judgment, so a status-only gate let `idea approve` -- whose
	// help says "accept the reviewed PRD" and which `idea next` only ever offers
	// for a stalled PRD review -- act on an initiative parked for the plan or
	// for verification. With the disposition below that is not a no-op: it signs
	// off findings the human was never shown, including the `plan_dependency`
	// finding whose wedge advance.go relies on three separate times ("approving
	// it would set plan_approved over a graph that is known to be incomplete").
	//
	// Read-then-CAS is safe here, unlike the claim this function used to skip:
	// the reason can only be rewritten by a producer, every producer runs from
	// `running`, and the CAS below refuses anything that is not still
	// needs_human_judgment or approved. The reason cannot move under us without
	// the status moving first.
	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return err
	}
	// The empty reason is accepted: initiatives parked before it was recorded
	// have none, and refusing would strand them with no CLI route at all.
	switch ini.JudgmentReason {
	case "", domain.JudgmentPRDReview:
	default:
		return fmt.Errorf("approve accepts a PRD waiting on your judgment; this initiative is waiting on %s", ini.JudgmentReason)
	}
	if err := e.db.TransitionInitiative(ctx, initiativeID, domain.InitiativeApproved, store.HumanActor,
		domain.InitiativeNeedsHumanJudgment, domain.InitiativeApproved); err != nil {
		return err
	}
	// "Findings and all" has to mean something. Without this the comment above
	// was the only place it was true: approve moved the status and left every
	// blocking finding undisposed, and hasOpenBlockingFindings (verify.go:263)
	// refuses `complete` while any undisposed critical/high exists. So the one
	// command offered as the way out of a non-converged PRD review handed back
	// an initiative that could reach a change set and never finish -- the
	// README's last step, `idea next` printing "✓ initiative complete", was
	// still unreachable. Nothing in the CLI can disposition a finding, so there
	// was no way to undo it from the CLI either; the disposition endpoint exists
	// but has no client outside the web UI.
	//
	// accepted_risk, not fixed: nothing was fixed. It is also the one
	// disposition the reviewer is forbidden to set ("Never set accepted_risk",
	// engine.go's review policy input), which keeps it unambiguously a human's
	// signature on the risk.
	if err := e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionAcceptedRisk, store.HumanActor); err != nil {
		return err
	}
	return e.db.ApproveRequirements(ctx, initiativeID, store.HumanActor)
}

// DiscussDecision runs one human→AI discussion turn on a decision (§8): the
// human asks for elaboration or proposes an alternative, and the product
// manager replies. Both messages are persisted to the decision's thread.
func (e *Engine) DiscussDecision(ctx context.Context, decisionID, message string) (string, error) {
	dec, err := e.db.GetDecision(ctx, decisionID)
	if err != nil {
		return "", err
	}
	thread, err := e.db.ListDecisionDiscussions(ctx, decisionID)
	if err != nil {
		return "", err
	}
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "You are discussing a product decision with the human. Explain the tradeoffs and reasoning behind your recommendation. If the human proposes an alternative, evaluate it fairly and state which you now prefer and why. Be concise and concrete."},
		{Selector: "decision", Content: renderDecisionForDiscussion(dec)},
		{Selector: "discussion", Content: renderDecisionDiscussion(thread)},
	}
	_, obj, err := e.runModel(ctx, dec.InitiativeID, "decision_discussion", RoleProductManager, "discussion", schema.Discussion, inputs, "Human: "+message)
	if err != nil {
		return "", err
	}
	response := strings.TrimSpace(str(obj["response"]))

	if err := e.db.CreateDecisionDiscussion(ctx, &domain.DecisionDiscussion{DecisionID: decisionID, Author: store.HumanActor, Message: e.redact(message)}); err != nil {
		return "", err
	}
	if response != "" {
		if err := e.db.CreateDecisionDiscussion(ctx, &domain.DecisionDiscussion{DecisionID: decisionID, Author: RoleProductManager, Message: e.redact(response)}); err != nil {
			return "", err
		}
	}
	return response, nil
}

func (e *Engine) runAdversarialReview(ctx context.Context, initiativeID string, reqs []domain.Requirement, rigor RigorLevel) ([]domain.Finding, error) {
	scale := projectScaleOf(ctx, e.workspaceRoot)
	conventions := projectConventions(e.workspaceRoot)
	runID, obj, err := e.runModel(ctx, initiativeID, StageReview, RoleAdversarialReviewer, "review", schema.Review,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Independently challenge the PRD. Emit structured findings. Never set accepted_risk."},
			{Selector: "project_scale", Content: scaleAndConventions(scale, changeSize{Requirements: len(reqs)}, rigor, conventions)},
			{Selector: "requirements", Content: renderRequirements(reqs)},
		},
		"Adversarial review of the PRD requirements.",
	)
	if err != nil {
		return nil, err
	}
	return review.ParseFindings(runID, "prd", initiativeID, obj)
}

// ---------- helpers ----------

func roleSystemPrompt(name string) string {
	rc, err := roles.Load(name)
	if err != nil {
		return "You are a Staff-level " + name + "."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are the %s role (version %s).\n", rc.Name, rc.Version)
	b.WriteString("Competencies:\n")
	for _, c := range rc.Competencies {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	b.WriteString("Required behaviors:\n")
	for _, c := range rc.Behaviors {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return b.String()
}

func activeRequirements(reqs []domain.Requirement) []domain.Requirement {
	out := make([]domain.Requirement, 0, len(reqs))
	for _, r := range reqs {
		if r.Status != domain.RequirementRemoved && r.SupersededBy == "" {
			out = append(out, r)
		}
	}
	return out
}

// activeDecisions filters out superseded decisions (review A2: "superseded
// requirements and superseded decisions are excluded from the inputs to
// design, plan, revise and review" -- decisions are open | decided |
// superseded; there is no "rejected"). Mirrors activeRequirements so the M2
// design/plan inputs and activeRequirements share one filtering idiom.
func activeDecisions(decs []domain.Decision) []domain.Decision {
	out := make([]domain.Decision, 0, len(decs))
	for _, d := range decs {
		if d.Status != domain.DecisionSuperseded {
			out = append(out, d)
		}
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func objSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// contextDocsMaxPerDoc caps a single context document's contribution so one
// oversized upload cannot crowd out the rest of the prompt (§48 input budget).
const contextDocsMaxPerDoc = 32000

// contextDocsMaxTotal caps the combined context-doc block.
const contextDocsMaxTotal = 100000

// truncateRunesForContext truncates s to at most n runes, appending an ellipsis
// when it was cut. Unlike truncateOutput it is rune-safe, so multibyte content
// is never split mid-rune.
func truncateRunesForContext(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "\n…[truncated]"
}

// contextDocs assembles the initiative's uploaded context documents as a single
// context block. Text docs contribute their (already-redacted) content; image
// docs contribute their vision-generated description (already-redacted text).
// Per-doc and total caps bound the prompt contribution, and failures are logged
// rather than silently dropping a document. Returns "" when there are no
// documents (or only undescribed images).
func (e *Engine) contextDocs(ctx context.Context, initiativeID string) string {
	docs, err := e.db.ListContextDocs(ctx, initiativeID)
	if err != nil {
		e.log.Warn("context docs unavailable", "initiative", initiativeID, "err", err)
		return ""
	}
	var b strings.Builder
	for _, doc := range docs {
		digest := doc.Digest
		label := doc.Kind
		if doc.Format == "image" {
			if doc.DescriptionDigest == "" {
				e.log.Warn("image context doc not yet described", "initiative", initiativeID, "doc", doc.ID, "title", doc.Title)
				continue
			}
			digest = doc.DescriptionDigest
			label = doc.Kind + " (image)"
		}
		content, err := e.blobs.Get(digest)
		if err != nil {
			e.log.Warn("context doc blob missing", "initiative", initiativeID, "doc", doc.ID, "digest", digest, "err", err)
			continue
		}
		body := string(content)
		if len([]rune(body)) > contextDocsMaxPerDoc {
			body = truncateRunesForContext(body, contextDocsMaxPerDoc)
		}
		fmt.Fprintf(&b, "## Context document: %s (kind: %s)\n\n%s\n", doc.Title, label, body)
		if b.Len() >= contextDocsMaxTotal {
			break
		}
	}
	out := b.String()
	return truncateRunesForContext(out, contextDocsMaxTotal)
}

// DescribeImages describes every image context doc that lacks a description,
// using a vision-capable model. Each description is stored (redacted) and its
// digest recorded so contextDocs can inject it. Failures are logged per-doc and
// do not block the remaining docs.
func (e *Engine) DescribeImages(ctx context.Context, initiativeID string) error {
	// Single-flight: concurrent uploads each spawn a describe goroutine, but only
	// one full scan runs at a time; the second re-scan sees description_digest
	// already set and is a no-op (no duplicate/amplified model calls).
	e.describeMu.Lock()
	defer e.describeMu.Unlock()

	docs, err := e.db.ListContextDocs(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Format != "image" || doc.DescriptionDigest != "" {
			continue
		}
		if err := e.describeImage(ctx, doc); err != nil {
			e.log.Warn("image description failed", "initiative", initiativeID, "doc", doc.ID, "err", err)
			continue
		}
	}
	return nil
}

// describeImage runs one vision-model call over an image doc's bytes and stores
// the returned description. It snapshots the reloadable engine components under
// e.mu (Reload swaps them under the same lock), so the read path cannot race a
// config reload.
func (e *Engine) describeImage(ctx context.Context, doc domain.ContextDoc) error {
	e.mu.Lock()
	spec := e.resolveModelSpec("vision")
	registry, redactor, blobs, rawBlobs := e.registry, e.redactor, e.blobs, e.rawBlobs
	e.mu.Unlock()

	if rawBlobs == nil {
		return fmt.Errorf("image store not configured")
	}
	image, err := rawBlobs.Get(doc.Digest)
	if err != nil {
		return fmt.Errorf("image blob: %w", err)
	}
	mediaType := docparse.ImageMediaType(image)
	if mediaType == "" {
		return fmt.Errorf("image doc %s is not a recognized image", doc.ID)
	}
	prov, modelName, err := registry.Resolve(spec)
	if err != nil {
		return err
	}
	req := model.Request{
		Model:    modelName,
		System:   "You are a product analyst. Describe this UI mockup or wireframe precisely and concretely: overall layout, screens/sections, key elements and controls, user flows, and any visible annotations or labels. Output plain text, no markdown fences.",
		Messages: []model.Message{{Role: "user", Content: "Describe this mockup for the design team."}},
		Images:   []model.Image{{Data: image, MediaType: mediaType}},
		// Under every declared ceiling today; routed through the cap anyway so
		// this call site cannot drift into the defect the other one had.
		MaxOutput: minOutput(2048, prov),
	}
	// A real run + manifest (M5, review fix: "describeImage model calls are
	// never costed"): this call used to charge the shared budget with no run
	// of its own, so its spend never appeared under GET .../costs at all.
	// Best-effort -- a failure to create the tracking row must never sink
	// the description itself, so runID stays "" and the writes below no-op.
	runID, runErr := e.db.CreateRun(ctx, domain.RunModel, "", "vision", doc.InitiativeID, "describe_image")
	setRunStatus := func(status domain.RunStatus) {
		if runErr == nil {
			_ = e.db.SetRunStatus(ctx, runID, status, "vision")
		}
	}
	reqStart := time.Now()
	resp, err := prov.Complete(ctx, req)
	if err != nil {
		setRunStatus(domain.RunFailed)
		return err
	}
	if err := e.chargeUSD(resp.Usage.CostUSD); err != nil {
		setRunStatus(domain.RunFailed)
		return err
	}
	if runErr == nil {
		_ = e.db.CreateRunManifest(ctx, &domain.RunManifest{
			RunID: runID, RequestedModel: spec, ActualModel: resp.ActualModel,
			Usage: domain.RunUsage{
				InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
				CostUSD: resp.Usage.CostUSD, DurationMS: time.Since(reqStart).Milliseconds(),
			},
			StartedAt: reqStart, FinishedAt: time.Now().UTC(),
		})
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		setRunStatus(domain.RunFailed)
		return fmt.Errorf("empty image description")
	}
	if redactor != nil {
		text = redactor.RedactString(text)
	}
	digest, err := blobs.Put([]byte(text))
	if err != nil {
		setRunStatus(domain.RunFailed)
		return fmt.Errorf("store image description: %w", err)
	}
	setRunStatus(domain.RunSucceeded)
	return e.db.SetContextDocDescription(ctx, doc.ID, digest)
}

// wireDocConsumesIfAny records run --consumes--> context_doc edges for any run
// whose inputs include the context_docs selector with non-empty content. It is
// called from runModel after the run row exists, so provenance is recorded even
// if the model call later fails. Failures are logged, not fatal: provenance
// edges must never sink a workflow run.
//
// Attribution is coarse by design: a run is wired to every initiative doc that
// was injected, even if the per-doc/total truncation caps (contextDocs) later
// dropped some of a doc's tail from the assembled prompt. This is intentional —
// the doc still informed the run, and precise sentence-level attribution is a
// stretch goal, not v1.
func (e *Engine) wireDocConsumesIfAny(ctx context.Context, initiativeID, runID string, inputs []ccontext.Input) {
	has := false
	for _, in := range inputs {
		if in.Selector == "context_docs" && strings.TrimSpace(in.Content) != "" {
			has = true
			break
		}
	}
	if !has {
		return
	}
	docs, err := e.db.ListContextDocs(ctx, initiativeID)
	if err != nil {
		e.log.Warn("context-doc consumes wiring: list failed", "initiative", initiativeID, "run", runID, "err", err)
		return
	}
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	if err := e.db.WireDocConsumes(ctx, initiativeID, runID, ids); err != nil {
		e.log.Warn("context-doc consumes wiring: write failed", "initiative", initiativeID, "run", runID, "err", err)
	}
}

// researchEvidence fetches the configured research seed URLs (public addresses
// only) and returns their extracted text as untrusted context (§46). Each
// fetched body is stored content-addressed as evidence. With network disabled
// or no URLs configured it returns empty, and the researcher marks everything
// as an assumption (offline path).
func (e *Engine) researchEvidence(ctx context.Context) string {
	if e.cfg.Network != config.NetworkEnabled || len(e.cfg.Research.URLs) == 0 {
		return ""
	}
	f := tools.NewFetcher()
	var b strings.Builder
	for _, url := range e.cfg.Research.URLs {
		content, err := f.Fetch(ctx, url)
		if err != nil {
			continue
		}
		e.storeBlob(content) // evidence, content-addressed (§31)
		b.WriteString("SOURCE: " + url + "\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	return b.String()
}
