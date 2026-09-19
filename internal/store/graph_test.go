package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestCreateTaskMultiWiresAllImplementsEdges is the review A4 #4 N:M test at
// the store layer: CreateTaskMulti wires one `implements` edge per requirement
// id, de-duplicated, and CreateTask (the back-compat single-id wrapper) still
// wires exactly one.
func TestCreateTaskMultiWiresAllImplementsEdges(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	r1 := &domain.Requirement{InitiativeID: ini.ID, Statement: "one", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r1); err != nil {
		t.Fatal(err)
	}
	r2 := &domain.Requirement{InitiativeID: ini.ID, Statement: "two", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r2); err != nil {
		t.Fatal(err)
	}

	task := &domain.Task{InitiativeID: ini.ID, Title: "multi", Statement: "s", Priority: domain.PriorityMust}
	// Duplicate r1.ID is included deliberately to prove de-duplication.
	if err := db.CreateTaskMulti(ctx, task, []string{r1.ID, r2.ID, r1.ID, ""}, nil); err != nil {
		t.Fatal(err)
	}

	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			got[e.ToID]++
		}
	}
	if got[r1.ID] != 1 || got[r2.ID] != 1 || len(got) != 2 {
		t.Fatalf("expected exactly one implements edge to each of r1, r2, got %+v", got)
	}

	// Back-compat: CreateTask wires exactly one.
	single := &domain.Task{InitiativeID: ini.ID, Title: "single", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, single, r1.ID, nil); err != nil {
		t.Fatal(err)
	}
	edges2, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", single.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range edges2 {
		if e.EdgeType == domain.EdgeImplements {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected CreateTask to wire exactly 1 implements edge, got %d", n)
	}
}

// TestReplaceTaskRequirements proves the Plan-editing "update task" primitive
// (review A4 #3): it replaces the prior set of implements edges entirely.
func TestReplaceTaskRequirements(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	r1 := &domain.Requirement{InitiativeID: ini.ID, Statement: "one", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r1); err != nil {
		t.Fatal(err)
	}
	r2 := &domain.Requirement{InitiativeID: ini.ID, Statement: "two", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r2); err != nil {
		t.Fatal(err)
	}
	r3 := &domain.Requirement{InitiativeID: ini.ID, Statement: "three", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r3); err != nil {
		t.Fatal(err)
	}

	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, task, []string{r1.ID, r2.ID}, nil); err != nil {
		t.Fatal(err)
	}

	if err := db.ReplaceTaskRequirements(ctx, ini.ID, task.ID, []string{r3.ID}); err != nil {
		t.Fatal(err)
	}
	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			got[e.ToID] = true
		}
	}
	if got[r1.ID] || got[r2.ID] {
		t.Fatalf("expected r1/r2 edges to be replaced, got %+v", got)
	}
	if !got[r3.ID] || len(got) != 1 {
		t.Fatalf("expected exactly one implements edge to r3, got %+v", got)
	}
}
