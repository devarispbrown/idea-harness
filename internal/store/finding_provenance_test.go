package store

import (
	"context"
	"testing"
)

// TestUnknownFindingDoesNotRecordAHumanDecision pins the provenance-integrity
// finding.
//
// SetFindingDisposition and SetFindingNote were bare UPDATEs with no
// RowsAffected check, while db.Apply commits the event in the same transaction.
// So a stale or human-readable ref returned success, changed nothing, and left
// a durable finding.dispositioned event saying a human accepted the risk.
//
// The event log is the thing this product sells as provable provenance, so a
// write that records a decision nobody made is worse than an error.
func TestUnknownFindingDoesNotRecordAHumanDecision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	before := countEvents(t, db)

	if err := db.SetFindingDisposition(ctx, "no-such-finding", "accepted", "someone"); err == nil {
		t.Error("dispositioning an unknown finding succeeded")
	}
	if err := db.SetFindingNote(ctx, "no-such-finding", "a note", "someone"); err == nil {
		t.Error("noting an unknown finding succeeded")
	}

	if after := countEvents(t, db); after != before {
		t.Errorf("%d event(s) recorded for a finding that does not exist; "+
			"the provenance log must not carry decisions nobody made", after-before)
	}
}

func countEvents(t *testing.T, db *DB) int {
	t.Helper()
	evs, err := db.ListEvents(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}
