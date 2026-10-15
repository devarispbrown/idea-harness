package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Run events: the normalized stream of a coding agent's run (domain.RunEvent).
// Storage is bounded two ways, because a chatty agent on a long task can emit
// thousands of events and nothing here is meant to be an archive:
//
//   - per run, the newest RunEventsPerRunMax events are kept. seq keeps
//     counting, so a client's cursor stays valid; a page reports FirstSeq so
//     a reader can tell that older events were dropped.
//   - per project, only the RunEventsKeepRuns most recent runs keep any
//     events. The run's change set, findings and manifest are the durable
//     record; the event stream is the live view and the run report.
const (
	RunEventsPerRunMax = 5000
	RunEventsKeepRuns  = 50
	// RunEventsPageMax bounds one page of ListRunEvents.
	RunEventsPageMax = 500
)

// RunEventPage is one page of a run's events, oldest first.
type RunEventPage struct {
	RunID  string            `json:"run_id"`
	Events []domain.RunEvent `json:"events"`
	// FirstSeq is the oldest seq still stored for the run (0 when it has
	// none). Greater than 1 means the per-run cap dropped earlier events.
	FirstSeq int64 `json:"first_seq"`
	// LastSeq is the newest seq stored for the run: the cursor to resume
	// from once this page has been read and More is false.
	LastSeq int64 `json:"last_seq"`
	// More reports that events after this page exist; ask again with
	// after = the last event's seq.
	More bool `json:"more"`
}

// RunEventRow is a stored event with its project-wide id, the cursor of the
// project event stream.
type RunEventRow struct {
	ID    int64
	Event domain.RunEvent
}

// AppendRunEvents stores events for runID, assigning each the next seq. The
// caller has already redacted them; they are bounded here. Returns the
// events as stored. RunID on each event is overwritten with runID.
func (db *DB) AppendRunEvents(ctx context.Context, runID string, evs []domain.RunEvent) ([]domain.RunEvent, error) {
	if runID == "" {
		return nil, fmt.Errorf("store: run events: run id is required")
	}
	if len(evs) == 0 {
		return nil, nil
	}
	var out []domain.RunEvent
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = appendRunEventsTx(ctx, tx, runID, evs)
		return err
	})
	if err != nil {
		return nil, err
	}
	db.signalRunEvents()
	return out, nil
}

// appendRunEventsTx is AppendRunEvents inside a caller's transaction. The
// caller must call db.signalRunEvents after commit.
func appendRunEventsTx(ctx context.Context, tx *sql.Tx, runID string, evs []domain.RunEvent) ([]domain.RunEvent, error) {
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM run_events WHERE run_id = ?`, runID).Scan(&last); err != nil {
		return nil, fmt.Errorf("store: run events: next seq: %w", err)
	}
	seq := last.Int64
	first := seq == 0
	out := make([]domain.RunEvent, 0, len(evs))
	for _, ev := range evs {
		seq++
		ev = ev.Bounded()
		ev.RunID, ev.Seq = runID, seq
		if ev.At.IsZero() {
			ev.At = time.Now().UTC()
		}
		body, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("store: run events: encode: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_events (run_id, seq, initiative_id, kind, at, body_json) VALUES (?, ?, ?, ?, ?, ?)`,
			runID, seq, ev.InitiativeID, string(ev.Kind), ev.At.UnixMilli(), string(body)); err != nil {
			return nil, fmt.Errorf("store: run events: insert: %w", err)
		}
		out = append(out, ev)
	}
	// Per-run cap: drop from the front. seq is never reused, so a client's
	// cursor stays meaningful.
	if seq > RunEventsPerRunMax {
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_events WHERE run_id = ? AND seq <= ?`, runID, seq-RunEventsPerRunMax); err != nil {
			return nil, fmt.Errorf("store: run events: cap: %w", err)
		}
	}
	// Per-project cap, once per run (on its first event): keep the newest
	// runs' events only.
	if first {
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_events WHERE run_id NOT IN (
			SELECT run_id FROM run_events GROUP BY run_id ORDER BY MIN(id) DESC LIMIT ?)`, RunEventsKeepRuns); err != nil {
			return nil, fmt.Errorf("store: run events: prune: %w", err)
		}
	}
	return out, nil
}

// ListRunEvents returns up to limit of runID's events with seq > after,
// oldest first.
func (db *DB) ListRunEvents(ctx context.Context, runID string, after int64, limit int) (RunEventPage, error) {
	if limit <= 0 || limit > RunEventsPageMax {
		limit = RunEventsPageMax
	}
	page := RunEventPage{RunID: runID, Events: []domain.RunEvent{}}
	var first, last sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT MIN(seq), MAX(seq) FROM run_events WHERE run_id = ?`, runID).Scan(&first, &last); err != nil {
		return page, fmt.Errorf("store: run events: bounds: %w", err)
	}
	page.FirstSeq, page.LastSeq = first.Int64, last.Int64
	rows, err := db.sql.QueryContext(ctx, `SELECT body_json FROM run_events WHERE run_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		runID, after, limit+1)
	if err != nil {
		return page, fmt.Errorf("store: run events: list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return page, err
		}
		if len(page.Events) == limit {
			page.More = true
			break
		}
		var ev domain.RunEvent
		if err := json.Unmarshal([]byte(body), &ev); err != nil {
			continue
		}
		page.Events = append(page.Events, ev)
	}
	return page, rows.Err()
}

// ListAllRunEvents returns every stored event of a run, oldest first (the
// run report). Bounded by RunEventsPerRunMax.
func (db *DB) ListAllRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error) {
	var all []domain.RunEvent
	after := int64(0)
	for {
		page, err := db.ListRunEvents(ctx, runID, after, RunEventsPageMax)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Events...)
		if !page.More || len(page.Events) == 0 {
			return all, nil
		}
		after = page.Events[len(page.Events)-1].Seq
	}
}

// ListRunEventsAfterID returns up to limit events of any run with a
// project-wide id > afterID, oldest first: the project event stream's run
// events.
func (db *DB) ListRunEventsAfterID(ctx context.Context, afterID int64, limit int) ([]RunEventRow, error) {
	if limit <= 0 || limit > RunEventsPageMax {
		limit = RunEventsPageMax
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT id, body_json FROM run_events WHERE id > ? ORDER BY id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: run events: list after id: %w", err)
	}
	defer rows.Close()
	out := []RunEventRow{}
	for rows.Next() {
		var r RunEventRow
		var body string
		if err := rows.Scan(&r.ID, &body); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(body), &r.Event) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunEventCostTotals sums a run's stored cost events: what an ingester that
// turns running totals into deltas has already accounted for (an OpenHands
// watcher re-attaching after a restart starts from here, not from zero).
func (db *DB) RunEventCostTotals(ctx context.Context, runID string) (in, out int64, cost float64, err error) {
	var i, o sql.NullInt64
	var c sql.NullFloat64
	err = db.sql.QueryRowContext(ctx, `SELECT
		SUM(COALESCE(json_extract(body_json, '$.input_tokens'), 0)),
		SUM(COALESCE(json_extract(body_json, '$.output_tokens'), 0)),
		SUM(COALESCE(json_extract(body_json, '$.cost_usd'), 0))
		FROM run_events WHERE run_id = ? AND kind = ?`, runID, string(domain.RunEventCost)).Scan(&i, &o, &c)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: run events: cost totals: %w", err)
	}
	return i.Int64, o.Int64, c.Float64, nil
}

// MaxRunEventID is the newest project-wide run event id (0 when none).
func (db *DB) MaxRunEventID(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT MAX(id) FROM run_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: run events: max id: %w", err)
	}
	return n.Int64, nil
}

// LatestEventRunForInitiative returns the run id of the most recent run of
// the initiative that has events ("" when none): what the run view shows
// once nothing is running.
func (db *DB) LatestEventRunForInitiative(ctx context.Context, initiativeID string) (string, error) {
	var runID string
	err := db.sql.QueryRowContext(ctx, `SELECT run_id FROM run_events WHERE initiative_id = ? ORDER BY id DESC LIMIT 1`, initiativeID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: run events: latest run: %w", err)
	}
	return runID, nil
}

// RunEventsChanged returns a channel closed at the next append of any run
// event. Take it BEFORE reading, then read, then wait on it: an append
// between the read and the wait still closes the channel taken first.
func (db *DB) RunEventsChanged() <-chan struct{} {
	db.runEventsMu.Lock()
	defer db.runEventsMu.Unlock()
	if db.runEventsCh == nil {
		db.runEventsCh = make(chan struct{})
	}
	return db.runEventsCh
}

func (db *DB) signalRunEvents() {
	db.runEventsMu.Lock()
	defer db.runEventsMu.Unlock()
	if db.runEventsCh != nil {
		close(db.runEventsCh)
		db.runEventsCh = nil
	}
}

// RunInfo is the part of a runs row the run view and report need.
type RunInfo struct {
	ID           string
	Kind         string
	Status       string
	InitiativeID string
	Stage        string
	Note         string
	StartedAt    time.Time
	FinishedAt   *time.Time
}

// GetRunInfo returns one run, or a *NotFoundError.
func (db *DB) GetRunInfo(ctx context.Context, runID string) (RunInfo, error) {
	var r RunInfo
	var started sql.NullInt64
	var finished sql.NullInt64
	var note, stage, ini sql.NullString
	err := db.sql.QueryRowContext(ctx, `SELECT id, kind, status, initiative_id, stage, note, started_at, finished_at FROM runs WHERE id = ?`, runID).
		Scan(&r.ID, &r.Kind, &r.Status, &ini, &stage, &note, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return r, &NotFoundError{Kind: "run", Ref: runID}
	}
	if err != nil {
		return r, fmt.Errorf("store: get run: %w", err)
	}
	r.InitiativeID, r.Stage, r.Note = ini.String, stage.String, note.String
	if started.Valid {
		r.StartedAt = time.UnixMilli(started.Int64).UTC()
	}
	if finished.Valid {
		t := time.UnixMilli(finished.Int64).UTC()
		r.FinishedAt = &t
	}
	return r, nil
}

// RunningOperationID is the id of the initiative's running operation ("" when
// none), the same row SetOperationStage writes.
func (db *DB) RunningOperationID(ctx context.Context, initiativeID string) string {
	var opID string
	_ = db.sql.QueryRowContext(ctx, `SELECT id FROM operations WHERE initiative_id = ? AND status = ? ORDER BY created_at DESC LIMIT 1`,
		initiativeID, domain.OperationRunning).Scan(&opID)
	return opID
}
