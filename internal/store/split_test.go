package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func newSplitFixture(t *testing.T) (*DB, *domain.Initiative, *domain.Requirement, *domain.Requirement) {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	implReq := &domain.Requirement{InitiativeID: ini.ID, Statement: "impl", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, implReq); err != nil {
		t.Fatal(err)
	}
	verifyReq := &domain.Requirement{InitiativeID: ini.ID, Statement: "verify", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, verifyReq); err != nil {
		t.Fatal(err)
	}
	return db, ini, implReq, verifyReq
}

// TestSplitTaskCarriesVerifiesEdges is the MEDIUM fix's other half: a split
// task's own `verifies` edges were dropped entirely -- only EdgeImplements
// was ever wired onto the replacements.
func TestSplitTaskCarriesVerifiesEdges(t *testing.T) {
	db, ini, implReq, verifyReq := newSplitFixture(t)
	ctx := context.Background()

	original := &domain.Task{InitiativeID: ini.ID, Title: "original", Statement: "original", Priority: domain.PriorityMust}
	if err := db.CreateVerificationTask(ctx, original, verifyReq.ID); err != nil {
		t.Fatal(err)
	}

	created, err := db.SplitTask(ctx, ini.ID, original.ID, []SplitTaskSpec{
		{Title: "part 1", Statement: "p1", Priority: domain.PriorityMust, RequirementIDs: []string{implReq.ID}},
		{Title: "part 2", Statement: "p2", Priority: domain.PriorityMust, RequirementIDs: []string{implReq.ID}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("expected 2 replacement tasks, got %d", len(created))
	}

	for _, nt := range created {
		edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", nt.ID)
		if err != nil {
			t.Fatal(err)
		}
		var sawVerifies bool
		for _, e := range edges {
			if e.EdgeType == domain.EdgeVerifies && e.ToID == verifyReq.ID {
				sawVerifies = true
			}
		}
		if !sawVerifies {
			t.Errorf("MEDIUM: replacement task %s (%s) lost the original's `verifies` edge", nt.Code, nt.Title)
		}
	}
}

// TestSplitTaskEventsAreReadable is the MEDIUM fix for the event-key bug:
// SplitTask's own "task.split" event used to be keyed on the ORIGINAL task
// id, which is deleted in the same transaction -- ListEventsForInitiative's
// per-type clause is a subquery against the live `tasks` table, so an event
// keyed on a row that no longer exists could never be read back, and
// createTaskTx (called directly, bypassing createTask's own db.Apply) never
// emitted "task.created" for any of the replacements either. The plan
// visibly changed and "What Changed" reported nothing.
func TestSplitTaskEventsAreReadable(t *testing.T) {
	db, ini, implReq, _ := newSplitFixture(t)
	ctx := context.Background()

	original := &domain.Task{InitiativeID: ini.ID, Title: "original", Statement: "original", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, original, []string{implReq.ID}, nil); err != nil {
		t.Fatal(err)
	}

	created, err := db.SplitTask(ctx, ini.ID, original.ID, []SplitTaskSpec{
		{Title: "part 1", Statement: "p1", Priority: domain.PriorityMust, RequirementIDs: []string{implReq.ID}},
		{Title: "part 2", Statement: "p2", Priority: domain.PriorityMust, RequirementIDs: []string{implReq.ID}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	events, err := db.ListEventsForInitiative(ctx, ini.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}

	var sawSplit bool
	created1, created2 := 0, 0
	for _, e := range events {
		if e.AggregateType == "task" && e.EventType == "task.split" {
			sawSplit = true
		}
		if e.AggregateType == "task" && e.EventType == "task.created" {
			switch e.AggregateID {
			case created[0].ID:
				created1++
			case created[1].ID:
				created2++
			}
		}
	}
	if !sawSplit {
		t.Error("MEDIUM: task.split event is not readable via ListEventsForInitiative")
	}
	if created1 != 1 {
		t.Errorf("MEDIUM: task.created for replacement 1 appeared %d times, want 1", created1)
	}
	if created2 != 1 {
		t.Errorf("MEDIUM: task.created for replacement 2 appeared %d times, want 1", created2)
	}
}
