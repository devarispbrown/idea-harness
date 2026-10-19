package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestExecuteWaveRunsIndependentTasks pins the wave driver: two independent
// tasks (no depends_on) execute concurrently and each produce a reviewed change
// set, without the serial peer-open refusal.
func TestExecuteWaveRunsIndependentTasks(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"A", "B"} {
		task := &domain.Task{InitiativeID: iniID, Title: title, Statement: title, Priority: domain.PriorityMust}
		if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
			t.Fatal(err)
		}
	}

	changesets, err := eng.ExecuteWave(ctx, iniID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(changesets) != 2 {
		t.Fatalf("expected 2 change sets from the wave, got %d", len(changesets))
	}
	for _, cs := range changesets {
		if cs.Status != domain.ChangeSetReviewRequired {
			t.Errorf("change set %s status = %q, want review_required", cs.HumanID, cs.Status)
		}
		if cs.TaskID == "" {
			t.Errorf("change set %s has no task id", cs.HumanID)
		}
	}
}

// TestExecuteWaveRespectsDependencies pins the dependency-aware half: a task
// whose depends_on predecessor has no applied change set is NOT in the wave.
func TestExecuteWaveRespectsDependencies(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	a := &domain.Task{InitiativeID: iniID, Title: "A", Statement: "a", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, a, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	b := &domain.Task{InitiativeID: iniID, Title: "B", Statement: "b", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, b, req.ID, []string{a.ID}); err != nil {
		t.Fatal(err)
	}

	changesets, err := eng.ExecuteWave(ctx, iniID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changesets) != 1 {
		t.Fatalf("expected only A (B depends on A) to run, got %d change sets", len(changesets))
	}
	if changesets[0].TaskID != a.ID {
		t.Fatalf("expected A to run, got task %q", changesets[0].TaskID)
	}
}
