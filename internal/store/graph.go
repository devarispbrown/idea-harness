package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// EdgeToWrite describes one Product Graph edge to write within a transaction.
type EdgeToWrite struct {
	FromType string
	FromID   string
	EdgeType domain.EdgeType
	ToType   string
	ToID     string
}

// wireEdges inserts graph edges inside an existing transaction. It is the single
// auditable codepath every object-creating method uses to record traceability
// atomically with the object itself (§26, adversarial review B2).
func (db *DB) wireEdges(tx *sql.Tx, initiativeID string, edges []EdgeToWrite) error {
	for _, e := range edges {
		if e.FromType == "" || e.FromID == "" || !e.EdgeType.Valid() || e.ToType == "" || e.ToID == "" {
			return fmt.Errorf("store: invalid graph edge %+v", e)
		}
		if _, err := tx.Exec(`
			INSERT INTO graph_edges (id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, '{}', ?)`,
			id.New(), initiativeID, e.FromType, e.FromID, string(e.EdgeType), e.ToType, e.ToID, nowMS()); err != nil {
			return err
		}
	}
	return nil
}

// ListGraphEdges returns every edge for an initiative, oldest first.
func (db *DB) ListGraphEdges(ctx context.Context, initiativeID string) ([]domain.GraphEdge, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at
		FROM graph_edges WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGraphEdges(rows)
}

// ListGraphEdgesFrom returns outgoing edges from a node.
func (db *DB) ListGraphEdgesFrom(ctx context.Context, initiativeID, fromType, fromID string) ([]domain.GraphEdge, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at
		FROM graph_edges WHERE initiative_id = ? AND from_type = ? AND from_id = ? ORDER BY created_at ASC`, initiativeID, fromType, fromID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGraphEdges(rows)
}

// ListGraphEdgesTo returns incoming edges to a node.
func (db *DB) ListGraphEdgesTo(ctx context.Context, initiativeID, toType, toID string) ([]domain.GraphEdge, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at
		FROM graph_edges WHERE initiative_id = ? AND to_type = ? AND to_id = ? ORDER BY created_at ASC`, initiativeID, toType, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGraphEdges(rows)
}

func scanGraphEdges(rows *sql.Rows) ([]domain.GraphEdge, error) {
	out := make([]domain.GraphEdge, 0)
	for rows.Next() {
		var e domain.GraphEdge
		var created int64
		var meta string
		if err := rows.Scan(&e.ID, &e.InitiativeID, &e.FromType, &e.FromID, &e.EdgeType, &e.ToType, &e.ToID, &meta, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created)
		if meta != "" && meta != "{}" {
			json.Unmarshal([]byte(meta), &e.Metadata)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CreateTask inserts an execution-plan task and wires its `implements`
// (task → requirement) and `depends_on` (task → task) edges in the same
// transaction.
func (db *DB) CreateTask(ctx context.Context, t *domain.Task, implementsRequirementID string, dependsOn []string) error {
	if t.ID == "" {
		t.ID = id.New()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if !t.Status.Valid() {
		t.Status = domain.TaskProposed
	}
	if !t.Priority.Valid() {
		t.Priority = domain.PriorityShould
	}
	return db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   t.ID,
		Actor:         ActorOrchestrator,
		EventType:     "task.created",
	}, func(tx *sql.Tx) error {
		if t.Code == "" {
			code, err := nextTaskCode(tx, t.InitiativeID)
			if err != nil {
				return err
			}
			t.Code = code
		}
		pos, err := nextTaskPosition(tx, t.InitiativeID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO tasks (id, initiative_id, code, title, statement, status, priority, estimate, produced_by_run, superseded_by, position, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.InitiativeID, t.Code, t.Title, t.Statement, t.Status, t.Priority, t.Estimate, t.ProducedByRun, t.SupersededBy, pos, t.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		var edges []EdgeToWrite
		if implementsRequirementID != "" {
			edges = append(edges, EdgeToWrite{FromType: "task", FromID: t.ID, EdgeType: domain.EdgeImplements, ToType: "requirement", ToID: implementsRequirementID})
		}
		for _, dep := range dependsOn {
			edges = append(edges, EdgeToWrite{FromType: "task", FromID: t.ID, EdgeType: domain.EdgeDependsOn, ToType: "task", ToID: dep})
		}
		return db.wireEdges(tx, t.InitiativeID, edges)
	})
}

// AddTaskDependencies wires depends_on edges from taskID to each depTaskID in
// one transaction. Used by the execution-plan two-pass resolution so backward
// (and normalized) dependency references resolve after all tasks exist.
func (db *DB) AddTaskDependencies(ctx context.Context, initiativeID, taskID string, depTaskIDs []string) error {
	edges := make([]EdgeToWrite, 0, len(depTaskIDs))
	for _, dep := range depTaskIDs {
		edges = append(edges, EdgeToWrite{FromType: "task", FromID: taskID, EdgeType: domain.EdgeDependsOn, ToType: "task", ToID: dep})
	}
	if len(edges) == 0 {
		return nil
	}
	return db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   taskID,
		Actor:         ActorOrchestrator,
		EventType:     "task.dependencies_added",
	}, func(tx *sql.Tx) error {
		return db.wireEdges(tx, initiativeID, edges)
	})
}

// SetTaskStatus updates a task's status (domain methods only, §13).
func (db *DB) SetTaskStatus(ctx context.Context, taskID string, status domain.TaskStatus) error {
	if !status.Valid() {
		return fmt.Errorf("store: invalid task status %q", status)
	}
	return db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   taskID,
		Actor:         ActorOrchestrator,
		EventType:     "task.status_changed",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, status, taskID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "task %q not found", taskID)
	})
}

// ListTasks returns tasks for an initiative, oldest first.
func (db *DB) ListTasks(ctx context.Context, initiativeID string) ([]domain.Task, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, code, title, statement, status, priority, estimate, produced_by_run, superseded_by, created_at
		FROM tasks WHERE initiative_id = ? ORDER BY position ASC, created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Task, 0)
	for rows.Next() {
		var t domain.Task
		var created int64
		if err := rows.Scan(&t.ID, &t.InitiativeID, &t.Code, &t.Title, &t.Statement, &t.Status, &t.Priority, &t.Estimate, &t.ProducedByRun, &t.SupersededBy, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = time.UnixMilli(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask returns a single task by id.
func (db *DB) GetTask(ctx context.Context, taskID string) (*domain.Task, error) {
	row := db.SQL().QueryRowContext(ctx, `
		SELECT id, initiative_id, code, title, statement, status, priority, estimate, produced_by_run, superseded_by, created_at
		FROM tasks WHERE id = ?`, taskID)
	var t domain.Task
	var created int64
	if err := row.Scan(&t.ID, &t.InitiativeID, &t.Code, &t.Title, &t.Statement, &t.Status, &t.Priority, &t.Estimate, &t.ProducedByRun, &t.SupersededBy, &created); err != nil {
		return nil, err
	}
	t.CreatedAt = time.UnixMilli(created)
	return &t, nil
}

// nextTaskPosition returns the ordinal for a new task within an initiative
// (one past the current maximum, 0 for the first).
func nextTaskPosition(tx *sql.Tx, initiativeID string) (int, error) {
	var n int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position), -1) + 1 FROM tasks WHERE initiative_id = ?`, initiativeID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// nextTaskCode returns the next monotonic TASK code from a per-initiative
// counter. Tasks are the one deletable code-bearing entity, so a MAX(code)+1
// scheme would reuse a deleted code; the counter never decreases.
func nextTaskCode(tx *sql.Tx, initiativeID string) (string, error) {
	if _, err := tx.Exec(`INSERT INTO task_counters (initiative_id, next_seq) VALUES (?, 1)
		ON CONFLICT(initiative_id) DO UPDATE SET next_seq = next_seq + 1`, initiativeID); err != nil {
		return "", err
	}
	var n int
	if err := tx.QueryRow(`SELECT next_seq FROM task_counters WHERE initiative_id = ?`, initiativeID).Scan(&n); err != nil {
		return "", err
	}
	return id.Code("TASK", n), nil
}

// UpdateTask edits a task's human-editable fields. Guarded at the handler layer
// (only while the plan is editable and the task is not done/cancelled).
func (db *DB) UpdateTask(ctx context.Context, initiativeID, taskID, title, statement string, priority domain.RequirementPriority, estimate string) error {
	return db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   taskID,
		Actor:         ActorOrchestrator,
		EventType:     "task.updated",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE tasks SET title = ?, statement = ?, priority = ?, estimate = ?
			WHERE id = ? AND initiative_id = ?`,
			title, statement, priority, estimate, taskID, initiativeID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "task %q not found", taskID)
	})
}

// DeleteTask removes a task and all of its graph edges in one transaction.
// Callers must first reject deletion when the task has change sets or other
// tasks depend on it (checked at the handler layer).
func (db *DB) DeleteTask(ctx context.Context, initiativeID, taskID string) error {
	return db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   taskID,
		Actor:         ActorOrchestrator,
		EventType:     "task.deleted",
	}, func(tx *sql.Tx) error {
		// Cascade edges in both directions so no dangling depends_on/implements
		// reference remains (graph_edges has no FK).
		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND (from_id = ? OR to_id = ?)`, initiativeID, taskID, taskID); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM tasks WHERE id = ? AND initiative_id = ?`, taskID, initiativeID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "task %q not found", taskID)
	})
}

// ReorderTasks assigns 0..n-1 positions to the given task ids, which must be
// exactly a permutation of the initiative's current tasks. Idempotent and
// transactional.
func (db *DB) ReorderTasks(ctx context.Context, initiativeID string, orderedIDs []string) error {
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "tasks.reordered",
	}, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM tasks WHERE initiative_id = ?`, initiativeID)
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing[id] = true
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(orderedIDs) != len(ids) {
			return fmt.Errorf("store: reorder needs exactly %d task ids, got %d", len(ids), len(orderedIDs))
		}
		seen := map[string]bool{}
		for _, id := range orderedIDs {
			if !existing[id] || seen[id] {
				return fmt.Errorf("store: reorder task id list is not a permutation of the initiative's tasks")
			}
			seen[id] = true
		}
		for i, id := range orderedIDs {
			if _, err := tx.Exec(`UPDATE tasks SET position = ? WHERE id = ? AND initiative_id = ?`, i, id, initiativeID); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteTasksForInitiative removes every task (and its edges) for an initiative.
// Used by plan revision, which regenerates the task list fresh. Callers must
// reject when any task has a change set.
func (db *DB) DeleteTasksForInitiative(ctx context.Context, initiativeID string) error {
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "tasks.deleted",
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND (from_type = 'task' OR to_type = 'task')`, initiativeID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM tasks WHERE initiative_id = ?`, initiativeID); err != nil {
			return err
		}
		return nil
	})
}

// ReplaceSuggestedAnswers replaces the stored AI-suggested answers for an
// initiative with a fresh set.
func (db *DB) ReplaceSuggestedAnswers(ctx context.Context, initiativeID string, answers []domain.SuggestedAnswer) error {
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "suggested_answers.replaced",
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM suggested_answers WHERE initiative_id = ?`, initiativeID); err != nil {
			return err
		}
		for _, a := range answers {
			if _, err := tx.Exec(`
				INSERT INTO suggested_answers (id, initiative_id, question_id, question, answer, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				id.New(), initiativeID, a.QuestionID, a.Question, a.Answer, nowMS()); err != nil {
				return err
			}
		}
		return nil
	})
}

// AppendSuggestedAnswers appends a batch of AI-suggested answers (used for
// incremental, chunked generation so progress is observable).
func (db *DB) AppendSuggestedAnswers(ctx context.Context, initiativeID string, answers []domain.SuggestedAnswer) error {
	if len(answers) == 0 {
		return nil
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "suggested_answers.appended",
	}, func(tx *sql.Tx) error {
		for _, a := range answers {
			if _, err := tx.Exec(`
				INSERT INTO suggested_answers (id, initiative_id, question_id, question, answer, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				id.New(), initiativeID, a.QuestionID, a.Question, a.Answer, nowMS()); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListSuggestedAnswers returns the stored AI-suggested answers for an initiative.
func (db *DB) ListSuggestedAnswers(ctx context.Context, initiativeID string) ([]domain.SuggestedAnswer, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT question_id, question, answer
		FROM suggested_answers WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.SuggestedAnswer, 0)
	for rows.Next() {
		var a domain.SuggestedAnswer
		if err := rows.Scan(&a.QuestionID, &a.Question, &a.Answer); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AuditGraphEdges reports traceability gaps for an initiative (§M2 tail): each
// task missing an `implements` edge, each requirement/assertion/artifact missing
// a `produced_by` edge, and cyclic task dependencies (DFS cycle check). It is
// read-only and returns findings rather than persisting them.
func (db *DB) AuditGraphEdges(ctx context.Context, initiativeID string) ([]domain.Finding, error) {
	var out []domain.Finding

	tasks, err := db.ListTasks(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	deps := map[string][]string{}
	for _, t := range tasks {
		edges, err := db.ListGraphEdgesFrom(ctx, initiativeID, "task", t.ID)
		if err != nil {
			return nil, err
		}
		hasImplements := false
		for _, ed := range edges {
			switch ed.EdgeType {
			case domain.EdgeDependsOn:
				deps[t.ID] = append(deps[t.ID], ed.ToID)
			case domain.EdgeImplements:
				hasImplements = true
			}
		}
		if !hasImplements {
			out = append(out, domain.Finding{
				InitiativeID:   initiativeID,
				ReviewRunID:    doctorReviewRunID,
				Severity:       domain.SeverityHigh,
				Confidence:     domain.ConfidenceHigh,
				Category:       "traceability",
				Finding:        fmt.Sprintf("%s has no implements edge to a requirement", t.Code),
				Evidence:       t.Title,
				Impact:         "untraced work cannot be verified against the PRD",
				Recommendation: "trace the task to a requirement",
				TargetType:     "task",
				TargetID:       t.ID,
			})
		}
	}

	// Requirements, assertions, and artifacts must each carry a produced_by edge
	// back to the run that produced them.
	reqs, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		if missing, err := db.missingProducedBy(ctx, initiativeID, "requirement", r.ID); err != nil {
			return nil, err
		} else if missing {
			out = append(out, producedByFinding(initiativeID, "requirement", r.ID, r.Code, r.Statement))
		}
	}

	assertions, err := db.ListAssertions(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, a := range assertions {
		if missing, err := db.missingProducedBy(ctx, initiativeID, "assertion", a.ID); err != nil {
			return nil, err
		} else if missing {
			out = append(out, producedByFinding(initiativeID, "assertion", a.ID, a.ID, a.Statement))
		}
	}

	artifacts, err := db.ListArtifacts(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, a := range artifacts {
		if missing, err := db.missingProducedBy(ctx, initiativeID, "artifact", a.ArtifactID); err != nil {
			return nil, err
		} else if missing {
			out = append(out, producedByFinding(initiativeID, "artifact", a.ArtifactID, string(a.Kind), a.Kind))
		}
	}

	if cycle := DetectTaskCycle(deps); cycle != "" {
		out = append(out, domain.Finding{
			InitiativeID:   initiativeID,
			ReviewRunID:    doctorReviewRunID,
			Severity:       domain.SeverityCritical,
			Confidence:     domain.ConfidenceHigh,
			Category:       "plan",
			Finding:        "task dependency cycle detected: " + cycle,
			Impact:         "the plan cannot be scheduled",
			Recommendation: "break the cycle",
			TargetType:     "task",
			TargetID:       cycle,
		})
	}
	return out, nil
}

// doctorReviewRunID is the synthetic review-run marker for doctor findings.
const doctorReviewRunID = "doctor-audit"

// missingProducedBy reports whether an object has no outgoing produced_by edge.
func (db *DB) missingProducedBy(ctx context.Context, initiativeID, fromType, fromID string) (bool, error) {
	edges, err := db.ListGraphEdgesFrom(ctx, initiativeID, fromType, fromID)
	if err != nil {
		return false, err
	}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeProducedBy {
			return false, nil
		}
	}
	return true, nil
}

// producedByFinding builds a traceability finding for a missing produced_by edge.
func producedByFinding(initiativeID, targetType, targetID, label string, evidence any) domain.Finding {
	return domain.Finding{
		InitiativeID:   initiativeID,
		ReviewRunID:    doctorReviewRunID,
		Severity:       domain.SeverityMedium,
		Confidence:     domain.ConfidenceHigh,
		Category:       "traceability",
		Finding:        fmt.Sprintf("%s %q has no produced_by edge", targetType, label),
		Evidence:       fmt.Sprint(evidence),
		Impact:         "provenance cannot be traced back to its producing run",
		Recommendation: "record the producing run",
		TargetType:     targetType,
		TargetID:       targetID,
	}
}

// DetectTaskCycle returns the id of a task that participates in a dependency
// cycle, or "" (three-color DFS). It is the single canonical cycle detector,
// shared by the plan checks and the doctor edge audit.
func DetectTaskCycle(deps map[string][]string) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var dfs func(string) string
	dfs = func(n string) string {
		color[n] = gray
		for _, m := range deps[n] {
			switch color[m] {
			case gray:
				return m
			case white:
				if c := dfs(m); c != "" {
					return c
				}
			}
		}
		color[n] = black
		return ""
	}
	for n := range deps {
		if color[n] == white {
			if c := dfs(n); c != "" {
				return c
			}
		}
	}
	return ""
}
