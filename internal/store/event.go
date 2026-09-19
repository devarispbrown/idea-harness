package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/id"
)

// Event is an append-only event record (§32). PayloadDigest references a blob
// already stored (and redacted) by the caller.
type Event struct {
	ID            string
	AggregateType string
	AggregateID   string
	Actor         string
	CorrelationID string
	CausationID   string
	Operation     string
	EventType     string
	PayloadDigest string
}

// Apply runs fn and appends ev in a single transaction. If fn returns an error,
// neither the state mutation nor the event is persisted (§32 atomicity).
func (db *DB) Apply(ctx context.Context, ev Event, fn func(tx *sql.Tx) error) error {
	if ev.ID == "" {
		ev.ID = id.New()
	}
	if ev.AggregateType == "" || ev.AggregateID == "" || ev.EventType == "" {
		return fmt.Errorf("store: event requires aggregate_type, aggregate_id, event_type")
	}
	if ev.PayloadDigest == "" {
		ev.PayloadDigest = "-"
	}

	return db.InTx(ctx, func(tx *sql.Tx) error {
		if fn != nil {
			if err := fn(tx); err != nil {
				return err
			}
		}
		return appendEvent(tx, ev)
	})
}

// ApplyNoop appends an event with no accompanying state mutation.
func (db *DB) ApplyNoop(ctx context.Context, ev Event) error {
	return db.Apply(ctx, ev, nil)
}

func appendEvent(tx *sql.Tx, ev Event) error {
	_, err := tx.Exec(`
		INSERT INTO events
			(id, aggregate_type, aggregate_id, actor, correlation_id, causation_id, operation, event_type, payload_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.AggregateType, ev.AggregateID, ev.Actor, ev.CorrelationID,
		ev.CausationID, ev.Operation, ev.EventType, ev.PayloadDigest, nowMS(),
	)
	return err
}

// ListEvents returns events with seq >= sinceSeq ordered by seq ascending.
func (db *DB) ListEvents(ctx context.Context, sinceSeq int64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT seq, id, aggregate_type, aggregate_id, actor, correlation_id, causation_id, operation, event_type, payload_digest, created_at
		FROM events WHERE seq > ? ORDER BY seq ASC LIMIT ?`, sinceSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	out := make([]EventRow, 0)
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Seq, &e.ID, &e.AggregateType, &e.AggregateID, &e.Actor,
			&e.CorrelationID, &e.CausationID, &e.Operation, &e.EventType, &e.PayloadDigest,
			&e.CreatedAtMS); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventRow is a materialized event.
type EventRow struct {
	Seq           int64  `json:"seq"`
	ID            string `json:"id"`
	AggregateType string `json:"aggregate_type"`
	AggregateID   string `json:"aggregate_id"`
	Actor         string `json:"actor"`
	CorrelationID string `json:"correlation_id"`
	CausationID   string `json:"causation_id"`
	Operation     string `json:"operation"`
	EventType     string `json:"event_type"`
	PayloadDigest string `json:"payload_digest"`
	CreatedAtMS   int64  `json:"created_at_ms"`
}

// CreatedAt converts the stored unix-ms timestamp to time.Time.
func (e EventRow) CreatedAt() time.Time { return time.UnixMilli(e.CreatedAtMS) }

// ListEventsForInitiative returns events with seq > sinceSeq that belong to
// initiativeID, ordered by seq ascending (C1/JTBD 6 fix). Events carry no
// initiative_id of their own, so each aggregate type is joined back to its
// owning table's initiative_id -- the same scoping BuildTimeline uses for
// change_set/decision events, extended here to every aggregate BuildChanged
// narrates. Before this, BuildChanged called ListEvents (the whole project,
// every initiative) and "what changed" for one initiative could report
// another's counts entirely.
func (db *DB) ListEventsForInitiative(ctx context.Context, initiativeID string, sinceSeq int64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT e.seq, e.id, e.aggregate_type, e.aggregate_id, e.actor, e.correlation_id, e.causation_id, e.operation, e.event_type, e.payload_digest, e.created_at
		FROM events e WHERE e.seq > ? AND (
			(e.aggregate_type = 'initiative' AND e.aggregate_id = ?)
			OR (e.aggregate_type = 'assertion' AND e.aggregate_id IN (SELECT id FROM assertions WHERE initiative_id = ?))
			OR (e.aggregate_type = 'requirement' AND e.aggregate_id IN (SELECT id FROM requirements WHERE initiative_id = ?))
			OR (e.aggregate_type = 'decision' AND e.aggregate_id IN (SELECT id FROM decisions WHERE initiative_id = ?))
			OR (e.aggregate_type = 'artifact' AND e.aggregate_id IN (SELECT id FROM artifacts WHERE initiative_id = ?))
			OR (e.aggregate_type = 'question' AND e.aggregate_id IN (SELECT id FROM questions WHERE initiative_id = ?))
			OR (e.aggregate_type = 'finding' AND e.aggregate_id IN (SELECT id FROM findings WHERE initiative_id = ?))
			OR (e.aggregate_type = 'change_set' AND e.aggregate_id IN (SELECT id FROM change_sets WHERE initiative_id = ?))
			OR (e.aggregate_type = 'task' AND e.aggregate_id IN (SELECT id FROM tasks WHERE initiative_id = ?))
		)
		ORDER BY e.seq ASC LIMIT ?`,
		sinceSeq, initiativeID, initiativeID, initiativeID, initiativeID, initiativeID, initiativeID, initiativeID, initiativeID, initiativeID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list events for initiative: %w", err)
	}
	defer rows.Close()

	out := make([]EventRow, 0)
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Seq, &e.ID, &e.AggregateType, &e.AggregateID, &e.Actor,
			&e.CorrelationID, &e.CausationID, &e.Operation, &e.EventType, &e.PayloadDigest,
			&e.CreatedAtMS); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MaxEventSeq returns the current maximum event sequence (0 if empty).
func (db *DB) MaxEventSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT MAX(seq) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: max seq: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}
