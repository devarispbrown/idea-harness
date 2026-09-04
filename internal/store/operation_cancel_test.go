package store

import (
	"context"
	"errors"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestCancelNeverOverwritesAFinishedOperation pins the fix for the DX audit's
// P0: cancelling an apply reported success while the goroutine kept writing.
//
// Both writers used to be unconditional UPDATEs, so they raced and whichever
// reached SQLite last won. An apply that had already committed files and marked
// the change set applied could be recorded as cancelled -- the operation log
// disagreeing with what was on disk.
//
// The two writers are not equals, so the fix is not symmetric: the operation's
// own goroutine knows what happened and writes unconditionally; a cancel is a
// request and is compare-and-swapped against the live states. Both orders are
// asserted here, because fixing only one is how this shipped wrong the first
// time.
func TestCancelNeverOverwritesAFinishedOperation(t *testing.T) {
	ctx := context.Background()

	t.Run("cancel after the work finished is refused", func(t *testing.T) {
		db := openTestDB(t)
		opID := newOperation(t, db)

		if err := db.SetOperationStatus(ctx, opID, domain.OperationSucceeded, "CHG-1"); err != nil {
			t.Fatal(err)
		}
		err := db.CancelOperationStatus(ctx, opID)
		if !errors.Is(err, ErrOperationNotLive) {
			t.Fatalf("cancel of a finished operation = %v, want ErrOperationNotLive", err)
		}
		if got := statusOf(t, db, opID); got != domain.OperationSucceeded {
			t.Errorf("a late cancel changed the outcome to %q; the work had already succeeded", got)
		}
	})

	t.Run("work that completes despite a cancel records the truth", func(t *testing.T) {
		db := openTestDB(t)
		opID := newOperation(t, db)

		// The cancel lands first, while the operation is still running.
		if err := db.CancelOperationStatus(ctx, opID); err != nil {
			t.Fatal(err)
		}
		// The apply then finishes anyway -- files committed, change set applied.
		// The log has to say so, or it contradicts the disk.
		if err := db.SetOperationStatus(ctx, opID, domain.OperationSucceeded, "CHG-1"); err != nil {
			t.Fatal(err)
		}
		if got := statusOf(t, db, opID); got != domain.OperationSucceeded {
			t.Errorf("operation reads %q after the apply completed; that is the audit's finding", got)
		}
	})

	t.Run("cancelling a live operation works", func(t *testing.T) {
		db := openTestDB(t)
		opID := newOperation(t, db)
		if err := db.CancelOperationStatus(ctx, opID); err != nil {
			t.Fatalf("cancelling a running operation failed: %v", err)
		}
		if got := statusOf(t, db, opID); got != domain.OperationCancelled {
			t.Errorf("status = %q, want cancelled", got)
		}
	})

	t.Run("cancelling an unknown id is refused, not silently accepted", func(t *testing.T) {
		db := openTestDB(t)
		if err := db.CancelOperationStatus(ctx, "no-such-operation"); !errors.Is(err, ErrOperationNotLive) {
			t.Errorf("cancel of an unknown id = %v, want ErrOperationNotLive", err)
		}
	})
}

func newOperation(t *testing.T, db *DB) string {
	t.Helper()
	opID, err := db.CreateOperation(context.Background(), "p1", "ini-1", "apply_change_set")
	if err != nil {
		t.Fatal(err)
	}
	return opID
}

func statusOf(t *testing.T, db *DB, opID string) domain.OperationStatus {
	t.Helper()
	op, err := db.GetOperation(context.Background(), opID)
	if err != nil {
		t.Fatal(err)
	}
	return op.Status
}
