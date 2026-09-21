package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// ---------- Runs ----------

// CreateRun creates a run row and returns its id. actor is the producing agent
// (role name) and is recorded on the run.created event (§14 actor separation).
// initiativeID and stage attribute the run to an initiative and workflow
// stage (C3, wave 3 contract), so GET .../costs can group spend/time by phase;
// both may be empty (e.g. a run with no initiative context).
func (db *DB) CreateRun(ctx context.Context, kind domain.RunKind, stageRunID, actor, initiativeID, stage string) (string, error) {
	if !kind.Valid() {
		return "", fmt.Errorf("store: invalid run kind %q", kind)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	runID := id.New()
	err := db.Apply(ctx, Event{
		AggregateType: "run",
		AggregateID:   runID,
		Actor:         actor,
		EventType:     "run.created",
		Operation:     string(kind),
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (id, kind, stage_run_id, status, initiative_id, stage, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			runID, kind, nullable(stageRunID), domain.RunPending, initiativeID, stage, time.Now().UTC().UnixMilli())
		return err
	})
	if err != nil {
		return "", err
	}
	return runID, nil
}

// SetRunNote records a human note on a run (D-a, wave 3 contract): the
// optional note on `POST .../execute`/`POST .../retry`, appended to the
// implementer's brief and kept here as the run's own record of it. Plain
// update, no event -- the note is provenance on the run, not itself a fact the
// event log narrates (the run's own created/status events already are).
func (db *DB) SetRunNote(ctx context.Context, runID, note string) error {
	_, err := db.SQL().ExecContext(ctx, `UPDATE runs SET note = ? WHERE id = ?`, note, runID)
	return err
}

// SetRunStatus transitions a run's status.
func (db *DB) SetRunStatus(ctx context.Context, runID string, status domain.RunStatus, actor string) error {
	if !status.Valid() {
		return fmt.Errorf("store: invalid run status %q", status)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "run",
		AggregateID:   runID,
		Actor:         actor,
		EventType:     "run.status_changed",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		finished := sql.NullInt64{}
		if status == domain.RunSucceeded || status == domain.RunFailed {
			finished = sql.NullInt64{Int64: time.Now().UTC().UnixMilli(), Valid: true}
		}
		// RowsAffected, for the same reason as the finding writes: db.Apply
		// commits the event whenever the closure returns nil, so a bogus run id
		// recorded a run.status_changed for a run that does not exist.
		res, err := tx.Exec(`UPDATE runs SET status = ?, finished_at = ? WHERE id = ?`, status, finished, runID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &NotFoundError{Kind: "run", Ref: runID}
		}
		return nil
	})
}

// CreateRunManifest records immutable run provenance (§36).
func (db *DB) CreateRunManifest(ctx context.Context, m *domain.RunManifest) error {
	if m.ID == "" {
		m.ID = id.New()
	}
	if m.StartedAt.IsZero() {
		m.StartedAt = time.Now().UTC()
	}
	if m.FinishedAt.IsZero() {
		m.FinishedAt = m.StartedAt
	}
	return db.Apply(ctx, Event{
		AggregateType: "run_manifest",
		AggregateID:   m.ID,
		Actor:         ActorOrchestrator,
		EventType:     "run_manifest.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO run_manifests
				(id, run_id, snapshot_digest, config_digest, workflow_digest, role_version, requested_model, actual_model, context_digest, skill_digests_json, tool_versions_json, usage_json, result_digest, started_at, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.RunID, m.SnapshotDigest, m.ConfigDigest, m.WorkflowDigest, m.RoleVersion,
			m.RequestedModel, m.ActualModel, m.ContextDigest, mustJSON(m.SkillDigests),
			mustJSON(m.ToolVersions), mustJSON(m.Usage), m.ResultDigest, m.StartedAt.UnixMilli(), m.FinishedAt.UnixMilli())
		return err
	})
}

// GetRunManifest returns a manifest by run id.
func (db *DB) GetRunManifest(ctx context.Context, runID string) (domain.RunManifest, error) {
	var m domain.RunManifest
	var skills, tools, usage string
	var started, finished int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT id, run_id, snapshot_digest, config_digest, workflow_digest, role_version, requested_model, actual_model, context_digest, skill_digests_json, tool_versions_json, usage_json, result_digest, started_at, finished_at
		FROM run_manifests WHERE run_id = ?`, runID).
		Scan(&m.ID, &m.RunID, &m.SnapshotDigest, &m.ConfigDigest, &m.WorkflowDigest, &m.RoleVersion,
			&m.RequestedModel, &m.ActualModel, &m.ContextDigest, &skills, &tools, &usage,
			&m.ResultDigest, &started, &finished)
	if err != nil {
		return m, err
	}
	m.StartedAt = time.UnixMilli(started)
	m.FinishedAt = time.UnixMilli(finished)
	_ = jsonUnmarshal(skills, &m.SkillDigests)
	_ = jsonUnmarshal(tools, &m.ToolVersions)
	_ = jsonUnmarshal(usage, &m.Usage)
	return m, nil
}

// ---------- Operations ----------

// CreateOperation creates a durable operation and returns its id.
func (db *DB) CreateOperation(ctx context.Context, projectID, initiativeID, opType string) (string, error) {
	opID := id.New()
	err := db.Apply(ctx, Event{
		AggregateType: "operation",
		AggregateID:   opID,
		Actor:         ActorOrchestrator,
		EventType:     "operation.created",
		Operation:     opType,
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO operations (id, project_id, initiative_id, type, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			opID, projectID, nullable(initiativeID), opType, domain.OperationRunning, time.Now().UTC().UnixMilli())
		return err
	})
	if err != nil {
		return "", err
	}
	return opID, nil
}

// SetOperationStatus transitions an operation.
func (db *DB) SetOperationStatus(ctx context.Context, opID string, status domain.OperationStatus, resultRef string) error {
	if !status.Valid() {
		return fmt.Errorf("store: invalid operation status %q", status)
	}
	return db.Apply(ctx, Event{
		AggregateType: "operation",
		AggregateID:   opID,
		Actor:         ActorOrchestrator,
		EventType:     "operation.status_changed",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		finished := sql.NullInt64{}
		terminal := status == domain.OperationSucceeded || status == domain.OperationFailed || status == domain.OperationCancelled
		if terminal {
			finished = sql.NullInt64{Int64: time.Now().UTC().UnixMilli(), Valid: true}
		}
		// review Low fix: a terminal operation's `stage` is cleared, not left
		// showing the last step it was on ("Reviewing the PRD (cycle 2)")
		// forever after it finished.
		if terminal {
			_, err := tx.Exec(`UPDATE operations SET status = ?, result_ref = ?, finished_at = ?, stage = '' WHERE id = ?`,
				status, nullable(resultRef), finished, opID)
			return err
		}
		_, err := tx.Exec(`UPDATE operations SET status = ?, result_ref = ?, finished_at = ? WHERE id = ?`,
			status, nullable(resultRef), finished, opID)
		return err
	})
}

// maxOperationErrorBytes caps what SetOperationFailed persists on an
// operation. Agent output and provider error bodies are unbounded, and this
// column is read back on every poll of a busy Build page; a multi-megabyte
// stack trace would otherwise sit in the row and in every JSON response that
// includes it.
const maxOperationErrorBytes = 8 * 1024

// truncateOperationError caps s at n bytes without splitting a multi-byte
// UTF-8 rune at the boundary, which a plain s[:n] can do.
func truncateOperationError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s + "…(truncated)"
}

// SetOperationFailed transitions an operation to a terminal failed or
// cancelled status and records why (B2).
//
// Separate from SetOperationStatus rather than an added parameter there:
// every other caller of SetOperationStatus is the success path, which has no
// error to record, and giving them all a permanent "" argument to thread
// through would have obscured which of the two paths callers actually meant.
// errMsg is expected to already be redacted by the caller (the daemon layer,
// which holds the project's redactor); this only bounds its size.
func (db *DB) SetOperationFailed(ctx context.Context, opID string, status domain.OperationStatus, errMsg string) error {
	if status != domain.OperationFailed && status != domain.OperationCancelled {
		return fmt.Errorf("store: SetOperationFailed called with non-terminal-failure status %q", status)
	}
	errMsg = truncateOperationError(errMsg, maxOperationErrorBytes)
	return db.Apply(ctx, Event{
		AggregateType: "operation",
		AggregateID:   opID,
		Actor:         ActorOrchestrator,
		EventType:     "operation.status_changed",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		finished := sql.NullInt64{Int64: time.Now().UTC().UnixMilli(), Valid: true}
		// review Low fix: always terminal here, so stage is always cleared
		// (see SetOperationStatus's own comment on the same fix).
		_, err := tx.Exec(`UPDATE operations SET status = ?, error = ?, finished_at = ?, stage = '' WHERE id = ?`,
			status, errMsg, finished, opID)
		return err
	})
}

// CancelOperationStatus marks an operation cancelled, but only while it is still
// pending or running.
//
// Separate from SetOperationStatus, and compare-and-swap, because the two
// writers are not equals. The operation's own goroutine knows what actually
// happened and must be able to record it unconditionally; a cancel is a request,
// and a request that arrives after the work finished has to lose.
//
// Both used to be unconditional, so they raced and whichever reached SQLite last
// won -- an apply that had already committed files and marked the change set
// applied could be recorded as cancelled, the operation log disagreeing with the
// disk. Guarding only the cancel side fixes that in both orders: a late cancel is
// a no-op, and a cancel during a run is overwritten by the goroutine's terminal
// write when the work turns out to have completed anyway.
//
// Modelled on TransitionChangeSet, the CAS precedent in this package.
func (db *DB) CancelOperationStatus(ctx context.Context, opID string) error {
	return db.Apply(ctx, Event{
		AggregateType: "operation",
		AggregateID:   opID,
		Actor:         ActorOrchestrator,
		EventType:     "operation.status_changed",
		Operation:     string(domain.OperationCancelled),
	}, func(tx *sql.Tx) error {
		finished := sql.NullInt64{Int64: time.Now().UTC().UnixMilli(), Valid: true}
		// review Low fix: cleared here too (always terminal).
		res, err := tx.Exec(
			`UPDATE operations SET status = ?, finished_at = ?, stage = ''
			 WHERE id = ? AND status IN (?, ?)`,
			domain.OperationCancelled, finished, opID,
			domain.OperationPending, domain.OperationRunning)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrOperationNotLive
		}
		return nil
	})
}

// ErrOperationNotLive reports that a cancel found no pending-or-running row: the
// operation had already finished, or the id does not exist. Callers report it as
// "not cancellable" rather than treating it as a write failure.
var ErrOperationNotLive = errors.New("store: operation is not pending or running")

// GetOperation returns an operation.
func (db *DB) GetOperation(ctx context.Context, opID string) (domain.Operation, error) {
	var op domain.Operation
	var iniID, resultRef sql.NullString
	var finished sql.NullInt64
	var created int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT id, project_id, initiative_id, type, status, result_ref, error, stage, created_at, finished_at
		FROM operations WHERE id = ?`, opID).
		Scan(&op.ID, &op.ProjectID, &iniID, &op.Type, &op.Status, &resultRef, &op.Error, &op.Stage, &created, &finished)
	if err != nil {
		return op, notFoundIfNoRows(err, "operation", opID)
	}
	op.InitiativeID = iniID.String
	op.ResultRef = resultRef.String
	op.CreatedAt = time.UnixMilli(created)
	if finished.Valid {
		t := time.UnixMilli(finished.Int64)
		op.FinishedAt = &t
	}
	return op, nil
}

// PhaseCostRow is one (stage, run kind) group of an initiative's model-call
// spend/time (C3, wave 3 contract).
type PhaseCostRow struct {
	Stage      string
	CostUSD    float64
	DurationMS int64
	Calls      int
}

// ListModelPhaseCosts groups an initiative's RunModel-kind runs by stage,
// summing cost and duration from their manifests (C3, wave 3 contract). Agent
// (coding) runs are excluded -- see ListAgentRunStats -- since their cost is
// not always measurable the same way and they are reported separately.
func (db *DB) ListModelPhaseCosts(ctx context.Context, initiativeID string) ([]PhaseCostRow, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT r.stage, m.usage_json, m.started_at, m.finished_at
		FROM runs r JOIN run_manifests m ON m.run_id = r.id
		WHERE r.initiative_id = ? AND r.kind = ?
		ORDER BY r.stage ASC`, initiativeID, domain.RunModel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byStage := map[string]*PhaseCostRow{}
	order := make([]string, 0)
	for rows.Next() {
		var stage, usage string
		var started, finished int64
		if err := rows.Scan(&stage, &usage, &started, &finished); err != nil {
			return nil, err
		}
		if stage == "" {
			stage = "other"
		}
		row, ok := byStage[stage]
		if !ok {
			row = &PhaseCostRow{Stage: stage}
			byStage[stage] = row
			order = append(order, stage)
		}
		var u domain.RunUsage
		_ = jsonUnmarshal(usage, &u)
		row.CostUSD += u.CostUSD
		row.Calls++
		if u.DurationMS > 0 {
			row.DurationMS += u.DurationMS
		} else if finished > started {
			row.DurationMS += finished - started
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PhaseCostRow, 0, len(order))
	for _, stage := range order {
		out = append(out, *byStage[stage])
	}
	return out, nil
}

// AgentRunStats summarizes an initiative's coding-agent runs (C3, wave 3
// contract): how many ran, their total duration, and each run's own output
// (for ParseAgentCostUSD's best-effort cost parse), oldest first.
type AgentRunStats struct {
	Runs       int
	DurationMS int64
}

// ListAgentRunStats returns the count and total duration of an initiative's
// RunAgent-kind runs (C3, wave 3 contract).
func (db *DB) ListAgentRunStats(ctx context.Context, initiativeID string) (AgentRunStats, error) {
	var out AgentRunStats
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT m.usage_json, m.started_at, m.finished_at
		FROM runs r JOIN run_manifests m ON m.run_id = r.id
		WHERE r.initiative_id = ? AND r.kind = ?`, initiativeID, domain.RunAgent)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var usage string
		var started, finished int64
		if err := rows.Scan(&usage, &started, &finished); err != nil {
			return out, err
		}
		out.Runs++
		var u domain.RunUsage
		_ = jsonUnmarshal(usage, &u)
		if u.DurationMS > 0 {
			out.DurationMS += u.DurationMS
		} else if finished > started {
			out.DurationMS += finished - started
		}
	}
	return out, rows.Err()
}

// SetOperationStage records the engine's current step on the initiative's
// running operation (C3, wave 3 contract). It is a plain, event-free update:
// the stage is a cosmetic progress hint for the CLI/UI wait loop, not a
// durable domain fact, and an event per stage change would spam the event log
// that BuildTimeline reads. A no-op (not an error) when the initiative has no
// running operation right now -- e.g. a synchronous call outside
// launchOperation, such as change-set review.
func (db *DB) SetOperationStage(ctx context.Context, initiativeID, stage string) error {
	if initiativeID == "" {
		return nil
	}
	_, err := db.SQL().ExecContext(ctx, `
		UPDATE operations SET stage = ?
		WHERE id = (
			SELECT id FROM operations
			WHERE initiative_id = ? AND status = ?
			ORDER BY created_at DESC LIMIT 1
		)`, stage, initiativeID, domain.OperationRunning)
	return err
}

// ListOperationsByInitiative returns an initiative's operations, most recent
// first, so the UI can find the running one and show elapsed time.
func (db *DB) ListOperationsByInitiative(ctx context.Context, initiativeID string) ([]domain.Operation, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, project_id, initiative_id, type, status, result_ref, error, stage, created_at, finished_at
		FROM operations WHERE initiative_id = ? ORDER BY created_at DESC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Operation, 0)
	for rows.Next() {
		var op domain.Operation
		var iniID, resultRef sql.NullString
		var finished sql.NullInt64
		var created int64
		if err := rows.Scan(&op.ID, &op.ProjectID, &iniID, &op.Type, &op.Status, &resultRef, &op.Error, &op.Stage, &created, &finished); err != nil {
			return nil, err
		}
		op.InitiativeID = iniID.String
		op.ResultRef = resultRef.String
		op.CreatedAt = time.UnixMilli(created)
		if finished.Valid {
			t := time.UnixMilli(finished.Int64)
			op.FinishedAt = &t
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ListUnfinishedOperations returns operations not in a terminal state (for
// daemon restart reconciliation, §16).
func (db *DB) ListUnfinishedOperations(ctx context.Context) ([]domain.Operation, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, project_id, initiative_id, type, status, result_ref, error, created_at, finished_at
		FROM operations WHERE status IN (?, ?) ORDER BY created_at ASC`,
		domain.OperationPending, domain.OperationRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Operation, 0)
	for rows.Next() {
		var op domain.Operation
		var iniID, resultRef sql.NullString
		var finished sql.NullInt64
		var created int64
		if err := rows.Scan(&op.ID, &op.ProjectID, &iniID, &op.Type, &op.Status, &resultRef, &op.Error, &created, &finished); err != nil {
			return nil, err
		}
		op.InitiativeID = iniID.String
		op.ResultRef = resultRef.String
		op.CreatedAt = time.UnixMilli(created)
		if finished.Valid {
			t := time.UnixMilli(finished.Int64)
			op.FinishedAt = &t
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ---------- Context bundles ----------

// CreateContextBundle persists a context bundle and its entries.
func (db *DB) CreateContextBundle(ctx context.Context, bundleID, runID, digest string, entries []domain.ContextEntry) error {
	if bundleID == "" {
		bundleID = id.New()
	}
	return db.Apply(ctx, Event{
		AggregateType: "context_bundle",
		AggregateID:   bundleID,
		Actor:         ActorOrchestrator,
		EventType:     "context_bundle.created",
		PayloadDigest: digest,
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO context_bundles (id, run_id, digest, created_at) VALUES (?, ?, ?, ?)`,
			bundleID, runID, digest, time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := tx.Exec(`
				INSERT INTO context_entries (bundle_id, priority, source, digest, trust, inclusion_reason, selector, transformation, size)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				bundleID, e.Priority, e.Source, e.Digest, e.Trust, e.InclusionReason, e.Selector, e.Transformation, e.Size); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- Read cursors ----------

// GetReadCursor returns the last-seen event seq for a scope (0 if none).
func (db *DB) GetReadCursor(ctx context.Context, scope string) (int64, error) {
	var seq int64
	err := db.SQL().QueryRowContext(ctx, `SELECT last_event_seq FROM read_cursors WHERE scope = ?`, scope).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// SetReadCursor records the last-seen event seq for a scope.
func (db *DB) SetReadCursor(ctx context.Context, scope string, seq int64) error {
	_, err := db.SQL().ExecContext(ctx, `
		INSERT INTO read_cursors (scope, last_event_seq, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(scope) DO UPDATE SET last_event_seq = excluded.last_event_seq, updated_at = excluded.updated_at`,
		scope, seq, time.Now().UTC().UnixMilli())
	return err
}

// ---------- helpers ----------

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func jsonUnmarshal(s string, v any) error {
	if s == "" || s == "null" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

// LatestOperationOfType returns the newest operation of opType for a project,
// or (nil, nil) when there is none. It answers "how did the last smoke test
// go", which no initiative-scoped listing can: a smoke belongs to no
// initiative.
func (db *DB) LatestOperationOfType(ctx context.Context, projectID, opType string) (*domain.Operation, error) {
	var op domain.Operation
	var iniID, resultRef sql.NullString
	var finished sql.NullInt64
	var created int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT id, project_id, initiative_id, type, status, result_ref, error, created_at, finished_at
		FROM operations WHERE project_id = ? AND type = ? ORDER BY created_at DESC LIMIT 1`, projectID, opType).
		Scan(&op.ID, &op.ProjectID, &iniID, &op.Type, &op.Status, &resultRef, &op.Error, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	op.InitiativeID = iniID.String
	op.ResultRef = resultRef.String
	op.CreatedAt = time.UnixMilli(created)
	if finished.Valid {
		t := time.UnixMilli(finished.Int64)
		op.FinishedAt = &t
	}
	return &op, nil
}

// SetOperationResultRef records a reference on a finished operation, e.g.
// what a smoke test tested, which the failure path has no other place for.
func (db *DB) SetOperationResultRef(ctx context.Context, opID, ref string) error {
	_, err := db.SQL().ExecContext(ctx, `UPDATE operations SET result_ref = ? WHERE id = ?`, ref, opID)
	return err
}
