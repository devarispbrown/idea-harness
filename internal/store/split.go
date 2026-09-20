package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// SplitTaskSpec is one replacement task for SplitTask (review M4, B3): a
// title, statement, priority, and the requirement ids it implements.
// RequirementIDs must be non-empty -- "Added tasks take a requirement" -- the
// caller (the handler, which alone has access to resolveRequirementRefs) is
// responsible for resolving referenced codes to ids and defaulting to the
// original task's own requirements before this is ever called.
type SplitTaskSpec struct {
	Title          string
	Statement      string
	Priority       domain.RequirementPriority
	RequirementIDs []string
}

// SplitTask replaces originalID with the given specs in ONE transaction
// (review M4): the old handler validated and created each replacement
// inside the same loop, so an invalid second (or third...) spec returned 400
// AFTER the first replacement was already created and the original was not
// yet deleted -- the exact half-applied-write bug this branch had already
// fixed for PATCH. Every spec here is validated BEFORE any write, all
// creations share this one transaction, and the original is deleted last,
// in the same transaction, only if every earlier step succeeded.
//
// Each new task inherits originalDeps (the original's own outgoing
// depends_on); every dependent of originalID is rewired to depend on ALL the
// new tasks (a safe superset -- narrowing it is a follow-up `set
// depends_on`). Returns the created tasks, in spec order.
func (db *DB) SplitTask(ctx context.Context, initiativeID, originalID string, specs []SplitTaskSpec, originalDeps []string) ([]*domain.Task, error) {
	if len(specs) < 2 {
		return nil, fmt.Errorf("store: split requires at least 2 replacement tasks")
	}
	for i, spec := range specs {
		if strings.TrimSpace(spec.Title) == "" {
			return nil, fmt.Errorf("store: replacement task %d needs a title", i+1)
		}
		if len(spec.RequirementIDs) == 0 {
			return nil, fmt.Errorf("store: replacement task %d (%q) needs at least one requirement", i+1, spec.Title)
		}
	}

	var created []*domain.Task
	err := db.Apply(ctx, Event{
		// MEDIUM fix: keyed on the INITIATIVE, not originalID. originalID is
		// deleted in this same transaction, so an event keyed on it could
		// never be read back: ListEventsForInitiative's per-type clause is a
		// subquery against the live table (`aggregate_id IN (SELECT id FROM
		// tasks WHERE ...)`), which a deleted row can never satisfy. Keying
		// on the initiative mirrors SupersedeRequirements/SupersedeAssertions'
		// own bulk-event shape, and the 'task' clause now carries the same
		// direct-match escape hatch those two already have.
		AggregateType: "task",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "task.split",
		Operation:     originalID,
	}, func(tx *sql.Tx) error {
		original, err := getTaskTx(tx, originalID)
		if err != nil {
			return err
		}
		if original.InitiativeID != initiativeID {
			return fmt.Errorf("store: task %q does not belong to initiative %q", originalID, initiativeID)
		}
		dependents, err := listGraphEdgesToTx(tx, initiativeID, "task", originalID)
		if err != nil {
			return err
		}
		// MEDIUM fix: the original's own `verifies` edges (task -> a
		// requirement it verifies, distinct from `implements`) were silently
		// dropped by a split -- only EdgeImplements was ever carried onto the
		// replacements. Carried onto EVERY new task, the same "safe
		// superset" choice already made for depends_on above.
		originalVerifies, err := taskEdgeTargetsTx(tx, initiativeID, domain.EdgeVerifies, originalID)
		if err != nil {
			return err
		}

		created = make([]*domain.Task, 0, len(specs))
		for _, spec := range specs {
			priority := spec.Priority
			if !priority.Valid() {
				priority = original.Priority
			}
			nt := &domain.Task{
				InitiativeID: initiativeID,
				Title:        spec.Title,
				Statement:    spec.Statement,
				Priority:     priority,
			}
			if err := db.createTaskTx(tx, nt, domain.EdgeImplements, spec.RequirementIDs, originalDeps); err != nil {
				return err
			}
			if len(originalVerifies) > 0 {
				verifyEdges := make([]EdgeToWrite, 0, len(originalVerifies))
				for _, reqID := range originalVerifies {
					verifyEdges = append(verifyEdges, EdgeToWrite{FromType: "task", FromID: nt.ID, EdgeType: domain.EdgeVerifies, ToType: "requirement", ToID: reqID})
				}
				if err := db.wireEdges(tx, initiativeID, verifyEdges); err != nil {
					return err
				}
			}
			// MEDIUM fix: createTaskTx is createTask's tx-scoped BODY,
			// called directly here (so every replacement shares this one
			// transaction, review M4) -- but createTask's own db.Apply
			// wrapper is what records "task.created" for every other
			// caller. Calling the body alone silently skipped it: the plan
			// visibly grew a task and "What Changed" reported nothing.
			if err := appendEvent(tx, Event{
				ID:            id.New(),
				AggregateType: "task",
				AggregateID:   nt.ID,
				Actor:         ActorOrchestrator,
				EventType:     "task.created",
				CausationID:   originalID,
				PayloadDigest: "-",
			}); err != nil {
				return err
			}
			created = append(created, nt)
		}

		newIDs := make([]string, len(created))
		for i, nt := range created {
			newIDs[i] = nt.ID
		}
		processed := map[string]bool{}
		for _, e := range dependents {
			if e.EdgeType != domain.EdgeDependsOn || e.FromID == originalID || processed[e.FromID] {
				continue
			}
			processed[e.FromID] = true
			edges := make([]EdgeToWrite, 0, len(newIDs))
			for _, newID := range newIDs {
				edges = append(edges, EdgeToWrite{FromType: "task", FromID: e.FromID, EdgeType: domain.EdgeDependsOn, ToType: "task", ToID: newID})
			}
			if err := db.wireEdges(tx, initiativeID, edges); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND (from_id = ? OR to_id = ?)`,
			initiativeID, originalID, originalID); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM tasks WHERE id = ? AND initiative_id = ?`, originalID, initiativeID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "task %q not found", originalID)
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}
