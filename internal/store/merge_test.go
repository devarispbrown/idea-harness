package store

import (
	"context"
	"errors"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestMergeTasksRefusesSelfMerge is review H4, at the store layer directly
// (the daemon handler has its own pre-check too, but that must not be the
// only thing standing between a self-merge and DeleteTask deleting the
// named survivor -- MergeTasks itself must refuse it).
func TestMergeTasksRefusesSelfMerge(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	a := &domain.Task{InitiativeID: ini.ID, Title: "A", Statement: "a", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, a, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := db.MergeTasks(ctx, ini.ID, a.ID, a.ID); !errors.Is(err, ErrSelfMerge) {
		t.Fatalf("expected ErrSelfMerge, got %v", err)
	}

	// A must survive untouched.
	got, err := db.GetTask(ctx, a.ID)
	if err != nil {
		t.Fatalf("task must survive a refused self-merge: %v", err)
	}
	if got.Title != "A" {
		t.Fatalf("unexpected task state after refused self-merge: %+v", got)
	}
}

// TestMergeTasksRejectsCycle is review H4's own example, at the store layer
// directly: A depends on B, B depends on C. Merging (A, C) would give A->B
// from the union of A's own outgoing dependency, and B->A from rewiring B
// (a dependent of C) onto the survivor -- a cycle.
func TestMergeTasksRejectsCycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	c := &domain.Task{InitiativeID: ini.ID, Title: "C", Statement: "c", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, c, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}
	b := &domain.Task{InitiativeID: ini.ID, Title: "B", Statement: "b", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, b, []string{req.ID}, []string{c.ID}); err != nil {
		t.Fatal(err)
	}
	a := &domain.Task{InitiativeID: ini.ID, Title: "A", Statement: "a", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, a, []string{req.ID}, []string{b.ID}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.MergeTasks(ctx, ini.ID, a.ID, c.ID); !errors.Is(err, ErrMergeCycle) {
		t.Fatalf("expected ErrMergeCycle, got %v", err)
	}

	// Nothing was applied: C still exists, B still depends only on C.
	if _, err := db.GetTask(ctx, c.ID); err != nil {
		t.Fatalf("C must still exist after a refused merge: %v", err)
	}
	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID != c.ID {
			t.Fatalf("B's depends_on must be unchanged (still only C), got edge to %s", e.ToID)
		}
	}
}
