package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	// Defensive: plan revision is a pre-execution action — refuse once any task
	// has produced work.
	cses, err := e.db.ListChangeSets(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	if changeSetsHoldingWork(cses) > 0 {
		return domain.InitiativeFailed, fmt.Errorf("cannot revise a plan that already has change sets")
	}
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

// storeArtifact renders a body into the BlobStore and records an artifact family
// + first version (immutable, produced by run).
func (e *Engine) storeArtifact(ctx context.Context, initiativeID string, kind domain.ArtifactKind, runID, body string) error {
	bodyDigest := e.storeBlob(body)
	art := &domain.Artifact{InitiativeID: initiativeID, Kind: kind}
	ver := &domain.ArtifactVersion{BodyDigest: bodyDigest, ProducedByRun: runID, Status: "draft"}
	return e.db.CreateArtifact(ctx, art, ver)
}

// createDesignDecision records a design-stage decision. If an equivalent
// decision already exists (raised by another role), the new role's position is
// recorded as a dissent instead of a duplicate — dissent is never erased (§8).
func (e *Engine) createDesignDecision(ctx context.Context, initiativeID, roleName string, dtype domain.DecisionType, question string, options []string, recommendation string) error {
	norm := normalizeJudgment(question)
	if norm == "" {
		return nil
	}
	existing, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, d := range existing {
		if normalizeJudgment(d.Question) == norm {
			if recommendation != "" && d.Recommendation != recommendation {
				return e.db.CreateDissent(ctx, &domain.DecisionDissent{
					DecisionID: d.ID, ProponentRole: roleName, Position: e.redact(recommendation),
				})
			}
			return nil
		}
	}
	return e.db.CreateDecision(ctx, &domain.Decision{
		InitiativeID: initiativeID, Type: dtype, Question: e.redact(question),
		Options: e.redactSlice(options), Recommendation: e.redact(recommendation),
	})
}

func (e *Engine) runArchitecture(ctx context.Context, initiativeID string) error {
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	reqs, _ := e.db.ListRequirements(ctx, initiativeID)
	decs, _ := e.db.ListDecisions(ctx, initiativeID)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a technical design that decomposes the PRD into components with clear responsibilities and tradeoffs. Raise an architecture Decision only for a genuine tradeoff."},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decs)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	runID, obj, err := e.runModel(ctx, initiativeID, StageArchitecture, RoleArchitect, "architecture", schema.Architecture, inputs, "Produce the architecture.")
	if err != nil {
		return err
	}
	if err := e.storeArtifact(ctx, initiativeID, domain.ArtifactTechnicalDesign, runID, renderArchitecture(obj)); err != nil {
		return err
	}
	for _, d := range objSlice(obj["decisions"]) {
		if err := e.createDesignDecision(ctx, initiativeID, RoleArchitect, domain.DecisionArchitecture, str(d["question"]), strSlice(d["options"]), str(d["recommendation"])); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runProductDesign(ctx context.Context, initiativeID string) error {
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	reqs, _ := e.db.ListRequirements(ctx, initiativeID)
	decs, _ := e.db.ListDecisions(ctx, initiativeID)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a product design (flows, screens, interaction model) that traces every flow to a user goal or Requirement. Raise a design Decision only for a genuine interaction tradeoff."},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decs)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	runID, obj, err := e.runModel(ctx, initiativeID, StageProductDesign, RoleProductDesigner, "product_design", schema.ProductDesign, inputs, "Produce the product design.")
	if err != nil {
		return err
	}
	if err := e.storeArtifact(ctx, initiativeID, domain.ArtifactDesignSpec, runID, renderProductDesign(obj)); err != nil {
		return err
	}
	for _, d := range objSlice(obj["decisions"]) {
		if err := e.createDesignDecision(ctx, initiativeID, RoleProductDesigner, domain.DecisionDesign, str(d["question"]), strSlice(d["options"]), str(d["recommendation"])); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runVerificationDesign(ctx context.Context, initiativeID string) error {
	reqs, _ := e.db.ListRequirements(ctx, initiativeID)
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Produce a verification plan: for every requirement, name a concrete verification hook (test type, deterministic check, or manual gate) and the evidence it will produce. Prefer deterministic evidence over LLM judgment."},
		{Selector: "requirements", Content: renderRequirements(reqs)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	runID, obj, err := e.runModel(ctx, initiativeID, StageVerificationDesign, RoleVerificationEngineer, "verification_plan", schema.VerificationPlan, inputs, "Produce the verification plan.")
	if err != nil {
		return err
	}
	return e.storeArtifact(ctx, initiativeID, domain.ArtifactVerificationPlan, runID, renderVerificationPlan(obj))
}

func (e *Engine) runExecutionPlan(ctx context.Context, initiativeID string) error {
	ini, _ := e.db.GetInitiative(ctx, initiativeID)
	reqs, _ := e.db.ListRequirements(ctx, initiativeID)
	decs, _ := e.db.ListDecisions(ctx, initiativeID)

	reqByCode := map[string]string{}
	for _, r := range reqs {
		reqByCode[r.Code] = r.ID
	}

	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "Decompose the converged design into an ordered, dependency-linked set of Tasks. Each Task implements exactly one Requirement (referenced by its code). Keep the dependency graph acyclic; list dependencies by task title."},
		{Selector: "task", Content: ini.Description},
		{Selector: "requirements", Content: renderRequirements(reqs)},
		{Selector: "decisions", Content: renderDecisions(decs)},
	}
	if docs := e.contextDocs(ctx, initiativeID); docs != "" {
		inputs = append(inputs, ccontext.Input{Selector: "context_docs", Content: docs})
	}
	runID, obj, err := e.runModel(ctx, initiativeID, StageExecutionPlan, RoleEngineeringLead, "execution_plan", schema.ExecutionPlan, inputs, "Produce the execution plan.")
	if err != nil {
		return err
	}
	if err := e.storeArtifact(ctx, initiativeID, domain.ArtifactExecutionPlan, runID, renderExecutionPlan(obj)); err != nil {
		return err
	}

	// Two-pass task creation so dependency references resolve regardless of order,
	// with normalized title matching (full-platform review: task-dependency
	// ordering — backward/typo'd references must not be silently dropped).
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	type pending struct {
		task      *domain.Task
		depTitles []string
	}
	titleToID := map[string]string{}
	var pendings []pending
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
		}
		// Create without deps; edges are added in the second pass.
		if err := e.db.CreateTask(ctx, task, reqByCode[str(t["requirement"])], nil); err != nil {
			return err
		}
		titleToID[norm(title)] = task.ID
		pendings = append(pendings, pending{task: task, depTitles: strSlice(t["depends_on"])})
	}

	var unresolved []string
	for _, p := range pendings {
		var depIDs []string
		for _, depTitle := range p.depTitles {
			if id, ok := titleToID[norm(depTitle)]; ok && id != p.task.ID {
				depIDs = append(depIDs, id)
			} else {
				unresolved = append(unresolved, depTitle)
			}
		}
		if err := e.db.AddTaskDependencies(ctx, initiativeID, p.task.ID, depIDs); err != nil {
			return err
		}
	}

	if len(unresolved) > 0 {
		if err := e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   initiativeID,
			TargetType:     PlanFindingTarget,
			TargetID:       initiativeID,
			Severity:       domain.SeverityHigh,
			Confidence:     domain.ConfidenceHigh,
			Category:       "plan_dependency",
			Finding:        e.redact("unresolved task dependency reference(s): " + strings.Join(unresolved, ", ")),
			Evidence:       "a task's depends_on referenced a title that does not match any task",
			Impact:         "the dependency graph is incomplete and cannot be executed safely",
			Recommendation: "correct the dependency titles and re-run the execution plan",
		}); err != nil {
			return err
		}
		return errUnresolvedDependencies
	}
	return nil
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
	det := e.deterministicPlanChecks(ctx, initiativeID)
	adv, err := e.runPlanAdversarialReview(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	all := append(det, adv...)
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
		if err := e.db.CreateFinding(ctx, &all[i]); err != nil {
			return domain.InitiativeFailed, err
		}
	}

	blocked, reason := review.EvaluateApproval(all)
	if blocked {
		e.log.Warn("plan review blocked", "initiative", initiativeID, "reason", reason)
		if err := e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentPlanReview, RoleAdversarialReviewer); err != nil {
			return domain.InitiativeFailed, err
		}
		return domain.InitiativeNeedsHumanJudgment, nil
	}
	if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativePlanApproved, RoleAdversarialReviewer); err != nil {
		return domain.InitiativeFailed, err
	}
	return domain.InitiativePlanApproved, nil
}

// deterministicPlanChecks runs plan-specific structural checks (§B4): acyclic
// dependencies and every task traced to a requirement.
func (e *Engine) deterministicPlanChecks(ctx context.Context, initiativeID string) []domain.Finding {
	var out []domain.Finding
	tasks, err := e.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return out
	}

	deps := map[string][]string{}
	for _, t := range tasks {
		edges, _ := e.db.ListGraphEdgesFrom(ctx, initiativeID, "task", t.ID)
		hasImplements := false
		for _, ed := range edges {
			switch ed.EdgeType {
			case domain.EdgeDependsOn:
				deps[t.ID] = append(deps[t.ID], ed.ToID)
			case domain.EdgeImplements, domain.EdgeVerifies:
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
	return out
}

func (e *Engine) runPlanAdversarialReview(ctx context.Context, initiativeID string) ([]domain.Finding, error) {
	tasks, _ := e.db.ListTasks(ctx, initiativeID)
	// "plan_review", not "review". The name is a demo/test discriminator only:
	// JSONSchemaName is read by nothing but the fake provider. Real providers
	// never see it at all -- runModel inlines the schema text into the system
	// prompt and sets the provider's own JSON mode. All three adversarial
	// reviews sharing one key
	// meant a queued demo response could outlive the review it was meant for and
	// land on the change-set review, where a blocking finding cannot be cleared
	// by anything the CLI can do. Separate keys make a surplus harmless.
	runID, obj, err := e.runModel(ctx, initiativeID, StageExecutionPlanReview, RoleAdversarialReviewer, "plan_review", schema.Review,
		[]ccontext.Input{
			{Selector: "immutable_policy", Content: "Independently challenge the execution plan. Emit structured findings. Never set accepted_risk."},
			{Selector: "tasks", Content: renderTasks(tasks)},
		},
		"Adversarial review of the execution plan.",
	)
	if err != nil {
		return nil, err
	}
	return review.ParseFindings(runID, "execution_plan", initiativeID, obj)
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
