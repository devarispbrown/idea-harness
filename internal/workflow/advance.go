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
// Verification Design, sequentially) → Execution Plan → independent plan review.
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
			_ = e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment, RoleOrchestrator)
			return domain.InitiativeNeedsHumanJudgment, nil
		}
		return domain.InitiativeFailed, err
	}
	return e.runExecutionPlanReview(ctx, initiativeID)
}

// ApprovePlan re-runs the execution-plan review only (it does not regenerate
// tasks). After the human resolves the plan-review blockers, this re-evaluates
// the plan and transitions it to plan_approved (or back to needs_human_judgment).
func (e *Engine) ApprovePlan(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
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
	if len(cses) > 0 {
		return domain.InitiativeFailed, fmt.Errorf("cannot revise a plan that already has change sets")
	}
	if err := e.db.DeleteTasksForInitiative(ctx, initiativeID); err != nil {
		return domain.InitiativeFailed, err
	}
	// Clear prior plan-review findings (they target the now-deleted tasks or the
	// superseded execution plan). NotApplicable preserves the record as dissent.
	_ = e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionNotApplicable, store.ActorOrchestrator)

	if err := e.runExecutionPlan(ctx, initiativeID); err != nil {
		if errors.Is(err, errUnresolvedDependencies) {
			_ = e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment, RoleOrchestrator)
			return domain.InitiativeNeedsHumanJudgment, nil
		}
		return domain.InitiativeFailed, err
	}
	return e.runExecutionPlanReview(ctx, initiativeID)
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
		task  *domain.Task
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
			TargetType:     "initiative",
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
			all[i].ReviewRunID = "deterministic-plan-check"
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
		if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeNeedsHumanJudgment, RoleAdversarialReviewer); err != nil {
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
			case domain.EdgeImplements:
				hasImplements = true
			}
		}
		if !hasImplements {
			out = append(out, domain.Finding{
				Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
				Category: "traceability",
				Finding:  fmt.Sprintf("%s has no implements edge to a requirement", t.Code),
				Evidence: t.Title,
				Impact:   "untraced work cannot be verified against the PRD",
				Recommendation: "trace the task to a requirement",
			})
		}
	}
	if cycle := detectCycle(deps); cycle != "" {
		out = append(out, domain.Finding{
			Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
			Category: "plan",
			Finding:  "task dependency cycle detected: " + cycle,
			Impact:   "the plan cannot be scheduled",
			Recommendation: "break the cycle",
		})
	}
	return out
}

func (e *Engine) runPlanAdversarialReview(ctx context.Context, initiativeID string) ([]domain.Finding, error) {
	tasks, _ := e.db.ListTasks(ctx, initiativeID)
	runID, obj, err := e.runModel(ctx, initiativeID, StageExecutionPlanReview, RoleAdversarialReviewer, "review", schema.Review,
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
