package store

import (
	"context"
	"database/sql"
)

// noOpBackfillMarkerKey is the project_state row key recording that the
// migration-34 no_op backfill (review MEDIUM #1) has already run for this
// project, so it runs at most once per project's lifetime rather than on
// every daemon start.
const noOpBackfillMarkerKey = "no_op_backfill_v1"

// ClaimNoOpBackfill reports whether THIS call is the one that gets to run the
// no_op backfill: it inserts the marker row and returns true only when the
// insert actually happened. Every later caller -- including a concurrent
// daemon start racing this one, or the next time this project is opened --
// finds the row already present and gets false. The marker is never cleared,
// so the backfill is a true one-time operation per project database, not a
// per-daemon-lifetime one.
func (db *DB) ClaimNoOpBackfill(ctx context.Context) (bool, error) {
	res, err := db.sql.ExecContext(ctx,
		`INSERT INTO project_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		noOpBackfillMarkerKey, "done")
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ChangeSetPatchRef is the minimal projection of a change set needed to
// recompute its no_op flag from its patch manifest.
type ChangeSetPatchRef struct {
	ID          string
	PatchDigest string
}

// ListChangeSetsForNoOpBackfill returns every change set currently recorded
// as no_op = 0. Before migration 34 introduced the column, EVERY change set
// defaulted to it (review MEDIUM #1's "0 for every change set created before
// this migration" default), including ones whose patch manifest, persisted
// unconditionally since efac66d, actually touched zero files. This package
// cannot recompute the flag itself -- that requires reading the patch
// manifest out of the blob store, which store does not import (migrations
// are plain SQL text; store has no fsdiff/blob dependency) -- so it hands the
// candidates to a caller that can (workflow.Engine.BackfillNoOpChangeSets).
func (db *DB) ListChangeSetsForNoOpBackfill(ctx context.Context) ([]ChangeSetPatchRef, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, patch_digest FROM change_sets WHERE no_op = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChangeSetPatchRef, 0)
	for rows.Next() {
		var r ChangeSetPatchRef
		if err := rows.Scan(&r.ID, &r.PatchDigest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetChangeSetNoOp backfills the no_op flag for one change set (review MEDIUM
// #1), after the caller has determined from the patch manifest that it
// touched zero files. This is the only writer of no_op other than
// CreateChangeSet's implicit default.
func (db *DB) SetChangeSetNoOp(ctx context.Context, changeSetID string, noOp bool) error {
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         ActorOrchestrator,
		EventType:     "change_set.no_op_backfilled",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET no_op = ? WHERE id = ?`, noOp, changeSetID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "change set %q not found", changeSetID)
	})
}
