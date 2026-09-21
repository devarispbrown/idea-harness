package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestBackfillNoOpChangeSetsFixesPreMigrationRows pins review MEDIUM #1:
// migration 34 left every change set created before it at no_op=false,
// including ones whose patch manifest -- persisted unconditionally since
// efac66d -- actually touched zero files. BackfillNoOpChangeSets recomputes
// the flag from that manifest against a populated pre-migration DB: a mix of
// a real (non-empty) change set and a zero-file one, exactly as migration 34
// would have left them.
func TestBackfillNoOpChangeSetsFixesPreMigrationRows(t *testing.T) {
	eng, db := newTestEngine(t, model.NewFakeProvider(), config.HarnessConfig{})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "idea", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	// A real, pre-migration change set: patch manifest has files. Backfill
	// must leave it alone.
	realDigest, err := eng.rawBlobs.Put([]byte("package main\n"))
	if err != nil {
		t.Fatal(err)
	}
	realPatch, err := json.Marshal(fsdiff.Patch{Files: []domain.FileChange{
		{Path: "main.go", Digest: realDigest, ChangeType: domain.FileAdded},
	}})
	if err != nil {
		t.Fatal(err)
	}
	realPatchDigest, err := eng.rawBlobs.Put(realPatch)
	if err != nil {
		t.Fatal(err)
	}
	realCS := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: realPatchDigest}
	if err := db.CreateChangeSet(ctx, realCS); err != nil {
		t.Fatal(err)
	}

	// A pre-migration NO-OP change set: an agent ran, produced an empty
	// patch (fsdiff.Diff found no changes), and CreateChangeSet's no_op
	// column defaulted to 0 because migration 34 did not exist yet.
	emptyPatch, err := json.Marshal(fsdiff.Patch{Files: []domain.FileChange{}})
	if err != nil {
		t.Fatal(err)
	}
	emptyPatchDigest, err := eng.rawBlobs.Put(emptyPatch)
	if err != nil {
		t.Fatal(err)
	}
	noOpCS := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: emptyPatchDigest}
	if err := db.CreateChangeSet(ctx, noOpCS); err != nil {
		t.Fatal(err)
	}

	// A pre-migration change set with NO patch manifest at all (PatchDigest
	// == ""), which loadPatch already treats as an empty patch.
	blankCS := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID}
	if err := db.CreateChangeSet(ctx, blankCS); err != nil {
		t.Fatal(err)
	}

	// Sanity: all three start no_op=false, migration 34's default.
	for _, cs := range []*domain.ChangeSet{realCS, noOpCS, blankCS} {
		got, err := db.GetChangeSet(ctx, cs.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.NoOp {
			t.Fatalf("test setup: change set %s already no_op", cs.ID)
		}
	}

	fixed, err := eng.BackfillNoOpChangeSets(ctx)
	if err != nil {
		t.Fatalf("BackfillNoOpChangeSets: %v", err)
	}
	if fixed != 2 {
		t.Fatalf("fixed = %d, want 2 (the empty-manifest and no-manifest rows)", fixed)
	}

	got, err := db.GetChangeSet(ctx, realCS.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NoOp {
		t.Fatal("backfill marked the real change set no_op")
	}
	got, err = db.GetChangeSet(ctx, noOpCS.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NoOp {
		t.Fatal("backfill did not fix the zero-file change set")
	}
	got, err = db.GetChangeSet(ctx, blankCS.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NoOp {
		t.Fatal("backfill did not fix the no-manifest change set")
	}
}

// TestBackfillNoOpChangeSetsRunsOnlyOnce pins the persisted-marker half of
// review MEDIUM #1: once claimed, a second call (e.g. the next daemon
// start) must not re-scan every change set, even if the marker's effect
// (no_op having been set) is somehow undone afterward.
func TestBackfillNoOpChangeSetsRunsOnlyOnce(t *testing.T) {
	eng, db := newTestEngine(t, model.NewFakeProvider(), config.HarnessConfig{})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "idea", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	noOpCS := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID}
	if err := db.CreateChangeSet(ctx, noOpCS); err != nil {
		t.Fatal(err)
	}

	fixed, err := eng.BackfillNoOpChangeSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 1 {
		t.Fatalf("first run fixed = %d, want 1", fixed)
	}

	// Undo the fix directly, simulating a row that reverted (or a fresh row
	// created after the marker was claimed) -- the marker must still block a
	// second full scan.
	if err := db.SetChangeSetNoOp(ctx, noOpCS.ID, false); err != nil {
		t.Fatal(err)
	}
	fixed, err = eng.BackfillNoOpChangeSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 0 {
		t.Fatalf("second run fixed = %d, want 0 (already claimed)", fixed)
	}
}
