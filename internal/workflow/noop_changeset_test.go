package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestExecuteMarksZeroFileChangeSetNoOp pins NoOp's own bit: an agent that
// changed nothing (Hermes exits 0 with every model call having failed, the
// common failure `TestAgentThatChangedNothingSaysWhy` already covers) must
// produce a change set flagged NoOp, not merely an advisory finding.
func TestExecuteMarksZeroFileChangeSetNoOp(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	cs, err := eng.Execute(context.Background(), iniID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !cs.NoOp {
		t.Error("a change set with zero file changes was not flagged NoOp")
	}
	got, err := eng.db.GetChangeSet(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NoOp {
		t.Error("NoOp was not persisted")
	}
	if got.ImplementsTask() {
		t.Error("a review_required NoOp change set reports ImplementsTask true (it is not even applied yet)")
	}
}

// TestNoOpChangeSetDoesNotImplementTaskEvenWhenApplied is ICP re-run defect
// #2's core assertion: an APPLIED NoOp change set must never satisfy
// "this task is implemented" -- domain.ChangeSet.ImplementsTask is the single
// predicate every call site (task completion, evidence eligibility,
// staleness propagation, next/guidance) is meant to share.
func TestNoOpChangeSetDoesNotImplementTaskEvenWhenApplied(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	// The existing, deliberate escape hatch: an empty change set may still be
	// approved and applied (the agent may have correctly found nothing left
	// to do). That must keep working -- only what it counts AS changes.
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := eng.db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("expected applied, got %q", got.Status)
	}
	if got.ImplementsTask() {
		t.Error("an applied NoOp change set reports ImplementsTask true")
	}
	implements, err := eng.taskHasAppliedChangeSet(ctx, cs.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if implements {
		t.Error("taskHasAppliedChangeSet counts an applied NoOp change set as implementing its task")
	}
}

// TestRevertReopensTaskDespiteAnAppliedNoOpChangeSet reproduces the real
// founder-journey defect verbatim: a no-op run (CHG-4, empty, still counted
// as implementing TASK-2) meant reverting the REAL applied change set for
// that task left it `done` while the actual feature was gone from disk.
// taskHasAppliedChangeSet must see past the no-op sibling and reopen the
// task.
func TestRevertReopensTaskDespiteAnAppliedNoOpChangeSet(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	ctx := context.Background()

	real := applyOneTask(t, eng, db, iniID)

	// A later, empty run for the SAME task, also applied -- CHG-4's own
	// shape: zero file changes, counted (before this fix) as "an applied
	// change set implementing" the task.
	noop := &domain.ChangeSet{InitiativeID: iniID, TaskID: real.TaskID, PatchDigest: real.PatchDigest, NoOp: true}
	if err := db.CreateChangeSet(ctx, noop); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, noop.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, noop.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, noop.ID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, noop.ID, domain.ChangeSetApproved, domain.ChangeSetApplying, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkChangeSetApplied(ctx, noop.ID, real.BaseSnapshotID, ""); err != nil {
		t.Fatal(err)
	}

	if err := eng.RevertChangeSet(ctx, iniID, real.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	task, err := db.GetTask(ctx, real.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskProposed {
		t.Fatalf("expected the task reopened to proposed (the only applied change set left is a no-op), got %q", task.Status)
	}
}
