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

// changeSetColumns is the shared column list for scanning change_sets rows.
const changeSetColumns = `id, human_id, initiative_id, task_id, base_snapshot_id, status, patch_digest, review_id, git_commit_sha, created_by_run_id, model, created_at`

// scanChangeSet scans one change_sets row into cs. createdMS receives the raw
// unix-ms timestamp so the caller can convert it.
func scanChangeSet(row interface{ Scan(...any) error }, cs *domain.ChangeSet, createdMS *int64) error {
	return row.Scan(&cs.ID, &cs.HumanID, &cs.InitiativeID, &cs.TaskID, &cs.BaseSnapshotID,
		&cs.Status, &cs.PatchDigest, &cs.ReviewID, &cs.GitCommitSHA, &cs.CreatedByRunID, &cs.Model, createdMS)
}

// CreateChangeSet inserts a change set, assigning its HumanID "CHG-N" from the
// per-initiative change_sets counter, and appends its creation event.
func (db *DB) CreateChangeSet(ctx context.Context, cs *domain.ChangeSet) error {
	if cs.ID == "" {
		cs.ID = id.New()
	}
	if cs.CreatedAt.IsZero() {
		cs.CreatedAt = time.Now().UTC()
	}
	// A ChangeSet must enter the state machine at its root: creation is always
	// draft, so a caller can never persist a change set that has already skipped
	// checks/review (§4.5, §6.3).
	cs.Status = domain.ChangeSetDraft
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   cs.ID,
		Actor:         ActorOrchestrator,
		EventType:     "change_set.created",
	}, func(tx *sql.Tx) error {
		if cs.HumanID == "" {
			code, err := nextCode(tx, "change_sets", "human_id", "CHG", cs.InitiativeID)
			if err != nil {
				return err
			}
			cs.HumanID = code
		}
		_, err := tx.Exec(`
			INSERT INTO change_sets
				(id, human_id, initiative_id, task_id, base_snapshot_id, status, patch_digest, review_id, git_commit_sha, created_by_run_id, model, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cs.ID, cs.HumanID, cs.InitiativeID, cs.TaskID, cs.BaseSnapshotID, cs.Status,
			cs.PatchDigest, cs.ReviewID, cs.GitCommitSHA, cs.CreatedByRunID, cs.Model, cs.CreatedAt.UnixMilli())
		return err
	})
}

// legalChangeSetTransition reports whether the from→to status transition is
// allowed by the ChangeSet state machine (§38).
func legalChangeSetTransition(from, to domain.ChangeSetStatus) bool {
	switch from {
	case domain.ChangeSetDraft:
		return to == domain.ChangeSetChecksRunning
	case domain.ChangeSetChecksRunning:
		return to == domain.ChangeSetReviewRequired || to == domain.ChangeSetRejected
	case domain.ChangeSetReviewRequired:
		// M3: a human reviewer may reject the change set outright while it is
		// awaiting review, in addition to requesting changes or approving it.
		return to == domain.ChangeSetChangesRequested || to == domain.ChangeSetApproved || to == domain.ChangeSetRejected
	case domain.ChangeSetChangesRequested:
		return to == domain.ChangeSetSuperseded
	case domain.ChangeSetApproved:
		// approved→rejected recovers a system-deny at the apply boundary (e.g. a
		// credential newly detected on re-sweep); the task is re-opened so a
		// corrected re-execute replaces the change set (review: system-deny).
		return to == domain.ChangeSetApplying || to == domain.ChangeSetRejected
	case domain.ChangeSetApplying:
		// applying→approved recovers a crash window where the pre-image was
		// captured but no byte was written (review: full-platform apply atomicity).
		return to == domain.ChangeSetApplied || to == domain.ChangeSetConflicted || to == domain.ChangeSetApproved
	case domain.ChangeSetApplied:
		// A change set that has been applied may be rolled back; this is the
		// only way an applied change set leaves the applied state.
		return to == domain.ChangeSetReverted
	case domain.ChangeSetConflicted:
		// A conflicted change set (apply/commit failed and the pre-image was
		// restored) resolves either by resetting to approved so a retry re-runs
		// the existing apply path, or by rejecting it outright.
		return to == domain.ChangeSetApproved || to == domain.ChangeSetRejected
	default:
		return false
	}
}

// TransitionChangeSet advances a change set from status `from` to status `to`,
// enforcing the legal transition matrix. Both the transition legality and the
// current-state precondition are checked; an illegal or stale transition fails
// with a clear error and mutates nothing.
func (db *DB) TransitionChangeSet(ctx context.Context, changeSetID string, from, to domain.ChangeSetStatus, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	if !legalChangeSetTransition(from, to) {
		return fmt.Errorf("store: illegal change set transition %q -> %q", from, to)
	}
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.status_changed",
		Operation:     string(from) + "->" + string(to),
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET status = ? WHERE id = ? AND status = ?`, to, changeSetID, from)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("store: change set %q not in state %q", changeSetID, from)
		}
		return nil
	})
}

// SetChangeSetReview records the review id for a change set.
func (db *DB) SetChangeSetReview(ctx context.Context, changeSetID, reviewID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.review_set",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET review_id = ? WHERE id = ?`, reviewID, changeSetID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "change set %q not found", changeSetID)
	})
}

// SetChangeSetCommit records the git commit SHA for a change set.
func (db *DB) SetChangeSetCommit(ctx context.Context, changeSetID, sha, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.commit_set",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET git_commit_sha = ? WHERE id = ?`, sha, changeSetID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "change set %q not found", changeSetID)
	})
}

// MarkChangeSetApplied atomically advances applying→applied and appends the
// committed journal entry (carrying the pre-image workspace snapshot digest) in
// one transaction, so the change set status and the apply journal can never
// diverge (§39).
func (db *DB) MarkChangeSetApplied(ctx context.Context, changeSetID, workspaceSnapshotDigest, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	entryID := id.New()
	now := time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.applied",
		Operation:     "applying->applied",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET status = ? WHERE id = ? AND status = ?`,
			domain.ChangeSetApplied, changeSetID, domain.ChangeSetApplying)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "change set %q not in state applying", changeSetID); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO apply_journal (id, change_set_id, state, workspace_snapshot_digest, applied_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			entryID, changeSetID, domain.ApplyCommitted, workspaceSnapshotDigest, now.UnixMilli(), now.UnixMilli())
		return err
	})
}

// MarkChangeSetReverted atomically advances applied→reverted and appends a
// rolled_back journal entry in one transaction, so the change set status and the
// apply journal can never diverge (§39).
func (db *DB) MarkChangeSetReverted(ctx context.Context, changeSetID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	entryID := id.New()
	now := time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.reverted",
		Operation:     "applied->reverted",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET status = ? WHERE id = ? AND status = ?`,
			domain.ChangeSetReverted, changeSetID, domain.ChangeSetApplied)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "change set %q not in state applied", changeSetID); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO apply_journal (id, change_set_id, state, workspace_snapshot_digest, applied_at, created_at) VALUES (?, ?, ?, '', NULL, ?)`,
			entryID, changeSetID, domain.ApplyRolledBack, now.UnixMilli())
		return err
	})
}

// GetChangeSet returns one change set by id, or nil if not found.
func (db *DB) GetChangeSet(ctx context.Context, changeSetID string) (*domain.ChangeSet, error) {
	var cs domain.ChangeSet
	var created int64
	if err := scanChangeSet(db.SQL().QueryRowContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE id = ?`, changeSetID), &cs, &created); err != nil {
		return nil, notFoundIfNoRows(err, "change set", changeSetID)
	}
	cs.CreatedAt = time.UnixMilli(created)
	return &cs, nil
}

// GetChangeSetByRef resolves a change set by its UUID or its HumanID (CHG-N),
// so CLI/UI can reference change sets by the human-facing id. HumanID is only
// unique per-initiative, so a bare CHG-N that matches more than one initiative
// fails closed with an ambiguity error instead of silently returning an
// arbitrary (possibly wrong) change set — review: full-platform authorization.
func (db *DB) GetChangeSetByRef(ctx context.Context, ref string) (*domain.ChangeSet, error) {
	if cs, err := db.GetChangeSet(ctx, ref); err == nil {
		return cs, nil
	}
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE human_id = ? ORDER BY created_at ASC`, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found *domain.ChangeSet
	var all []domain.ChangeSet
	count := 0
	for rows.Next() {
		var cs domain.ChangeSet
		var created int64
		if err := scanChangeSet(rows, &cs, &created); err != nil {
			return nil, err
		}
		cs.CreatedAt = time.UnixMilli(created)
		found = &cs
		all = append(all, cs)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch count {
	case 0:
		return nil, notFound("change set", ref)
	case 1:
		return found, nil
	default:
		return nil, db.ambiguousChangeSet(ctx, ref, all)
	}
}

// ambiguousChangeSet names the candidates, because the old message did not.
//
// `idea next` prescribes `idea apply CHG-1`; with two initiatives in one project
// each holding a CHG-1, that command always failed with "use the full change set
// id" -- and no human-readable command printed one. `idea changes` resolves a
// single initiative and prints HumanID only, while apply/review/change resolve
// project-wide, so the lister and the resolver disagreed about scope. The id
// existed only in `idea changes --json`, which nothing named. `next` went on
// answering 0 and prescribing the same failing command.
//
// The initiative title is looked up per candidate rather than joined in the main
// query: this path runs only on the ambiguous branch, and a failed lookup must
// degrade to the id rather than replace a useful refusal with a database error.
func (db *DB) ambiguousChangeSet(ctx context.Context, ref string, candidates []domain.ChangeSet) error {
	var b strings.Builder
	fmt.Fprintf(&b, "ambiguous change set reference %q: matches %d change sets across initiatives. Use the full id:", ref, len(candidates))
	for _, cs := range candidates {
		title := ""
		if err := db.SQL().QueryRowContext(ctx,
			`SELECT title FROM initiatives WHERE id = ?`, cs.InitiativeID).Scan(&title); err != nil {
			title = ""
		}
		if title == "" {
			b.WriteString("\n  " + cs.ID)
			continue
		}
		fmt.Fprintf(&b, "\n  %s  (%s in %q)", cs.ID, cs.HumanID, title)
	}
	return errors.New(b.String())
}

// ListChangeSetsByTask returns change sets for a task, oldest first.
func (db *DB) ListChangeSetsByTask(ctx context.Context, taskID string) ([]domain.ChangeSet, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ChangeSet, 0)
	for rows.Next() {
		var cs domain.ChangeSet
		var created int64
		if err := scanChangeSet(rows, &cs, &created); err != nil {
			return nil, err
		}
		cs.CreatedAt = time.UnixMilli(created)
		out = append(out, cs)
	}
	return out, rows.Err()
}

// ListChangeSets returns change sets for an initiative, oldest first.
func (db *DB) ListChangeSets(ctx context.Context, initiativeID string) ([]domain.ChangeSet, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ChangeSet, 0)
	for rows.Next() {
		var cs domain.ChangeSet
		var created int64
		if err := scanChangeSet(rows, &cs, &created); err != nil {
			return nil, err
		}
		cs.CreatedAt = time.UnixMilli(created)
		out = append(out, cs)
	}
	return out, rows.Err()
}

// AppendJournal inserts one apply-journal record (§39).
func (db *DB) AppendJournal(ctx context.Context, e *domain.ApplyJournalEntry) error {
	if e.ID == "" {
		e.ID = id.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.State == "" {
		e.State = domain.ApplyPrepared
	}
	if !e.State.Valid() {
		return fmt.Errorf("store: invalid apply state %q", e.State)
	}
	// The committed state is the point of no return: record the applied timestamp.
	if e.State == domain.ApplyCommitted && e.AppliedAt == nil {
		now := time.Now().UTC()
		e.AppliedAt = &now
	}
	return db.Apply(ctx, Event{
		AggregateType: "apply_journal",
		AggregateID:   e.ID,
		Actor:         ActorOrchestrator,
		EventType:     "apply_journal.appended",
		Operation:     string(e.State),
	}, func(tx *sql.Tx) error {
		var appliedAt any
		if e.AppliedAt != nil {
			appliedAt = e.AppliedAt.UnixMilli()
		}
		_, err := tx.Exec(`
			INSERT INTO apply_journal (id, change_set_id, state, workspace_snapshot_digest, applied_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			e.ID, e.ChangeSetID, e.State, e.WorkspaceSnapshotDigest, appliedAt, e.CreatedAt.UnixMilli())
		return err
	})
}

// ListJournal returns apply-journal records for a change set, oldest first.
func (db *DB) ListJournal(ctx context.Context, changeSetID string) ([]domain.ApplyJournalEntry, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, change_set_id, state, workspace_snapshot_digest, created_at
		FROM apply_journal WHERE change_set_id = ? ORDER BY created_at ASC`, changeSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJournalRows(rows)
}

// JournalSnapshotDigest returns the workspace_snapshot_digest of the most recent
// committed journal entry for a change set (the pre-image to restore on revert).
func (db *DB) JournalSnapshotDigest(ctx context.Context, changeSetID string) (string, error) {
	var digest string
	err := db.SQL().QueryRowContext(ctx, `
		SELECT workspace_snapshot_digest FROM apply_journal
		WHERE change_set_id = ? AND state = ? ORDER BY created_at DESC LIMIT 1`,
		changeSetID, domain.ApplyCommitted).Scan(&digest)
	if err != nil {
		return "", err
	}
	return digest, nil
}

// ChangeSetRecovery describes a change set stuck in a non-terminal state that
// startup reconciliation must move to a terminal state (and, for an interrupted
// apply, restore the workspace).
type ChangeSetRecovery struct {
	ChangeSetID  string
	InitiativeID string
	Status       domain.ChangeSetStatus
	TaskID       string
	// LatestJournalState is the most recent apply-journal state, or "" if none.
	LatestJournalState domain.ApplyState
	// PreImageDigest is the workspace pre-image to restore (workspace_mutated
	// rows only); empty when no mutation was recorded.
	PreImageDigest string
}

// ReconcileChangeSets finds change sets stuck in applying/checks_running and
// marks the non-terminal journal rows of the applying ones recovery_required.
// It only touches change sets that are themselves non-terminal, so the journal
// history of already-applied/reverted change sets is left intact.
func (db *DB) ReconcileChangeSets(ctx context.Context) ([]ChangeSetRecovery, error) {
	var out []ChangeSetRecovery
	err := db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   "reconcile",
		Actor:         ActorOrchestrator,
		EventType:     "change_sets.reconciled",
	}, func(tx *sql.Tx) error {
		rows, err := tx.Query(`
			SELECT cs.id, cs.initiative_id, cs.status, cs.task_id,
			       (SELECT j.state FROM apply_journal j WHERE j.change_set_id = cs.id ORDER BY j.rowid DESC LIMIT 1),
			       (SELECT j.workspace_snapshot_digest FROM apply_journal j WHERE j.change_set_id = cs.id AND j.workspace_snapshot_digest != '' ORDER BY j.rowid DESC LIMIT 1)
			FROM change_sets cs
			WHERE cs.status IN (?, ?)
			ORDER BY cs.rowid ASC`,
			domain.ChangeSetApplying, domain.ChangeSetChecksRunning)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r ChangeSetRecovery
			var latest sql.NullString
			var preDigest sql.NullString
			if err := rows.Scan(&r.ChangeSetID, &r.InitiativeID, &r.Status, &r.TaskID, &latest, &preDigest); err != nil {
				rows.Close()
				return err
			}
			if latest.Valid {
				r.LatestJournalState = domain.ApplyState(latest.String)
			}
			if preDigest.Valid {
				r.PreImageDigest = preDigest.String
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		// Mark the interrupted-apply journal rows recovery_required (only for
		// change sets still in applying).
		for _, r := range out {
			if r.Status != domain.ChangeSetApplying {
				continue
			}
			if _, err := tx.Exec(`UPDATE apply_journal SET state = ? WHERE change_set_id = ? AND state IN (?, ?)`,
				domain.ApplyRecoveryRequired, r.ChangeSetID, domain.ApplyPrepared, domain.ApplyWorkspaceMutated); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanJournalRows materializes apply-journal rows from an open result set.
func scanJournalRows(rows *sql.Rows) ([]domain.ApplyJournalEntry, error) {
	out := make([]domain.ApplyJournalEntry, 0)
	for rows.Next() {
		var e domain.ApplyJournalEntry
		var created int64
		if err := rows.Scan(&e.ID, &e.ChangeSetID, &e.State, &e.WorkspaceSnapshotDigest, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// requireOneRow asserts that res affected exactly one row, returning a clear
// error (formatted with format/args) otherwise.
func requireOneRow(res sql.Result, format string, args ...any) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("store: "+format, args...)
	}
	return nil
}

// CountChangeSetsByHumanID reports how many change sets in this project share a
// human id.
//
// Human ids are assigned per initiative, so CHG-1 exists once per initiative
// while apply/review/change resolve project-wide. `idea next` printed the human
// id unconditionally and so prescribed `idea apply CHG-1`, which refuses as
// ambiguous the moment a project has two initiatives with change sets. Callers
// use this to print the full id in exactly that case, and the friendly one
// otherwise.
func (db *DB) CountChangeSetsByHumanID(ctx context.Context, humanID string) (int, error) {
	var n int
	err := db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM change_sets WHERE human_id = ?`, humanID).Scan(&n)
	return n, err
}
