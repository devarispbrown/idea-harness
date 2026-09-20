package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

// newAgentExecuteFixtureTwoTasks is newAgentExecuteFixture's setup, but with
// two tasks: T2 depends_on T1. Used to reproduce C4's dependent-deadlock
// shape end to end through the real Execute path.
func newAgentExecuteFixtureTwoTasks(t *testing.T, exit string) (eng *Engine, iniID string, t1, t2 *domain.Task) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeAgentDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_AGENT_EXIT", exit)
	t.Setenv("OPENAI_API_KEY", "sk-test-leaked-value")

	e, db, id := newExecuteFixture(t)
	e.cfg.Executor = config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "openai/gpt-5.6-luna", Image: "img"}

	t1 = &domain.Task{InitiativeID: id, Title: "T1", Statement: "build T1", Priority: domain.PriorityMust}
	if err := db.CreateTask(context.Background(), t1, "", nil); err != nil {
		t.Fatal(err)
	}
	t2 = &domain.Task{InitiativeID: id, Title: "T2", Statement: "build T2", Priority: domain.PriorityMust}
	if err := db.CreateTask(context.Background(), t2, "", []string{t1.ID}); err != nil {
		t.Fatal(err)
	}
	return e, id, t1, t2
}

// TestNoOpChangeSetLeavesTaskReopenedNotDone is C4's write-side assertion: an
// agent that changed nothing produces a NoOp change set, and Execute must not
// mark the task `done` for it -- domain.ChangeSet.ImplementsTask() (applied
// AND !NoOp) can never become true for it, so `done` would be permanent.
func TestNoOpChangeSetLeavesTaskReopenedNotDone(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	ctx := context.Background()

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.NoOp {
		t.Fatal("fixture did not produce a NoOp change set")
	}
	task, err := eng.db.GetTask(ctx, cs.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status == domain.TaskDone {
		t.Fatalf("C4: a NoOp change set marked the task done -- ImplementsTask() can never become true for it, "+
			"so this deadlocks the task (and every dependent) forever; got status %q", task.Status)
	}
	if task.Status != domain.TaskProposed {
		t.Fatalf("expected the task reopened to proposed, got %q", task.Status)
	}
}

// TestExecuteRefusesToRepickTaskWhileItsNoOpChangeSetIsOpen is the "naive fix"
// trap the report calls out: leaving the task `proposed` (above) makes it
// look re-pickable to nextReadyTask the instant the NoOp change set is
// created. A second Execute must refuse until that change set is resolved,
// or every retry burns a fresh paid agent run on top of an unreviewed one.
func TestExecuteRefusesToRepickTaskWhileItsNoOpChangeSetIsOpen(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	ctx := context.Background()

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.NoOp {
		t.Fatal("fixture did not produce a NoOp change set")
	}

	if _, err := eng.Execute(ctx, iniID); err == nil {
		t.Fatal("C4: Execute re-picked a task whose own NoOp change set is still open (review_required), " +
			"which would spend a second agent run on top of an unreviewed one")
	}
}

// TestExecuteMayRepickTaskAfterNoOpChangeSetIsResolved shows the guard lifts
// once the NoOp change set reaches a terminal disposition (here: approved and
// applied), so the task is not stuck forever -- only while genuinely
// unresolved.
func TestExecuteMayRepickTaskAfterNoOpChangeSetIsResolved(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	ctx := context.Background()

	cs1, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs1.NoOp {
		t.Fatal("fixture did not produce a NoOp change set")
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs1.ID, "approved"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs1.ID); err != nil {
		t.Fatalf("apply: %v", err)
	}

	task, err := eng.db.GetTask(ctx, cs1.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status == domain.TaskDone {
		t.Fatal("an applied NoOp change set must not mark its task done")
	}

	cs2, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatalf("Execute refused to re-pick the task even after its NoOp change set was resolved (applied): %v", err)
	}
	if cs2.TaskID != cs1.TaskID {
		t.Fatalf("expected Execute to re-pick the same still-not-implemented task, got a change set for %s", cs2.TaskID)
	}
	if cs2.HumanID == cs1.HumanID {
		t.Fatalf("expected a fresh change set, got the same one back: %s", cs2.HumanID)
	}
}

// TestExecuteNoOpDoesNotDeadlockDependentTask reproduces the report's full
// end-to-end scenario: T2 depends_on T1, and T1's only change set is a NoOp.
// Before C4's fix this permanently wedged both nextReadyTask (T1 marked
// `done` so never re-picked) and taskDependenciesApplied (T2 never
// satisfied) with no way out short of editing the database.
func TestExecuteNoOpDoesNotDeadlockDependentTask(t *testing.T) {
	eng, iniID, t1, t2 := newAgentExecuteFixtureTwoTasks(t, "0")
	ctx := context.Background()

	cs1, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs1.NoOp || cs1.TaskID != t1.ID {
		t.Fatalf("expected a NoOp change set for T1, got NoOp=%v task=%s", cs1.NoOp, cs1.TaskID)
	}

	applied, err := eng.taskDependenciesApplied(ctx, iniID, t2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("T2 reports its dependency satisfied by a NoOp (unapplied, and even once applied, non-implementing) change set")
	}

	// T1 must still be the one nextReadyTask offers -- reopened, not a dead
	// end -- even though its change set is still open.
	if _, err := eng.nextReadyTask(ctx, iniID); err != nil {
		t.Fatalf("nextReadyTask: %v (T1 should be re-pickable once its change set resolves, not a permanent dead end)", err)
	}

	if err := eng.ReviewChangeSet(ctx, iniID, cs1.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs1.ID); err != nil {
		t.Fatal(err)
	}

	// Still not implemented -- T2 remains correctly blocked...
	applied, err = eng.taskDependenciesApplied(ctx, iniID, t2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("T2 reports satisfied by an APPLIED NoOp change set")
	}
	// ...but T1 is re-executable, which is the way out of the deadlock.
	next, err := eng.nextReadyTask(ctx, iniID)
	if err != nil {
		t.Fatalf("nextReadyTask: %v", err)
	}
	if next.ID != t1.ID {
		t.Fatalf("nextReadyTask = %s, want T1 re-picked so it can be genuinely implemented", next.ID)
	}
}

// TestReapplyOfRevertedChangeSetMarksTaskDoneAgain is H2: RevertChangeSet
// reopens the task to `proposed` (ICP re-run defect E); re-applying that same
// change set must move it back to `done`, or the task stays re-executable
// forever and a later Execute spends a phantom paid agent run producing a
// duplicate change set on a task that is, in fact, already implemented.
func TestReapplyOfRevertedChangeSetMarksTaskDoneAgain(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	ctx := context.Background()
	addTask(t, db, iniID, "add /health")

	real := applyOneTask(t, eng, db, iniID)

	task, err := db.GetTask(ctx, real.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskDone {
		t.Fatalf("setup: expected the task done after the first apply, got %q", task.Status)
	}

	if err := eng.RevertChangeSet(ctx, iniID, real.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	task, err = db.GetTask(ctx, real.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskProposed {
		t.Fatalf("setup: expected the task reopened after revert, got %q", task.Status)
	}

	if err := eng.ApplyChangeSet(ctx, iniID, real.ID); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	task, err = db.GetTask(ctx, real.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskDone {
		t.Fatalf("H2: re-applying a reverted change set left the task %q, want done "+
			"(otherwise a fresh Execute spends a phantom agent run on an already-implemented task)", task.Status)
	}

	// And a fresh Execute must not re-pick it now that it is genuinely done
	// again -- there is only the one task in this fixture.
	if _, err := eng.Execute(ctx, iniID); err == nil {
		t.Fatal("H2: Execute re-picked a task whose reverted change set had just been successfully re-applied")
	}
}
