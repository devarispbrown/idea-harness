package store

import (
	"context"
	"time"
)

// TimelineChangeSetEvent is one change_set-aggregate event scoped to a change
// set that belongs to the given initiative (C1, wave 3 contract): the join
// that makes BuildTimeline correct where the old What Changed projection
// was not -- it read the whole events table with no initiative filter at
// all, so one initiative's "changed" leaked another's counts.
type TimelineChangeSetEvent struct {
	ChangeSetID string
	HumanID     string
	TaskID      string
	EventType   string // "change_set.applied" | "change_set.reverted" | "change_set.status_changed"
	Operation   string // "from->to" for status_changed
	Actor       string
	At          time.Time
}

// ListChangeSetEventsForInitiative returns the change-set lifecycle events
// (applied/reverted/status_changed) for change sets belonging to
// initiativeID, oldest first. Scoped by joining events back to change_sets on
// aggregate_id, not by trusting anything on the event row itself -- events
// carry no initiative_id.
func (db *DB) ListChangeSetEventsForInitiative(ctx context.Context, initiativeID string) ([]TimelineChangeSetEvent, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT e.aggregate_id, cs.human_id, cs.task_id, e.event_type, e.operation, e.actor, e.created_at
		FROM events e JOIN change_sets cs ON cs.id = e.aggregate_id
		WHERE cs.initiative_id = ? AND e.aggregate_type = 'change_set'
			AND e.event_type IN ('change_set.applied', 'change_set.reverted', 'change_set.status_changed')
		ORDER BY e.seq ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TimelineChangeSetEvent, 0)
	for rows.Next() {
		var ev TimelineChangeSetEvent
		var created int64
		if err := rows.Scan(&ev.ChangeSetID, &ev.HumanID, &ev.TaskID, &ev.EventType, &ev.Operation, &ev.Actor, &created); err != nil {
			return nil, err
		}
		ev.At = time.UnixMilli(created)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// TimelineDecisionEvent is one decision-aggregate event scoped to a decision
// that belongs to the given initiative (C1, wave 3 contract).
type TimelineDecisionEvent struct {
	DecisionID string
	Code       string
	Question   string
	EventType  string // "decision.resolved" | "decision.reopened"
	Actor      string
	At         time.Time
}

// ListDecisionEventsForInitiative returns decision resolve/reopen events for
// decisions belonging to initiativeID, oldest first.
func (db *DB) ListDecisionEventsForInitiative(ctx context.Context, initiativeID string) ([]TimelineDecisionEvent, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT e.aggregate_id, d.code, d.question, e.event_type, e.actor, e.created_at
		FROM events e JOIN decisions d ON d.id = e.aggregate_id
		WHERE d.initiative_id = ? AND e.aggregate_type = 'decision'
			AND e.event_type IN ('decision.resolved', 'decision.reopened')
		ORDER BY e.seq ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TimelineDecisionEvent, 0)
	for rows.Next() {
		var ev TimelineDecisionEvent
		var created int64
		if err := rows.Scan(&ev.DecisionID, &ev.Code, &ev.Question, &ev.EventType, &ev.Actor, &created); err != nil {
			return nil, err
		}
		ev.At = time.UnixMilli(created)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// TimelineStatusEvent is one initiative.status_changed event.
type TimelineStatusEvent struct {
	Operation string // "from->to"
	Actor     string
	At        time.Time
}

// ListInitiativeStatusEvents returns an initiative's own status_changed
// events, oldest first (the phase-completed entries of BuildTimeline).
func (db *DB) ListInitiativeStatusEvents(ctx context.Context, initiativeID string) ([]TimelineStatusEvent, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT operation, actor, created_at FROM events
		WHERE aggregate_type = 'initiative' AND aggregate_id = ? AND event_type = 'initiative.status_changed'
		ORDER BY seq ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TimelineStatusEvent, 0)
	for rows.Next() {
		var ev TimelineStatusEvent
		var created int64
		if err := rows.Scan(&ev.Operation, &ev.Actor, &created); err != nil {
			return nil, err
		}
		ev.At = time.UnixMilli(created)
		out = append(out, ev)
	}
	return out, rows.Err()
}
