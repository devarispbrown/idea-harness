package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// Remote runs: the durable side of the openhands executor (see the
// remote_runs migration's comment). The executor package reaches these
// through its own RemoteRunStore interface, so it never imports this one.

const remoteRunColumns = `id, executor, idempotency_key, initiative_id, task_id, attempt, server_url, conversation_id,
	remote_dir, base_digest, status, remote_status, last_event_id, event_count, progress, error, created_at, updated_at, finished_at`

// RemoteRunProgressMax bounds the stored progress log. It is a live view and
// a post-restart summary, not an archive: the remote server keeps the full
// event history for as long as it keeps the conversation.
const RemoteRunProgressMax = 64 << 10

// ErrRemoteRunNotFound is returned for an unknown remote run id.
var ErrRemoteRunNotFound = errors.New("store: remote run not found")

func scanRemoteRun(row interface{ Scan(...any) error }) (*domain.RemoteRun, error) {
	var r domain.RemoteRun
	var status string
	var created, updated int64
	var finished sql.NullInt64
	if err := row.Scan(&r.ID, &r.Executor, &r.IdempotencyKey, &r.InitiativeID, &r.TaskID, &r.Attempt, &r.ServerURL,
		&r.ConversationID, &r.RemoteDir, &r.BaseDigest, &status, &r.RemoteStatus, &r.LastEventID, &r.EventCount,
		&r.Progress, &r.Error, &created, &updated, &finished); err != nil {
		return nil, err
	}
	r.Status = domain.RemoteRunStatus(status)
	r.CreatedAt = time.UnixMilli(created)
	r.UpdatedAt = time.UnixMilli(updated)
	if finished.Valid {
		t := time.UnixMilli(finished.Int64)
		r.FinishedAt = &t
	}
	return &r, nil
}

// CreateRemoteRun inserts a run in status dispatching, or -- when a row with
// the same idempotency key already exists -- returns that row unchanged with
// created=false. The UNIQUE constraint is the claim, so two concurrent
// dispatches of the same (task, attempt) cannot both create one.
func (db *DB) CreateRemoteRun(ctx context.Context, r domain.RemoteRun) (*domain.RemoteRun, bool, error) {
	if r.IdempotencyKey == "" {
		return nil, false, fmt.Errorf("store: remote run: idempotency key is required")
	}
	if r.ID == "" {
		r.ID = id.New()
	}
	if r.Status == "" {
		r.Status = domain.RemoteRunDispatching
	}
	if !r.Status.Valid() {
		return nil, false, fmt.Errorf("store: remote run: invalid status %q", r.Status)
	}
	now := nowMS()
	_, err := db.sql.ExecContext(ctx, `INSERT INTO remote_runs (`+remoteRunColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		r.ID, r.Executor, r.IdempotencyKey, r.InitiativeID, r.TaskID, r.Attempt, r.ServerURL, r.ConversationID,
		r.RemoteDir, r.BaseDigest, string(r.Status), r.RemoteStatus, r.LastEventID, r.EventCount, r.Progress, r.Error, now, now)
	if err != nil {
		if isUniqueOrPKViolation(err) {
			existing, gerr := db.GetRemoteRunByKey(ctx, r.IdempotencyKey)
			if gerr != nil {
				return nil, false, gerr
			}
			if existing != nil {
				return existing, false, nil
			}
		}
		return nil, false, fmt.Errorf("store: create remote run: %w", err)
	}
	got, err := db.GetRemoteRun(ctx, r.ID)
	return got, true, err
}

// GetRemoteRun returns one run by its local id.
func (db *DB) GetRemoteRun(ctx context.Context, runID string) (*domain.RemoteRun, error) {
	r, err := scanRemoteRun(db.sql.QueryRowContext(ctx, `SELECT `+remoteRunColumns+` FROM remote_runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrRemoteRunNotFound, runID)
	}
	return r, err
}

// GetRemoteRunByKey returns the run with this idempotency key, or nil.
func (db *DB) GetRemoteRunByKey(ctx context.Context, key string) (*domain.RemoteRun, error) {
	r, err := scanRemoteRun(db.sql.QueryRowContext(ctx, `SELECT `+remoteRunColumns+` FROM remote_runs WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// CountTerminalRemoteRuns is how many of a task's remote runs are over
// (collected, failed or cancelled). The next attempt number is this plus one,
// which is what makes the idempotency key deterministic: a crash leaves the
// current attempt non-terminal, so the retry computes the same number.
func (db *DB) CountTerminalRemoteRuns(ctx context.Context, executor, taskID string) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_runs WHERE executor = ? AND task_id = ? AND status IN (?, ?, ?)`,
		executor, taskID, string(domain.RemoteRunCollected), string(domain.RemoteRunFailed), string(domain.RemoteRunCancelled)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count remote runs: %w", err)
	}
	return n, nil
}

// ListRemoteRuns returns runs newest first. With activeOnly, only those not
// yet terminal -- what reconciliation re-attaches to. limit <= 0 means all.
func (db *DB) ListRemoteRuns(ctx context.Context, activeOnly bool, limit int) ([]domain.RemoteRun, error) {
	q := `SELECT ` + remoteRunColumns + ` FROM remote_runs`
	var args []any
	if activeOnly {
		q += ` WHERE status IN (?, ?, ?)`
		args = append(args, string(domain.RemoteRunDispatching), string(domain.RemoteRunRunning), string(domain.RemoteRunFinished))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list remote runs: %w", err)
	}
	defer rows.Close()
	out := []domain.RemoteRun{}
	for rows.Next() {
		r, err := scanRemoteRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RemoteRunUpdate is a partial update; nil fields are left alone.
type RemoteRunUpdate struct {
	ConversationID *string
	RemoteDir      *string
	BaseDigest     *string
	Status         *domain.RemoteRunStatus
	RemoteStatus   *string
	Error          *string
}

// UpdateRemoteRun applies u. A run already terminal keeps its status: a late
// "running" from a slow poller must not resurrect a run the operator
// cancelled, and a reconciler that lost a race to the executor must not
// overwrite "collected" with "finished". The guard is inside the statement,
// so there is no read-then-write window.
func (db *DB) UpdateRemoteRun(ctx context.Context, runID string, u RemoteRunUpdate) error {
	now := nowMS()
	sets := []string{"updated_at = ?"}
	args := []any{now}
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if u.ConversationID != nil {
		add("conversation_id", *u.ConversationID)
	}
	if u.RemoteDir != nil {
		add("remote_dir", *u.RemoteDir)
	}
	if u.BaseDigest != nil {
		add("base_digest", *u.BaseDigest)
	}
	if u.RemoteStatus != nil {
		add("remote_status", *u.RemoteStatus)
	}
	if u.Error != nil {
		add("error", *u.Error)
	}
	if u.Status != nil {
		if !u.Status.Valid() {
			return fmt.Errorf("store: remote run: invalid status %q", *u.Status)
		}
		sets = append(sets, `status = CASE WHEN status IN ('collected', 'failed', 'cancelled') THEN status ELSE ? END`)
		args = append(args, string(*u.Status))
		if u.Status.Terminal() {
			sets = append(sets, `finished_at = COALESCE(finished_at, ?)`)
			args = append(args, now)
		}
	}
	args = append(args, runID)
	res, err := db.sql.ExecContext(ctx, `UPDATE remote_runs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: update remote run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrRemoteRunNotFound, runID)
	}
	return nil
}

// AppendRemoteRunProgress folds newly ingested events into the run: lines
// (already redacted by the caller) are appended to the progress log, which is
// trimmed from the front to RemoteRunProgressMax, and the cursor advances to
// lastEventID. It is guarded on the cursor the caller read (fromEventID), so
// two ingesters racing over the same events -- the executor and a
// reconciler -- cannot both append them: the loser matches nothing and gets
// applied=false, and re-reads the cursor.
func (db *DB) AppendRemoteRunProgress(ctx context.Context, runID, fromEventID, lastEventID string, events int, lines []string) (applied bool, err error) {
	err = db.InTx(ctx, func(tx *sql.Tx) error {
		var progress, cursor string
		if err := tx.QueryRowContext(ctx, `SELECT progress, last_event_id FROM remote_runs WHERE id = ?`, runID).Scan(&progress, &cursor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrRemoteRunNotFound, runID)
			}
			return err
		}
		if cursor != fromEventID {
			return nil
		}
		if len(lines) > 0 {
			progress += strings.Join(lines, "\n") + "\n"
		}
		if len(progress) > RemoteRunProgressMax {
			progress = progress[len(progress)-RemoteRunProgressMax:]
			// Drop the partial first line the cut left.
			if i := strings.IndexByte(progress, '\n'); i >= 0 {
				progress = progress[i+1:]
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE remote_runs SET progress = ?, last_event_id = ?, event_count = event_count + ?, updated_at = ? WHERE id = ?`,
			progress, lastEventID, events, nowMS(), runID); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: remote run progress: %w", err)
	}
	return applied, nil
}
