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
	"time"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/config"
	ccontext "github.com/idea-harness/idea-harness/internal/context"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/review"
	"github.com/idea-harness/idea-harness/internal/roles"
	"github.com/idea-harness/idea-harness/internal/schema"
	"github.com/idea-harness/idea-harness/internal/store"
	"github.com/idea-harness/idea-harness/internal/tools"
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
	RoleOrchestrator          = "product_orchestrator"
	RoleResearchAnalyst       = "research_analyst"
	RoleProductManager        = "product_manager"
	RoleAdversarialReviewer   = "adversarial_reviewer"
	RoleArchitect             = "architect"
	RoleProductDesigner       = "product_designer"
	RoleVerificationEngineer  = "verification_engineer"
	RoleEngineeringLead       = "engineering_lead"
)

// Engine drives the M1 workflow.
type Engine struct {
	db            *store.DB
	blobs         *blob.Store
	registry      *model.Registry
	cfg           config.HarnessConfig
	log           *slog.Logger
	workRoot      string
	workspaceRoot string
}

// New builds an engine.
func New(db *store.DB, blobs *blob.Store, registry *model.Registry, cfg config.HarnessConfig, log *slog.Logger) *Engine {
	if cfg.Review.MaxCycles <= 0 {
		cfg.Review.MaxCycles = 3
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Engine{db: db, blobs: blobs, registry: registry, cfg: cfg, log: log}
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

// maxOutputForStage returns the completion token budget for a stage. The PRD is
// the largest output (full requirement set + acceptance criteria) and was
// previously truncated at 4096 tokens; framing is a short brief. Values stay
// within the providers' declared MaxOutput ceilings (16k for Anthropic/OpenAI).
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
	runID, err := e.db.CreateRun(ctx, domain.RunModel, "", roleName)
	if err != nil {
		return "", nil, err
	}

	bundle := ccontext.Compile(inputs)
	modelSpec := e.resolveModelSpec(roleName)
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
		MaxOutput:      maxOutputForStage(stageKey),
	}

	reqStart := time.Now()
	e.log.Info("model request", "stage", stageKey, "role", roleName, "model", modelSpec)
	// Bounded retry (3 attempts): transient infra errors (503/429/…), empty
	// output, and schema-validation failure are each retried before failing, so
	// a transient provider error or malformed JSON does not sink the run.
	var resp model.Response
	var decoded *model.StructuredResult
	var lastErr error
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err = prov.Complete(ctx, req)
		if err != nil {
			if attempt < maxAttempts && isTransientModelError(err) {
				e.log.Warn("transient model error, retrying", "stage", stageKey, "role", roleName, "model", modelSpec, "attempt", attempt, "err", err)
				time.Sleep(time.Duration(attempt) * 1500 * time.Millisecond)
				continue
			}
			e.log.Error("model request failed", "stage", stageKey, "role", roleName, "model", modelSpec, "err", err, "attempt", attempt, "duration", time.Since(reqStart).Round(time.Millisecond))
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
	e.log.Info("model response", "stage", stageKey, "role", roleName, "model", modelSpec, "actual", resp.ActualModel, "duration", time.Since(reqStart).Round(time.Millisecond), "out_tokens", resp.Usage.OutputTokens)

	if decoded == nil {
		e.log.Error("structured output invalid", "stage", stageKey, "role", roleName, "err", lastErr)
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
		Usage:          domain.RunUsage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, CostUSD: resp.Usage.CostUSD},
		StartedAt:      time.Now().UTC(),
		FinishedAt:     time.Now().UTC(),
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
	if m := e.cfg.Models[roleName]; m != "" {
		return m
	}
	if rc, err := roles.Load(roleName); err == nil && rc.Model != "" {
		return rc.Model
	}
	if e.cfg.DefaultModel != "" {
		return e.cfg.DefaultModel
	}
	if e.registry != nil {
		if d := e.registry.Default(); d != "" && d != "idea-harness-default" {
			return d + "/" + model.DefaultModelFor(d)
		}
	}
	return "idea-harness-default"
}

// implementerModel resolves the coding agent's model: the per-role map
// ("implementer") wins, then the executor-level model, then the default chain.
// This is how OMP and Hermes get different models per role.
func (e *Engine) implementerModel() string {
	if m := e.cfg.Models["implementer"]; m != "" {
		return m
	}
	if m := e.cfg.Executor.Model; m != "" {
		return m
	}
	return e.resolveModelSpec("implementer")
}

// ---------- Stages ----------

func (e *Engine) runFraming(ctx context.Context, initiativeID, idea string) error {
	runID, obj, err := e.runModel(ctx, initiativeID, StageFraming, RoleOrchestrator, "idea_brief", schema.IdeaBrief,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Produce a concise IdeaBrief."},
			{Selector: "task", Content: idea},
		},
		"Framing task:\n"+idea,
	)
	if err != nil {
		return err
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
			Statement:     a,
			Confidence:    domain.ConfidenceLow,
			Source:        "framing",
			ProducedByRun: runID,
		}); err != nil {
			return err
		}
	}
	// Record open questions from framing.
	for _, q := range strSlice(obj["open_questions"]) {
		if err := e.createQuestionIfNew(ctx, initiativeID, q); err != nil {
			return err
		}
	}
	return nil
}

// createQuestionIfNew adds a question unless an equivalent one already exists
// for the initiative (§4: no duplicate open questions). A judgment already
// raised as a structured decision is not re-asked as a free-form question —
// decisions take precedence over questions.
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
			return nil
		}
	}
	return e.db.CreateQuestion(ctx, &domain.Question{InitiativeID: initiativeID, Question: text})
}

// createDecisionIfNew adds a decision unless an equivalent one already exists.
// The model re-lists prior decisions (prefixed with "[Source: ...]") on every
// revise cycle, so this collapses those re-listings onto the original row. It
// also resolves any equivalent open question — a structured decision takes
// precedence over a free-form question.
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
		if normalizeJudgment(x.Question) == norm {
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
		}
	}
	return nil
}

func (e *Engine) runResearch(ctx context.Context, initiativeID, idea string) error {
	runID, obj, err := e.runModel(ctx, initiativeID, StageResearch, RoleResearchAnalyst, "research_brief", schema.ResearchBrief,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Produce an evidence-backed ResearchBrief. Mark facts vs assumptions vs hypotheses. No unattributed facts. Cite the provided sources."},
			{Selector: "task", Content: idea},
			{Selector: "untrusted", Content: e.researchEvidence(ctx)},
		},
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
			Statement:     str(f["statement"]),
			Confidence:    domain.Confidence(str(f["confidence"])),
			Source:        str(f["source"]),
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
		{Selector: "immutable_policy", Content: "Author a PRD with verifiable requirements and measurable acceptance criteria (metric + target + condition). Raise a decision only for a genuinely open, undecided choice; do not re-list decisions already marked DECIDED, and do not restate an open question as a decision. Do not re-raise a question that already exists (open or answered); only add genuinely new questions. Any review finding dispositioned 'mitigate' must be addressed directly: incorporate the mitigation into the requirements."},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decisions)},
		{Selector: "questions", Content: renderQuestions(questions)},
		{Selector: "findings", Content: renderFindings(findings)},
	}

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

	for _, r := range objSlice(obj["requirements"]) {
		var acs []domain.AcceptanceCriterion
		for _, ac := range objSlice(r["acceptance_criteria"]) {
			acs = append(acs, domain.AcceptanceCriterion{
				Metric: str(ac["metric"]), Target: str(ac["target"]), Condition: str(ac["condition"]),
			})
		}
		if err := e.db.CreateRequirement(ctx, &domain.Requirement{
			InitiativeID:       initiativeID,
			Statement:          str(r["statement"]),
			Priority:           domain.RequirementPriority(str(r["priority"])),
			AcceptanceCriteria: acs,
			ProducedByRun:      runID,
		}); err != nil {
			return err
		}
	}
	for _, d := range objSlice(obj["decisions"]) {
		if err := e.createDecisionIfNew(ctx, initiativeID, &domain.Decision{
			InitiativeID:   initiativeID,
			Type:           domain.DecisionProduct,
			Question:       str(d["question"]),
			Options:        strSlice(d["options"]),
			Recommendation: str(d["recommendation"]),
		}); err != nil {
			return err
		}
	}
	for _, q := range strSlice(obj["open_questions"]) {
		if err := e.createQuestionIfNew(ctx, initiativeID, q); err != nil {
			return err
		}
	}
	return nil
}

// runReviewLoop runs deterministic checks + adversarial review with bounded
// revise cycles (§11).
func (e *Engine) runReviewLoop(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	maxCycles := e.cfg.Review.MaxCycles

	for cycle := 1; ; cycle++ {
		reqs, err := e.db.ListRequirements(ctx, initiativeID)
		if err != nil {
			return domain.InitiativeFailed, err
		}
		active := activeRequirements(reqs)

		detFindings := review.DeterministicChecks(initiativeID, active)
		advFindings, err := e.runAdversarialReview(ctx, initiativeID, active)
		if err != nil {
			return domain.InitiativeFailed, err
		}
		all := append(detFindings, advFindings...)

		for i := range all {
			all[i].InitiativeID = initiativeID
			if all[i].ReviewRunID == "" {
				all[i].ReviewRunID = "deterministic-check"
			}
			if err := e.db.CreateFinding(ctx, &all[i]); err != nil {
				return domain.InitiativeFailed, err
			}
		}

		blocked, reason := review.EvaluateApproval(all)
		if !blocked {
			e.log.Info("review passed", "cycle", cycle, "findings", len(all))
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
			if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment, RoleOrchestrator); err != nil {
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
func (e *Engine) Resume(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
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

// Approve records explicit human approval of the initiative (§11).
func (e *Engine) Approve(ctx context.Context, initiativeID string) error {
	if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeApproved, store.HumanActor); err != nil {
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

	if err := e.db.CreateDecisionDiscussion(ctx, &domain.DecisionDiscussion{DecisionID: decisionID, Author: store.HumanActor, Message: message}); err != nil {
		return "", err
	}
	if response != "" {
		if err := e.db.CreateDecisionDiscussion(ctx, &domain.DecisionDiscussion{DecisionID: decisionID, Author: RoleProductManager, Message: response}); err != nil {
			return "", err
		}
	}
	return response, nil
}

func (e *Engine) runAdversarialReview(ctx context.Context, initiativeID string, reqs []domain.Requirement) ([]domain.Finding, error) {
	runID, obj, err := e.runModel(ctx, initiativeID, StageReview, RoleAdversarialReviewer, "review", schema.Review,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Independently challenge the PRD. Emit structured findings. Never set accepted_risk."},
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
