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
	// review M4: committing does not change the file's bytes, so it cannot
	// make the stale-workspace check pass -- the advice must say discard (or
	// stash), never "commit".
	if !strings.Contains(err.Error(), "discard") {
		t.Errorf("the refusal does not say what to do next: %v", err)
	}
	if strings.Contains(err.Error(), "commit") {
		t.Errorf("the refusal suggests committing, which cannot help (bytes are unchanged): %v", err)
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
			// review M4: stale_post must not block `complete` -- only
			// Severity.Blocking() (critical/high) findings do.
			if f.Severity.Blocking() {
				t.Errorf("stale_post Severity = %q, want an observation-level (non-blocking) severity", f.Severity)
			}
		}
	}
	if !found {
		t.Error("no stale_post finding was recorded")
	}
}

// TestRevertRefusalNamesALaterChangeSetInstead is review M4: when a LATER
// applied change set is the one that actually touched the stale file, the
// advice must name it ("revert CHG-n first"), not tell the human to discard
// what is really a legitimate later change.
func TestRevertRefusalNamesALaterChangeSetInstead(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs1 := applyOneTask(t, eng, db, iniID)

	// A second task/change set, applied after cs1, that (via the internal
	// executor's placeholder) also touches server.js -- exactly the
	// "something later changed this file" scenario.
	addTask(t, db, iniID, "add /metrics")
	cs2 := applyOneTask(t, eng, db, iniID)

	err := eng.RevertChangeSet(context.Background(), iniID, cs1.ID)
	if err == nil {
		t.Fatal("want a refusal: cs2 touched the same file after cs1 was applied")
	}
	if !strings.Contains(err.Error(), "revert "+cs2.HumanID+" first") {
		t.Errorf("refusal does not name %s to revert first: %v", cs2.HumanID, err)
	}
	if strings.Contains(err.Error(), "discard") {
		t.Errorf("refusal should not tell the human to discard %s's own change: %v", cs2.HumanID, err)
	}
}

// TestRevertRefusalNamesTheLatestLaterChangeSetNotTheEarliest is review the
// low-priority item: laterChangeSetsTouchingByFile used to keep only the
// FIRST later change set it found touching a file, so with cs1 (being
// reverted) < cs2 < cs3 all touching server.js, the advice named cs2 --
// "revert CHG-2 first" -- when cs3 is what's actually sitting on top.
// Following that advice would fail the same stale-workspace check again
// (cs3 still touches the file), producing a second refusal instead of
// unwinding anything. The advice must name the LATEST toucher, cs3.
func TestRevertRefusalNamesTheLatestLaterChangeSetNotTheEarliest(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs1 := applyOneTask(t, eng, db, iniID)

	addTask(t, db, iniID, "add /metrics")
	cs2 := applyOneTask(t, eng, db, iniID)

	addTask(t, db, iniID, "add /status")
	cs3 := applyOneTask(t, eng, db, iniID)

	err := eng.RevertChangeSet(context.Background(), iniID, cs1.ID)
	if err == nil {
		t.Fatal("want a refusal: cs2 and cs3 both touched the same file after cs1 was applied")
	}
	if !strings.Contains(err.Error(), "revert "+cs3.HumanID+" first") {
		t.Errorf("refusal does not name the LATEST toucher %s to revert first: %v", cs3.HumanID, err)
	}
	if strings.Contains(err.Error(), "revert "+cs2.HumanID+" first") {
		t.Errorf("refusal names the EARLIEST later change set %s instead of the latest %s: %v", cs2.HumanID, cs3.HumanID, err)
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

