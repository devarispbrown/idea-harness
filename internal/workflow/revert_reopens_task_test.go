package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRevertChangeSetReopensItsTask is ICP re-run defect E: after reverting
// CHG-3, TASK-2 (the task it implemented) stayed `done` forever. Guidance said
// "re-run the affected tasks", but nextReadyTask only ever considers
// ready/proposed tasks, so `idea execute` printed "✓ execute started" and then
// failed "no ready tasks ... (dependencies not satisfied)" with no way back.
// Reverting must return the task to a runnable state so a subsequent execute
// re-picks it.
func TestRevertChangeSetReopensItsTask(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs := applyOneTask(t, eng, db, iniID)

	ctx := context.Background()
	task, err := db.GetTask(ctx, cs.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskDone {
		t.Fatalf("setup: expected the task done after apply, got %q", task.Status)
	}

	if err := eng.RevertChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}

	after, err := db.GetTask(ctx, cs.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.TaskProposed {
		t.Fatalf("expected the task reopened to proposed after revert, got %q", after.Status)
	}

	// `idea execute` (nextReadyTask) must now re-pick this exact task instead
	// of failing "no ready tasks" -- the actual reported symptom.
	next, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatalf("execute after revert should re-pick the reopened task: %v", err)
	}
	if next.TaskID != cs.TaskID {
		t.Fatalf("execute picked a different task after revert: got task %q, want %q", next.TaskID, cs.TaskID)
	}
}

// TestRevertChangeSetLeavesTaskDoneWhenAnotherChangeSetStillApplies covers the
// guard half of the same fix: a task can have more than one change set across
// retries (changes_requested -> supersede -> re-execute), so reverting an OLD,
// already-superseded-in-effect change set must not reopen a task a NEWER
// change set still carries as applied.
func TestRevertChangeSetLeavesTaskDoneWhenAnotherChangeSetStillApplies(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	ctx := context.Background()

	cs1 := applyOneTask(t, eng, db, iniID)
	// Manually record a second, later change set for the SAME task and mark it
	// applied too, standing in for a retry that produced and applied a second
	// change set without ever superseding the first (the guard must hold
	// regardless of how that came about).
	cs2 := &domain.ChangeSet{InitiativeID: iniID, TaskID: cs1.TaskID, PatchDigest: cs1.PatchDigest}
	if err := db.CreateChangeSet(ctx, cs2); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs2.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs2.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs2.ID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs2.ID, domain.ChangeSetApproved, domain.ChangeSetApplying, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkChangeSetApplied(ctx, cs2.ID, cs1.BaseSnapshotID, ""); err != nil {
		t.Fatal(err)
	}

	if err := eng.RevertChangeSet(ctx, iniID, cs1.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	task, err := db.GetTask(ctx, cs1.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskDone {
		t.Fatalf("expected the task to stay done (cs2 is still applied), got %q", task.Status)
	}
}
