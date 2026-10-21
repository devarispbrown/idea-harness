package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ErrSelfMerge is returned by MergeTasks when keepID == mergeID (review H4):
// a self-merge used to pass both existence guards and then delete the
// survivor, since nothing checked the two ids were distinct.
//
// No "store: " prefix (review LOW #10): unlike most of this package's
// sentinel errors, the handler surfaces THIS ONE's Error() text verbatim to
// the API caller as a 400 body -- daemon/errors.go's whole contract is that
// "the storage layer's own strings" never become the product's error
// messages, and a literal "store: " leaking through here contradicted it for
// the one sentinel that is genuinely meant to be read by a person.
var ErrSelfMerge = errors.New("cannot merge a task with itself")

// ErrMergeCycle is returned by MergeTasks, wrapped with the cycling task's
// id, when the merge would introduce a dependency cycle (review H4). The
// handler maps it to 400, the same as the depends-on endpoint's own cycle
// refusal, and surfaces its Error() text verbatim -- see ErrSelfMerge's
// comment on why it carries no "store: " prefix (review LOW #10).
var ErrMergeCycle = errors.New("merging would create a dependency cycle")

// MergeTasks merges mergeID into keepID as ONE transaction (review H4). Every
// step below -- and the cycle check -- runs inside it: the handler used to
// make six separate Apply calls, so a failure partway (or a concurrent
// request racing it) could leave a half-merged plan.
//
// It:
//   - refuses outright when keepID == mergeID (a self-merge used to pass both
//     guards and then delete the survivor);
//   - unions the requirements ("implements" edges) each task holds onto
//     keepID;
//   - concatenates their statements;
//   - unions their outgoing depends_on (each other and self excluded) onto
//     keepID;
//   - rewires every dependent of mergeID onto keepID, deduplicated -- a task
//     that depended on BOTH merged tasks must not end up with two identical
//     depends_on edges to keepID;
//   - builds the FINAL task graph (mergeID removed, its incoming edges
//     redirected to keepID) and refuses the whole merge, writing nothing, if
//     that graph has a cycle;
//   - only then deletes mergeID and its remaining edges.
//
// Returns the updated survivor task.
func (db *DB) MergeTasks(ctx context.Context, initiativeID, keepID, mergeID string) (*domain.Task, error) {
	if keepID == mergeID {
		return nil, ErrSelfMerge
	}
	var result *domain.Task
	err := db.Apply(ctx, Event{
		AggregateType: "task",
		AggregateID:   keepID,
		Actor:         ActorOrchestrator,
		EventType:     "task.merged",
	}, func(tx *sql.Tx) error {
		keep, err := getTaskTx(tx, keepID)
		if err != nil {
			return err
		}
		merge, err := getTaskTx(tx, mergeID)
		if err != nil {
			return err
		}
		if keep.InitiativeID != initiativeID {
			return fmt.Errorf("store: task %q does not belong to initiative %q", keepID, initiativeID)
		}
		if merge.InitiativeID != initiativeID {
			return fmt.Errorf("store: task %q does not belong to initiative %q", mergeID, initiativeID)
		}

		// Union the requirements each task implements onto keepID.
		reqIDs, err := taskEdgeTargetsTx(tx, initiativeID, domain.EdgeImplements, keepID, mergeID)
		if err != nil {
			return err
		}
		if err := replaceEdgesTx(tx, db, initiativeID, keepID, domain.EdgeImplements, "requirement", reqIDs); err != nil {
			return err
		}
		// MEDIUM fix: `verifies` edges (task -> a requirement it verifies,
		// distinct from `implements`) were silently destroyed by a merge --
		// only EdgeImplements was ever unioned onto keepID, so a generated
		// verification task merged for tidiness dropped its requirement's
		// verification coverage entirely. Same union-then-replace shape.
		verifyIDs, err := taskEdgeTargetsTx(tx, initiativeID, domain.EdgeVerifies, keepID, mergeID)
		if err != nil {
			return err
		}
		if err := replaceEdgesTx(tx, db, initiativeID, keepID, domain.EdgeVerifies, "requirement", verifyIDs); err != nil {
			return err
		}

		// Concatenate the statements.
		statement := keep.Statement
		if s2 := merge.Statement; s2 != "" {
			if statement != "" {
				statement += "\n\n"
			}
			statement += s2
		}
		if _, err := tx.Exec(`UPDATE tasks SET statement = ? WHERE id = ? AND initiative_id = ?`,
			statement, keepID, initiativeID); err != nil {
			return err
		}

		// Union outgoing depends_on (each other and self excluded) onto keepID.
		unionDeps, err := taskEdgeTargetsTx(tx, initiativeID, domain.EdgeDependsOn, keepID, mergeID)
		if err != nil {
			return err
		}
		outgoing := unionDeps[:0]
		for _, id := range unionDeps {
			if id != keepID && id != mergeID {
				outgoing = append(outgoing, id)
			}
		}
		if err := replaceEdgesTx(tx, db, initiativeID, keepID, domain.EdgeDependsOn, "task", outgoing); err != nil {
			return err
		}

		// Rewire every dependent of mergeID onto keepID, deduplicated (review
		// H4) against both keepID's own (just-replaced) depends_on set and
		// repeats within this loop.
		dependents, err := listGraphEdgesToTx(tx, initiativeID, "task", mergeID)
		if err != nil {
			return err
		}
		already := map[string]bool{}
		for _, e := range dependents {
			if e.EdgeType != domain.EdgeDependsOn || e.FromID == keepID || already[e.FromID] {
				continue
			}
			has, err := hasDependsOnEdgeTx(tx, initiativeID, e.FromID, keepID)
			if err != nil {
				return err
			}
			already[e.FromID] = true
			if has {
				continue
			}
			if err := db.wireEdges(tx, initiativeID, []EdgeToWrite{
				{FromType: "task", FromID: e.FromID, EdgeType: domain.EdgeDependsOn, ToType: "task", ToID: keepID},
			}); err != nil {
				return err
			}
		}

		// Cycle check on the FINAL graph, before anything is deleted (review
		// H4): mergeID removed, any edge that pointed at it redirected to
		// keepID (exactly what the rewiring above just did).
		deps, err := finalTaskDepsGraphTx(tx, initiativeID, keepID, mergeID, outgoing)
		if err != nil {
			return err
		}
		if cyc := DetectTaskCycle(deps); cyc != "" {
			return fmt.Errorf("%w (through %s)", ErrMergeCycle, cyc)
		}

		// Only now is mergeID removed -- its incoming depends_on edges were
		// already rewired above; any implements/verifies edges it still
		// holds are cascaded away with it.
		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND (from_id = ? OR to_id = ?)`,
			initiativeID, mergeID, mergeID); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM tasks WHERE id = ? AND initiative_id = ?`, mergeID, initiativeID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "task %q not found", mergeID); err != nil {
			return err
		}

		result, err = getTaskTx(tx, keepID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// getTaskTx reads one task within an existing transaction. Duplicated from
// GetTask (which reads via db.SQL(), the pool -- calling that from inside a
// transaction on a single-connection pool (SetMaxOpenConns(1)) would
// deadlock: the transaction holds the one connection, and the pool query
// blocks forever waiting for another).
func getTaskTx(tx *sql.Tx, taskID string) (*domain.Task, error) {
	row := tx.QueryRow(`
		SELECT id, initiative_id, code, title, statement, status, priority, estimate, produced_by_run, superseded_by, env_vars, created_at
		FROM tasks WHERE id = ?`, taskID)
	var t domain.Task
	var created int64
	var envVars string
	if err := row.Scan(&t.ID, &t.InitiativeID, &t.Code, &t.Title, &t.Statement, &t.Status, &t.Priority, &t.Estimate, &t.ProducedByRun, &t.SupersededBy, &envVars, &created); err != nil {
		return nil, notFoundIfNoRows(err, "task", taskID)
	}
	t.CreatedAt = time.UnixMilli(created)
	_ = json.Unmarshal([]byte(envVars), &t.EnvVars)
	return &t, nil
}

// listGraphEdgesToTx is ListGraphEdgesTo, reading through tx instead of the
// pool (see getTaskTx's comment on why that matters inside a transaction).
func listGraphEdgesToTx(tx *sql.Tx, initiativeID, toType, toID string) ([]domain.GraphEdge, error) {
	rows, err := tx.Query(`
		SELECT id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at
		FROM graph_edges WHERE initiative_id = ? AND to_type = ? AND to_id = ? ORDER BY created_at ASC`,
		initiativeID, toType, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGraphEdges(rows)
}

// taskEdgeTargetsTx returns the de-duplicated union of edgeType targets
// across each of taskIDs' own outgoing edges (review H4's merge: union
// requirements, union outgoing depends_on), read within tx.
func taskEdgeTargetsTx(tx *sql.Tx, initiativeID string, edgeType domain.EdgeType, taskIDs ...string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, taskID := range taskIDs {
		rows, err := tx.Query(`
			SELECT to_id FROM graph_edges
			WHERE initiative_id = ? AND from_type = 'task' AND from_id = ? AND edge_type = ?`,
			initiativeID, taskID, string(edgeType))
		if err != nil {
			return nil, err
		}
		scanErr := func() error {
			defer rows.Close()
			for rows.Next() {
				var to string
				if err := rows.Scan(&to); err != nil {
					return err
				}
				if !seen[to] {
					seen[to] = true
					out = append(out, to)
				}
			}
			return rows.Err()
		}()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	return out, nil
}

// hasDependsOnEdgeTx reports whether a task->task depends_on edge already
// exists, for the merge's dependent-rewiring dedup (review H4).
func hasDependsOnEdgeTx(tx *sql.Tx, initiativeID, fromID, toID string) (bool, error) {
	var one int
	err := tx.QueryRow(`
		SELECT 1 FROM graph_edges
		WHERE initiative_id = ? AND from_type = 'task' AND from_id = ? AND edge_type = ? AND to_type = 'task' AND to_id = ?
		LIMIT 1`, initiativeID, fromID, string(domain.EdgeDependsOn), toID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// replaceEdgesTx replaces taskID's outgoing edgeType edges wholesale with one
// per (de-duplicated, non-empty) id in targetIDs, within tx. Shared by
// MergeTasks for both the requirements union and the depends_on union --
// the same delete-then-wire shape as ReplaceTaskRequirements/
// ReplaceTaskDependencies, but tx-scoped so it can share MergeTasks' single
// transaction.
func replaceEdgesTx(tx *sql.Tx, db *DB, initiativeID, taskID string, edgeType domain.EdgeType, toType string, targetIDs []string) error {
	if _, err := tx.Exec(`
		DELETE FROM graph_edges
		WHERE initiative_id = ? AND from_type = 'task' AND from_id = ? AND edge_type = ? AND to_type = ?`,
		initiativeID, taskID, string(edgeType), toType); err != nil {
		return err
	}
	var edges []EdgeToWrite
	seen := map[string]bool{}
	for _, targetID := range targetIDs {
		if targetID == "" || targetID == taskID || seen[targetID] {
			continue
		}
		seen[targetID] = true
		edges = append(edges, EdgeToWrite{FromType: "task", FromID: taskID, EdgeType: edgeType, ToType: toType, ToID: targetID})
	}
	return db.wireEdges(tx, initiativeID, edges)
}

// finalTaskDepsGraphTx builds the task depends_on graph MergeTasks's merge
// would leave behind, for a pre-write cycle check (review H4): mergeID is
// excluded (it is about to be deleted), keepID's depends_on is keepOutgoing
// (the just-computed union), and every other task's existing depends_on is
// read as-is except that a reference to mergeID is redirected to keepID --
// exactly what the dependent-rewiring step in MergeTasks does.
func finalTaskDepsGraphTx(tx *sql.Tx, initiativeID, keepID, mergeID string, keepOutgoing []string) (map[string][]string, error) {
	rows, err := tx.Query(`SELECT id FROM tasks WHERE initiative_id = ?`, initiativeID)
	if err != nil {
		return nil, err
	}
	var taskIDs []string
	func() {
		defer rows.Close()
		for rows.Next() {
			var tid string
			if err := rows.Scan(&tid); err != nil {
				return
			}
			taskIDs = append(taskIDs, tid)
		}
	}()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	deps := map[string][]string{}
	for _, tid := range taskIDs {
		if tid == mergeID {
			continue
		}
		if tid == keepID {
			deps[tid] = keepOutgoing
			continue
		}
		targets, err := taskEdgeTargetsTx(tx, initiativeID, domain.EdgeDependsOn, tid)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var out []string
		for _, to := range targets {
			if to == mergeID {
				to = keepID
			}
			if to == tid || seen[to] {
				continue
			}
			seen[to] = true
			out = append(out, to)
		}
		deps[tid] = out
	}
	return deps, nil
}
