package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestNextReadyTasksReturnsDependencySatisfiedSet pins the ready-set: a task
// whose depends_on predecessor has no applied change set is excluded, while
// independent ready tasks are all returned in creation order.
func TestNextReadyTasksReturnsDependencySatisfiedSet(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	a := &domain.Task{InitiativeID: ini.ID, Title: "A", Statement: "a", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, a, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	b := &domain.Task{InitiativeID: ini.ID, Title: "B", Statement: "b", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, b, req.ID, []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	c := &domain.Task{InitiativeID: ini.ID, Title: "C", Statement: "c", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, c, req.ID, nil); err != nil {
		t.Fatal(err)
	}

	ready, err := eng.nextReadyTasks(ctx, ini.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 || ready[0].ID != a.ID || ready[1].ID != c.ID {
		t.Fatalf("ready set = %v, want [A C] (B blocked on A)", taskTitles(ready))
	}
	// The limit is honoured.
	limited, err := eng.nextReadyTasks(ctx, ini.ID, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit=1 returned %d (err=%v)", len(limited), err)
	}
}

func taskTitles(ts []domain.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Title
	}
	return out
}
