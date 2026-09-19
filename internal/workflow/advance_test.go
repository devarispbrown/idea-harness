package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

func TestAdvanceProducesReviewedPlan(t *testing.T) {
	fp := model.NewFakeProvider() // input-aware demo responses for every schema
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// M1 slice must still approve.
	status, err := eng.Run(ctx, ini.ID, ini.Description)
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativeApproved {
		t.Fatalf("expected approved after M1, got %q", status)
	}

	// M2 slice.
	status, err = eng.Advance(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativePlanApproved {
		t.Fatalf("expected plan_approved, got %q", status)
	}

	tasks, err := db.ListTasks(ctx, ini.ID)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("expected tasks, got %d (err=%v)", len(tasks), err)
	}
	if tasks[0].Code == "" {
		t.Fatal("expected a task code")
	}

	// Task must have an implements edge to a requirement.
	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	hasImplements := false
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			hasImplements = true
		}
	}
	if !hasImplements {
		t.Fatal("expected an implements edge from the task to a requirement")
	}

	// The three design artifacts + execution plan exist.
	arts, err := db.ListArtifacts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, v := range arts {
		kinds[string(v.Kind)] = true
	}
	for _, k := range []string{"technical_design", "design_spec", "verification_plan", "execution_plan"} {
		if !kinds[k] {
			t.Fatalf("expected artifact %q, got %v", k, kinds)
		}
	}
}

func TestDeterministicPlanChecksDetectUntraced(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// Create a task with NO implements edge (empty requirement id).
	task := &domain.Task{InitiativeID: ini.ID, Title: "untraced", Statement: "no req", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	findings := eng.deterministicPlanChecks(ctx, ini.ID)
	found := false
	for _, f := range findings {
		if f.Category == "traceability" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an untraced-task finding, got %+v", findings)
	}
}

func TestDetectCycle(t *testing.T) {
	acyclic := map[string][]string{"a": {"b"}, "b": {"c"}, "c": {}}
	if c := detectCycle(acyclic); c != "" {
		t.Fatalf("expected no cycle, got %q", c)
	}
	cyclic := map[string][]string{"a": {"b"}, "b": {"a"}}
	if c := detectCycle(cyclic); c == "" {
		t.Fatal("expected a cycle")
	}
}

func TestSuggestAnswers(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}

	var answers []domain.SuggestedAnswer
	if err := eng.SuggestAnswers(ctx, ini.ID, func(batch []domain.SuggestedAnswer) {
		answers = append(answers, batch...)
	}); err != nil {
		t.Fatal(err)
	}
	if len(answers) != 2 {
		t.Fatalf("expected 2 suggested answers (demo open questions), got %d", len(answers))
	}
	for _, a := range answers {
		if a.QuestionID == "" || a.Answer == "" {
			t.Fatalf("expected question_id + answer, got %+v", a)
		}
	}
}

func TestReviseKeepsRequirementContextBounded(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Revise(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}

	// Active requirements stay bounded (the scripted PRD emits one), even though
	// the superseded history grows.
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	if got := len(activeRequirements(reqs)); got != 1 {
		t.Fatalf("expected 1 active requirement after revise, got %d (history %d)", got, len(reqs))
	}

	// The second prd call's context must reference the prior active requirement
	// ("Meter usage events") exactly once — not a growing duplicate history.
	var prdSystems []string
	for _, c := range fp.Calls() {
		if c.JSONSchemaName == "prd" {
			prdSystems = append(prdSystems, c.System)
		}
	}
	if len(prdSystems) != 2 {
		t.Fatalf("expected 2 prd calls, got %d", len(prdSystems))
	}
	if got := strings.Count(prdSystems[1], "Meter usage events"); got != 1 {
		t.Fatalf("expected prior active requirement once in revise context, got %d", got)
	}
}

func TestWhyImpactSemanticDiff(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Advance(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}

	tasks, _ := db.ListTasks(ctx, ini.ID)
	if len(tasks) == 0 {
		t.Fatal("expected tasks")
	}
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	if len(reqs) == 0 {
		t.Fatal("expected requirements")
	}

	// Why(task) walks task → requirement.
	why, err := eng.Why(ctx, ini.ID, "task", tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	hasImplements := false
	for _, it := range why {
		if it.EdgeType == "implements" && it.ToID == reqs[0].ID {
			hasImplements = true
		}
	}
	if !hasImplements {
		t.Fatalf("expected why(task) to include implements → requirement, got %+v", why)
	}

	// Impact(requirement) finds the task as a dependent.
	impact, err := eng.Impact(ctx, ini.ID, "requirement", reqs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range impact {
		if it.FromType == "task" && it.FromID == tasks[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected impact(requirement) to include the task, got %+v", impact)
	}

	// SemanticDiff reports the active requirement as added.
	diff, err := eng.SemanticDiff(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Requirements.Added) != 1 {
		t.Fatalf("expected 1 added requirement, got %+v", diff.Requirements.Added)
	}
}

func TestImpactCounts(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}

	counts, err := eng.ImpactCounts(ctx, ini.ID, []NodeRef{{Type: "requirement", ID: req.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if counts["requirement:"+req.ID] != 1 {
		t.Fatalf("expected requirement to block exactly 1 dependent, got %d", counts["requirement:"+req.ID])
	}
}

func TestReopenPhaseGuard(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// Unknown phase is rejected.
	if _, err := eng.ReopenPhase(ctx, ini.ID, "nope"); err == nil {
		t.Fatal("expected unknown phase to be rejected")
	}

	// Once a change set exists, reopen is refused (pre-execution only).
	req := &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChangeSet(ctx, &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ReopenPhase(ctx, ini.ID, "architecture"); err == nil {
		t.Fatal("expected reopen to be refused once a change set exists")
	}
}

func TestDecisionGraphWiring(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	q := &domain.Question{InitiativeID: ini.ID, Question: "How is billing structured?"}
	if err := db.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	d := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "How is billing structured?"}
	if err := db.CreateDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Support metered billing", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	if err := db.WireDecisionDerivedFromQuestion(ctx, ini.ID, d.ID, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, d.ID); err != nil {
		t.Fatal(err)
	}

	// Impact(decision) finds the requirement as a dependent.
	impact, err := eng.Impact(ctx, ini.ID, "decision", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReq := false
	for _, it := range impact {
		if it.FromType == "requirement" && it.FromID == req.ID {
			foundReq = true
		}
	}
	if !foundReq {
		t.Fatalf("expected decision impact to include the requirement, got %+v", impact)
	}

	// Why(decision) finds the question as provenance.
	why, err := eng.Why(ctx, ini.ID, "decision", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundQ := false
	for _, it := range why {
		if it.ToType == "question" && it.ToID == q.ID {
			foundQ = true
		}
	}
	if !foundQ {
		t.Fatalf("expected decision why to include the question, got %+v", why)
	}
}

func TestCreateVerificationTaskWiresVerifiesEdge(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	task := &domain.Task{InitiativeID: ini.ID, Title: "Verify " + req.Code, Statement: "check", Priority: req.Priority}
	if err := db.CreateVerificationTask(ctx, task, req.ID); err != nil {
		t.Fatal(err)
	}

	// The verification task must appear as a dependent of the requirement.
	impact, err := eng.Impact(ctx, ini.ID, "requirement", req.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range impact {
		if it.FromType == "task" && it.FromID == task.ID && it.EdgeType == string(domain.EdgeVerifies) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a verifies edge task->requirement, got %+v", impact)
	}
}

// Every plan-review finding targets the plan. The deterministic ones had no
// target and the reviewer's defaulted to "requirement", so the UI showed plan
// findings on the PRD page and a CRITICAL cycle -- which blocks approve-plan --
// on no page at all.
func TestPlanReviewFindingsTargetThePlan(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "untraced", Statement: "no req", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.runExecutionPlanReview(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings; this check proves nothing")
	}
	for _, f := range findings {
		if f.TargetType != PlanFindingTarget {
			t.Errorf("%s finding targets %q, not the plan", f.Category, f.TargetType)
		}
	}
}

// TestActiveDecisionsExcludesSuperseded mirrors the existing activeRequirements
// unit test (review A2): decisions are open | decided | superseded, and only
// superseded is excluded from design/plan inputs.
func TestActiveDecisionsExcludesSuperseded(t *testing.T) {
	decs := []domain.Decision{
		{ID: "1", Status: domain.DecisionOpen},
		{ID: "2", Status: domain.DecisionDecided},
		{ID: "3", Status: domain.DecisionSuperseded},
	}
	got := activeDecisions(decs)
	if len(got) != 2 {
		t.Fatalf("expected 2 active decisions, got %d: %+v", len(got), got)
	}
	for _, d := range got {
		if d.Status == domain.DecisionSuperseded {
			t.Fatalf("superseded decision leaked through: %+v", d)
		}
	}
}

// TestExecutionPlanExcludesSupersededInputsAndBlocksSupersededCitation is the
// review A2 acceptance test: a newer requirement contradicts a decided
// decision. The plan inputs must carry the decided decision and NOT the
// superseded requirement, and a plan citing the superseded requirement's code
// must be blocked.
func TestExecutionPlanExcludesSupersededInputsAndBlocksSupersededCitation(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// A decided decision: "use Postgres" (not "MongoDB").
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionArchitecture, Question: "Which database?", Options: []string{"Postgres", "MongoDB"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "Postgres", "human"); err != nil {
		t.Fatal(err)
	}

	// An old requirement, now superseded.
	oldReq := &domain.Requirement{InitiativeID: ini.ID, Statement: "Store data in a legacy flat file format",
		Priority: domain.PriorityMust, AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, oldReq); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeRequirements(ctx, ini.ID, "test-cycle"); err != nil {
		t.Fatal(err)
	}

	// The newer, active requirement.
	newReq := &domain.Requirement{InitiativeID: ini.ID, Statement: "Persist data reliably in a managed database",
		Priority: domain.PriorityMust, AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, newReq); err != nil {
		t.Fatal(err)
	}

	// The model's plan cites the SUPERSEDED requirement's code.
	fp.SetResponse("execution_plan", `{"summary":"plan","tasks":[{"title":"Do it","statement":"work","requirement":"`+oldReq.Code+`","priority":"must"}]}`)

	err := eng.runExecutionPlan(ctx, ini.ID)
	if !errors.Is(err, errUnresolvedDependencies) {
		t.Fatalf("expected errUnresolvedDependencies for a plan citing a superseded requirement, got %v", err)
	}

	// The rendered inputs must carry the decided decision and the active
	// requirement, and exclude the superseded one.
	var sys string
	for _, c := range fp.Calls() {
		if c.JSONSchemaName == "execution_plan" {
			sys = c.System
		}
	}
	if sys == "" {
		t.Fatal("expected an execution_plan model call")
	}
	if !strings.Contains(sys, "Persist data reliably") {
		t.Error("expected the active requirement in the plan inputs")
	}
	if strings.Contains(sys, oldReq.Statement) {
		t.Error("expected the superseded requirement NOT to be in the plan inputs")
	}
	if !strings.Contains(sys, "Postgres") {
		t.Error("expected the decided decision's resolution in the plan inputs")
	}

	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Category == "plan_traceability" && f.Severity.Blocking() {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a blocking plan_traceability finding for citing a superseded requirement, got %+v", findings)
	}
}

// TestUnresolvedRequirementReferenceFailsLoudly is the review A4 #1 root-cause
// test: before the fix, runExecutionPlan matched the model's free-text
// requirement field against Requirement.Code and, on a miss, passed an empty
// target id to createTask, which silently skipped wiring the edge -- nothing
// was recorded anywhere, unlike an unresolved depends_on. This uses the fake
// provider with a mismatched requirement field to prove the fix: a genuinely
// unresolvable reference now fails loudly.
func TestUnresolvedRequirementReferenceFailsLoudly(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// The model's free-text requirement field does not match any Requirement.Code.
	fp.SetResponse("execution_plan", `{"summary":"plan","tasks":[{"title":"Do it","statement":"work","requirement":"REQ-9-does-not-exist","priority":"must"}]}`)

	err := eng.runExecutionPlan(ctx, ini.ID)
	if !errors.Is(err, errUnresolvedDependencies) {
		t.Fatalf("expected the unresolved requirement reference to fail loudly, got %v", err)
	}

	tasks, err := db.ListTasks(ctx, ini.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d (err=%v)", len(tasks), err)
	}
	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			t.Fatal("expected no implements edge for an unresolved requirement reference")
		}
	}

	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Category == "plan_traceability" && f.Severity.Blocking() {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a blocking plan_traceability finding, got %+v", findings)
	}
}

// TestRequirementReferenceToleratesFormatting proves the tolerant matching
// (review A4 #4): trimmed, case-insensitive, and a "REQ-3: title" form.
func TestRequirementReferenceToleratesFormatting(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	fp.SetResponse("execution_plan", `{"summary":"plan","tasks":[{"title":"Do it","statement":"work","requirement":"  `+strings.ToLower(req.Code)+` : some title  ","priority":"must"}]}`)

	if err := eng.runExecutionPlan(ctx, ini.ID); err != nil {
		t.Fatalf("expected tolerant matching to resolve, got %v", err)
	}
	tasks, _ := db.ListTasks(ctx, ini.ID)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	edges, _ := db.ListGraphEdgesFrom(ctx, ini.ID, "task", tasks[0].ID)
	found := false
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements && e.ToID == req.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the tolerant-matched requirement to be wired, got %+v", edges)
	}
}

// TestTaskImplementsMultipleRequirements is the review A4 #4 N:M test: a task
// may implement several requirements via the `requirements` array.
func TestTaskImplementsMultipleRequirements(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req1 := &domain.Requirement{InitiativeID: ini.ID, Statement: "First", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req1); err != nil {
		t.Fatal(err)
	}
	req2 := &domain.Requirement{InitiativeID: ini.ID, Statement: "Second", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req2); err != nil {
		t.Fatal(err)
	}

	fp.SetResponse("execution_plan", `{"summary":"plan","tasks":[{"title":"Do it","statement":"work","requirements":["`+req1.Code+`","`+req2.Code+`"],"priority":"must"}]}`)

	if err := eng.runExecutionPlan(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	tasks, _ := db.ListTasks(ctx, ini.ID)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	edges, _ := db.ListGraphEdgesFrom(ctx, ini.ID, "task", tasks[0].ID)
	got := map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			got[e.ToID] = true
		}
	}
	if !got[req1.ID] || !got[req2.ID] {
		t.Fatalf("expected implements edges to both requirements, got %+v", edges)
	}
}

// TestDeterministicPlanChecksFlagSupersededCitationInProse covers the prose
// form of review A2's "cites a superseded requirement code": the task's
// structured requirement correctly points at the ACTIVE requirement, but its
// own text still names the superseded code.
func TestDeterministicPlanChecksFlagSupersededCitationInProse(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	oldReq := &domain.Requirement{InitiativeID: ini.ID, Statement: "old", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, oldReq); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeRequirements(ctx, ini.ID, "test"); err != nil {
		t.Fatal(err)
	}
	newReq := &domain.Requirement{InitiativeID: ini.ID, Statement: "new", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, newReq); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Follow up on " + oldReq.Code,
		Statement: "This depends on the old " + oldReq.Code + " approach.", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, newReq.ID, nil); err != nil {
		t.Fatal(err)
	}

	findings := eng.deterministicPlanChecks(ctx, ini.ID)
	found := false
	for _, f := range findings {
		if f.Category == "plan_traceability" && f.Severity.Blocking() && strings.Contains(f.Finding, oldReq.Code) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a blocking plan_traceability finding for the prose citation, got %+v", findings)
	}
}

// TestDeterministicPlanChecksFlagsDecisionContradictionAdvisory covers the
// advisory-only heuristic (review A2 #3): a task whose words overlap a
// decided decision's REJECTED option more than its resolution.
func TestDeterministicPlanChecksFlagsDecisionContradictionAdvisory(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionArchitecture, Question: "Which database?", Options: []string{"Postgres", "MongoDB"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "Postgres", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Persist data", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	contradicting := &domain.Task{InitiativeID: ini.ID, Title: "Switch storage to MongoDB",
		Statement: "Migrate the storage layer to MongoDB for flexibility.", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, contradicting, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	aligned := &domain.Task{InitiativeID: ini.ID, Title: "Set up Postgres schema",
		Statement: "Create the Postgres schema for the storage layer.", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, aligned, req.ID, nil); err != nil {
		t.Fatal(err)
	}

	findings := eng.deterministicPlanChecks(ctx, ini.ID)
	var advisoryFor map[string]bool = map[string]bool{}
	for _, f := range findings {
		if f.Category == "plan_traceability" && !f.Severity.Blocking() {
			if strings.Contains(f.Finding, contradicting.Code) {
				advisoryFor[contradicting.Code] = true
			}
			if strings.Contains(f.Finding, aligned.Code) {
				advisoryFor[aligned.Code] = true
			}
		}
	}
	if !advisoryFor[contradicting.Code] {
		t.Fatalf("expected an advisory plan_traceability finding for the contradicting task, got %+v", findings)
	}
	if advisoryFor[aligned.Code] {
		t.Fatalf("did not expect the aligned task to be flagged, got %+v", findings)
	}
}

// TestReopenDecisionMarksDependentsStaleAndImpactShowsThem is the review A4 #5
// acceptance test: apply a change set, change (reopen) a decision it depends
// on, and impact lists the requirement, task and CHG, and guidance flags it.
func TestReopenDecisionMarksDependentsStaleAndImpactShowsThem(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionArchitecture, Question: "Which database?", Options: []string{"Postgres", "MongoDB"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "Postgres", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Persist data", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}

	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, cs.ID, step.from, step.to, "test"); err != nil {
			t.Fatalf("transition %s -> %s: %v", step.from, step.to, err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, cs.ID, "digest", "test"); err != nil {
		t.Fatal(err)
	}

	// Before the decision changes, impact(decision) has no dependents at all --
	// this is the exact bug: "impact decision DEC-9 said 'No dependents found'".
	before, err := eng.Impact(ctx, ini.ID, "decision", dec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("expected impact(decision) to already show the requirement via derived_from (sanity check)")
	}

	// Change the decision: reopen it.
	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	stale, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleTargets := map[string]bool{}
	for _, s := range stale {
		staleTargets[s.TargetType+":"+s.TargetID] = true
	}
	for _, want := range []string{"requirement:" + req.ID, "task:" + task.ID, "change_set:" + cs.ID} {
		if !staleTargets[want] {
			t.Fatalf("expected %s to be marked stale, got %+v", want, stale)
		}
	}

	// impact(decision) now lists the requirement, task and change set, each
	// staled.
	impact, err := eng.Impact(ctx, ini.ID, "decision", dec.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotStale := map[string]string{}
	for _, it := range impact {
		gotStale[it.FromType+":"+it.FromID] = it.Staleness
	}
	for _, want := range []string{"requirement:" + req.ID, "task:" + task.ID, "change_set:" + cs.ID} {
		if gotStale[want] != "stale" {
			t.Fatalf("expected %s staleness=stale in impact(decision), got %+v", want, impact)
		}
	}
}

// TestClearStaleItemsForInitiativeOnRevisePlan proves the clearing half of
// review A4 #5: re-planning clears the stale state.
func TestClearStaleItemsForInitiativeOnRevisePlan(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Persist data", Priority: domain.PriorityMust,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}

	// Mark it stale directly (bypassing a decision reopen, which is exercised
	// elsewhere) to isolate the clearing behaviour under test.
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionArchitecture, Question: "Which database?", Options: []string{"Postgres", "MongoDB"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "Postgres", "human"); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "Postgres", "human"); err != nil {
		t.Fatal(err)
	}
	if items, err := db.ListStaleItems(ctx, ini.ID); err != nil || len(items) == 0 {
		t.Fatalf("expected stale items before revise-plan, got %d (err=%v)", len(items), err)
	}

	if _, err := eng.RevisePlan(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected revise-plan to clear stale items, got %+v", items)
	}
}
