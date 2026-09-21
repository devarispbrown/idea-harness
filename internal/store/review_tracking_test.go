package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestPredatesReviewTrackingForPreMigrationRow pins review MEDIUM #2: a
// change set created before migration 33 (review_completed_at) existed in
// this database must be recognized as such, so the four "never finished its
// adversarial review" surfaces stop calling it "likely cancelled mid-review"
// -- nothing was cancelled; the column simply didn't exist yet, and before
// the upgrade a change set's review ran (or was skipped) at approve time.
func TestPredatesReviewTrackingForPreMigrationRow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "idea", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}

	// Simulate a genuinely pre-migration row: on a real upgrade, migration 33
	// stamps schema_migrations.applied_at at the moment the daemon first
	// starts against the upgraded DB, well after every existing change set's
	// created_at. Backdate this one the same way.
	if _, err := db.SQL().ExecContext(ctx, `UPDATE change_sets SET created_at = 1 WHERE id = ?`, cs.ID); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewCompleted() {
		t.Fatal("test setup: change set should have no completed review")
	}
	if !got.PredatesReviewTracking {
		t.Fatal("GetChangeSet did not mark the pre-migration row as predating review tracking")
	}
	if got.ReviewNeverCompletedCause() == "likely cancelled mid-review" {
		t.Fatalf("ReviewNeverCompletedCause still blames cancellation for a pre-migration row: %q", got.ReviewNeverCompletedCause())
	}

	// GetChangeSetByRef and ListChangeSets must agree (all three read paths
	// compute PredatesReviewTracking independently).
	byRef, err := db.GetChangeSetByRef(ctx, cs.HumanID)
	if err != nil {
		t.Fatal(err)
	}
	if !byRef.PredatesReviewTracking {
		t.Fatal("GetChangeSetByRef did not mark the row as predating review tracking")
	}
	listed, err := db.ListChangeSets(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].PredatesReviewTracking {
		t.Fatalf("ListChangeSets did not mark the row as predating review tracking: %+v", listed)
	}
	listedByTask, err := db.ListChangeSetsByTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedByTask) != 1 || !listedByTask[0].PredatesReviewTracking {
		t.Fatalf("ListChangeSetsByTask did not mark the row as predating review tracking: %+v", listedByTask)
	}
}

// TestReviewNeverCompletedCauseBlamesCancellationAfterTrackingExisted is the
// contrasting case: a change set created normally (after migration 33
// already exists in this database, true for every DB created going
// forward) whose review never completed really is best explained as
// "cancelled mid-review" -- the column existed the whole time and nothing
// ever stamped it.
func TestReviewNeverCompletedCauseBlamesCancellationAfterTrackingExisted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "idea", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PredatesReviewTracking {
		t.Fatal("a freshly created change set in a fresh (already-migrated) DB must not predate review tracking")
	}
	if got.ReviewNeverCompletedCause() != "likely cancelled mid-review" {
		t.Fatalf("ReviewNeverCompletedCause = %q, want the cancellation wording", got.ReviewNeverCompletedCause())
	}
}
