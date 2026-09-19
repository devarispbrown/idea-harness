package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// applyOneTask executes, approves and applies iniID's first task, returning
// the applied change set.
func applyOneTask(t *testing.T, eng *Engine, db *store.DB, iniID string) domain.ChangeSet {
	t.Helper()
	ctx := context.Background()
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
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *got
}

// TestRevertRefusesAStalePost is the review A3 defect: an ICP user added a
// comment to a file after apply, and RevertChangeSet restored the pre-image
// without ever checking, silently erasing it. Revert must refuse instead,
// name the file, and write nothing.
func TestRevertRefusesAStalePost(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs := applyOneTask(t, eng, db, iniID)

	serverJS := filepath.Join(ws, "server.js")
	postApply, err := os.ReadFile(serverJS)
	if err != nil {
		t.Fatal(err)
	}
	editedByHuman := string(postApply) + "// a human's comment, added after apply\n"
	writeFile(t, serverJS, editedByHuman)

	err = eng.RevertChangeSet(context.Background(), iniID, cs.ID)
	if err == nil {
		t.Fatal("revert erased an edit made after apply")
	}
	if !strings.Contains(err.Error(), "server.js") {
		t.Errorf("the refusal does not name the changed file: %v", err)
	}
	if !strings.Contains(err.Error(), "commit or discard") {
		t.Errorf("the refusal does not say what to do next: %v", err)
	}

	// Nothing was written: the human's edit survives byte-for-byte.
	got, err := os.ReadFile(serverJS)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != editedByHuman {
		t.Errorf("revert wrote the workspace despite refusing:\ngot:  %q\nwant: %q", got, editedByHuman)
	}

	// The change set is untouched -- still applied, not reverted, not
	// conflicted, so a later retry (after the human resolves it) can proceed.
	after, err := db.GetChangeSet(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.ChangeSetApplied {
		t.Errorf("status = %q, want applied (refusal must not change it)", after.Status)
	}

	// A finding records which files changed and why, mirroring ApplyChangeSet's
	// stale_base finding.
	findings, err := db.ListFindingsByTarget(context.Background(), "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Category == "stale_post" {
			found = true
			if !strings.Contains(f.Evidence, "server.js") {
				t.Errorf("stale_post finding does not name the file: %q", f.Evidence)
			}
		}
	}
	if !found {
		t.Error("no stale_post finding was recorded")
	}
}

// TestRevertSucceedsWhenWorkspaceUntouched is the control: revert must still
// work in the ordinary case where nothing changed after apply.
func TestRevertSucceedsWhenWorkspaceUntouched(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs := applyOneTask(t, eng, db, iniID)

	if err := eng.RevertChangeSet(context.Background(), iniID, cs.ID); err != nil {
		t.Fatalf("revert refused an unmodified workspace: %v", err)
	}
	got, err := db.GetChangeSet(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReverted {
		t.Errorf("status = %q, want reverted", got.Status)
	}
}

// TestRevertBlockedReasonNamesTheFiles exercises the read-only, lock-free
// preflight Runtime uses to answer 409 synchronously, before starting an
// operation that could only fail in the background.
func TestRevertBlockedReasonNamesTheFiles(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs := applyOneTask(t, eng, db, iniID)

	if reason, err := eng.RevertBlockedReason(context.Background(), iniID, cs.ID); err != nil {
		t.Fatal(err)
	} else if reason != "" {
		t.Errorf("blocked reason on an untouched workspace: %q", reason)
	}

	writeFile(t, filepath.Join(ws, "server.js"), "app.get('/', hello)\napp.get('/health', ok)\nedited\n")
	reason, err := eng.RevertBlockedReason(context.Background(), iniID, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("want a blocked reason after an edit, got none")
	}
	if !strings.Contains(reason, "server.js") {
		t.Errorf("reason does not name the file: %q", reason)
	}

	// Read-only: RevertBlockedReason must not itself change anything.
	after, err := db.GetChangeSet(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.ChangeSetApplied {
		t.Errorf("RevertBlockedReason changed status to %q", after.Status)
	}
}

