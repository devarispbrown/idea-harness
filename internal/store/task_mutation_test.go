package store

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func mkInitiative(t *testing.T, db *DB) *domain.Initiative {
	t.Helper()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(context.Background(), ini); err != nil {
		t.Fatal(err)
	}
	return ini
}

func TestTaskCRUDAndReorder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := mkInitiative(t, db)

	t1 := &domain.Task{InitiativeID: ini.ID, Title: "one"}
	t2 := &domain.Task{InitiativeID: ini.ID, Title: "two"}
	if err := db.CreateTask(ctx, t1, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, t2, "", nil); err != nil {
		t.Fatal(err)
	}
	if t1.Code != "TASK-1" || t2.Code != "TASK-2" {
		t.Fatalf("codes = %s, %s", t1.Code, t2.Code)
	}

	tasks, err := db.ListTasks(ctx, ini.ID)
	if err != nil || len(tasks) != 2 || tasks[0].ID != t1.ID {
		t.Fatalf("initial order = %+v (err=%v)", tasks, err)
	}

	if err := db.UpdateTask(ctx, ini.ID, t1.ID, "one-edit", "stmt", domain.PriorityMust, "M"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetTask(ctx, t1.ID)
	if got.Title != "one-edit" || got.Priority != domain.PriorityMust {
		t.Fatalf("update = %+v", got)
	}

	// Reorder: swap.
	if err := db.ReorderTasks(ctx, ini.ID, []string{t2.ID, t1.ID}); err != nil {
		t.Fatal(err)
	}
	tasks, _ = db.ListTasks(ctx, ini.ID)
	if len(tasks) != 2 || tasks[0].ID != t2.ID {
		t.Fatalf("reorder = %+v", tasks)
	}
	// Non-permutation lists are rejected.
	if err := db.ReorderTasks(ctx, ini.ID, []string{t1.ID}); err == nil {
		t.Fatal("expected a missing-id permutation error")
	}
	if err := db.ReorderTasks(ctx, ini.ID, []string{t1.ID, t1.ID}); err == nil {
		t.Fatal("expected a duplicate-id error")
	}
	if err := db.ReorderTasks(ctx, ini.ID, []string{"nope", t1.ID}); err == nil {
		t.Fatal("expected an unknown-id error")
	}

	if err := db.DeleteTask(ctx, ini.ID, t2.ID); err != nil {
		t.Fatal(err)
	}
	tasks, _ = db.ListTasks(ctx, ini.ID)
	if len(tasks) != 1 || tasks[0].ID != t1.ID {
		t.Fatalf("after delete = %+v", tasks)
	}
}

func TestTaskCodeMonotonicAfterDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := mkInitiative(t, db)

	t1 := &domain.Task{InitiativeID: ini.ID, Title: "a"}
	t2 := &domain.Task{InitiativeID: ini.ID, Title: "b"}
	_ = db.CreateTask(ctx, t1, "", nil)
	_ = db.CreateTask(ctx, t2, "", nil)
	if err := db.DeleteTask(ctx, ini.ID, t2.ID); err != nil {
		t.Fatal(err)
	}
	t3 := &domain.Task{InitiativeID: ini.ID, Title: "c"}
	if err := db.CreateTask(ctx, t3, "", nil); err != nil {
		t.Fatal(err)
	}
	if t3.Code != "TASK-3" {
		t.Fatalf("expected TASK-3 (monotonic, never reused), got %s", t3.Code)
	}
}

func TestDeleteTasksForInitiative(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := mkInitiative(t, db)
	t1 := &domain.Task{InitiativeID: ini.ID, Title: "a"}
	_ = db.CreateTask(ctx, t1, "", nil)
	if err := db.DeleteTasksForInitiative(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	tasks, _ := db.ListTasks(ctx, ini.ID)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestConflictTransitions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := mkInitiative(t, db)
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: "task-x", PatchDigest: "p"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	setChangeSetStatus(t, db, cs.ID, domain.ChangeSetConflicted)
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetConflicted, domain.ChangeSetApproved, "actor"); err != nil {
		t.Fatalf("conflicted->approved: %v", err)
	}
	setChangeSetStatus(t, db, cs.ID, domain.ChangeSetConflicted)
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetConflicted, domain.ChangeSetRejected, "actor"); err != nil {
		t.Fatalf("conflicted->rejected: %v", err)
	}
}

// TestReorderTasksErrorsCarryNoStorePrefix pins docs/plans/deferred.md's
// "audit 6 / A13c" row: POST .../tasks/reorder answered
// `400 store: reorder task id list is not a permutation…`, prefix and all,
// because handleReorderTasks surfaces ReorderTasks's own error text verbatim
// (daemon/errors.go's writeErr falls through to err.Error() for an ordinary
// domain error) -- the same class of leak ErrSelfMerge/ErrMergeCycle/
// ErrDependsOnCycle were fixed for (see TestErrDependsOnCycleHasNoInternalPrefix
// and merge_test.go). `idea reorder` (internal/cli/tasks.go) is the first
// CLI-reachable caller of this path, and it pre-validates the count and
// membership client-side before ever sending the request -- this is the
// defense-in-depth layer for the race that defeats that pre-check (the task
// list changing between the CLI's GET and its POST).
func TestReorderTasksErrorsCarryNoStorePrefix(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := mkInitiative(t, db)
	t1 := &domain.Task{InitiativeID: ini.ID, Title: "one"}
	if err := db.CreateTask(ctx, t1, "", nil); err != nil {
		t.Fatal(err)
	}
	for _, orderedIDs := range [][]string{
		{},               // wrong count
		{t1.ID, "bogus"}, // not a permutation: unknown id
		{t1.ID, t1.ID},   // not a permutation: duplicate
	} {
		err := db.ReorderTasks(ctx, ini.ID, orderedIDs)
		if err == nil {
			t.Fatalf("ReorderTasks(%v): want an error", orderedIDs)
		}
		if strings.HasPrefix(err.Error(), "store:") {
			t.Errorf("ReorderTasks(%v).Error() = %q, leaks the internal package prefix", orderedIDs, err.Error())
		}
	}
}
