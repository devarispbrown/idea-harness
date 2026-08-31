package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
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
