package store

import (
	"context"
	"errors"
	"strings"
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

// TestErrSelfMergeHasNoInternalPrefix pins review LOW #10: the daemon
// handler surfaces this sentinel's Error() text verbatim in a 400 response
// (its own pre-check catches the common case, but ErrSelfMerge is the
// defense-in-depth layer a race could still reach), and daemon/errors.go's
// whole contract is that "the storage layer's own strings" never become the
// product's own error messages -- a literal "store: " prefix leaking
// through here contradicted it.
func TestErrSelfMergeHasNoInternalPrefix(t *testing.T) {
	if got := ErrSelfMerge.Error(); strings.HasPrefix(got, "store:") {
		t.Fatalf("ErrSelfMerge.Error() = %q, leaks the internal package prefix", got)
	}
}

// TestErrMergeCycleHasNoInternalPrefix is ErrSelfMerge's counterpart for the
// other sentinel this handler surfaces verbatim (review LOW #10).
func TestErrMergeCycleHasNoInternalPrefix(t *testing.T) {
	if got := ErrMergeCycle.Error(); strings.HasPrefix(got, "store:") {
		t.Fatalf("ErrMergeCycle.Error() = %q, leaks the internal package prefix", got)
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

// TestMergeTasksCarriesVerifiesEdges is the MEDIUM fix: MergeTasks used to
// union only EdgeImplements onto the survivor, silently destroying any
// `verifies` edge either task carried -- a generated verification task
// merged into another for tidiness dropped its requirement's verification
// coverage entirely, with no error and no trace.
func TestMergeTasksCarriesVerifiesEdges(t *testing.T) {
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

	keep := &domain.Task{InitiativeID: ini.ID, Title: "keep", Statement: "keep", Priority: domain.PriorityMust}
	if err := db.CreateTaskMulti(ctx, keep, []string{implReq.ID}, nil); err != nil {
		t.Fatal(err)
	}
	// A generated verification task, wired with `verifies` (not `implements`).
	merge := &domain.Task{InitiativeID: ini.ID, Title: "verify it", Statement: "verify it", Priority: domain.PriorityMust}
	if err := db.CreateVerificationTask(ctx, merge, verifyReq.ID); err != nil {
		t.Fatal(err)
	}

	got, err := db.MergeTasks(ctx, ini.ID, keep.ID, merge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != keep.ID {
		t.Fatalf("expected survivor %s, got %s", keep.ID, got.ID)
	}

	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "task", keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawImplements, sawVerifies bool
	for _, e := range edges {
		switch {
		case e.EdgeType == domain.EdgeImplements && e.ToID == implReq.ID:
			sawImplements = true
		case e.EdgeType == domain.EdgeVerifies && e.ToID == verifyReq.ID:
			sawVerifies = true
		}
	}
	if !sawImplements {
		t.Error("the survivor lost its own `implements` edge")
	}
	if !sawVerifies {
		t.Error("MEDIUM: the merged task's `verifies` edge was destroyed, not carried onto the survivor")
	}
}
