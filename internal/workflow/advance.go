package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
	ccontext "github.com/devarispbrown/idea-harness/internal/context"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/review"
	"github.com/devarispbrown/idea-harness/internal/schema"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Advance runs the M2 slice: Design (Architecture → Product Design →
// Verification Design, sequentially) → Execution Plan → plan review.
//
// "Independent" would overstate it: the reviewer is a separate prompt and a
// separate run, not a separate identity. Every automated write shares one
// actor, so nothing prevents the implementer and reviewer roles resolving to
// the same model -- see docs/security.md.
// It returns the final initiative status.
// errUnresolvedDependencies signals that the execution plan referenced a task
// title that could not be resolved; the initiative is left in needs_human_judgment
// with a blocking finding rather than failed outright.
var errUnresolvedDependencies = errors.New("unresolved task dependency")

func (e *Engine) Advance(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	if err := e.runArchitecture(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runProductDesign(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runVerificationDesign(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.ConvergeDecisions(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runExecutionPlan(ctx, initiativeID); err != nil {
		if errors.Is(err, errUnresolvedDependencies) {
			// plan_dependencies, not plan_review: only regenerating the plan
			// resolves this. Approving it would set plan_approved over a graph
			// that is known to be incomplete and leave the `plan_dependency`
			// finding undisposed, which then wedges the completion gate.
			_ = e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPlanDependencies, RoleOrchestrator)
			return domain.InitiativeNeedsHumanJudgment, nil
		}
		return domain.InitiativeFailed, err
	}
	return e.runExecutionPlanReview(ctx, initiativeID)
}

// ApprovePlan accepts a plan whose review raised findings: it records the
// human's acceptance of them and moves the initiative to plan_approved. It does
// not regenerate tasks, and it is terminal -- there is no re-evaluation and no
// path back to needs_human_judgment, which is what makes it an accept rather
// than a retry.
//
// Gated on the reason, exactly as Approve is, and for the same defect. All four
// JudgmentReasons share one status, so a status-only gate let this accept a plan
// parked on `plan_dependencies` -- where the undisposed `plan_dependency` finding
// IS the guard that keeps a dependency graph known to be incomplete out of
// `complete`, something advance.go relies on in three places and the CHANGELOG
// states outright. Approving there shipped the incomplete graph.
//
// The change-set guard matches RevisePlan and ReopenPhase. Without it, an
// initiative already building could have its change set's findings swept by a
// plan-level command -- and a blocking change-set finding is the one this
// repository repeatedly calls unclearable by any CLI command.
//
// The disposition lives here and not in runExecutionPlanReview, which Advance,
// RevisePlan and ReopenPhase also call: on those paths the plan was regenerated
// and nobody accepted anything, so attributing an acceptance to a human would be
// a lie. accepted_risk by HumanActor, the same pair Approve records, because
// that is what happened -- nothing was fixed.
func (e *Engine) ApprovePlan(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	// The gates -- running, status, judgment reason, change sets, and the
	// deterministic-findings check -- are in Runtime.ApprovePlanInitiative, not
	// here: this runs inside the launched operation, so a refusal raised at this
	// point fails the initiative rather than declining the request. A
	// precondition has to be answered before the work starts.
	//
	// This used to end `return e.runExecutionPlanReview(...)`, and that made
	// `approve-plan` a retry rather than an accept. runExecutionPlanReview runs a
	// fresh adversarial review, and a model reviewer is not a fixed point: on a
	// real provider the plan review blocked again, and again -- observed eleven
	// consecutive times in DX audit 7, with no route to plan_approved from any
	// surface. `revise-plan` re-enters the same function after regenerating the
	// plan; the web UI posts to the same endpoint. The PRD half never had this
	// problem because Engine.Approve accepts outright and does not re-run
	// runReviewLoop. This is that asymmetry closed.
	//
	// Six audits could not see it. demoProvider primes a FINITE queue from
	// demo.block_plan_review_cycles, so once it drains the demo reviewer
	// converges and approve-plan appears to work -- scripts/smoke.sh says so in
	// its own comment. A converging blocker proves the park is reachable; it can
	// never prove the escape exists, because the escape is indistinguishable
	// from the queue running out.
	//
	// Unconditional, not a compare-and-swap -- including the one CAS that WOULD
	// match here, TransitionInitiative(..., plan_approved, ..., running). The
	// package documents TransitionInitiative as the compare-and-swap for a
	// status change that is not a claim, so that is the conventional call, and
	// it is declined on purpose: it converts a race that cannot happen (nothing
	// moves the status out of `running` between the claim and this write) into a
	// FAILED initiative if it ever did, because this runs inside the operation.
	// The whole reason the first draft of this fix was rejected was a CAS in
	// exactly this position failing every initiative, so the safe write wins over
	// the conventional one, and the reasoning is recorded rather than implied.
	//
	// Engine.Approve CASes, but it can:
	// Runtime.ApproveInitiative calls it synchronously on the request goroutine
	// with no claim, so the status is still needs_human_judgment. This runs from
	// launchOperation AFTER ClaimInitiativeRunning (runtime.go), so the status
	// here is always `running` and a CAS from {needs_human_judgment,
	// plan_approved} would match zero rows -- sending failOperation(...,
	// failInitiative: true) down the error path and marking every initiative
	// FAILED. The claim is already the compare-and-swap this would have added.
	//
	// Disposition before the status write. Either order ends `failed` if the
	// other call errors, since this is inside the operation -- what the order
	// decides is which inconsistency survives into that failed state.
	// Disposition-first leaves a human's accepted_risk signature on a plan that
	// was never approved: visible in `idea reviews`, and recoverable.
	// Status-first would show a plan_approved the human never got. The
	// precedent is runReviewLoop's convergence path (engine.go), which
	// dispositions and then writes the status.
	//
	// accepted_risk, not fixed: nothing was fixed. It is also the one
	// disposition the reviewer is forbidden to set, which keeps it
	// unambiguously a human's signature on the risk.
	if err := e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionAcceptedRisk, store.HumanActor); err != nil {
		return "", err
	}
	if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativePlanApproved, store.HumanActor); err != nil {
		return "", err
	}
	return domain.InitiativePlanApproved, nil
}

// ReopenPhase re-runs the design pipeline from the given design stage through
// plan review (downstream cascade), regenerating artifacts as new versions and
// the plan from scratch. It is a pre-execution action: once any change set
// exists the reopen is refused, since re-planning would orphan applied work.
// phase must be one of architecture, product_design, verification_design.
func (e *Engine) ReopenPhase(ctx context.Context, initiativeID, phase string) (domain.InitiativeStatus, error) {
	cses, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	if changeSetsHoldingWork(cses) > 0 {
		return domain.InitiativeFailed, fmt.Errorf("cannot reopen a design phase after execution has begun")
	}

	stages := []struct {
		name string
		run  func(context.Context, string) error
	}{
		{"architecture", e.runArchitecture},
		{"product_design", e.runProductDesign},
		{"verification_design", e.runVerificationDesign},
	}
	start := -1
	for i, s := range stages {
		if s.name == phase {
			start = i
			break
		}
	}
	if start < 0 {
		return domain.InitiativeFailed, fmt.Errorf("unknown design phase %q", phase)
	}
	for _, s := range stages[start:] {
		if err := s.run(ctx, initiativeID); err != nil {
			return domain.InitiativeFailed, err
		}
	}
	if err := e.ConvergeDecisions(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.db.DeleteTasksForInitiative(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	// Not ignored. This write is what keeps a finding from outliving the plan it
	// described: hasOpenBlockingFindings refuses `complete` while any undisposed
	// critical/high exists, and approve-plan's structural gate now refuses while
	// an undisposed critical deterministic finding exists. A silent failure here
	// wedges both, and the escape approve-plan's refusal names is this function.
	if err := e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionNotApplicable, store.ActorOrchestrator); err != nil {
		return domain.InitiativeFailed, err
	}
	// Review A4 #5: re-planning clears staleness. The new plan is re-derived
	// from the live (post-reopen) decision state, so whatever was stale about
	// the discarded one no longer applies.
	if err := e.db.ClearStaleItemsForInitiative(ctx, initiativeID, store.ActorOrchestrator); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runExecutionPlan(ctx, initiativeID); err != nil {
		if errors.Is(err, errUnresolvedDependencies) {
			// plan_dependencies, not plan_review: only regenerating the plan
			// resolves this. Approving it would set plan_approved over a graph
			// that is known to be incomplete and leave the `plan_dependency`
			// finding undisposed, which then wedges the completion gate.
			_ = e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPlanDependencies, RoleOrchestrator)
			return domain.InitiativeNeedsHumanJudgment, nil
		}
		return domain.InitiativeFailed, err
	}
	return e.runExecutionPlanReview(ctx, initiativeID)
}

// RevisePlan regenerates the execution plan (and its review) in place after the
// human edited or rejected the prior plan. It deletes the prior tasks and their
// edges and disposes prior plan-review findings first, so no duplicate tasks or
// stale blocking findings survive the regeneration (full-platform review: plan
// revision must not leave orphans or wedge the verify gate).
func (e *Engine) RevisePlan(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	// A revision note (POST .../revise-plan {"note"}) steers this revision
	// only, exactly like execute's: cleared whatever happens, so it can never
	// leak into a later plan or run.
	defer e.clearNoteOverride(initiativeID)
	// Defensive: plan revision is a pre-execution action — refuse once any task
	// has produced work.
	cses, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	if changeSetsHoldingWork(cses) > 0 {
		return domain.InitiativeFailed, fmt.Errorf("cannot revise a plan that already has change sets")
	}
	// The codes the plan's tasks carry now, by title, so the regenerated plan
	// can give a task it kept the code it already had. The task counter is
	// monotonic on purpose (a deleted code is never reused for a DIFFERENT
	// task), and without this every revision renumbered every task -- a
	// revision that changed nothing turned TASK-1 into TASK-2.
	oldTasks, err := e.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	keepCodes := map[string]string{}
	for _, t := range oldTasks {
		keepCodes[normTaskTitle(t.Title)] = t.Code
	}
	e.setKeepTaskCodes(initiativeID, keepCodes)
	defer e.setKeepTaskCodes(initiativeID, nil)
	if err := e.db.DeleteTasksForInitiative(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	// Clear prior plan-review findings (they target the now-deleted tasks or the
	// superseded execution plan). NotApplicable preserves the record as dissent.
	// Not ignored. This write is what keeps a finding from outliving the plan it
	// described: hasOpenBlockingFindings refuses `complete` while any undisposed
	// critical/high exists, and approve-plan's structural gate now refuses while
	// an undisposed critical deterministic finding exists. A silent failure here
	// wedges both, and the escape approve-plan's refusal names is this function.
	if err := e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionNotApplicable, store.ActorOrchestrator); err != nil {
		return domain.InitiativeFailed, err
	}
	// Review A4 #5: re-planning clears staleness -- see the identical comment
	// in ReopenPhase.
	if err := e.db.ClearStaleItemsForInitiative(ctx, initiativeID, store.ActorOrchestrator); err != nil {
		return domain.InitiativeFailed, err
	}

	if err := e.runExecutionPlan(ctx, initiativeID); err != nil {
		if errors.Is(err, errUnresolvedDependencies) {
			// plan_dependencies, not plan_review: only regenerating the plan
			// resolves this. Approving it would set plan_approved over a graph
			// that is known to be incomplete and leave the `plan_dependency`
			// finding undisposed, which then wedges the completion gate.
			_ = e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPlanDependencies, RoleOrchestrator)
			return domain.InitiativeNeedsHumanJudgment, nil
		}
		return domain.InitiativeFailed, err
	}
	return e.runExecutionPlanReview(ctx, initiativeID)
}

// changeSetsHoldingWork counts the change sets a plan-level command must not
// disturb. The daemon applies the same predicate before the claim; this is the
// invariant behind that precondition, and the two must agree or the API refuses
// what the engine would have allowed.
func changeSetsHoldingWork(cses []domain.ChangeSet) int {
	n := 0
	for _, cs := range cses {
		if cs.Status.HoldsWork() {
			n++
		}
	}
	return n
}

// storeArtifact renders a body into the BlobStore and records it as the next
// version of the initiative's artifact of that kind (v1 when there is none).
// A body identical to the current version records nothing: regenerating a
// design document or the plan with no change -- revise-plan with nothing to
// revise, say -- must not mint a "v2" that differs from v1 only in its number.
func (e *Engine) storeArtifact(ctx context.Context, initiativeID string, kind domain.ArtifactKind, runID, body string) error {
	bodyDigest := e.storeBlob(body)
	ver := &domain.ArtifactVersion{BodyDigest: bodyDigest, ProducedByRun: runID, Status: "draft"}
	_, err := e.db.RecordArtifactVersion(ctx, initiativeID, kind, ver, true)
	return err
}

// createDesignDecision records a design-stage decision. If an equivalent
// decision already exists (raised by another role), the new role's position is
// recorded as a dissent instead of a duplicate — dissent is never erased (§8).
//
// Three fixes an ICP re-run found, all in the direction of "this stage must
// behave like runProductDefinition's own decision handling, not a separate,
// weaker copy of it":
//
//  1. Matching. This used to compare questions by exact normalized text only
//     (normalizeJudgment(d.Question) == norm), where runProductDefinition's
//     createDecisionIfNew already used similarDecision's fuzzy (token-Jaccard)
//     match. A design-stage decision reworded across architecture vs product
//     design (or across a later reopen) could miss an equivalent already on
//     record and duplicate it instead of merging/dissenting.
//  2. Answered questions. This never consulted the questions table at all, so
//     a later phase could -- and, on a real run, did -- re-ask a question the
//     human had already answered in an earlier phase (DEC-3 re-asked "API keys
//     come from an environment variable, never a file" as a fresh decision and
//     recommended a config file). A decision whose question matches an
//     ANSWERED question is not created; matching createQuestionIfNew's own
//     `question.reraised_answered` no-op event so the audit trail shows the
//     re-ask happened rather than looking like nothing did.
//  3. Provenance. Architecture/design decisions carried no derived_from edge
//     to anything, so resolving or reopening one marked no dependent
//     requirement/task/change-set stale and /impact reported none (review A4
//     #5's cascade never had anything to walk from). reqIDs -- resolved by the
//     caller from the decision's own `requirements` code references, exactly
//     as runExecutionPlan resolves a task's -- are now wired the same way
//     runProductDefinition wires a PRD requirement's `decision` reference:
//     requirement --derived_from--> decision.
func (e *Engine) createDesignDecision(ctx context.Context, initiativeID, roleName string, dtype domain.DecisionType, question string, options []string, recommendation string, reqIDs []string) error {
	norm := normalizeJudgment(question)
	if norm == "" {
		return nil
	}

	// Fix 2: an already-answered question is authoritative; do not re-raise it
	// as a "new" decision just because this phase worded it differently.
	questions, err := e.db.ListQuestions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if q.Status != domain.QuestionResolved {
			continue
		}
		qn := normalizeJudgment(q.Question)
		if qn == norm || similarJudgment(qn, norm) {
			return e.db.ApplyNoop(ctx, store.Event{
				AggregateType: "question",
				AggregateID:   q.ID,
				Actor:         store.ActorOrchestrator,
				EventType:     "question.reraised_answered",
			})
		}
	}

	existing, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return err
	}
	// Fix 1: similarDecision's fuzzy match, same as createDecisionIfNew --
	// not exact-text-only comparison.
	for _, d := range existing {
		if d.Type != dtype || !(normalizeJudgment(d.Question) == norm || similarDecision(d.Question, question, d.Options, options)) {
			continue
		}
		// A decision a human already resolved is authoritative (the prompt
		// itself tells the model so); a fresh, disagreeing recommendation from
		// this phase is not a dissent against a live open decision, it is a
		// model proposing to overrule a human, which dissent is not for.
		if d.Status != domain.DecisionOpen {
			return e.wireRequirementDecisionEdges(ctx, initiativeID, reqIDs, d.ID)
		}
		if recommendation != "" && d.Recommendation != recommendation {
			if err := e.db.CreateDissent(ctx, &domain.DecisionDissent{
				DecisionID: d.ID, ProponentRole: roleName, Position: e.redact(recommendation),
			}); err != nil {
				return err
			}
		}
		return e.wireRequirementDecisionEdges(ctx, initiativeID, reqIDs, d.ID)
	}
	d := &domain.Decision{
		InitiativeID: initiativeID, Type: dtype, Question: e.redact(question),
		Options: e.redactSlice(options), Recommendation: e.redact(recommendation),
	}
	if err := e.db.CreateDecision(ctx, d); err != nil {
		return err
	}
	return e.wireRequirementDecisionEdges(ctx, initiativeID, reqIDs, d.ID)
}

// wireRequirementDecisionEdges wires requirement --derived_from--> decision
// for each of reqIDs, skipping any pair already wired: wireEdges (the single
// codepath every graph write goes through) has no uniqueness constraint, and
// createDesignDecision can be asked to wire the same requirement to the same
// merged-into decision more than once (architecture raises it, product design
// later cites the same requirement against the same merged decision) --
// duplicate edges would double-count that requirement in handleTriage's
// dependents tally and in /impact's dependent list.
func (e *Engine) wireRequirementDecisionEdges(ctx context.Context, initiativeID string, reqIDs []string, decisionID string) error {
	for _, reqID := range reqIDs {
		edges, err := e.db.ListGraphEdgesFrom(ctx, initiativeID, "requirement", reqID)
		if err != nil {
			return err
		}
		already := false
		for _, edge := range edges {
			if edge.EdgeType == domain.EdgeDerivedFrom && edge.ToType == "decision" && edge.ToID == decisionID {
				already = true
				break
			}
		}
		if already {
			continue
		}
		if err := e.db.WireRequirementDerivedFromDecision(ctx, initiativeID, reqID, decisionID); err != nil {
			return err
		}
	}
	return nil
}

// designDecisionRequirementIDs resolves a design-stage decision's own
// `requirements` code references (e.g. "REQ-2") against reqByCode, the same
// tolerant matching runExecutionPlan applies to a task's requirement
// references (NormalizeRequirementRef). Unlike the execution plan's citations,
// an unresolved reference here is best-effort, not fatal: it is metadata for
// provenance, not a scheduling dependency, and a miss is exactly the
// "not shaped by a specific requirement" case the schema tells the model to
// leave empty.
func designDecisionRequirementIDs(d map[string]any, reqByCode map[string]string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, ref := range strSlice(d["requirements"]) {
		id, ok := reqByCode[NormalizeRequirementRef(ref)]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func (e *Engine) runArchitecture(ctx context.Context, initiativeID string) error {
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	allReqs, _ := e.db.ListRequirements(ctx, initiativeID)
	reqs := activeRequirements(allReqs)
	allDecs, _ := e.db.ListDecisions(ctx, initiativeID)
	decs := activeDecisions(allDecs)
	questions, _ := e.db.ListQuestions(ctx, initiativeID)
	reqByCode := requirementCodeIndex(reqs)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a technical design that decomposes the PRD into components with clear responsibilities and tradeoffs. Raise an architecture Decision only for a genuine tradeoff. A decided human decision is authoritative and an already-answered question is settled: do not propose or imply anything that contradicts either, and do not re-raise a question that already exists (open or answered) as a new decision. When a decision is about a specific Requirement, set its 'requirements' field to that requirement's code(s) (e.g. \"REQ-2\")."},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decs)},
		{Selector: "questions", Content: e.renderQuestionsWithAnswers(questions)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	e.setStage(ctx, initiativeID, "Designing the architecture")
	runID, obj, err := e.runModel(ctx, initiativeID, StageArchitecture, RoleArchitect, "architecture", schema.Architecture, inputs, "Produce the architecture.")
	if err != nil {
		return err
	}
	if err := e.storeArtifact(ctx, initiativeID, domain.ArtifactTechnicalDesign, runID, renderArchitecture(obj)); err != nil {
		return err
	}
	for _, d := range objSlice(obj["decisions"]) {
		reqIDs := designDecisionRequirementIDs(d, reqByCode)
		if err := e.createDesignDecision(ctx, initiativeID, RoleArchitect, domain.DecisionArchitecture, str(d["question"]), strSlice(d["options"]), str(d["recommendation"]), reqIDs); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runProductDesign(ctx context.Context, initiativeID string) error {
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	allReqs, _ := e.db.ListRequirements(ctx, initiativeID)
	reqs := activeRequirements(allReqs)
	allDecs, _ := e.db.ListDecisions(ctx, initiativeID)
	decs := activeDecisions(allDecs)
	questions, _ := e.db.ListQuestions(ctx, initiativeID)
	reqByCode := requirementCodeIndex(reqs)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a product design (flows, screens, interaction model) that traces every flow to a user goal or Requirement. Raise a design Decision only for a genuine interaction tradeoff. A decided human decision is authoritative and an already-answered question is settled: do not propose or imply anything that contradicts either, and do not re-raise a question that already exists (open or answered) as a new decision. When a decision is about a specific Requirement, set its 'requirements' field to that requirement's code(s) (e.g. \"REQ-2\")."},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decs)},
		{Selector: "questions", Content: e.renderQuestionsWithAnswers(questions)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	e.setStage(ctx, initiativeID, "Designing the product")
	runID, obj, err := e.runModel(ctx, initiativeID, StageProductDesign, RoleProductDesigner, "product_design", schema.ProductDesign, inputs, "Produce the product design.")
	if err != nil {
		return err
	}
	if err := e.storeArtifact(ctx, initiativeID, domain.ArtifactDesignSpec, runID, renderProductDesign(obj)); err != nil {
		return err
	}
	for _, d := range objSlice(obj["decisions"]) {
		reqIDs := designDecisionRequirementIDs(d, reqByCode)
		if err := e.createDesignDecision(ctx, initiativeID, RoleProductDesigner, domain.DecisionDesign, str(d["question"]), strSlice(d["options"]), str(d["recommendation"]), reqIDs); err != nil {
			return err
		}
	}
	return nil
}

// requirementCodeIndex maps a normalized requirement code to its id, for
// resolving a design-stage decision's `requirements` references the same
// tolerant way runExecutionPlan resolves a task's.
func requirementCodeIndex(reqs []domain.Requirement) map[string]string {
	reqByCode := make(map[string]string, len(reqs))
	for _, r := range reqs {
		reqByCode[NormalizeRequirementRef(r.Code)] = r.ID
	}
	return reqByCode
}

func (e *Engine) runVerificationDesign(ctx context.Context, initiativeID string) error {
	allReqs, _ := e.db.ListRequirements(ctx, initiativeID)
	reqs := activeRequirements(allReqs)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a verification plan: for every requirement, name a concrete verification hook (test type, deterministic check, or manual gate) and the evidence it will produce. Prefer deterministic evidence over LLM judgment."},
		{Selector: "requirements", Content: renderRequirements(reqs)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	e.setStage(ctx, initiativeID, "Designing verification")
	runID, obj, err := e.runModel(ctx, initiativeID, StageVerificationDesign, RoleVerificationEngineer, "verification_plan", schema.VerificationPlan, inputs, "Produce the verification plan.")
	if err != nil {
		return err
	}
	return e.storeArtifact(ctx, initiativeID, domain.ArtifactVerificationPlan, runID, renderVerificationPlan(obj))
}

func (e *Engine) runExecutionPlan(ctx context.Context, initiativeID string) error {
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	allReqs, _ := e.db.ListRequirements(ctx, initiativeID)
	reqs := activeRequirements(allReqs)
	allDecs, _ := e.db.ListDecisions(ctx, initiativeID)
	decs := activeDecisions(allDecs)

	// reqByCode resolves against ACTIVE requirements only (review A2): a task
	// citing a superseded requirement's code must not resolve, the same way a
	// code that never existed must not resolve -- both are unresolvable
	// references now (review A4 #2/#4), not silently dropped.
	reqByCode := map[string]string{}
	for _, r := range reqs {
		reqByCode[NormalizeRequirementRef(r.Code)] = r.ID
	}

	scale := projectScaleOf(ctx, e.workspaceRoot)
	rigor := calibrateRigor(RigorLevel(e.snapshotConfig().cfg.Review.Rigor), scale, changeSize{Requirements: len(reqs)})
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Decompose the converged design into 2-4 Tasks, each shipping a working vertical slice end to end -- not one task per layer or per requirement. A Task implements one or more Requirements (referenced by code); it is fine, and expected, for one task to implement several. Only go finer than 2-4 tasks when the plan is genuinely that granular (many independent, unrelated slices). A decided human decision is authoritative and must not be contradicted. Keep the dependency graph acyclic; list dependencies by task title. For each task, declare every environment variable its code will read from process.env under env_vars: the exact name (e.g. STRIPE_SECRET_KEY), one sentence saying how the human obtains it, and the URL of the page where they create/read it. Omit env_vars entirely when a task needs none."},
		{Selector: "project_scale", Content: scaleAndConventions(scale, changeSize{Requirements: len(reqs)}, rigor, projectConventions(e.workspaceRoot))},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decs)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	// The human's revision note, when this plan is a revision they asked for
	// with one (RevisePlan). Only ever set for a revise-plan: advance never
	// sets a note override, and the override is cleared when RevisePlan returns.
	note := strings.TrimSpace(e.currentNoteOverride(initiativeID))
	if note != "" {
		inputs = append(inputs, ccontext.Input{Selector: "human_note", Content: "The human asked for this revision of the plan: " + note})
	}
	e.setStage(ctx, initiativeID, "Planning the execution")
	runID, obj, err := e.runModel(ctx, initiativeID, StageExecutionPlan, RoleEngineeringLead, "execution_plan", schema.ExecutionPlan, inputs, "Produce the execution plan.")
	if err != nil {
		return err
	}
	if note != "" {
		// Best effort, like execute's: the run says what steered it, and a
		// failure to record that must never fail the plan itself.
		if err := e.db.SetRunNote(ctx, runID, note); err != nil && e.log != nil {
			e.log.Warn("could not record the revision note on the run", "initiative", initiativeID, "run", runID, "error", err)
		}
	}
	if err := e.storeArtifact(ctx, initiativeID, domain.ArtifactExecutionPlan, runID, renderExecutionPlan(obj)); err != nil {
		return err
	}

	// Two-pass task creation so dependency references resolve regardless of order,
	// with normalized title matching (full-platform review: task-dependency
	// ordering — backward/typo'd references must not be silently dropped).
	norm := normTaskTitle
	// Codes RevisePlan asks to keep: a regenerated task with the same title as
	// one it replaced keeps that task's code (see RevisePlan). Each is used at
	// most once, so two new tasks with one old title cannot share a code.
	keepCodes := e.keptTaskCodes(initiativeID)
	type pending struct {
		task      *domain.Task
		depTitles []string
	}
	titleToID := map[string]string{}
	var pendings []pending
	var unresolvedReqs []string
	for _, t := range objSlice(obj["tasks"]) {
		title := str(t["title"])
		if title == "" {
			continue
		}
		task := &domain.Task{
			InitiativeID:  initiativeID,
			Title:         e.redact(title),
			Statement:     e.redact(str(t["statement"])),
			Priority:      domain.RequirementPriority(str(t["priority"])),
			Estimate:      str(t["estimate"]),
			ProducedByRun: runID,
			EnvVars:       envVarsOf(t["env_vars"]),
		}
		if code, ok := keepCodes[norm(task.Title)]; ok {
			task.Code = code
			delete(keepCodes, norm(task.Title))
		}

		// N:M requirement resolution (review A4 #4): the model may name several
		// requirement codes via `requirements`; `requirement` (singular) is kept
		// for compatibility. Tolerant matching -- trim, case-insensitive, and a
		// "REQ-3: title" form -- via NormalizeRequirementRef. A reference that
		// still does not resolve fails loudly, exactly as an unresolved
		// depends_on already does, rather than silently dropping the edge (the
		// root cause: reqByCode[miss] used to be "", and createTask skipped
		// wiring the edge on an empty target id with nothing recorded anywhere).
		var reqIDs []string
		seenReq := map[string]bool{}
		for _, ref := range requirementRefs(t) {
			id, ok := reqByCode[NormalizeRequirementRef(ref)]
			if !ok {
				unresolvedReqs = append(unresolvedReqs, ref)
				continue
			}
			if !seenReq[id] {
				seenReq[id] = true
				reqIDs = append(reqIDs, id)
			}
		}

		// Create without deps; edges are added in the second pass.
		if err := e.db.CreateTaskMulti(ctx, task, reqIDs, nil); err != nil {
			return err
		}
		titleToID[norm(title)] = task.ID
		pendings = append(pendings, pending{task: task, depTitles: strSlice(t["depends_on"])})
	}

	var unresolvedDeps []string
	for _, p := range pendings {
		var depIDs []string
		for _, depTitle := range p.depTitles {
			if id, ok := titleToID[norm(depTitle)]; ok && id != p.task.ID {
				depIDs = append(depIDs, id)
			} else {
				unresolvedDeps = append(unresolvedDeps, depTitle)
			}
		}
		if err := e.db.AddTaskDependencies(ctx, initiativeID, p.task.ID, depIDs); err != nil {
			return err
		}
	}

	if len(unresolvedReqs) > 0 {
		if err := e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   initiativeID,
			TargetType:     PlanFindingTarget,
			TargetID:       initiativeID,
			Severity:       domain.SeverityHigh,
			Confidence:     domain.ConfidenceHigh,
			Category:       "plan_traceability",
			Finding:        e.redact("unresolved requirement reference(s): " + strings.Join(unresolvedReqs, ", ")),
			Evidence:       "a task's requirement reference did not match any active requirement code",
			Impact:         "the task cannot be traced to a live requirement, and its edge was not wired",
			Recommendation: "correct the requirement code(s) (it may cite a superseded requirement) and re-run the execution plan",
		}); err != nil {
			return err
		}
	}
	if len(unresolvedDeps) > 0 {
		if err := e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   initiativeID,
			TargetType:     PlanFindingTarget,
			TargetID:       initiativeID,
			Severity:       domain.SeverityHigh,
			Confidence:     domain.ConfidenceHigh,
			Category:       "plan_dependency",
			Finding:        e.redact("unresolved task dependency reference(s): " + strings.Join(unresolvedDeps, ", ")),
			Evidence:       "a task's depends_on referenced a title that does not match any task",
			Impact:         "the dependency graph is incomplete and cannot be executed safely",
			Recommendation: "correct the dependency titles and re-run the execution plan",
		}); err != nil {
			return err
		}
	}
	if len(unresolvedReqs) > 0 || len(unresolvedDeps) > 0 {
		return errUnresolvedDependencies
	}
	return nil
}

// requirementRefs collects a task's raw requirement references from the
// model's output: the plural `requirements` array (review A4 #4) plus the
// singular `requirement` field kept for compatibility. Order is
// requirement-first so a single-requirement plan (the common case, and every
// existing scripted/demo response) behaves exactly as before.
func requirementRefs(t map[string]any) []string {
	var out []string
	if s := strings.TrimSpace(str(t["requirement"])); s != "" {
		out = append(out, s)
	}
	for _, s := range strSlice(t["requirements"]) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// NormalizeRequirementRef normalizes a model- or human-supplied requirement
// reference for tolerant matching against a Requirement.Code (review A4 #4):
// trimmed, upper-cased, and a "REQ-3: title" form reduced to the code prefix
// before the colon. Exported so the daemon's task-editing handlers (`add
// task` / `update task`) apply the identical tolerance.
func NormalizeRequirementRef(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, ":"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.ToUpper(s)
}

// DeterministicPlanCheckRun is the ReviewRunID stamped on every finding
// deterministicPlanChecks produced, distinguishing them from the adversarial
// reviewer's, whose run id runModel supplies. The daemon reads it to refuse
// `approve-plan` over a structural defect a human cannot meaningfully accept.
// The PRD loop's equivalent literal is "deterministic-check", deliberately
// different, so the two sets cannot be confused.
const DeterministicPlanCheckRun = "deterministic-plan-check"

// PlanFindingTarget is the target type of every finding raised against the
// execution plan. The UI shows these on the Plan page, where approval happens.
const PlanFindingTarget = "execution_plan"

func (e *Engine) runExecutionPlanReview(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	tasks, _ := e.db.ListTasks(ctx, initiativeID)
	scale := projectScaleOf(ctx, e.workspaceRoot)
	rigor := calibrateRigor(RigorLevel(e.snapshotConfig().cfg.Review.Rigor), scale, changeSize{Requirements: len(tasks)})

	rules := e.snapshotConfig().cfg.Review.Convergence.Effective()
	guarded := !rules.Legacy()

	det := e.deterministicPlanChecks(ctx, initiativeID)
	reviewRunID, adv, err := e.runPlanAdversarialReview(ctx, initiativeID, tasks, rigor, rules)
	if err != nil {
		return domain.InitiativeFailed, err
	}

	// C2: rigor only ever relaxes the ADVERSARIAL reviewer's findings.
	// Deterministic findings (a dependency cycle, an untraced task, a
	// reference to a superseded requirement) must always go through
	// EvaluateApproval / needs_human_judgment, at every rigor -- quick must
	// never auto-approve a plan with a structural defect just because the
	// vertical slice produced few enough tasks to calibrate to quick.
	//
	// C1: when quick DOES accept the adversarial reviewer's findings, they
	// must be marked Advisory=true before they are capped and persisted.
	// Leaving them unmarked (the old behaviour) saved them as plain open
	// blockers: approval would go through here, and `idea verify`/complete
	// would then refuse forever with no way to un-stick the initiative.
	if rigor == RigorQuick {
		for i := range adv {
			adv[i].Advisory = true
		}
	}

	// The plan review is a single pass, so of the convergence rules only the
	// per-finding ones apply here: the severity gate and the citation rule
	// (a plan finding must name the TASK or REQ it is about). The plan
	// review parked 2 of 2 measured real-model runs on blocking findings,
	// and a finding that names no task or requirement is not one a human
	// re-planning can act on either. There is no prior cycle, no re-raise
	// and no stall -- RevisePlan starts a fresh plan, not a revision of
	// this one.
	tel := newReviewTelemetry("execution_plan", 1, rules, rigor)
	cls := classification{reRaiseOf: map[int]int{}, demoted: map[string]int{}, cites: make([]string, len(adv))}
	if guarded {
		allReqs, _ := e.db.ListRequirements(ctx, initiativeID)
		taskCodes := make(map[string]bool, len(tasks))
		for _, t := range tasks {
			taskCodes[NormalizeRequirementRef(t.Code)] = true
		}
		ini, _ := e.db.GetInitiative(ctx, initiativeID)
		cls = classifyFindings(classifyInput{
			rules: rules, active: activeRequirements(allReqs), taskCodes: taskCodes, idea: ini.Description,
		}, adv)
		// Every plan finding targets the plan (see the loop below); the
		// requirement a citation resolved to is the PRD loop's re-targeting,
		// not this review's, so it is undone here rather than left as a
		// requirement id under an execution_plan target type.
		for i := range adv {
			adv[i].TargetType, adv[i].TargetID = PlanFindingTarget, initiativeID
		}
	}
	countCycle(&tel, adv, cls, nil)

	all := append(det, adv...)
	all = capFindings(all, findingCap(rigor), rigor)
	for i := range all {
		all[i].InitiativeID = initiativeID
		if all[i].ReviewRunID == "" {
			all[i].ReviewRunID = DeterministicPlanCheckRun
		}
		// Every finding of the plan review targets the plan. The deterministic
		// ones had no target at all, and the adversarial reviewer's defaulted
		// to "requirement", so the UI -- which places a finding by its target
		// -- showed plan findings on the PRD page and a CRITICAL dependency
		// cycle, the one finding that blocks approve-plan, on no page at all.
		all[i].TargetType = PlanFindingTarget
		if all[i].TargetID == "" {
			all[i].TargetID = initiativeID
		}
		all[i].Finding = e.redact(all[i].Finding)
		all[i].Evidence = e.redact(all[i].Evidence)
		all[i].Impact = e.redact(all[i].Impact)
		all[i].Recommendation = e.redact(all[i].Recommendation)
		all[i].Note = e.redact(all[i].Note)
	}

	// ICP re-run defect A: RevisePlan/ReopenPhase regenerate the plan and
	// re-run this review from scratch -- a legitimate re-review that
	// disposes prior OPEN findings as not_applicable (see their own doc
	// comments) but leaves an already-dispositioned one exactly as the human
	// left it, with no link to whatever the fresh pass raises. Carry a
	// human's disposition forward onto its near-duplicate here too, so a
	// finding the plan reviewer merely rewords does not re-litigate a
	// judgment call already made. After redaction, so it compares
	// like-for-like against prior findings (already redacted before they
	// were persisted).
	if err := e.inheritDispositionFromPrior(ctx, initiativeID, all); err != nil {
		return domain.InitiativeFailed, err
	}
	for i := range all {
		if err := e.db.CreateFinding(ctx, &all[i]); err != nil {
			return domain.InitiativeFailed, err
		}
	}

	// blocked now reflects only non-advisory blocking findings: at quick,
	// that means exactly the deterministic ones (the adversarial reviewer's
	// were just marked Advisory above), and at standard/thorough it means
	// any of them. Either way it must always route to human judgment --
	// there is no rigor level at which a structural plan defect is
	// auto-approved.
	blocked, reason := review.EvaluateApproval(all)
	tel.OpenBlocking = countGating(all)
	tel.Deterministic = countGating(det)
	tel.Outcome = outcomeConverged
	if blocked {
		tel.Outcome = outcomeBlocked
	}
	e.recordReviewTelemetry(ctx, reviewRunID, tel)
	if blocked {
		e.log.Warn("plan review blocked", "initiative", initiativeID, "reason", reason)
		if err := e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPlanReview, RoleAdversarialReviewer); err != nil {
			return domain.InitiativeFailed, err
		}
		return domain.InitiativeNeedsHumanJudgment, nil
	}
	if rigor == RigorQuick && len(adv) > 0 {
		// Quick converges in one pass and accepts with advisories (review
		// B1): the findings stand but do not gate plan approval.
		e.log.Info("plan review passed (quick, advisory)", "initiative", initiativeID, "findings", len(all))
	}
	if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativePlanApproved, RoleAdversarialReviewer); err != nil {
		return domain.InitiativeFailed, err
	}
	return domain.InitiativePlanApproved, nil
}

// deterministicPlanChecks runs plan-specific structural checks (§B4): acyclic
// dependencies, every task traced to a requirement, no task or plan text
// citing a superseded requirement code (review A2 #3, blocking), and no task
// whose own words contradict a decided decision's resolution more than they
// agree with it (review A2 #3, advisory only -- keyword overlap is a
// heuristic, not a semantic check, and is documented as such on the finding).
func (e *Engine) deterministicPlanChecks(ctx context.Context, initiativeID string) []domain.Finding {
	var out []domain.Finding
	tasks, err := e.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return out
	}

	allReqs, _ := e.db.ListRequirements(ctx, initiativeID)
	var supersededCodes []string
	for _, r := range allReqs {
		if r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
			supersededCodes = append(supersededCodes, r.Code)
		}
	}
	decs, _ := e.db.ListDecisions(ctx, initiativeID)

	deps := map[string][]string{}
	implemented := map[string]bool{}
	for _, t := range tasks {
		edges, _ := e.db.ListGraphEdgesFrom(ctx, initiativeID, "task", t.ID)
		hasImplements := false
		for _, ed := range edges {
			switch ed.EdgeType {
			case domain.EdgeDependsOn:
				deps[t.ID] = append(deps[t.ID], ed.ToID)
			case domain.EdgeImplements:
				hasImplements = true
				implemented[ed.ToID] = true
			case domain.EdgeVerifies:
				hasImplements = true
			}
		}
		if !hasImplements {
			out = append(out, domain.Finding{
				Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
				Category:       "traceability",
				Finding:        fmt.Sprintf("%s has no implements edge to a requirement", t.Code),
				Evidence:       t.Title,
				Impact:         "untraced work cannot be verified against the PRD",
				Recommendation: "trace the task to a requirement",
			})
		}

		combined := t.Title + " " + t.Statement
		for _, code := range supersededCodes {
			if mentionsCode(combined, code) {
				out = append(out, domain.Finding{
					Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
					Category:       "plan_traceability",
					Finding:        fmt.Sprintf("%s cites superseded requirement %s", t.Code, code),
					Evidence:       combined,
					Impact:         "the plan is built in part on a requirement that no longer applies",
					Recommendation: "remove the reference or re-plan against the active requirement",
				})
			}
		}

		for _, d := range decs {
			if contradictsDecision(combined, d) {
				out = append(out, domain.Finding{
					Severity: domain.SeverityMedium, Confidence: domain.ConfidenceLow,
					Category:       "plan_traceability",
					Finding:        fmt.Sprintf("%s may contradict decided %s", t.Code, d.Code),
					Evidence:       combined,
					Impact:         "heuristic keyword match against a rejected option of the decision; verify manually",
					Recommendation: fmt.Sprintf("confirm the task aligns with %s's resolution", d.Code),
					Note:           "heuristic: keyword overlap only, not a semantic check",
				})
			}
		}
	}
	if cycle := detectCycle(deps); cycle != "" {
		out = append(out, domain.Finding{
			Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
			Category:       "plan",
			Finding:        "task dependency cycle detected: " + cycle,
			Impact:         "the plan cannot be scheduled",
			Recommendation: "break the cycle",
		})
	}
	// Coverage in the other direction: every live requirement must be
	// implemented by at least one task. A verification task's EdgeVerifies does
	// not count — verifying is not implementing. (review: the task→requirement
	// direction above already flags orphan tasks; this closes the requirement→
	// task direction that previously only `idea gaps` reported, non-blocking.)
	for _, r := range allReqs {
		if r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
			continue
		}
		if !implemented[r.ID] {
			// Medium, not high: a requirement legitimately served by a
			// verification task (EdgeVerifies) or by design/verification rather
			// than a dedicated coding task has no implements edge, and blocking
			// plan approval on that is too strict (the eval's scripted plans
			// would never converge). This surfaces in the plan review and
			// `idea gaps` without blocking.
			out = append(out, domain.Finding{
				Severity: domain.SeverityMedium, Confidence: domain.ConfidenceHigh,
				Category:       "traceability",
				Finding:        fmt.Sprintf("%s has no implementing task", r.Code),
				Evidence:       r.Statement,
				Impact:         "an unimplemented requirement ships incomplete",
				Recommendation: "add a task that implements it, or remove the requirement from the PRD",
			})
		}
	}
	return out
}

// mentionsCode reports whether text references code as a whole token, so
// "REQ-2" does not match inside "REQ-20" or "XREQ-2". Deliberately simple
// (byte-level, case-folded): a code has a fixed, narrow alphabet, so this
// needs no general tokenizer.
func mentionsCode(text, code string) bool {
	if code == "" {
		return false
	}
	upper := strings.ToUpper(text)
	codeUpper := strings.ToUpper(code)
	isWordByte := func(b byte) bool {
		return b == '-' || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	for start := 0; ; {
		i := strings.Index(upper[start:], codeUpper)
		if i < 0 {
			return false
		}
		matchStart := start + i
		matchEnd := matchStart + len(codeUpper)
		before := byte(0)
		if matchStart > 0 {
			before = upper[matchStart-1]
		}
		after := byte(0)
		if matchEnd < len(upper) {
			after = upper[matchEnd]
		}
		if !isWordByte(before) && !isWordByte(after) {
			return true
		}
		start = matchStart + 1
	}
}

// contradictsDecision is a deliberately simple heuristic (review A2 #3: "the
// resolution keyword check is advisory only"): a task's own words overlap
// more with a REJECTED option of a decided decision than with the option
// actually chosen. Keyword overlap proves nothing about semantics -- a task
// can legitimately mention a rejected alternative to explain why it was not
// used -- which is why every finding this produces is advisory (medium/low),
// never blocking, and carries a Note saying so.
func contradictsDecision(taskText string, d domain.Decision) bool {
	if d.Status != domain.DecisionDecided || strings.TrimSpace(d.Resolution) == "" {
		return false
	}
	taskTokens := tokenize(taskText)
	if len(taskTokens) == 0 {
		return false
	}
	taskSet := make(map[string]bool, len(taskTokens))
	for _, tok := range taskTokens {
		taskSet[tok] = true
	}
	resolutionOverlap := overlapCount(taskSet, tokenize(d.Resolution))
	for _, opt := range d.Options {
		if normalizeJudgment(opt) == normalizeJudgment(d.Resolution) {
			continue // the chosen option itself, not a rejected one
		}
		if n := overlapCount(taskSet, tokenize(opt)); n > 0 && n > resolutionOverlap {
			return true
		}
	}
	return false
}

// overlapCount counts how many tokens appear in both set and tokens.
func overlapCount(set map[string]bool, tokens []string) int {
	n := 0
	for _, t := range tokens {
		if set[t] {
			n++
		}
	}
	return n
}

func (e *Engine) runPlanAdversarialReview(ctx context.Context, initiativeID string, tasks []domain.Task, rigor RigorLevel, rules config.ConvergenceConfig) (string, []domain.Finding, error) {
	scale := projectScaleOf(ctx, e.workspaceRoot)
	conventions := projectConventions(e.workspaceRoot)
	// "plan_review", not "review". The name is a demo/test discriminator only:
	// JSONSchemaName is read by nothing but the fake provider. Real providers
	// never see it at all -- runModel inlines the schema text into the system
	// prompt and sets the provider's own JSON mode. All three adversarial
	// reviews sharing one key
	// meant a queued demo response could outlive the review it was meant for and
	// land on the change-set review, where a blocking finding cannot be cleared
	// by anything the CLI can do. Separate keys make a surplus harmless.
	e.setStage(ctx, initiativeID, "Reviewing the plan")
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Independently challenge the execution plan. Emit structured findings. Never set accepted_risk."},
		{Selector: "project_scale", Content: scaleAndConventions(scale, changeSize{Requirements: len(tasks)}, rigor, conventions)},
		{Selector: "tasks", Content: renderTasks(tasks)},
	}
	if !rules.Legacy() {
		// The plan reviewer never got the PRD reviewer's severity calibration
		// (reviewerSeverityGuidance is PRD-only), so "high" meant whatever the
		// model felt it meant. The convergence policy states the bar and the
		// citation rule the review is judged under.
		inputs = append(inputs, ccontext.Input{Selector: "convergence_policy", Content: reviewerConvergencePolicy(rules, "a TASK code from the tasks below, or the REQ code it implements")})
	}
	runID, obj, err := e.runModel(ctx, initiativeID, StageExecutionPlanReview, RoleAdversarialReviewer, "plan_review", schema.Review,
		inputs, "Adversarial review of the execution plan.")
	if err != nil {
		return runID, nil, err
	}
	findings, err := review.ParseFindings(runID, "execution_plan", initiativeID, obj)
	return runID, findings, err
}

// detectCycle returns the first node id that participates in a cycle, or "".
// It delegates to the single canonical detector in the store package so the
// plan checks and the doctor audit share one implementation.
func detectCycle(deps map[string][]string) string {
	return store.DetectTaskCycle(deps)
}

// suggestAnswersChunkSize bounds how many open questions are answered per model
// call, so a large question set cannot overflow the output token budget.
const suggestAnswersChunkSize = 12

// SuggestAnswers asks the product manager to propose a concise answer for every
// open question, grounded in the project's intent and purpose. Questions are
// answered in bounded chunks, and each chunk's answers are emitted via `emit`
// as they complete (so callers can persist progress incrementally).
func (e *Engine) SuggestAnswers(ctx context.Context, initiativeID string, emit func([]domain.SuggestedAnswer)) error {
	questions, err := e.db.ListQuestions(ctx, initiativeID)
	if err != nil {
		return err
	}
	var open []domain.Question
	for _, q := range questions {
		if q.Status == domain.QuestionOpen {
			open = append(open, q)
		}
	}
	if len(open) == 0 {
		return nil
	}

	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	for start := 0; start < len(open); start += suggestAnswersChunkSize {
		end := start + suggestAnswersChunkSize
		if end > len(open) {
			end = len(open)
		}
		answers, err := e.suggestAnswersChunk(ctx, initiativeID, ini.Description, open[start:end])
		if err != nil {
			return err
		}
		if len(answers) > 0 {
			emit(answers)
		}
	}
	return nil
}

func (e *Engine) suggestAnswersChunk(ctx context.Context, initiativeID, description string, chunk []domain.Question) ([]domain.SuggestedAnswer, error) {
	var qtexts []string
	for _, q := range chunk {
		qtexts = append(qtexts, q.Question)
	}
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Propose a concise, concrete answer for each open question, grounded in the project's intent and purpose. Answer as the product manager. Echo each question exactly."},
		{Selector: "task", Content: description},
		{Selector: "open_questions", Content: strings.Join(qtexts, "\n")},
	}
	_, obj, err := e.runModel(ctx, initiativeID, "suggest_answers", RoleProductManager, "answers", schema.Answers, inputs, "Answer the open questions.")
	if err != nil {
		return nil, err
	}

	out := make([]domain.SuggestedAnswer, 0)
	for _, a := range objSlice(obj["answers"]) {
		qtext := str(a["question"])
		answer := str(a["answer"])
		norm := normalizeJudgment(qtext)
		for _, q := range chunk {
			if normalizeJudgment(q.Question) == norm {
				out = append(out, domain.SuggestedAnswer{QuestionID: q.ID, Question: q.Question, Answer: answer})
				break
			}
		}
	}
	return out, nil
}

// normTaskTitle is the title comparison task-dependency resolution and
// RevisePlan's code reuse share: case- and whitespace-insensitive.
func normTaskTitle(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// setKeepTaskCodes records (or, with nil, clears) the title->code map
// runExecutionPlan reuses for the initiative's regenerated tasks.
func (e *Engine) setKeepTaskCodes(initiativeID string, codes map[string]string) {
	e.noteMu.Lock()
	defer e.noteMu.Unlock()
	if codes == nil {
		delete(e.keepTaskCodes, initiativeID)
		return
	}
	if e.keepTaskCodes == nil {
		e.keepTaskCodes = map[string]map[string]string{}
	}
	e.keepTaskCodes[initiativeID] = codes
}

// keptTaskCodes returns a copy of the map setKeepTaskCodes recorded, so the
// caller can consume entries without touching the shared one.
func (e *Engine) keptTaskCodes(initiativeID string) map[string]string {
	e.noteMu.Lock()
	defer e.noteMu.Unlock()
	out := map[string]string{}
	for k, v := range e.keepTaskCodes[initiativeID] {
		out[k] = v
	}
	return out
}
