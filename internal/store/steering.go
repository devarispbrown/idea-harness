package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Held steering notes: a note sent to a running agent that could not take it
// (workflow.SteerRun) waits here for the task's next run. Durable so a
// daemon restart between the note and that run does not lose it.

// HeldSteeringNote is one note held for an initiative's next run.
type HeldSteeringNote struct {
	ID           int64
	InitiativeID string
	// TaskID and RunID are the task and run the note was sent to.
	TaskID string
	RunID  string
	Note   string
}

// HoldSteeringNote stores a note for the initiative's next run. The caller
// has already redacted it.
func (db *DB) HoldSteeringNote(ctx context.Context, n HeldSteeringNote) error {
	if n.InitiativeID == "" || strings.TrimSpace(n.Note) == "" {
		return fmt.Errorf("store: hold steering note: initiative and note are required")
	}
	_, err := db.sql.ExecContext(ctx, `INSERT INTO held_steering_notes (initiative_id, task_id, run_id, note, created_at) VALUES (?, ?, ?, ?, ?)`,
		n.InitiativeID, n.TaskID, n.RunID, n.Note, nowMS())
	if err != nil {
		return fmt.Errorf("store: hold steering note: %w", err)
	}
	return nil
}

// PendingSteeringNotes lists the initiative's notes no run has taken yet,
// oldest first.
func (db *DB) PendingSteeringNotes(ctx context.Context, initiativeID string) ([]HeldSteeringNote, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, initiative_id, task_id, run_id, note FROM held_steering_notes
		WHERE initiative_id = ? AND consumed_at IS NULL ORDER BY id`, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("store: pending steering notes: %w", err)
	}
	defer rows.Close()
	var out []HeldSteeringNote
	for rows.Next() {
		var n HeldSteeringNote
		if err := rows.Scan(&n.ID, &n.InitiativeID, &n.TaskID, &n.RunID, &n.Note); err != nil {
			return nil, fmt.Errorf("store: pending steering notes: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TakeSteeringNotes marks every pending note of the initiative consumed by
// taskID's run and returns them, oldest first. One UPDATE ... RETURNING: the
// read and the mark are a single statement, so two concurrent takers can
// never both get the same note.
func (db *DB) TakeSteeringNotes(ctx context.Context, initiativeID, taskID string) ([]HeldSteeringNote, error) {
	rows, err := db.sql.QueryContext(ctx, `UPDATE held_steering_notes SET consumed_at = ?, consumed_task_id = ?
		WHERE initiative_id = ? AND consumed_at IS NULL
		RETURNING id, initiative_id, task_id, run_id, note`, nowMS(), taskID, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("store: take steering notes: %w", err)
	}
	defer rows.Close()
	var out []HeldSteeringNote
	for rows.Next() {
		var n HeldSteeringNote
		if err := rows.Scan(&n.ID, &n.InitiativeID, &n.TaskID, &n.RunID, &n.Note); err != nil {
			return nil, fmt.Errorf("store: take steering notes: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: take steering notes: %w", err)
	}
	// RETURNING's row order is unspecified; the brief wants them as sent.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
