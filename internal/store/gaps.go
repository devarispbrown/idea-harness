package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// The reads below exist for projection.BuildGaps (`idea gaps`). Each answers
// one ordering question the gap analysis needs to be exact rather than
// heuristic -- "was this applied after that evidence was recorded", "was this
// requirement revised after that decision was made" -- from data the store
// already records: the apply journal and the event log. None adds a column.

// ChangeSetWorkspaceTimes returns, for every change set of the initiative that
// ever touched the working tree, the last time it did: its most recent
// committed apply or rolled-back revert in the apply journal. Both count
// because both change the code a piece of verification evidence was recorded
// against -- a revert after a pass removes exactly what was verified.
//
// applied_at is NULL on journal rows written before it existed, and is never
// set on a rolled_back row; created_at is the same instant for both
// (MarkChangeSetApplied/Reverted write them from one clock read), so it is
// what is read.
func (db *DB) ChangeSetWorkspaceTimes(ctx context.Context, initiativeID string) (map[string]time.Time, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT j.change_set_id, MAX(j.created_at)
		FROM apply_journal j JOIN change_sets c ON c.id = j.change_set_id
		WHERE c.initiative_id = ? AND j.state IN (?, ?)
		GROUP BY j.change_set_id`, initiativeID, domain.ApplyCommitted, domain.ApplyRolledBack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var ms int64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		out[id] = time.UnixMilli(ms)
	}
	return out, rows.Err()
}

// GraphEventSeqs returns the highest event seq per (aggregate id, event type)
// for the decision and requirement events the gap analysis orders against
// each other: decision.created, decision.resolved, requirement.created and
// requirement.revised. Keyed "<aggregate id>|<event type>".
//
// Seq, not created_at: seq is the log's own total order, so two events in the
// same millisecond still compare correctly, and "revised after it was
// decided" is never a coin toss.
func (db *DB) GraphEventSeqs(ctx context.Context, initiativeID string) (map[string]int64, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT e.aggregate_id, e.event_type, MAX(e.seq) FROM events e
		WHERE (e.aggregate_type = 'decision' AND e.event_type IN ('decision.created', 'decision.resolved')
				AND e.aggregate_id IN (SELECT id FROM decisions WHERE initiative_id = ?))
			OR (e.aggregate_type = 'requirement' AND e.event_type IN ('requirement.created', 'requirement.revised')
				AND e.aggregate_id IN (SELECT id FROM requirements WHERE initiative_id = ?))
		GROUP BY e.aggregate_id, e.event_type`, initiativeID, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id, typ string
		var seq int64
		if err := rows.Scan(&id, &typ, &seq); err != nil {
			return nil, err
		}
		out[id+"|"+typ] = seq
	}
	return out, rows.Err()
}

// SetAssertionStatus records a human's judgment on an assumption or
// hypothesis: accepted (validated), disputed (contested, still unvalidated)
// or retracted (refuted). Retraction also writes an invalidates edge from
// the assertion to each live requirement or decision resting on it (see
// syncInvalidatesTx); a later accept or dispute removes them.
//
// It is the only path that moves an assertion out of `candidate`. Framing and
// research record every assumption as a candidate and nothing ever validated
// one, so `idea gaps` could report an unvalidated assumption that nothing
// could close. A superseded assertion is history, not a live claim, and is
// refused rather than resurrected.
func (db *DB) SetAssertionStatus(ctx context.Context, assertionID string, status domain.AssertionStatus, actor string) error {
	switch status {
	case domain.AssertionAccepted, domain.AssertionDisputed, domain.AssertionRetracted:
	default:
		return fmt.Errorf("status must be one of accepted, disputed, retracted (got %q)", status)
	}
	if actor == "" {
		actor = HumanActor
	}
	return db.Apply(ctx, Event{
		AggregateType: "assertion",
		AggregateID:   assertionID,
		Actor:         actor,
		EventType:     "assertion.status_changed",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE assertions SET status = ? WHERE id = ? AND status != ? AND superseded_by = ''`,
			status, assertionID, domain.AssertionSuperseded)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundOrState(tx, "assertions", assertionID, "assertion",
				fmt.Errorf("assertion %s is superseded; its replacement is the live claim", assertionID))
		}
		// Retracting writes assertion --invalidates--> every live node
		// resting on it; any other judgment removes them.
		var initiativeID string
		if err := tx.QueryRow(`SELECT initiative_id FROM assertions WHERE id = ?`, assertionID).Scan(&initiativeID); err != nil {
			return err
		}
		return db.syncInvalidatesTx(tx, initiativeID, actor)
	})
}
