package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRestoreWorkspaceRefusesOnStalePost is H3's second hazard: startup
// reconciliation's RestoreWorkspace unwinds an interrupted apply by writing a
// persisted pre-image straight over the workspace, with no check that the
// touched paths still hold what the (interrupted) apply itself wrote --
// unlike RevertChangeSet, which refuses (fsdiff.StalePost) rather than erase
// an edit made to the same file after the mutation it is unwinding. Without
// the same guard here, a crash-recovery restore could silently overwrite
// whatever the user (or a later, successful operation) wrote to that path
// between the crash and the next daemon startup.
func TestRestoreWorkspaceRefusesOnStalePost(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	ctx := context.Background()
	addTask(t, db, iniID, "add /health")

	real := applyOneTask(t, eng, db, iniID)
	preDigest, err := db.JournalSnapshotDigest(ctx, real.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Someone (or something) edited the applied file after the fact -- the
	// same shape TestRevertRefusesAStalePost exercises for revert.
	target := filepath.Join(ws, "server.js")
	if err := os.WriteFile(target, []byte("someone else's edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := eng.RestoreWorkspace(ctx, real.ID, preDigest); err == nil {
		t.Fatal("RestoreWorkspace overwrote an edit made to the workspace since the interrupted apply")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "someone else's edit\n" {
		t.Errorf("the edit was overwritten despite the refusal: %q", data)
	}
}

// TestRestoreWorkspaceRestoresWhenNotStale is the control: with nothing else
// touching the file since the (simulated) interrupted apply, RestoreWorkspace
// must still actually restore the pre-image -- the guard above must not
// become a blanket refusal.
func TestRestoreWorkspaceRestoresWhenNotStale(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	ctx := context.Background()
	addTask(t, db, iniID, "add /health")

	real := applyOneTask(t, eng, db, iniID)
	preDigest, err := db.JournalSnapshotDigest(ctx, real.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ws, "server.js")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.RestoreWorkspace(ctx, real.ID, preDigest); err != nil {
		t.Fatalf("RestoreWorkspace: %v", err)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Fatal("setup: restore did not change anything -- the pre-image should differ from the applied content")
	}
}
