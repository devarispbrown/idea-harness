package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/id"
)

// Integration bookkeeping (migration 39): the outbox, the per-subscriber
// cursor into the event log, and the local<->remote link table.
//
// None of these writes goes through db.Apply. Apply appends to `events`, and
// the integration bus is a TAILER of `events` -- an outbox write that appended
// an event would hand the bus a new event to translate on its next poll,
// forever. They are delivery state, in the same category as schema_migrations
// or the secret fingerprint key, not product state a reader of the event log
// needs to see.

// Outbox row statuses.
const (
	OutboxPending   = "pending"
	OutboxDelivered = "delivered"
	OutboxDead      = "dead"
)

// OutboxRow is one (subscriber, domain event) delivery.
type OutboxRow struct {
	ID             string
	Subscriber     string
	IdempotencyKey string
	EventSeq       int64
	Kind           string
	PayloadJSON    string
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	DeliveredAt    *time.Time
}

// OutboxInsert is the ingest-side shape of an outbox row.
type OutboxInsert struct {
	Subscriber     string
	IdempotencyKey string
	EventSeq       int64
	Kind           string
	PayloadJSON    string
}

// IntegrationCursor returns subscriber's last ingested event seq, and false
// when it has none yet (a subscriber seen for the first time).
func (db *DB) IntegrationCursor(ctx context.Context, subscriber string) (int64, bool, error) {
	var seq int64
	err := db.sql.QueryRowContext(ctx, `SELECT last_seq FROM integration_cursors WHERE subscriber = ?`, subscriber).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: integration cursor: %w", err)
	}
	return seq, true, nil
}

// IngestOutbox inserts rows and advances subscriber's cursor to lastSeq in ONE
// transaction. A row whose idempotency key already exists is ignored, not an
// error: that is the re-ingest of an event this subscriber already queued
// (a crash between the insert and the cursor write of a previous attempt can
// only ever re-offer rows, never lose them, because both commit together).
func (db *DB) IngestOutbox(ctx context.Context, subscriber string, lastSeq int64, rows []OutboxInsert) error {
	now := nowMS()
	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO integration_outbox
					(id, subscriber, idempotency_key, event_seq, kind, payload_json, status, attempts, next_attempt_at, last_error, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, '', ?)`,
				id.New(), r.Subscriber, r.IdempotencyKey, r.EventSeq, r.Kind, r.PayloadJSON, OutboxPending, now, now); err != nil {
				return fmt.Errorf("store: outbox insert: %w", err)
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO integration_cursors (subscriber, last_seq, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(subscriber) DO UPDATE SET last_seq = excluded.last_seq, updated_at = excluded.updated_at
			WHERE excluded.last_seq > integration_cursors.last_seq`,
			subscriber, lastSeq, now)
		if err != nil {
			return fmt.Errorf("store: cursor: %w", err)
		}
		return nil
	})
}

// NextOutbox returns subscriber's oldest pending row (lowest event seq), or nil
// when there is none. It does NOT filter on next_attempt_at: the dispatcher
// delivers in order, so a head row still in backoff must hold back the rows
// behind it (a Slack thread reply must not land before its root).
func (db *DB) NextOutbox(ctx context.Context, subscriber string) (*OutboxRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, subscriber, idempotency_key, event_seq, kind, payload_json, status, attempts, next_attempt_at, last_error, created_at, delivered_at
		FROM integration_outbox WHERE subscriber = ? AND status = ?
		ORDER BY event_seq ASC, created_at ASC, id ASC LIMIT 1`, subscriber, OutboxPending)
	r, err := scanOutbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func scanOutbox(row interface{ Scan(...any) error }) (*OutboxRow, error) {
	var r OutboxRow
	var next, created int64
	var delivered sql.NullInt64
	if err := row.Scan(&r.ID, &r.Subscriber, &r.IdempotencyKey, &r.EventSeq, &r.Kind, &r.PayloadJSON,
		&r.Status, &r.Attempts, &next, &r.LastError, &created, &delivered); err != nil {
		return nil, err
	}
	r.NextAttemptAt = time.UnixMilli(next)
	r.CreatedAt = time.UnixMilli(created)
	if delivered.Valid {
		t := time.UnixMilli(delivered.Int64)
		r.DeliveredAt = &t
	}
	return &r, nil
}

// MarkOutboxDelivered records a successful delivery. Conditional on the row
// still being pending, so a delivered row can never flip back.
func (db *DB) MarkOutboxDelivered(ctx context.Context, rowID string) error {
	now := nowMS()
	_, err := db.sql.ExecContext(ctx, `
		UPDATE integration_outbox SET status = ?, attempts = attempts + 1, delivered_at = ?, last_error = ''
		WHERE id = ? AND status = ?`, OutboxDelivered, now, rowID, OutboxPending)
	if err != nil {
		return fmt.Errorf("store: outbox delivered: %w", err)
	}
	return nil
}

// MarkOutboxFailed records a failed attempt: either a retry at nextAttempt or,
// when dead is true, the end of the line for this row.
func (db *DB) MarkOutboxFailed(ctx context.Context, rowID, lastError string, nextAttempt time.Time, dead bool) error {
	status := OutboxPending
	if dead {
		status = OutboxDead
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE integration_outbox SET status = ?, attempts = attempts + 1, next_attempt_at = ?, last_error = ?
		WHERE id = ? AND status = ?`, status, nextAttempt.UnixMilli(), truncateErr(lastError), rowID, OutboxPending)
	if err != nil {
		return fmt.Errorf("store: outbox failed: %w", err)
	}
	return nil
}

// truncateErr bounds a remote error message before it is stored: it is shown
// back in `idea integrations status`, and a remote server is free to answer
// with an arbitrarily large body.
func truncateErr(s string) string {
	const max = 500
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// OutboxStats summarizes one subscriber's outbox for health reporting.
type OutboxStats struct {
	Pending         int        `json:"pending"`
	Delivered       int        `json:"delivered"`
	Dead            int        `json:"dead"`
	LastError       string     `json:"last_error,omitempty"`
	LastDeliveredAt *time.Time `json:"last_delivered_at,omitempty"`
}

// IntegrationOutboxStats returns counts by status, the newest recorded error
// (from a pending or dead row), and the newest delivery time.
func (db *DB) IntegrationOutboxStats(ctx context.Context, subscriber string) (OutboxStats, error) {
	var s OutboxStats
	rows, err := db.sql.QueryContext(ctx, `SELECT status, COUNT(*) FROM integration_outbox WHERE subscriber = ? GROUP BY status`, subscriber)
	if err != nil {
		return s, fmt.Errorf("store: outbox stats: %w", err)
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return s, err
		}
		switch st {
		case OutboxPending:
			s.Pending = n
		case OutboxDelivered:
			s.Delivered = n
		case OutboxDead:
			s.Dead = n
		}
	}
	rows.Close()
	var lastErr sql.NullString
	err = db.sql.QueryRowContext(ctx, `
		SELECT last_error FROM integration_outbox
		WHERE subscriber = ? AND last_error != '' AND status IN (?, ?)
		ORDER BY next_attempt_at DESC LIMIT 1`, subscriber, OutboxPending, OutboxDead).Scan(&lastErr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	s.LastError = lastErr.String
	var last sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT MAX(delivered_at) FROM integration_outbox WHERE subscriber = ?`, subscriber).Scan(&last); err != nil {
		return s, err
	}
	if last.Valid {
		t := time.UnixMilli(last.Int64)
		s.LastDeliveredAt = &t
	}
	return s, nil
}

// IntegrationLink maps a local object to a remote one.
type IntegrationLink struct {
	Integration string    `json:"integration"`
	Kind        string    `json:"kind"`
	LocalID     string    `json:"local_id"`
	RemoteID    string    `json:"remote_id"`
	RemoteKey   string    `json:"remote_key,omitempty"`
	RemoteURL   string    `json:"remote_url,omitempty"`
	RemoteState string    `json:"remote_state,omitempty"`
	Note        string    `json:"note,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const linkColumns = `integration, kind, local_id, remote_id, remote_key, remote_url, remote_state, note, created_at, updated_at`

func scanLink(row interface{ Scan(...any) error }) (*IntegrationLink, error) {
	var l IntegrationLink
	var created, updated int64
	if err := row.Scan(&l.Integration, &l.Kind, &l.LocalID, &l.RemoteID, &l.RemoteKey, &l.RemoteURL,
		&l.RemoteState, &l.Note, &created, &updated); err != nil {
		return nil, err
	}
	l.CreatedAt = time.UnixMilli(created)
	l.UpdatedAt = time.UnixMilli(updated)
	return &l, nil
}

// PutIntegrationLink inserts a link, or updates the remote side of an
// existing one (same integration/kind/local id).
func (db *DB) PutIntegrationLink(ctx context.Context, l IntegrationLink) error {
	now := nowMS()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO integration_links (`+linkColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(integration, kind, local_id) DO UPDATE SET
			remote_id = excluded.remote_id, remote_key = excluded.remote_key, remote_url = excluded.remote_url,
			remote_state = excluded.remote_state, note = excluded.note, updated_at = excluded.updated_at`,
		l.Integration, l.Kind, l.LocalID, l.RemoteID, l.RemoteKey, l.RemoteURL, l.RemoteState, l.Note, now, now)
	if err != nil {
		return fmt.Errorf("store: integration link: %w", err)
	}
	return nil
}

// GetIntegrationLink returns the link for a local object, or nil when none.
func (db *DB) GetIntegrationLink(ctx context.Context, integration, kind, localID string) (*IntegrationLink, error) {
	l, err := scanLink(db.sql.QueryRowContext(ctx, `SELECT `+linkColumns+` FROM integration_links
		WHERE integration = ? AND kind = ? AND local_id = ?`, integration, kind, localID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

// GetIntegrationLinkByRemote returns the link for a remote object, or nil.
func (db *DB) GetIntegrationLinkByRemote(ctx context.Context, integration, kind, remoteID string) (*IntegrationLink, error) {
	l, err := scanLink(db.sql.QueryRowContext(ctx, `SELECT `+linkColumns+` FROM integration_links
		WHERE integration = ? AND kind = ? AND remote_id = ? ORDER BY created_at ASC LIMIT 1`, integration, kind, remoteID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

// ListIntegrationLinks returns every link of one integration (and kind, when
// kind is non-empty), oldest first.
func (db *DB) ListIntegrationLinks(ctx context.Context, integration, kind string) ([]IntegrationLink, error) {
	q := `SELECT ` + linkColumns + ` FROM integration_links WHERE integration = ?`
	args := []any{integration}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at ASC, local_id ASC`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list integration links: %w", err)
	}
	defer rows.Close()
	out := []IntegrationLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// RecordExternalNote appends an initiative.external_note event: a
// non-destructive, human-visible record that something happened to this
// initiative OUTSIDE the harness (a Linear issue was canceled). It changes no
// state -- an integration never gets to cancel, delete or transition anything
// on its own say-so -- and it is an event precisely so it shows up where a
// human already looks (What Changed, the activity feed).
func (db *DB) RecordExternalNote(ctx context.Context, initiativeID, actor, note string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.ApplyNoop(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.external_note",
		Operation:     truncateErr(note),
	})
}

// PreviousInitiativeStatus returns the status an initiative held, per the event
// log, immediately before event seq beforeSeq. With skipRunning, `running` --
// a claim, not a milestone -- is skipped. Returns "" when there is none.
//
// The integration bus needs this because a status event is not a status
// CHANGE: every execute/apply claims plan_approved -> running and releases
// running -> plan_approved, so a naive "status became plan_approved" rule would
// announce "plan approved" to Slack after every apply. The previous
// non-running status is what distinguishes a release from a real transition.
func (db *DB) PreviousInitiativeStatus(ctx context.Context, initiativeID string, beforeSeq int64, skipRunning bool) (string, error) {
	var eventType, op string
	running := ""
	if skipRunning {
		running = "running"
	}
	err := db.sql.QueryRowContext(ctx, `
		SELECT event_type, operation FROM events
		WHERE aggregate_type = 'initiative' AND aggregate_id = ? AND seq < ?
		  AND (event_type = 'initiative.failed' OR (event_type = 'initiative.status_changed' AND operation != ?))
		ORDER BY seq DESC LIMIT 1`, initiativeID, beforeSeq, running).Scan(&eventType, &op)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: previous initiative status: %w", err)
	}
	if eventType == "initiative.failed" {
		return "failed", nil
	}
	return op, nil
}

// MetricRef is what the integration bus needs to narrate an outcome metric's
// drift: its initiative, its code and name, and its objective's code.
type MetricRef struct {
	InitiativeID  string
	Code          string
	Name          string
	ObjectiveCode string
}

// GetMetricRef resolves an outcome metric to its initiative and codes.
func (db *DB) GetMetricRef(ctx context.Context, metricID string) (*MetricRef, error) {
	var r MetricRef
	var obj sql.NullString
	err := db.sql.QueryRowContext(ctx, `
		SELECT m.initiative_id, m.code, m.name, o.code
		FROM outcome_metrics m LEFT JOIN objectives o ON o.id = m.objective_id
		WHERE m.id = ?`, metricID).Scan(&r.InitiativeID, &r.Code, &r.Name, &obj)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("metric", metricID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: metric ref: %w", err)
	}
	r.ObjectiveCode = obj.String
	return &r, nil
}

// VerificationEvidenceRef is what the integration bus needs to narrate one
// recorded piece of verification evidence.
type VerificationEvidenceRef struct {
	InitiativeID    string
	RequirementCode string
	Status          string
	Note            string
}

// GetVerificationEvidenceRef resolves a verification_evidence row to its
// initiative and requirement code.
func (db *DB) GetVerificationEvidenceRef(ctx context.Context, evidenceID string) (*VerificationEvidenceRef, error) {
	var r VerificationEvidenceRef
	var code sql.NullString
	err := db.sql.QueryRowContext(ctx, `
		SELECT v.initiative_id, r.code, v.status, v.note
		FROM verification_evidence v LEFT JOIN requirements r ON r.id = v.requirement_id
		WHERE v.id = ?`, evidenceID).Scan(&r.InitiativeID, &code, &r.Status, &r.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("verification evidence", evidenceID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: verification evidence ref: %w", err)
	}
	r.RequirementCode = code.String
	return &r, nil
}
