package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// decisionDependents discovers (read-only) the requirements that cite
// decisionID (via `requirement --derived_from--> decision` edges, wired by
// WireRequirementDerivedFromDecision), the tasks that implement or verify
// those requirements, and the applied change sets built from those tasks
// (review A4 #5).
//
// Deliberately separate from markDependentsStaleTx and called BEFORE any
// transaction starts: db.sql has MaxOpenConns(1) (a single connection,
// serializing all access -- see store.go), so a read issued through the
// connection pool from INSIDE an already-open transaction would try to
// acquire that same single connection the transaction itself is holding and
// deadlock forever waiting for it. Keeping every pool read here, outside
// ReopenDecision's transaction, and passing the results into
// markDependentsStaleTx (which only ever writes, through tx) is what makes
// review M5's "mark in the same transaction as the reopen" safe to do at all.
func (db *DB) decisionDependents(ctx context.Context, initiativeID, decisionID string) (reqIDs, taskIDs, csIDs []string, err error) {
	reqEdges, err := db.ListGraphEdgesTo(ctx, initiativeID, "decision", decisionID)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, e := range reqEdges {
		if e.FromType == "requirement" && e.EdgeType == domain.EdgeDerivedFrom {
			reqIDs = append(reqIDs, e.FromID)
		}
	}

	for _, reqID := range reqIDs {
		taskEdges, err := db.ListGraphEdgesTo(ctx, initiativeID, "requirement", reqID)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, e := range taskEdges {
			if e.FromType == "task" && (e.EdgeType == domain.EdgeImplements || e.EdgeType == domain.EdgeVerifies) {
				taskIDs = append(taskIDs, e.FromID)
			}
		}
	}

	for _, taskID := range taskIDs {
		cses, err := db.ListChangeSetsByTask(ctx, taskID)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, cs := range cses {
			if cs.Status == domain.ChangeSetApplied {
				csIDs = append(csIDs, cs.ID)
			}
		}
	}
	return reqIDs, taskIDs, csIDs, nil
}

// markDependentsStaleTx writes one active stale mark (via markStaleTx, keyed
// on (target, decision_id) -- see there) for each of reqIDs/taskIDs/csIDs,
// through tx. Takes the already-discovered id lists (decisionDependents)
// rather than reading them itself, so it is safe to call from inside an
// existing transaction (review M5) without touching the connection pool.
func markDependentsStaleTx(tx *sql.Tx, initiativeID, decisionID, decisionCode string, reqIDs, taskIDs, csIDs []string) error {
	if len(reqIDs) == 0 && len(taskIDs) == 0 && len(csIDs) == 0 {
		return nil
	}
	reason := decisionCode + " changed"
	mark := func(targetType, targetID string) error {
		return markStaleTx(tx, initiativeID, targetType, targetID, reason, decisionID)
	}
	for _, rid := range reqIDs {
		if err := mark("requirement", rid); err != nil {
			return err
		}
	}
	for _, tid := range taskIDs {
		if err := mark("task", tid); err != nil {
			return err
		}
	}
	for _, cid := range csIDs {
		if err := mark("change_set", cid); err != nil {
			return err
		}
	}
	return nil
}

// markStaleTx idempotently inserts one active stale mark, keyed on (target,
// decision_id) (review M5): a target already carrying an active (uncleared)
// mark FROM THE SAME DECISION is left alone rather than duplicated, so
// reopening the same decision twice in a row does not pile up rows -- but a
// target already stale from one decision still gets its OWN, second mark
// when a DIFFERENT decision is reopened. Before this fix the check ignored
// decision_id entirely, so reopening decision B against a target decision A
// had already marked silently dropped B's mark -- acknowledging A's alone
// then read as "nothing is stale here" even though B's reopening was never
// separately acknowledged.
func markStaleTx(tx *sql.Tx, initiativeID, targetType, targetID, reason, decisionID string) error {
	var exists int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM stale_marks
		WHERE initiative_id = ? AND target_type = ? AND target_id = ? AND decision_id = ? AND cleared_at IS NULL`,
		initiativeID, targetType, targetID, decisionID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	_, err := tx.Exec(`
		INSERT INTO stale_marks (id, initiative_id, target_type, target_id, reason, decision_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id.New(), initiativeID, targetType, targetID, reason, decisionID, nowMS())
	return err
}

// ListStaleItems returns the active (uncleared) stale marks for an initiative,
// oldest first.
func (db *DB) ListStaleItems(ctx context.Context, initiativeID string) ([]domain.StaleItem, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, target_type, target_id, reason, decision_id, created_at, cleared_at, cleared_by
		FROM stale_marks WHERE initiative_id = ? AND cleared_at IS NULL ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.StaleItem, 0)
	for rows.Next() {
		var s domain.StaleItem
		var created int64
		var cleared sql.NullInt64
		var clearedBy sql.NullString
		if err := rows.Scan(&s.ID, &s.InitiativeID, &s.TargetType, &s.TargetID, &s.Reason, &s.DecisionID, &created, &cleared, &clearedBy); err != nil {
			return nil, err
		}
		s.CreatedAt = time.UnixMilli(created)
		if cleared.Valid {
			t := time.UnixMilli(cleared.Int64)
			s.ClearedAt = &t
		}
		if clearedBy.Valid {
			s.ClearedBy = clearedBy.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ClearStaleItem explicitly acknowledges one stale item, clearing it (review
// A4 #5: "explicitly acknowledging the item clears the stale state").
func (db *DB) ClearStaleItem(ctx context.Context, initiativeID, staleID, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "stale_item",
		AggregateID:   staleID,
		Actor:         actor,
		EventType:     "stale.acknowledged",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE stale_marks SET cleared_at = ?, cleared_by = ?
			WHERE id = ? AND initiative_id = ? AND cleared_at IS NULL`,
			nowMS(), actor, staleID, initiativeID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundOrState(tx, "stale_marks", staleID, "stale item",
				fmt.Errorf("stale item %q is already cleared", staleID))
		}
		return nil
	})
}

// ClearStaleItemsForInitiative clears every active TASK and CHANGE-SET stale
// mark for an initiative -- never a REQUIREMENT mark (review M5) -- when
// re-planning regenerates the execution plan (review A4 #5: "re-planning
// clears the stale state"). Used by RevisePlan and ReopenPhase.
//
// A requirement's staleness is a property of the requirement itself (the
// decision it was derived from changed), not of the specific plan built for
// it: regenerating tasks does not re-verify the requirement, so a fresh plan
// has done nothing to make "this requirement's basis changed" untrue. Before
// this fix, revise-plan cleared requirement marks too, so a decision reopened
// mid-initiative could be silently forgotten the moment the plan was
// regenerated -- with the requirement itself never re-examined against the
// new answer.
func (db *DB) ClearStaleItemsForInitiative(ctx context.Context, initiativeID, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "stale.cleared",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			UPDATE stale_marks SET cleared_at = ?, cleared_by = ?
			WHERE initiative_id = ? AND cleared_at IS NULL AND target_type IN ('task', 'change_set')`,
			nowMS(), actor, initiativeID)
		return err
	})
}
