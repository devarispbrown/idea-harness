// Package workflow implements the deterministic M1 stage graph and the
// orchestrator that drives it (§6, §11). The orchestrator is constrained by
// workflow policy: it runs stages in order and may only rank/explain next moves
// within that policy.
package workflow

import (
	"context"
	"errors"
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
	db *store.DB
	// blobs, redactor, registry, cfg, dotEnv are swapped as a group by
	// Reload (review H4) and are guarded by cfgMu, not by mu: Execute/
	// ApplyChangeSet/RevertChangeSet hold mu for their whole duration and so
	// are already excluded from a concurrent Reload, but Run, Advance,
	// Retry, RevisePlan, Resume, Revise, ReopenPhase and friends do not hold
	// mu (mu is scoped to the state-mutating trio above, not every entry
	// point) and read these fields throughout a long, multi-model-call
	// pipeline. Proven by the race detector: a Reload racing
	// handleListExecutors/storeBlob/runReviewLoop. Read them only through
	// snapshotConfig, never directly, outside of Reload itself.
	cfgMu    sync.RWMutex
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
	// keepTaskCodes (also guarded by noteMu) is RevisePlan's title->code map
	// for the initiative's tasks before it deleted them, so a regenerated
	// task that kept its title keeps its code (advance.go).
	keepTaskCodes map[string]map[string]string

	// remoteMu guards remote, the supervisor of this project's runs on a
	// remote execution plane (openhands). Not mu: a watcher follows a run
	// for as long as it lasts and must never wait on, or hold, the lock
	// Execute holds (remote.go).
	remoteMu sync.Mutex
	remote   *executor.RemoteSupervisor

	// qualitySandbox runs verify's sandboxed quality commands (quality.go).
	// Nil means the real Docker sandbox; tests script it.
	qualitySandbox QualitySandbox

	// metricSandbox and metricFetcher are how `idea metric measure` runs a
	// command source and fetches a url source (measure.go). nil means the
	// real no-network container sandbox and the SSRF-guarded fetcher; tests
	// substitute both.
	metricSandbox func(image string) sandboxRunner
	metricFetcher jsonFetcher
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
	// The running agent run's event stream narrates the stages after the
	// agent (checks, review) too.
	e.emitStage(initiativeID, stage)
}

// redact applies the engine's known-values redactor to a free-text string at the
// write boundary, so a secret that reached the model in any context is not
// persisted/re-served in structured columns (full-platform review: secret-catalog
// completion). Nil-safe: without a redactor it returns s unchanged.
func (e *Engine) redact(s string) string {
	if s == "" {
		return s
	}
	e.cfgMu.RLock()
	redactor := e.redactor
	e.cfgMu.RUnlock()
	if redactor == nil {
		return s
	}
	return redactor.RedactString(s)
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
	cfg.Review.MaxCycles = cfg.Review.EffectiveMaxCycles()
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
// engine's mutex continues to serialize execute/apply/revert across a reload.
// e.mu excludes Execute/ApplyChangeSet/RevertChangeSet, which hold it for
// their whole duration; cfgMu (review H4) is what actually guards the field
// writes below against every OTHER entry point (Run, Advance, Retry,
// RevisePlan, Resume, Revise, ReopenPhase, ...), none of which hold e.mu --
// the doc comment this replaces claimed "ALL entry points now hold [e.mu]",
// which was false, and go test -race proved it.
func (e *Engine) Reload(cfg config.HarnessConfig, registry *model.Registry, redactor *security.Redactor, blobs *blob.Store, dotEnv map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg.Review.MaxCycles = cfg.Review.EffectiveMaxCycles()
	e.cfgMu.Lock()
	// Swapped under the same lock as cfg, so a reader of the snapshot never
	// pairs a new config with the old .env (or vice versa).
	e.dotEnv = dotEnv
	e.cfg = cfg
	e.registry = registry
	e.redactor = redactor
	e.blobs = blobs
	e.cfgMu.Unlock()
	e.costMu.Lock()
	e.budgetUSD = cfg.Review.MaxCostUSD
	e.costMu.Unlock()
}

// engineConfig is a consistent, point-in-time copy of the config-derived
// fields Reload swaps as a group (review H4). Every entry point that does
// not hold mu for its whole duration -- Run, Advance, Retry, RevisePlan,
// Resume, Revise, ReopenPhase, ApprovePlan, ConvergeDecisions,
// SuggestAnswers, DiscussDecision, PromoteDocComment, DescribeImages, and
// their helpers -- reads through snapshotConfig instead of the raw fields,
// so a concurrent Reload is either fully visible or not at all to that read,
// never a torn mix of an old redactor with a new cfg.
type engineConfig struct {
	cfg      config.HarnessConfig
	registry *model.Registry
	redactor *security.Redactor
	blobs    *blob.Store
	dotEnv   map[string]string
}

func (e *Engine) snapshotConfig() engineConfig {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return engineConfig{cfg: e.cfg, registry: e.registry, redactor: e.redactor, blobs: e.blobs, dotEnv: e.dotEnv}
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
	if err := e.runProductDefinition(ctx, initiativeID, nil, ""); err != nil {
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
//
// reviewMaxTokens is harness.yaml's review.max_tokens (config.ReviewConfig.
// MaxTokens), and this is the fix for the second layer of the change-set
// review truncation defect: before this, that field was read in exactly ONE
// place -- a post-hoc check, after the model already answered, that FAILED a
// stage whose output exceeded it (still true, see runModel) -- and nothing
// ever raised the number actually SENT to the provider. A human hitting
// model.ErrTruncated was told to "raise the stage budget", and
// review.max_tokens -- the one field in harness.yaml that sounds like it
// does that -- silently did nothing: docs/configuration.md already
// documented it as "a per-call cap", and it was, but only downward, never
// up. An explicit review.max_tokens now overrides the stage's built-in want
// directly (not just as a floor or a ceiling), still bounded below by the
// model's real output ceiling, so raising it is the reachable fix the error
// message can honestly point to.
//
// modelName is the resolved model (registry.Resolve's second return, e.g.
// "claude-sonnet-5"), threaded through to maxOutputForStage so the review
// stage's default want (see reviewDefaultMaxOutput) can be sized off the
// model actually in play rather than a flat constant. It affects only that
// default; an explicit reviewMaxTokens above still wins outright.
func effectiveMaxOutput(stageKey string, prov model.Provider, modelName string, reviewMaxTokens int64) int {
	want := maxOutputForStage(stageKey, modelName)
	if reviewMaxTokens > 0 {
		want = int(reviewMaxTokens)
	}
	return minOutput(want, prov)
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
//
// modelName is used only by the review branch (see reviewDefaultMaxOutput);
// every other stage's want is a flat constant, unaffected by it.
func maxOutputForStage(stageKey, modelName string) int {
	switch stageKey {
	case StageProductDefinition:
		return 32000
	case StageReview, StageExecutionPlanReview:
		// The adversarial reviewer can emit many findings (15+); a truncated
		// findings list silently fails schema validation.
		return reviewDefaultMaxOutput(modelName)
	case StageResearch, StageArchitecture, StageProductDesign,
		StageVerificationDesign, StageConvergence, StageExecutionPlan:
		return 8192
	case "suggest_answers":
		return 8192
	default:
		return 4096
	}
}

// reviewDefaultMaxOutput is the review stage's default completion-token ask --
// used whenever harness.yaml's review.max_tokens is unset (0) -- and this is
// the fix for D2 of the 2026-09-21 truncation probe: before this, the default
// was the flat constant 16384 regardless of model, so the exact failure the
// probe reproduced (a claude-sonnet-5 change-set review truncated at 16384,
// real ceiling 128000) recurred for every review call until a human set
// review.max_tokens by hand in harness.yaml. Layer (a) of the earlier fix
// (model.MaxOutputFor / capOutput) only helps once a caller asks for more
// than 16384 in the first place; this is what actually asks.
//
// The default is derived from the model's own verified output ceiling
// (model.MaxOutputFor), not a second flat number, but deliberately not raised
// to the full ceiling: reviewDefaultCeiling (32000) matches
// maxOutputForStage's own PRD want, the largest of the other stage defaults,
// as a reasoned "this is a big structured reply" midpoint -- asking for the
// full 128000/64000/65536 a model might allow on every single review call
// would be needlessly slow and expensive for the common case, where the
// review schema's findings list does not come close to needing it.
// review.max_tokens remains the documented, reachable override for a review
// that legitimately needs more (the probe's own real-world case needed
// 40000, which review.max_tokens already reaches).
//
// reviewDefaultFloor (16384) is the old flat constant, kept as the floor for
// two cases: a model this catalog has not verified (MaxOutputFor's ok=false,
// e.g. a brand-new model that postdates modelMaxOutput), and modelMaxOutput's
// own smallest verified ceiling, 16384 (gpt-4o / gpt-4o-mini) -- neither
// regresses below what shipped before this fix.
const (
	reviewDefaultFloor   = 16384
	reviewDefaultCeiling = 32000
)

func reviewDefaultMaxOutput(modelName string) int {
	ceiling, ok := model.MaxOutputFor(modelName)
	if !ok || ceiling <= reviewDefaultFloor {
		return reviewDefaultFloor
	}
	if ceiling > reviewDefaultCeiling {
		return reviewDefaultCeiling
	}
	return ceiling
}

// wrapTruncationGuidance adds reachable remediation to a model.ErrTruncated
// once runModel's own retry-with-a-smaller-ask has been exhausted; every
// other error passes through unchanged.
//
// model.truncatedError (internal/model/httperr.go) deliberately names only
// the fact (which model, what budget was hit) and not a fix: that package has
// no idea whether harness.yaml is even in play. This is the layer that does
// know, so this is where the honest, reachable suggestion belongs -- the
// defect this closes is exactly a suggestion that was NOT reachable ("raise
// the stage budget" pointed at review.max_tokens, which effectiveMaxOutput
// now actually reads; see its own doc comment).
func wrapTruncationGuidance(err error) error {
	if !errors.Is(err, model.ErrTruncated) {
		return err
	}
	return fmt.Errorf("%w; raise `review.max_tokens` in harness.yaml to ask for a larger reply and retry this stage, "+
		"or reduce the size of what is being sent to it", err)
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

	// One snapshot for the whole call (review H4): a model spec resolved
	// against one registry generation must be Resolve()d against that SAME
	// generation, and the max_tokens ceiling enforced below must be the one
	// that generation configured, not whatever Reload has swapped in by the
	// time the response comes back.
	snap := e.snapshotConfig()
	bundle := ccontext.Compile(inputs)
	modelSpec := e.modelSpecFor(initiativeID, roleName)
	prov, modelName, err := snap.registry.Resolve(modelSpec)
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
		MaxOutput:      effectiveMaxOutput(stageKey, prov, modelName, snap.cfg.Review.MaxTokens),
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
		// The spending cap is checked before every billed attempt, retries
		// included: a retry costs what the first try did (budget.go).
		if err := e.checkBudget(ctx, initiativeID, "the "+stageKey+" model call"); err != nil {
			failureManifest()
			e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
			return runID, nil, err
		}
		resp, err = prov.Complete(ctx, req)
		if err != nil {
			if attempt < maxAttempts && isTransientModelError(err) {
				e.log.Warn("transient model error, retrying", "stage", stageKey, "role", roleName, "model", modelSpec, "attempt", attempt, "err", err)
				time.Sleep(time.Duration(attempt) * 1500 * time.Millisecond)
				continue
			}
			// No wedge, no silent truncation (the change-set adversarial
			// review defect's third layer): truncation is not transient --
			// the same request truncates the same way every time -- so
			// retrying it unchanged would just repeat the failure. Instead,
			// retry ONCE with a smaller ask: the reply must fit inside the
			// same budget, so ask for less of it. A genuinely exhausted
			// retry still surfaces model.ErrTruncated to the caller; for the
			// change-set review, that is handled as an explicit, recorded
			// "review unavailable" state rather than a hard failure (see
			// ErrReviewUnavailable in execute.go), which is what keeps this
			// from wedging `idea execute` behind a guard with no way
			// forward.
			if attempt < maxAttempts && errors.Is(err, model.ErrTruncated) {
				e.log.Warn("model output truncated, retrying with a smaller ask", "stage", stageKey, "role", roleName, "model", modelSpec, "attempt", attempt, "err", err)
				req.System += "\n\nYour previous reply was cut off because it exceeded the output budget. " +
					"This time respond with SIGNIFICANTLY less so the full reply fits well within the budget: " +
					"for a findings list, return only the 5 highest-severity findings; for any other content, " +
					"be much more concise throughout."
				continue
			}
			e.log.Error("model request failed", "stage", stageKey, "role", roleName, "model", modelSpec, "err", err, "attempt", attempt, "duration", time.Since(reqStart).Round(time.Millisecond))
			failureManifest()
			e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
			return runID, nil, fmt.Errorf("workflow: %s: %w", stageKey, wrapTruncationGuidance(err))
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

	// Enforce the review token ceiling as a hard stop (review M4). snap, not
	// e.cfg: the ceiling this run is held to is the one in force when it
	// started (review H4).
	if snap.cfg.Review.MaxTokens > 0 && resp.Usage.OutputTokens > snap.cfg.Review.MaxTokens {
		failureManifest()
		e.db.SetRunStatus(ctx, runID, domain.RunFailed, roleName)
		return runID, nil, fmt.Errorf("workflow: %s exceeded max_tokens (%d > %d)", stageKey, resp.Usage.OutputTokens, snap.cfg.Review.MaxTokens)
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
	e.cfgMu.RLock()
	blobs := e.blobs
	e.cfgMu.RUnlock()
	d, err := blobs.Put([]byte(content))
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
	snap := e.snapshotConfig()
	return resolveModelSpecFor(snap.cfg, snap.registry, roleName)
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
	snap := e.snapshotConfig()
	return ImplementerModelSpec(snap.cfg, snap.registry)
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

	// review H6: completedPhases keys off ArtifactIdeaBrief's mere existence
	// (CreateArtifact just above, already committed) to decide whether Retry
	// resumes past framing. The derived rows below are just as much a part
	// of "framing completed" as the artifact is -- a Stop between two of
	// them left the artifact durable and the assertions/questions
	// truncated, and Retry then skipped framing (and its cost) entirely,
	// building research/PRD from a half-recorded brief with no error.
	// context.WithoutCancel: the model call above already returned and was
	// billed; a cancellation from here on stops the NEXT phase from
	// starting, not this one from finishing recording what it already paid
	// for.
	dctx := context.WithoutCancel(ctx)
	// Record assumptions as assertions.
	for _, a := range strSlice(obj["assumptions"]) {
		if err := e.db.CreateAssertion(dctx, &domain.Assertion{
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
		if err := e.createQuestionIfNew(dctx, initiativeID, e.redact(q)); err != nil {
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
//
// Returns the id of the decision this call resolved to (review M1): the
// EXISTING, merged-into decision's id when a near-duplicate was found, or
// the newly-created decision's id otherwise -- "" only when d.Question was
// empty and nothing happened. Callers that need to wire something to "the
// decision this call just represents" (a requirement's derived_from edge)
// must use this return value, not re-look-up the decision by d's own
// (possibly since-merged-away) wording afterward: that was exactly the M1
// bug -- the requirement's own citation (`ref`) names this cycle's exact
// wording, but the DB row a near-duplicate merges into keeps the EARLIER
// wording, so re-fetching decisions and matching by normalized text after
// the fact never found it.
func (e *Engine) createDecisionIfNew(ctx context.Context, initiativeID string, d *domain.Decision) (string, error) {
	if strings.TrimSpace(d.Question) == "" {
		return "", nil
	}
	norm := normalizeJudgment(d.Question)
	existing, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	for _, x := range existing {
		// ICP re-run #2: similarDecision also catches a same-subject decision
		// re-asked with more wording drift than similarJudgment's pure-text
		// threshold tolerates, PROVIDED it offers materially the same
		// options -- see similarDecision's doc for the false-merge guard.
		if x.Type == d.Type && (normalizeJudgment(x.Question) == norm || similarDecision(x.Question, d.Question, x.Options, d.Options)) {
			return x.ID, nil
		}
	}
	if err := e.db.CreateDecision(ctx, d); err != nil {
		return "", err
	}
	// Resolve any open question this decision now covers. M2: matched the
	// same way createQuestionIfNew matches a decision against a question --
	// exact normalized text OR the same Jaccard-similarity test -- rather
	// than exact text only, which left a near-duplicate open question
	// unresolved by a decision that (by createDecisionIfNew's own merge
	// rule, above) is considered the same judgment.
	questions, err := e.db.ListQuestions(ctx, initiativeID)
	if err != nil {
		return d.ID, err
	}
	for _, q := range questions {
		if q.Status != domain.QuestionOpen {
			continue
		}
		qn := normalizeJudgment(q.Question)
		if qn == norm || similarJudgment(qn, norm) {
			digest := e.storeBlob("covered by decision " + d.Code)
			_ = e.db.AnswerQuestion(ctx, q.ID, digest, store.ActorOrchestrator)
			_ = e.db.WireDecisionDerivedFromQuestion(ctx, initiativeID, d.ID, q.ID)
		}
	}
	return d.ID, nil
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

	// review H6: see the identical comment in runFraming. completedPhases
	// treats ArtifactResearchBrief's existence as "research completed";
	// these assertions are part of that same fact and must not be truncated
	// by a cancellation the model call already survived.
	dctx := context.WithoutCancel(ctx)

	// Offline/disabled network: findings must be assumptions, never facts.
	// snapshotConfig, not e.cfg directly (review H4): runResearch is an
	// entry point Run reaches without holding mu.
	offline := e.snapshotConfig().cfg.Network == config.NetworkDisabled
	for _, f := range objSlice(obj["findings"]) {
		ftype := domain.AssertionType(str(f["type"]))
		if offline && ftype == domain.AssertionFact {
			ftype = domain.AssertionAssumption
		}
		if err := e.db.CreateAssertion(dctx, &domain.Assertion{
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
//
// reviseReason is "" for a from-scratch authoring (the initial PRD, or a
// retry that has already unconditionally superseded and wants a clean
// rewrite) and non-empty for a revision that should keep requirement codes
// STABLE (review ICP re-run #1): "review-cycle-N" from the adversarial
// review loop, "human-revise" from Revise(). When non-empty, a response
// requirement whose `code` names one of priorReqs is UPDATED IN PLACE (same
// id, same code -- see store.UpdateRequirement) instead of superseded and
// recreated, so its graph edges (derived_from a decision, implements from a
// task, produced_by earlier runs) survive the revision. Only the priorReqs
// NOT referenced by this cycle's response are superseded afterward. A prior
// re-run of the whole set on every cycle is exactly what churned REQ codes
// on every revise cycle and made requirement history unreadable.
func (e *Engine) runProductDefinition(ctx context.Context, initiativeID string, priorReqs []domain.Requirement, reviseReason string) error {
	_, err := e.runProductDefinitionRevising(ctx, initiativeID, priorReqs, reviseReason, nil)
	return err
}

// runProductDefinitionRevising is runProductDefinition with an optional
// review-driven revision brief (rev; see prdRevision), returning the PRD
// author's review_responses keyed by finding label. With rev nil it is
// exactly runProductDefinition, and the responses are empty.
func (e *Engine) runProductDefinitionRevising(ctx context.Context, initiativeID string, priorReqs []domain.Requirement, reviseReason string, rev *prdRevision) (map[string]string, error) {
	reqs := priorReqs
	if reqs == nil {
		all, _ := e.db.ListRequirements(ctx, initiativeID)
		reqs = activeRequirements(all)
	}
	decisions, _ := e.db.ListDecisions(ctx, initiativeID)
	findings, _ := e.db.ListFindingsByInitiative(ctx, initiativeID)
	questions, _ := e.db.ListQuestions(ctx, initiativeID)
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	liveObjs, liveMetrics := e.liveOutcomes(ctx, initiativeID)

	scale := projectScaleOf(ctx, e.workspaceRoot)
	// Calibrated from the idea's own size and the project's scale (ICP
	// re-run #1) -- NEVER from a requirement count this very step produces,
	// which is exactly the circularity that made `auto` unable to select
	// `quick` for the PRD no matter how small the idea.
	//
	// snapshotConfig, not e.cfg directly (review H4): runProductDefinition is
	// an entry point Run/Revise reach without holding mu.
	rigor := calibrateRigor(RigorLevel(e.snapshotConfig().cfg.Review.Rigor), scale, changeSize{IdeaWords: ideaWordCount(ini.Description)})

	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Author a PRD with verifiable requirements and measurable acceptance criteria (metric + target + condition). Raise a decision only for a genuinely open, undecided choice; do not re-list decisions already marked DECIDED, and do not restate an open question as a decision. Do not re-raise a question that already exists (open or answered); only add genuinely new questions. Any review finding dispositioned 'mitigate' must be addressed directly: incorporate the mitigation into the requirements. When a requirement is shaped by one of the decisions you raise, set the requirement's 'decision' field to that decision's exact question text. When revising, if a requirement continues one already listed under Requirements below, set its 'code' field to that requirement's exact existing code (e.g. \"REQ-4\") so it is updated rather than duplicated; leave 'code' empty for a genuinely new requirement. State the product objectives this work exists to move -- outcomes for users or the business, not features -- each with at least one measurable metric (direction, target, a baseline only when known, an optional guardrail and cadence), and list in every requirement's 'objectives' field the objective(s) it serves; every requirement must serve at least one. When revising, keep each continuing objective's and metric's existing code (OBJ-n, MET-n) as its key."},
		{Selector: "project_scale", Content: proportionateContext(scale, detectStack(e.workspaceRoot), projectConventions(e.workspaceRoot)) + requirementCountGuidance(rigor)},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "objectives", Content: renderObjectives(liveObjs, liveMetrics, reqs)},
		{Selector: "decisions", Content: renderDecisions(decisions)},
		{Selector: "questions", Content: e.renderQuestionsWithAnswers(questions)},
		{Selector: "findings", Content: renderFindings(findings)},
	}
	if rev != nil {
		// The precise brief (convergence.go root cause 4): the "findings"
		// input above is every finding the initiative ever had, severity and
		// text only, and the policy only obliges acting on a "mitigate"
		// disposition no in-loop finding carries. This is the list that
		// blocked THIS cycle, with evidence and recommendation, by label.
		inputs = append(inputs, ccontext.Input{Selector: "review_findings", Content: rev.findings})
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}

	e.setStage(ctx, initiativeID, "Writing the PRD")
	runID, obj, err := e.runModel(ctx, initiativeID, StageProductDefinition, RoleProductManager, "prd", schema.PRD, inputs, "Author the PRD.")
	if err != nil {
		return nil, err
	}
	responses := reviewResponses(obj)

	// review H6: completedPhases keys Retry's "PRD completed" off
	// ArtifactPRD's existence alone -- committed at the end of this function,
	// after the objectives are recorded so its body names them by their
	// stored codes -- but the requirements/decisions/questions below are
	// what that fact actually means downstream (review/architecture/plan are all built from the
	// requirement set, not the artifact body). Stop after 6 of 20
	// CreateRequirement calls used to leave the artifact durable and the
	// requirement set truncated with no error; Retry then saw "PRD done"
	// and skipped runProductDefinition entirely, building everything after
	// it from that truncated set, silently, at real cost. WithoutCancel:
	// the model call above already returned and was billed; from here on a
	// cancellation should stop the NEXT phase, not truncate this one's
	// already-earned result.
	dctx := context.WithoutCancel(ctx)

	// priorByCode indexes THIS cycle's starting requirements by normalized
	// code, so a revision response can update one in place instead of
	// superseding and recreating it (review ICP re-run #1). Only consulted
	// when reviseReason is set: an initial authoring (or a retry, which
	// already superseded everything itself) has nothing to update in place.
	var priorByCode map[string]domain.Requirement
	if reviseReason != "" {
		priorByCode = make(map[string]domain.Requirement, len(reqs))
		for _, r := range reqs {
			if r.Code != "" {
				priorByCode[NormalizeRequirementRef(r.Code)] = r
			}
		}
	}
	keptIDs := make([]string, 0, len(reqs))
	// usedCodes tracks every existing code this cycle's response has already
	// consumed (review C5): a response naming the same code twice used to
	// silently last-writer-wins -- the first UpdateRequirement's work is
	// discarded with no duplicate finding, no error, and no trace of the
	// dropped requirement. The second occurrence now refuses instead of
	// silently overwriting the first.
	usedCodes := map[string]bool{}

	var decisionRefByReqID map[string]string // requirementID -> decision question text
	objectiveRefs := map[string][]string{}   // requirementID -> objective keys/codes it serves
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
		code := NormalizeRequirementRef(str(r["code"]))
		if code != "" {
			if usedCodes[code] {
				return nil, fmt.Errorf("workflow: revision response names requirement code %q more than once", str(r["code"]))
			}
			usedCodes[code] = true
		}
		if prior, ok := priorByCode[code]; ok && code != "" {
			req.ID = prior.ID
			req.Code = prior.Code
			if err := e.db.UpdateRequirement(dctx, req); err != nil {
				return nil, err
			}
			// Consume the match (review C5): once this cycle's response has
			// updated a prior requirement in place, that code is spent -- a
			// later occurrence of the SAME code in this same response must
			// hit the usedCodes refusal above, never be treated as a second,
			// fresh match against the same prior row.
			delete(priorByCode, code)
		} else {
			if err := e.db.CreateRequirement(dctx, req); err != nil {
				return nil, err
			}
		}
		// Every requirement THIS cycle produced must survive the
		// supersede-except below, whether it was updated in place or
		// created brand new -- only a PRIOR requirement this cycle's
		// response does not continue at all is dropped.
		keptIDs = append(keptIDs, req.ID)
		objectiveRefs[req.ID] = strSlice(r["objectives"])
		if ref := strings.TrimSpace(e.redact(str(r["decision"]))); ref != "" {
			if decisionRefByReqID == nil {
				decisionRefByReqID = map[string]string{}
			}
			decisionRefByReqID[req.ID] = ref
		}
	}
	if reviseReason != "" {
		if err := e.db.SupersedeRequirementsExcept(dctx, initiativeID, reviseReason, keptIDs); err != nil {
			return nil, err
		}
	}
	// M1: decIDByNorm is keyed by THIS CYCLE's own decision wording (what the
	// model actually put in decisions[], which is also what a requirement's
	// `decision` field cites) mapped to whatever id createDecisionIfNew
	// resolved it to -- the existing decision's id on a near-duplicate merge,
	// or the newly-created one otherwise. Re-fetching decisions from the
	// store afterward and matching by normalized text (the old approach)
	// missed exactly the merge case: the requirement's ref names this
	// cycle's wording, but the stored row for a merged-into decision keeps
	// its EARLIER wording, and the two need not normalize identically even
	// though they're Jaccard-similar enough to have merged.
	decIDByNorm := make(map[string]string, len(decisionRefByReqID))
	for _, d := range objSlice(obj["decisions"]) {
		question := e.redact(str(d["question"]))
		id, err := e.createDecisionIfNew(dctx, initiativeID, &domain.Decision{
			InitiativeID:   initiativeID,
			Type:           domain.DecisionProduct,
			Question:       question,
			Options:        e.redactSlice(strSlice(d["options"])),
			Recommendation: e.redact(str(d["recommendation"])),
		})
		if err != nil {
			return nil, err
		}
		if id != "" {
			decIDByNorm[normalizeJudgment(question)] = id
		}
	}
	// allDecisionsByNorm is a fallback lookup across EVERY decision already on
	// record for the initiative -- not just the ones raised this cycle (ICP
	// re-run defect #4). A requirement's `decision` reference can legitimately
	// name a decision from an earlier cycle this cycle's PRD does not re-raise
	// (a standing, already-decided choice) or restate with slightly different
	// wording that still normalizes to a prior decision's own question text;
	// decIDByNorm alone, keyed only to this cycle's `decisions[]`, missed that
	// case entirely -- a requirement regenerated in a later revise cycle
	// (REQ-19..29 after review rounds) never got wired to the earlier
	// decision it still derives from, leaving that decision's reopen with "0
	// stale items" and an empty /impact for a live requirement that plainly
	// covered its subject. `decisions` was already loaded above (before this
	// cycle's own decisions were created), so this costs no extra query.
	allDecisionsByNorm := make(map[string]string, len(decisions))
	for _, d := range decisions {
		allDecisionsByNorm[normalizeJudgment(d.Question)] = d.ID
	}
	// Wire requirement→decision edges for requirements shaped by a decision,
	// matched by the decision's exact question text (normalized), so an open
	// decision's impact ("blocks N") is the set of requirements waiting on it.
	if len(decisionRefByReqID) > 0 {
		for reqID, ref := range decisionRefByReqID {
			norm := normalizeJudgment(ref)
			decID, ok := decIDByNorm[norm]
			if !ok {
				decID, ok = allDecisionsByNorm[norm]
			}
			if ok {
				if err := e.db.WireRequirementDerivedFromDecision(dctx, initiativeID, reqID, decID); err != nil {
					return nil, err
				}
			} else {
				e.log.Debug("requirement decision reference did not match any known decision; link skipped (best-effort)", "initiative", initiativeID, "requirement", reqID)
			}
		}
	}
	// Objectives and metrics after the requirements exist, so each
	// requirement's serves edges can be wired to them in the same pass.
	if err := e.recordPRDObjectives(dctx, initiativeID, runID, obj, reviseReason, objectiveRefs); err != nil {
		return nil, err
	}
	for _, q := range strSlice(obj["open_questions"]) {
		if err := e.createQuestionIfNew(dctx, initiativeID, e.redact(q)); err != nil {
			return nil, err
		}
	}

	// Rendered last: recordPRDObjectives has rewritten obj's objective keys
	// to their stored codes, so the body says OBJ-1 where the response said
	// O1. Committed last too, which only strengthens H6: the artifact now
	// exists only once everything it stands for does.
	body := renderPRD(obj)
	bodyDigest := e.storeBlob(body)
	// The next version of the PRD, not a new "PRD v1": a revision used to
	// start a fresh family, which is how Define came to offer "Diff v1 → v1".
	// Every revision cycle keeps its version even when the body repeats --
	// the review loop counts cycles by what was produced.
	ver := &domain.ArtifactVersion{BodyDigest: bodyDigest, ProducedByRun: runID, Status: "draft"}
	if _, err := e.db.RecordArtifactVersion(dctx, initiativeID, domain.ArtifactPRD, ver, false); err != nil {
		return nil, err
	}
	return responses, nil
}

// runReviewLoop runs deterministic checks + adversarial review with bounded
// revise cycles (§11). Rigor (review B1) is calibrated once, from the idea's
// own size and the project's scale (ICP re-run #1) -- NEVER from the PRD's
// own requirement count, which a revise cycle only ever grows, and which the
// PM model writes >=9 of regardless of how small the idea is. Those two
// inputs are also both stable across cycles (a revise cycle is the review
// asking for more of the same PRD, not a new, larger one), so calibrating
// once up front cannot flip level mid-review either way.
func (e *Engine) runReviewLoop(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	// One snapshot for the whole loop (review H4): Run/Resume reach this
	// without holding mu, and a multi-cycle review should run under one
	// config generation throughout, not whichever one a concurrent Reload
	// happens to have swapped in by a later cycle.
	snap := e.snapshotConfig()
	maxCycles := snap.cfg.Review.MaxCycles
	rules := snap.cfg.Review.Convergence.Effective()
	guarded := !rules.Legacy()
	var rigor RigorLevel

	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}

	// Cross-cycle state (review.convergence; see convergence.go for the root
	// causes each piece answers). prior is what the reviewer is shown and
	// judged against; changed is what the last revision touched (nil until a
	// revision inside THIS loop has happened); prevOpen is the previous
	// cycle's gating count, -1 before there is one.
	var prior []priorFinding
	var changed map[string]bool
	prevOpen, stalls := -1, 0
	if guarded {
		// A loop entered from Revise or Resume has been reviewed before: the
		// previous loop's still-open findings are this one's prior cycle, so
		// a reworded twin of a finding a human just revised away is
		// recognized as a re-raise rather than counted as brand new.
		if prior, err = e.seedPriorFindings(ctx, initiativeID); err != nil {
			return domain.InitiativeFailed, err
		}
	}

	for cycle := 1; ; cycle++ {
		reqs, err := e.db.ListRequirements(ctx, initiativeID)
		if err != nil {
			return domain.InitiativeFailed, err
		}
		active := activeRequirements(reqs)

		if rigor == "" {
			scale := projectScaleOf(ctx, e.workspaceRoot)
			rigor = calibrateRigor(RigorLevel(snap.cfg.Review.Rigor), scale, changeSize{IdeaWords: ideaWordCount(ini.Description)})
			if rigor == RigorQuick {
				maxCycles = 1
			}
		}

		detFindings := review.DeterministicChecks(initiativeID, active)
		liveObjs, liveMetrics := e.liveOutcomes(ctx, initiativeID)
		detFindings = append(detFindings, review.OutcomeChecks(initiativeID, active, liveObjs, liveMetrics)...)
		outcomeRefs, outcomeCodes := e.outcomeCitables(ctx, initiativeID)
		e.setStage(ctx, initiativeID, fmt.Sprintf("Reviewing the PRD (cycle %d)", cycle))
		var extra []ccontext.Input
		if guarded {
			subject := "a REQ code from the requirements below"
			if len(liveObjs) > 0 {
				subject = "a REQ code from the requirements below, or an OBJ/MET code from the objectives"
			}
			extra = append(extra, ccontext.Input{Selector: "convergence_policy", Content: reviewerConvergencePolicy(rules, subject)})
			if len(prior) > 0 {
				extra = append(extra, ccontext.Input{Selector: "prior_findings", Content: renderPriorFindings(prior, changed, rules)})
			}
		}
		reviewRunID, advFindings, err := e.runAdversarialReview(ctx, initiativeID, active, rigor, extra)
		if err != nil {
			return domain.InitiativeFailed, err
		}

		// C2: rigor only ever relaxes the ADVERSARIAL reviewer's findings.
		// The deterministic checks (duplicate REQ code, missing/vague
		// acceptance criteria) must always go through EvaluateApproval, at
		// every rigor -- quick must never auto-approve a PRD that fails
		// them just because it has few requirements.
		//
		// C1: when quick DOES accept the adversarial reviewer's findings,
		// they must be marked Advisory=true before they are capped and
		// persisted -- an unmarked finding here is a plain open blocker
		// that the verify gate (hasOpenBlockingFindings) can never clear.
		if rigor == RigorQuick {
			for i := range advFindings {
				advFindings[i].Advisory = true
			}
		}

		// The convergence rules (guarded mode) run on the adversarial
		// findings only, before capping, so the cap's "never drop a blocker"
		// rule sees the post-rule blockers, and before redaction, which never
		// touches a requirement code.
		tel := newReviewTelemetry("prd", cycle, rules, rigor)
		tel.PrevOpenBlocking = prevOpen
		var cls classification
		if guarded {
			cls = classifyFindings(classifyInput{
				rules: rules, later: len(prior) > 0, active: active, idea: ini.Description,
				prior: prior, changed: changed, outcomeRefs: outcomeRefs,
			}, advFindings)
		} else {
			cls = classification{reRaiseOf: map[int]int{}, demoted: map[string]int{}, cites: make([]string, len(advFindings))}
		}
		countCycle(&tel, advFindings, cls, prior)

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
			all[i].Note = e.redact(all[i].Note)
		}

		// ICP re-run defect A: Revise (human answers a question / resolves a
		// decision -> re-runs product definition -> re-runs this loop from
		// cycle 1) is a legitimate re-review of the same PRD, and it does not
		// know a human already dispositioned an earlier cycle's finding --
		// the fresh pass has no way to see that a near-identical finding was
		// already judged a false positive. Carry that disposition forward so
		// a reworded twin does not silently re-block approval. After
		// redaction, so it compares like-for-like against prior findings
		// (already redacted themselves before they were persisted).
		if err := e.inheritDispositionFromPrior(ctx, initiativeID, all); err != nil {
			return domain.InitiativeFailed, err
		}

		for i := range all {
			if err := e.db.CreateFinding(ctx, &all[i]); err != nil {
				return domain.InitiativeFailed, err
			}
		}

		if guarded {
			// The previous cycle's gating findings have now been ruled on:
			// each one re-raised is superseded by its re-raise (duplicate),
			// and each one not re-raised is resolved -- fixed when the
			// revision addressed it, not_applicable when the reviewer simply
			// stopped raising it. Without this, a parked initiative showed
			// every cycle's findings as open and current (root cause 3), and
			// `idea approve` signed off findings the loop had already closed.
			if err := e.retirePriorFindings(ctx, prior, cls); err != nil {
				return domain.InitiativeFailed, err
			}
		}

		// blocked now reflects only non-advisory blocking findings: at quick
		// that is exactly the deterministic ones (the adversarial reviewer's
		// were just marked Advisory above), and at standard/thorough it is
		// any of them. Either way it must always go through the cycle/
		// human-judgment path below -- there is no rigor level at which a
		// failed deterministic check is auto-approved.
		blocked, reason := review.EvaluateApproval(all)
		tel.OpenBlocking = countGating(all)
		tel.Deterministic = countGating(detFindings)

		if !blocked {
			tel.Outcome = outcomeConverged
			e.recordReviewTelemetry(ctx, reviewRunID, tel)
			if rigor == RigorQuick && len(advFindings) > 0 {
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

		// The stall detector (root cause 5): the open blocking count must
		// fall from one cycle to the next. When it does not, the remaining
		// cycles are spent re-reviewing a PRD that is not getting closer --
		// usually a genuine blocker only a human can resolve (a contradiction
		// in the idea, a decision nobody has made), sometimes a reviewer the
		// rules above could not pin down. Either way a human is the next
		// step, and reaching them a cycle early is cheaper than reaching them
		// at max_cycles.
		if guarded && prevOpen >= 0 && tel.OpenBlocking >= prevOpen {
			stalls++
		} else {
			stalls = 0
		}
		prevOpen = tel.OpenBlocking

		e.log.Warn("review blocked", "cycle", cycle, "findings", len(all), "open_blocking", tel.OpenBlocking, "reason", reason)
		if cycle >= maxCycles || (guarded && stalls >= rules.StallCycles) {
			tel.Outcome = outcomeMaxCycles
			if cycle < maxCycles {
				tel.Outcome = outcomeStalled
			}
			e.recordReviewTelemetry(ctx, reviewRunID, tel)
			e.log.Warn("review non-convergence", "cycles", cycle, "outcome", tel.Outcome)
			if err := e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPRDReview, RoleOrchestrator); err != nil {
				return domain.InitiativeFailed, err
			}
			return domain.InitiativeNeedsHumanJudgment, nil
		}
		tel.Outcome = outcomeRevise
		e.recordReviewTelemetry(ctx, reviewRunID, tel)

		// Revise: re-run product definition, updating requirements that
		// continue in place (same code) and superseding only the ones this
		// cycle's response dropped (review ICP re-run #1 -- see
		// runProductDefinition's reviseReason doc).
		e.log.Info("revising", "cycle", cycle)
		var rev *prdRevision
		var next []priorFinding
		if guarded {
			// The next cycle's prior list is THIS cycle's gating adversarial
			// findings, as persisted (IDs, redacted text, the rule notes).
			var gating, det []domain.Finding
			var cites []string
			for _, f := range all {
				if f.Advisory || !f.Severity.Blocking() || f.ResolvesBlocking() {
					continue
				}
				switch f.ReviewRunID {
				case reviewRunID:
					gating = append(gating, f)
					cites = append(cites, citationOf(f, active, outcomeCodes))
				case "deterministic-check":
					det = append(det, f)
				}
			}
			next = gatingPriors(gating, cites)
			rev = &prdRevision{findings: renderReviewFindingsForReviser(det, next, all)}
		}
		beforeOutcomes := outcomeFingerprints(liveObjs, liveMetrics)
		responses, err := e.runProductDefinitionRevising(ctx, initiativeID, active, "review-cycle-"+fmt.Sprint(cycle), rev)
		if err != nil {
			return domain.InitiativeFailed, err
		}
		if guarded {
			after, err := e.db.ListRequirements(ctx, initiativeID)
			if err != nil {
				return domain.InitiativeFailed, err
			}
			var removed map[string]bool
			changed, removed = changedRequirements(active, activeRequirements(after))
			// An OBJ/MET citation is judged against whether the revision
			// changed that objective or metric, like a REQ citation.
			afterObjs, afterMetrics := e.liveOutcomes(ctx, initiativeID)
			changedOutcomes(beforeOutcomes, outcomeFingerprints(afterObjs, afterMetrics), changed, removed)
			markAddressed(next, responses, changed, removed)
			prior = next
		}
	}
}

// prdRevision is what a review-driven revision tells the PRD author beyond
// the ordinary inputs: the precise list of findings it must address
// (renderReviewFindingsForReviser). nil for an initial authoring, a human
// revise, a retry, and every revision in legacy convergence mode.
type prdRevision struct {
	findings string
}

// citationOf recovers the citation a persisted finding carries -- the code
// of the requirement it targets, or "" -- for the prior list. The parse-time
// Cites field does not survive persistence, but the re-targeting it caused
// does.
func citationOf(f domain.Finding, reqs []domain.Requirement, outcomeCodeByID map[string]string) string {
	if f.TargetType == "objective" || f.TargetType == "metric" {
		return strings.ToUpper(outcomeCodeByID[f.TargetID])
	}
	if f.TargetType != "requirement" {
		return ""
	}
	for _, r := range reqs {
		if r.ID == f.TargetID {
			return NormalizeRequirementRef(r.Code)
		}
	}
	return ""
}

// seedPriorFindings returns the still-open gating adversarial findings an
// earlier PRD review loop left behind, as this loop's prior cycle. Empty on a
// first review. A seeded finding counts as addressed when the requirement it
// cited is no longer active, or the PRD has been regenerated since it was
// raised (a human revise); a resume, which re-reviews the same PRD, leaves it
// unaddressed, so its re-raise needs no justification.
func (e *Engine) seedPriorFindings(ctx context.Context, initiativeID string) ([]priorFinding, error) {
	findings, err := e.db.ListFindingsByInitiative(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	reqs, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	activeIDs := map[string]bool{}
	for _, r := range activeRequirements(reqs) {
		activeIDs[r.ID] = true
	}
	_, outcomeCodes := e.outcomeCitables(ctx, initiativeID)
	arts, err := e.db.ListArtifacts(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	var latestPRD time.Time
	for _, a := range arts {
		if a.Kind == domain.ArtifactPRD && a.CreatedAt.After(latestPRD) {
			latestPRD = a.CreatedAt
		}
	}
	var gating []domain.Finding
	var cites []string
	var addressed []bool
	for _, f := range findings {
		if f.Disposition != "" || f.Advisory || !f.Severity.Blocking() || f.Code != "" {
			continue
		}
		if f.ReviewRunID == "deterministic-check" {
			continue
		}
		switch f.TargetType {
		case "prd", "requirement", "objective", "metric":
		default:
			continue
		}
		gating = append(gating, f)
		cites = append(cites, citationOf(f, reqs, outcomeCodes))
		gone := (f.TargetType == "requirement" && !activeIDs[f.TargetID]) ||
			((f.TargetType == "objective" || f.TargetType == "metric") && outcomeCodes[f.TargetID] == "")
		addressed = append(addressed, gone || latestPRD.After(f.CreatedAt))
	}
	out := gatingPriors(gating, cites)
	for i := range out {
		out[i].Addressed = addressed[i]
	}
	return out, nil
}

// retirePriorFindings dispositions the previous cycle's gating findings once
// this cycle has ruled on them (see the call site). Actor is the reviewer:
// this is the review's own bookkeeping, never a human's signature, and
// accepted_risk is not among the dispositions it can set.
func (e *Engine) retirePriorFindings(ctx context.Context, prior []priorFinding, cls classification) error {
	reraised := make(map[int]bool, len(cls.reRaiseOf))
	for _, p := range cls.reRaiseOf {
		reraised[p] = true
	}
	for i, p := range prior {
		if p.ID == "" {
			continue
		}
		disp := domain.DispositionNotApplicable
		switch {
		case reraised[i]:
			disp = domain.DispositionDuplicate
		case p.Addressed:
			disp = domain.DispositionFixed
		}
		if err := e.db.SetFindingDisposition(ctx, p.ID, disp, RoleAdversarialReviewer); err != nil {
			return err
		}
	}
	return nil
}

// recordReviewTelemetry attaches one cycle's convergence record to the
// reviewer's run manifest. Best-effort: telemetry is evidence about the
// review, not part of it, and a failure to record it must not fail a review
// that otherwise completed -- it is logged instead.
func (e *Engine) recordReviewTelemetry(ctx context.Context, runID string, t domain.ReviewTelemetry) {
	if runID == "" {
		return
	}
	if err := e.db.SetRunManifestReview(context.WithoutCancel(ctx), runID, t); err != nil {
		e.log.Warn("could not record review telemetry", "run", runID, "err", err)
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
	// Capture the current active requirements so the model improves the
	// immediately preceding set rather than re-reading the full (unbounded)
	// requirement history. Superseding happens inside runProductDefinition
	// now (review ICP re-run #1), and only for the ones this cycle's
	// response does not continue in place.
	all, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	prior := activeRequirements(all)
	if err := e.runProductDefinition(ctx, initiativeID, prior, "human-revise"); err != nil {
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

// runAdversarialReview runs one PRD review pass and returns the reviewer's
// run id (whose manifest the cycle's convergence telemetry is attached to)
// with its parsed findings. extra carries the guarded-mode convergence inputs
// (the citation policy and, from cycle 2, the prior cycle's findings); nil in
// legacy mode, which sends exactly the prompt it always has.
func (e *Engine) runAdversarialReview(ctx context.Context, initiativeID string, reqs []domain.Requirement, rigor RigorLevel, extra []ccontext.Input) (string, []domain.Finding, error) {
	scale := projectScaleOf(ctx, e.workspaceRoot)
	conventions := projectConventions(e.workspaceRoot)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Independently challenge the PRD. Emit structured findings. Never set accepted_risk. " + reviewerSeverityGuidance(rigor, scale)},
		{Selector: "project_scale", Content: scaleAndConventions(scale, changeSize{Requirements: len(reqs)}, rigor, conventions)},
		{Selector: "requirements", Content: renderRequirements(reqs)},
	}
	// The outcome chain, when the PRD has one: which objective each
	// requirement serves and how each objective is measured, so the
	// reviewer can challenge a requirement that serves no objective or an
	// objective with no measurable metric -- citing the REQ/OBJ code. A PRD
	// with no objectives sends exactly the prompt it always has.
	if objs, metrics := e.liveOutcomes(ctx, initiativeID); len(objs) > 0 {
		inputs = append(inputs, ccontext.Input{Selector: "objectives", Content: renderObjectives(objs, metrics, reqs)})
	}
	inputs = append(inputs, extra...)
	runID, obj, err := e.runModel(ctx, initiativeID, StageReview, RoleAdversarialReviewer, "review", schema.Review,
		inputs, "Adversarial review of the PRD requirements.")
	if err != nil {
		return runID, nil, err
	}
	findings, err := review.ParseFindings(runID, "prd", initiativeID, obj)
	return runID, findings, err
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
	// review H4: contextDocs is reached by Run/Advance/Revise/Resume without
	// mu, and every digest below must come from the SAME blob store.
	blobs := e.snapshotConfig().blobs
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
		content, err := blobs.Get(digest)
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
// the returned description. It snapshots the reloadable engine components via
// cfgMu (review H4: Reload swaps them under cfgMu, not mu -- mu serializes
// only execute/apply/revert, and taking it here would needlessly contend with
// those instead of with Reload), so the read path cannot race a config
// reload. rawBlobs is set once at construction and never swapped by Reload,
// so it is read directly.
func (e *Engine) describeImage(ctx context.Context, doc domain.ContextDoc) error {
	spec := e.resolveModelSpec("vision")
	snap := e.snapshotConfig()
	registry, redactor, blobs, rawBlobs := snap.registry, snap.redactor, snap.blobs, e.rawBlobs

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
	if err := e.checkBudget(ctx, doc.InitiativeID, "the image-description model call"); err != nil {
		setRunStatus(domain.RunFailed)
		return err
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
	// The description is model output over untrusted input, and a vision
	// model OCRs whatever the image shows -- a screenshot of a terminal or a
	// settings page puts its API key into this text. Redact it before it is
	// stored or ever reaches a prompt. A nil redactor (an engine built
	// without one) falls back to the pattern catalog rather than storing the
	// text as-is: the daemon's redacted blob store would mask it again on
	// Put, but this must not depend on which store happens to be wired.
	if redactor != nil {
		text = redactor.RedactString(text)
	} else {
		text = security.Redact(text)
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
	// review H4: reached by Run/Revise without mu; one generation for the
	// whole fetch loop below.
	cfg := e.snapshotConfig().cfg
	if cfg.Network != config.NetworkEnabled || len(cfg.Research.URLs) == 0 {
		return ""
	}
	f := tools.NewFetcher()
	var b strings.Builder
	for _, url := range cfg.Research.URLs {
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
