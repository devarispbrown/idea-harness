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
const changeSetColumns = `id, human_id, initiative_id, task_id, base_snapshot_id, status, patch_digest, review_id, git_commit_sha, created_by_run_id, model, executor, agent_output, git_commit_warning, created_at, review_completed_at, no_op, review_unavailable_reason`

// scanChangeSet scans one change_sets row into cs. createdMS receives the raw
// unix-ms timestamp so the caller can convert it; reviewCompletedMS receives
// the nullable review_completed_at column (migration 33).
// review_unavailable_reason (migration 36) is NOT NULL DEFAULT '' and scans
// directly into cs.ReviewUnavailableReason, unlike review_completed_at, since
// an empty string already means "not set" with no separate nullability to
// track.
func scanChangeSet(row interface{ Scan(...any) error }, cs *domain.ChangeSet, createdMS *int64, reviewCompletedMS *sql.NullInt64) error {
	return row.Scan(&cs.ID, &cs.HumanID, &cs.InitiativeID, &cs.TaskID, &cs.BaseSnapshotID,
		&cs.Status, &cs.PatchDigest, &cs.ReviewID, &cs.GitCommitSHA, &cs.CreatedByRunID, &cs.Model, &cs.Executor, &cs.AgentOutput, &cs.GitCommitWarning, createdMS, reviewCompletedMS, &cs.NoOp, &cs.ReviewUnavailableReason)
}

// applyReviewCompletedAt converts the nullable review_completed_at scan
// target into cs.ReviewCompletedAt.
func applyReviewCompletedAt(cs *domain.ChangeSet, v sql.NullInt64) {
	if !v.Valid {
		return
	}
	t := time.UnixMilli(v.Int64)
	cs.ReviewCompletedAt = &t
}

// reviewTrackingSince returns when migration 33 (review_completed_at) was
// applied to this database, or ok=false if it somehow has not run yet (the
// caller should then leave PredatesReviewTracking at its zero value, false,
// rather than guess).
func (db *DB) reviewTrackingSince(ctx context.Context) (t time.Time, ok bool, err error) {
	var appliedMS int64
	err = db.sql.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE version = 33`).Scan(&appliedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(appliedMS), true, nil
}

// setPredatesReviewTracking is the pure half of applyPredatesReviewTracking:
// true when cs's review never completed AND cs was created strictly before
// review-completion tracking (migration 33) existed in this database --
// review_completed_at NULL is then the expected, permanent state for that
// row, not a sign that anything was cancelled. Split out so a caller loading
// many rows (ListChangeSets, ListChangeSetsByTask) looks up `since` once
// rather than once per row.
func setPredatesReviewTracking(cs *domain.ChangeSet, since time.Time, sinceOK bool) {
	if cs.ReviewCompleted() || !sinceOK {
		return
	}
	cs.PredatesReviewTracking = cs.CreatedAt.Before(since)
}

// applyPredatesReviewTracking sets cs.PredatesReviewTracking (review MEDIUM
// #2) for a single change set already scanned from the database. See
// setPredatesReviewTracking for the semantics; this variant does the
// `since` lookup itself, for the single-row read paths.
func (db *DB) applyPredatesReviewTracking(ctx context.Context, cs *domain.ChangeSet) error {
	if cs.ReviewCompleted() {
		return nil
	}
	since, ok, err := db.reviewTrackingSince(ctx)
	if err != nil {
		return err
	}
	setPredatesReviewTracking(cs, since, ok)
	return nil
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
	// A change set's review never completes at creation time (ICP re-run
	// defect #1): whatever the caller set is discarded, exactly like Status
	// above, so a caller can never persist a change set that already claims a
	// review that has not run.
	cs.ReviewCompletedAt = nil
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
				(id, human_id, initiative_id, task_id, base_snapshot_id, status, patch_digest, review_id, git_commit_sha, created_by_run_id, model, executor, agent_output, git_commit_warning, created_at, no_op)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cs.ID, cs.HumanID, cs.InitiativeID, cs.TaskID, cs.BaseSnapshotID, cs.Status,
			cs.PatchDigest, cs.ReviewID, cs.GitCommitSHA, cs.CreatedByRunID, cs.Model, cs.Executor, cs.AgentOutput, cs.GitCommitWarning, cs.CreatedAt.UnixMilli(), cs.NoOp)
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
		// applying→reverted is the same recovery, for a re-apply of a reverted
		// change set (ICP re-run defect #3): nothing was written, so it is
		// still exactly as reverted as it was before the re-apply attempt.
		return to == domain.ChangeSetApplied || to == domain.ChangeSetConflicted || to == domain.ChangeSetApproved || to == domain.ChangeSetReverted
	case domain.ChangeSetApplied:
		// A change set that has been applied may be rolled back; this is the
		// only way an applied change set leaves the applied state.
		return to == domain.ChangeSetReverted
	case domain.ChangeSetReverted:
		// ICP re-run defect #3: a reverted change set could never be applied
		// again ("cannot be applied from status reverted"), so recovering
		// from a revert always cost a full agent run even when the reverted
		// content was still exactly what was wanted. Re-apply runs through
		// the same ApplyChangeSet path (and its staleness/secret-sweep
		// guards) as a fresh approved change set; only the starting state
		// differs.
		return to == domain.ChangeSetApplying
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
		if to == domain.ChangeSetRejected {
			// review MEDIUM: stale-mark garbage collection -- a rejected
			// change set's stale mark is dead weight, not an outstanding
			// acknowledgement (see clearStaleMarksForTargetTx).
			return clearStaleMarksForTargetTx(tx, "change_set", changeSetID, actor)
		}
		return nil
	})
}

// SetChangeSetReview records the review id for a change set AND stamps
// review_completed_at (ICP re-run defect #1). This is the ONLY place a
// completed adversarial review is ever recorded — called once from Execute
// after runChangeSetAdversarialReview succeeds, and again from
// RerunChangeSetReview when a human explicitly retries a review that never
// completed the first time. If the caller's review attempt itself errors,
// this must never be called, so the change set correctly keeps
// ReviewCompletedAt nil (unreviewed).
func (db *DB) SetChangeSetReview(ctx context.Context, changeSetID, reviewID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	now := time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.review_set",
	}, func(tx *sql.Tx) error {
		// review_unavailable_reason is cleared here too: a review that just
		// completed supersedes whatever an earlier attempt on this same
		// change set left behind (e.g. a truncation `idea review <id> rerun`
		// has since retried past -- see SetChangeSetReviewUnavailable). Once
		// ReviewCompletedAt is set, ReviewNeverCompletedCause/
		// ReviewUnavailable are never consulted for cs again, so this is
		// hygiene, not a behavior change.
		res, err := tx.Exec(`UPDATE change_sets SET review_id = ?, review_completed_at = ?, review_unavailable_reason = '' WHERE id = ?`, reviewID, now.UnixMilli(), changeSetID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "change set %q not found", changeSetID)
	})
}

// SetChangeSetReviewUnavailable records that the adversarial reviewer was
// genuinely ATTEMPTED for cs but could not complete -- e.g. every attempt
// (including runModel's own reduced-ask retry) was cut off at the model's
// output-token ceiling (model.ErrTruncated) -- as a plain, non-empty reason.
// It deliberately does NOT set review_completed_at: round-4's guarantee that
// an unreviewed change set cannot be silently approved must hold exactly as
// before ReviewCompleted() still reports false. What changes is
// ReviewNeverCompletedCause, which now names this reason instead of the
// generic "likely cancelled mid-review", and ReviewUnavailable, which
// reviewChangeSet's "approved" verdict consults to allow the one new,
// explicit, recorded escape: --accept-findings on a change set whose review
// never completed but was genuinely attempted and exhausted.
//
// Called from Execute's own review call and from RerunChangeSetReview, in
// place of returning the truncation error outright -- that early return is
// exactly what used to leave the task never marked done and the change set
// wedged behind a guard with no supported way forward (the defect this
// closes).
func (db *DB) SetChangeSetReviewUnavailable(ctx context.Context, changeSetID, reason, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	if reason == "" {
		return fmt.Errorf("store: SetChangeSetReviewUnavailable: reason must not be empty")
	}
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.review_unavailable",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET review_unavailable_reason = ? WHERE id = ?`, reason, changeSetID)
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

// SetChangeSetGitWarning records why an applied change set was not committed
// even though git_commit is on (review C4, adopted #2): a detached HEAD, a
// rebase/merge/cherry-pick in progress, or the repository's default branch
// without git.allow_default_branch.
func (db *DB) SetChangeSetGitWarning(ctx context.Context, changeSetID, warning, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "change_set",
		AggregateID:   changeSetID,
		Actor:         actor,
		EventType:     "change_set.git_commit_warning_set",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE change_sets SET git_commit_warning = ? WHERE id = ?`, warning, changeSetID)
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
		if _, err := tx.Exec(`INSERT INTO apply_journal (id, change_set_id, state, workspace_snapshot_digest, applied_at, created_at) VALUES (?, ?, ?, '', NULL, ?)`,
			entryID, changeSetID, domain.ApplyRolledBack, now.UnixMilli()); err != nil {
			return err
		}
		// review MEDIUM: stale-mark garbage collection -- a reverted change
		// set's stale mark is dead weight, not an outstanding acknowledgement
		// (see clearStaleMarksForTargetTx).
		return clearStaleMarksForTargetTx(tx, "change_set", changeSetID, actor)
	})
}

// GetChangeSet returns one change set by id, or nil if not found.
func (db *DB) GetChangeSet(ctx context.Context, changeSetID string) (*domain.ChangeSet, error) {
	var cs domain.ChangeSet
	var created int64
	var reviewCompleted sql.NullInt64
	if err := scanChangeSet(db.SQL().QueryRowContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE id = ?`, changeSetID), &cs, &created, &reviewCompleted); err != nil {
		return nil, notFoundIfNoRows(err, "change set", changeSetID)
	}
	cs.CreatedAt = time.UnixMilli(created)
	applyReviewCompletedAt(&cs, reviewCompleted)
	if err := db.applyPredatesReviewTracking(ctx, &cs); err != nil {
		return nil, err
	}
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
		var reviewCompleted sql.NullInt64
		if err := scanChangeSet(rows, &cs, &created, &reviewCompleted); err != nil {
			return nil, err
		}
		cs.CreatedAt = time.UnixMilli(created)
		applyReviewCompletedAt(&cs, reviewCompleted)
		found = &cs
		all = append(all, cs)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close explicitly (defer above is then a harmless no-op / safety net):
	// applyPredatesReviewTracking below runs its own query, and the pool is a
	// single connection, so it would otherwise block forever waiting for the
	// connection rows here is still holding (review MEDIUM #2 regression).
	rows.Close()
	switch count {
	case 0:
		return nil, notFound("change set", ref)
	case 1:
		if err := db.applyPredatesReviewTracking(ctx, found); err != nil {
			return nil, err
		}
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
	// reviewTrackingSince runs its own query, so it must happen before rows
	// below is opened, not while it's still open: the pool is one connection
	// (single-writer SQLite), and a second query issued while rows still
	// holds it would block forever waiting for a connection rows itself is
	// holding (review MEDIUM #2 regression, caught by go test hanging).
	since, sinceOK, sinceErr := db.reviewTrackingSince(ctx)
	if sinceErr != nil {
		return nil, sinceErr
	}
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ChangeSet, 0)
	for rows.Next() {
		var cs domain.ChangeSet
		var created int64
		var reviewCompleted sql.NullInt64
		if err := scanChangeSet(rows, &cs, &created, &reviewCompleted); err != nil {
			return nil, err
		}
		cs.CreatedAt = time.UnixMilli(created)
		applyReviewCompletedAt(&cs, reviewCompleted)
		setPredatesReviewTracking(&cs, since, sinceOK)
		out = append(out, cs)
	}
	return out, rows.Err()
}

// ListChangeSets returns change sets for an initiative, oldest first.
func (db *DB) ListChangeSets(ctx context.Context, initiativeID string) ([]domain.ChangeSet, error) {
	// See ListChangeSetsByTask: this must run before rows is opened, not
	// while it's held (single-connection pool).
	since, sinceOK, sinceErr := db.reviewTrackingSince(ctx)
	if sinceErr != nil {
		return nil, sinceErr
	}
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+changeSetColumns+` FROM change_sets WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ChangeSet, 0)
	for rows.Next() {
		var cs domain.ChangeSet
		var created int64
		var reviewCompleted sql.NullInt64
		if err := scanChangeSet(rows, &cs, &created, &reviewCompleted); err != nil {
			return nil, err
		}
		cs.CreatedAt = time.UnixMilli(created)
		applyReviewCompletedAt(&cs, reviewCompleted)
		setPredatesReviewTracking(&cs, since, sinceOK)
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
	// PreImageDigest is the workspace pre-image to restore, scoped to the
	// CURRENT (interrupted) apply attempt only -- i.e. a workspace_mutated
	// row recorded after the most recent "prepared" row for this change set
	// (H3): apply_journal accumulates rows across every apply/revert/
	// re-apply of a change set, never cleared, so picking merely the newest
	// non-empty digest regardless of which attempt it belonged to let a
	// re-apply crashed BEFORE its own mutation was recorded inherit the
	// FIRST apply's committed digest instead of correctly reporting "no
	// bytes written yet". Empty when no mutation was recorded for this
	// attempt.
	PreImageDigest string
	// FromStatus is the status this change set transitioned FROM when it
	// entered `applying`, recovered from the status_changed event (H3): both
	// approved->applying (a fresh apply) and reverted->applying (a re-apply)
	// are legal, and a crash-recovery that always assumes Approved
	// mislabels an in-progress re-apply of an already-reverted change set as
	// merely "approved" -- contradicting the reverted->applying edge added
	// specifically for this recovery (changeset.go's legalChangeSetTransition)
	// and CHANGELOG.md's documented promise that a failed re-apply recovers
	// back to `reverted`, not `approved`. Empty (ChangeSetStatus("")) if no
	// such event is found, in which case the caller falls back to Approved
	// (the only possibility before re-apply existed).
	FromStatus domain.ChangeSetStatus
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
		// The PreImageDigest subquery's boundary (innermost) picks out the
		// START of the CURRENT apply attempt by its "prepared" journal row --
		// identified by an EMPTY digest, not by its `state` column, because
		// the recovery_required rewrite below (a second, idempotent
		// reconcile pass after a restore itself failed/crashed) overwrites
		// `state` on both the prepared and workspace_mutated rows for an
		// applying change set, but never touches their digest column. Keying
		// the boundary on `state = 'prepared'` looked right on a first pass
		// and silently broke on a second one (H3): the boundary row could no
		// longer be found at all, so COALESCE fell back to 0 and the
		// pre-image digest reverted to being picked from the WHOLE history
		// again -- the very re-apply-inherits-the-first-apply's-digest bug
		// this exists to fix, just delayed one crash. Every apply/re-apply
		// attempt appends exactly one empty-digest ("prepared") row before
		// any mutation, so this boundary is stable across any number of
		// reconcile passes.
		rows, err := tx.Query(`
			SELECT cs.id, cs.initiative_id, cs.status, cs.task_id,
			       (SELECT j.state FROM apply_journal j WHERE j.change_set_id = cs.id ORDER BY j.rowid DESC LIMIT 1),
			       (SELECT j.workspace_snapshot_digest FROM apply_journal j
			          WHERE j.change_set_id = cs.id
			            AND j.workspace_snapshot_digest != ''
			            AND j.rowid > COALESCE((
			              SELECT MAX(p.rowid) FROM apply_journal p
			              WHERE p.change_set_id = cs.id AND p.workspace_snapshot_digest = ''
			            ), 0)
			          ORDER BY j.rowid DESC LIMIT 1),
			       (SELECT e.operation FROM events e
			          WHERE e.aggregate_type = 'change_set' AND e.aggregate_id = cs.id
			            AND e.event_type = 'change_set.status_changed' AND e.operation LIKE '%->' || ?
			          ORDER BY e.seq DESC LIMIT 1)
			FROM change_sets cs
			WHERE cs.status IN (?, ?)
			ORDER BY cs.rowid ASC`,
			domain.ChangeSetApplying,
			domain.ChangeSetApplying, domain.ChangeSetChecksRunning)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r ChangeSetRecovery
			var latest sql.NullString
			var preDigest sql.NullString
			var fromOp sql.NullString
			if err := rows.Scan(&r.ChangeSetID, &r.InitiativeID, &r.Status, &r.TaskID, &latest, &preDigest, &fromOp); err != nil {
				rows.Close()
				return err
			}
			if latest.Valid {
				r.LatestJournalState = domain.ApplyState(latest.String)
			}
			if preDigest.Valid {
				r.PreImageDigest = preDigest.String
			}
			if fromOp.Valid {
				if from, ok := strings.CutSuffix(fromOp.String, "->"+string(domain.ChangeSetApplying)); ok {
					r.FromStatus = domain.ChangeSetStatus(from)
				}
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
