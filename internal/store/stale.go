package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// markDecisionDependentsStale marks the requirements that cite decisionID
// (via `requirement --derived_from--> decision` edges, wired by
// WireRequirementDerivedFromDecision), the tasks that implement or verify
// those requirements, and the applied change sets built from those tasks, as
// stale (review A4 #5). Called by ReopenDecision: a decision is authoritative
// only while it stays decided, and everything built on it stops being
// trustworthy the moment a human reopens it to change the answer.
//
// A no-op (no rows written) when nothing depends on the decision yet, which is
// the common case for most decisions.
func (db *DB) markDecisionDependentsStale(ctx context.Context, initiativeID, decisionID, decisionCode string) error {
	reqEdges, err := db.ListGraphEdgesTo(ctx, initiativeID, "decision", decisionID)
	if err != nil {
		return err
	}
	var reqIDs []string
	for _, e := range reqEdges {
		if e.FromType == "requirement" && e.EdgeType == domain.EdgeDerivedFrom {
			reqIDs = append(reqIDs, e.FromID)
		}
	}

	var taskIDs []string
	for _, reqID := range reqIDs {
		taskEdges, err := db.ListGraphEdgesTo(ctx, initiativeID, "requirement", reqID)
		if err != nil {
			return err
		}
		for _, e := range taskEdges {
			if e.FromType == "task" && (e.EdgeType == domain.EdgeImplements || e.EdgeType == domain.EdgeVerifies) {
				taskIDs = append(taskIDs, e.FromID)
			}
		}
	}

	var csIDs []string
	for _, taskID := range taskIDs {
		cses, err := db.ListChangeSetsByTask(ctx, taskID)
		if err != nil {
			return err
		}
		for _, cs := range cses {
			if cs.Status == domain.ChangeSetApplied {
				csIDs = append(csIDs, cs.ID)
			}
		}
	}

	if len(reqIDs) == 0 && len(taskIDs) == 0 && len(csIDs) == 0 {
		return nil
	}

	reason := decisionCode + " changed"
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "stale.marked",
		Operation:     reason,
	}, func(tx *sql.Tx) error {
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
	})
}

// markStaleTx idempotently inserts one active stale mark: a target already
// carrying an active (uncleared) mark is left alone rather than duplicated, so
// reopening the same decision twice in a row does not pile up rows.
func markStaleTx(tx *sql.Tx, initiativeID, targetType, targetID, reason, decisionID string) error {
	var exists int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM stale_marks
		WHERE initiative_id = ? AND target_type = ? AND target_id = ? AND cleared_at IS NULL`,
		initiativeID, targetType, targetID).Scan(&exists); err != nil {
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

// ClearStaleItemsForInitiative clears every active stale mark for an
// initiative (review A4 #5: "re-planning clears the stale state" -- a fresh
// plan is re-derived from the live decision state, so whatever was stale about
// the old one no longer applies). Used by RevisePlan and ReopenPhase.
func (db *DB) ClearStaleItemsForInitiative(ctx context.Context, initiativeID, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "stale.cleared",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			UPDATE stale_marks SET cleared_at = ?, cleared_by = ?
			WHERE initiative_id = ? AND cleared_at IS NULL`,
			nowMS(), actor, initiativeID)
		return err
	})
}
