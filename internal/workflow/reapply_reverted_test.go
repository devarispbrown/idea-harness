package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRevertedChangeSetCanBeReapplied is ICP re-run defect #3: a reverted
// change set used to be permanently un-appliable ("cannot be applied from
// status reverted"), so recovering from a revert always cost a full agent
// run even when the reverted content was still exactly what was wanted.
// Re-apply must succeed, going through the same guards (staleness,
// secret-sweep) ApplyChangeSet already runs for a fresh approved change set.
func TestRevertedChangeSetCanBeReapplied(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	target := filepath.Join(eng.workspaceRoot, "hello.txt")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("setup: applied file missing: %v", err)
	}

	if err := eng.RevertChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("setup: revert did not remove the file: %v", err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReverted {
		t.Fatalf("setup: expected reverted, got %q", got.Status)
	}

	// The actual defect: this used to fail with "cannot be applied from
	// status reverted".
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("re-apply of a reverted change set: %v", err)
	}
	got, err = db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("expected applied after re-apply, got %q", got.Status)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("re-apply did not restore the file: %v", err)
	}

	// And it can be reverted again from here -- the state machine's normal
	// applied -> reverted edge, unaffected by having come from a re-apply.
	if err := eng.RevertChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("second revert: %v", err)
	}
}

// TestReapplyOfRevertedChangeSetRefusesOnStaleBase covers the "clear refusal
// when the workspace moved on" half of defect #3's fix: re-apply must run
// through the SAME fsdiff.StaleBase guard a fresh apply does, not silently
// overwrite a file that showed up at the change set's path after the revert.
func TestReapplyOfRevertedChangeSetRefusesOnStaleBase(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	if err := eng.RevertChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}

	// The workspace moved on: something else now occupies the exact path the
	// change set would (re-)add.
	target := filepath.Join(eng.workspaceRoot, "hello.txt")
	if err := os.WriteFile(target, []byte("someone else's file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = eng.ApplyChangeSet(ctx, iniID, cs.ID)
	if err == nil {
		t.Fatal("re-apply overwrote a file that appeared after the revert")
	}

	got, gerr := db.GetChangeSet(ctx, cs.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != domain.ChangeSetConflicted {
		t.Fatalf("expected the change set marked conflicted by the staleness refusal, got %q", got.Status)
	}
	// Nothing was written: the interloper file survives untouched.
	data, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != "someone else's file\n" {
		t.Errorf("the interloper file was overwritten: %q", data)
	}
}
