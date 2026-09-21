package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// mustMarkStale directly inserts an active stale mark for target, bypassing
// the decision-reopen flow that normally produces one (ReopenDecision):
// markStaleTx only needs the ids and a reason, and testing the GC in
// isolation from decisionDependents' own graph-walking keeps these tests
// about one thing.
func mustMarkStale(t *testing.T, db *DB, initiativeID, targetType, targetID string) {
	t.Helper()
	if err := db.InTx(context.Background(), func(tx *sql.Tx) error {
		return markStaleTx(tx, initiativeID, targetType, targetID, "test reason", "dec-1")
	}); err != nil {
		t.Fatalf("mustMarkStale: %v", err)
	}
}

func activeStaleTargets(t *testing.T, db *DB, initiativeID string) map[string]bool {
	t.Helper()
	items, err := db.ListStaleItems(context.Background(), initiativeID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, it := range items {
		out[it.TargetID] = true
	}
	return out
}

// TestSupersedeRequirementsClearsItsOwnStaleMark is the MEDIUM regression
// test for requirement stale-mark garbage collection.
//
// ClearStaleItemsForInitiative deliberately never clears a REQUIREMENT mark
// (re-planning does not re-examine the requirement itself), which is correct
// for a LIVE requirement -- but before this fix, nothing EVER cleared one
// either, including when the requirement itself was superseded/removed and
// so could never again be re-examined against anything. The mark then
// survived forever as pure noise: display-only, but permanently visible on a
// requirement that no longer exists.
func TestSupersedeRequirementsClearsItsOwnStaleMark(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "s", Status: domain.RequirementApproved}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	mustMarkStale(t, db, ini.ID, "requirement", req.ID)
	if !activeStaleTargets(t, db, ini.ID)[req.ID] {
		t.Fatal("fixture precondition: expected an active stale mark on the requirement")
	}

	if err := db.SupersedeRequirements(ctx, ini.ID, "retry"); err != nil {
		t.Fatal(err)
	}

	if activeStaleTargets(t, db, ini.ID)[req.ID] {
		t.Fatal("a superseded requirement's stale mark is still active -- it is never garbage-collected (review MEDIUM)")
	}
	// It is cleared, not deleted -- BuildTimeline still needs to see it.
	all, err := db.ListAllStaleMarks(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range all {
		if m.TargetID == req.ID {
			found = true
			if m.ClearedAt == nil {
				t.Fatal("the mark exists but was not marked cleared")
			}
		}
	}
	if !found {
		t.Fatal("the mark was deleted rather than cleared")
	}
}

// TestRevertChangeSetClearsItsOwnStaleMark and
// TestRejectingAChangeSetClearsItsOwnStaleMark are the MEDIUM regression
// tests for change-set stale-mark garbage collection: a change set that is
// reverted or rejected can never again apply the work its stale mark was
// warning about, so the mark surviving forever (as ClearStaleItemsForInitiative
// never touches it either, outside a re-plan) was pure noise.

func changeSetThrough(t *testing.T, db *DB, csID string, path ...domain.ChangeSetStatus) {
	t.Helper()
	from := domain.ChangeSetDraft
	for _, to := range path {
		if err := db.TransitionChangeSet(context.Background(), csID, from, to, ActorOrchestrator); err != nil {
			t.Fatalf("transition %s -> %s: %v", from, to, err)
		}
		from = to
	}
}

func TestRevertChangeSetClearsItsOwnStaleMark(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "task"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	changeSetThrough(t, db, cs.ID,
		domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired,
		domain.ChangeSetApproved, domain.ChangeSetApplying, domain.ChangeSetApplied)

	mustMarkStale(t, db, ini.ID, "change_set", cs.ID)
	if !activeStaleTargets(t, db, ini.ID)[cs.ID] {
		t.Fatal("fixture precondition: expected an active stale mark on the change set")
	}

	if err := db.MarkChangeSetReverted(ctx, cs.ID, ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	if activeStaleTargets(t, db, ini.ID)[cs.ID] {
		t.Fatal("a reverted change set's stale mark is still active -- it is never garbage-collected (review MEDIUM)")
	}
}

func TestRejectingAChangeSetClearsItsOwnStaleMark(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "task"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	changeSetThrough(t, db, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired)

	mustMarkStale(t, db, ini.ID, "change_set", cs.ID)
	if !activeStaleTargets(t, db, ini.ID)[cs.ID] {
		t.Fatal("fixture precondition: expected an active stale mark on the change set")
	}

	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetReviewRequired, domain.ChangeSetRejected, ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	if activeStaleTargets(t, db, ini.ID)[cs.ID] {
		t.Fatal("a rejected change set's stale mark is still active -- it is never garbage-collected (review MEDIUM)")
	}
}
