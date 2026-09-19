package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ErrDependsOnCycle is returned by SetTaskDependencies when the requested
// depends_on set would introduce a dependency cycle (review M5). It is
// checked again INSIDE the write transaction here, not only by the
// handler's own pre-check (wouldCycle): the daemon's per-initiative
// lockPlanEdit serializes the common race (two concurrent set-depends_on
// requests) away, but this is the defense-in-depth layer for any other path
// that reaches this method without going through that lock.
var ErrDependsOnCycle = errors.New("store: that dependency would create a cycle")

// SetTaskDependencies replaces taskID's depends_on edges wholesale with one
// per (de-duplicated, non-empty, non-self) id in depTaskIDs, refusing --
// writing nothing -- if doing so would introduce a dependency cycle
// anywhere in the initiative's task graph (review M5). The cycle check and
// the write share this one transaction.
func (db *DB) SetTaskDependencies(ctx context.Context, initiativeID, taskID string, depTaskIDs []string) error {
	return db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   taskID,
		Actor:         ActorOrchestrator,
		EventType:     "task.dependencies_replaced",
	}, func(tx *sql.Tx) error {
		seen := map[string]bool{}
		deduped := make([]string, 0, len(depTaskIDs))
		for _, dep := range depTaskIDs {
			if dep == "" || dep == taskID || seen[dep] {
				continue
			}
			seen[dep] = true
			deduped = append(deduped, dep)
		}

		taskIDs, err := allTaskIDsTx(tx, initiativeID)
		if err != nil {
			return err
		}
		graph := make(map[string][]string, len(taskIDs))
		for _, tid := range taskIDs {
			if tid == taskID {
				graph[tid] = deduped
				continue
			}
			targets, err := taskEdgeTargetsTx(tx, initiativeID, domain.EdgeDependsOn, tid)
			if err != nil {
				return err
			}
			graph[tid] = targets
		}
		if cyc := DetectTaskCycle(graph); cyc != "" {
			return fmt.Errorf("%w (through %s)", ErrDependsOnCycle, cyc)
		}

		if _, err := tx.Exec(`
			DELETE FROM graph_edges
			WHERE initiative_id = ? AND from_type = 'task' AND from_id = ? AND edge_type = ? AND to_type = 'task'`,
			initiativeID, taskID, string(domain.EdgeDependsOn)); err != nil {
			return err
		}
		edges := make([]EdgeToWrite, 0, len(deduped))
		for _, dep := range deduped {
			edges = append(edges, EdgeToWrite{FromType: "task", FromID: taskID, EdgeType: domain.EdgeDependsOn, ToType: "task", ToID: dep})
		}
		return db.wireEdges(tx, initiativeID, edges)
	})
}

// allTaskIDsTx returns every task id in initiativeID, within tx.
func allTaskIDsTx(tx *sql.Tx, initiativeID string) ([]string, error) {
	rows, err := tx.Query(`SELECT id FROM tasks WHERE initiative_id = ?`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
