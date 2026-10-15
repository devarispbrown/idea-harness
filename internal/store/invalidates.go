package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// The invalidates edge: assertion --invalidates--> requirement|decision.
//
// A retracted assertion is a refuted claim. Every requirement or decision that
// rests on it (its PRD `based_on` citation or a requirement's source
// assertion: node --derived_from|depends_on--> assertion) no longer holds as
// written, and the graph says so with one invalidates edge per resting node,
// pointing from the refuted claim to what it undercuts. That is the direction
// gap analysis reads ("a node invalidates the decision": basis_refuted), and
// the target is what `idea impact` on the assertion classes `obsolete`.
//
// The set is derived, not asserted: syncInvalidatesTx reconciles it with the
// assertions' statuses and the basis edges, so it is idempotent and follows a
// judgment both ways. Retracting writes the edges; accepting or disputing the
// same assertion afterwards, or a revision dropping the citation, removes
// them. A node that stops being live keeps its edge (it is history, and every
// reader already skips dead nodes); a dead node never gains one.

type invalidation struct{ fromID, toType, toID string }

// syncInvalidatesTx reconciles the initiative's invalidates edges with its
// retracted assertions inside tx, and appends one assertion.invalidates_set
// event per assertion whose set changed (Operation lists the nodes it now
// invalidates). A call that changes nothing writes nothing.
func (db *DB) syncInvalidatesTx(tx *sql.Tx, initiativeID, actor string) error {
	// want: a live retracted assertion and the live nodes resting on it.
	want := map[invalidation]bool{}
	// keep: the same pairs, dead nodes included -- an existing edge to a node
	// since superseded stays.
	keep := map[invalidation]bool{}
	rows, err := tx.Query(`
		SELECT e.to_id, e.from_type, e.from_id,
			CASE e.from_type
				WHEN 'requirement' THEN COALESCE((SELECT r.status != ? AND r.superseded_by = '' FROM requirements r WHERE r.id = e.from_id), 0)
				ELSE COALESCE((SELECT d.status != ? AND d.superseded_by = '' FROM decisions d WHERE d.id = e.from_id), 0)
			END
		FROM graph_edges e JOIN assertions a ON a.id = e.to_id
		WHERE e.initiative_id = ? AND e.to_type = 'assertion'
			AND e.edge_type IN (?, ?) AND e.from_type IN ('requirement', 'decision')
			AND a.status = ? AND a.superseded_by = ''`,
		string(domain.RequirementRemoved), string(domain.DecisionSuperseded), initiativeID,
		string(domain.EdgeDerivedFrom), string(domain.EdgeDependsOn), string(domain.AssertionRetracted))
	if err != nil {
		return err
	}
	for rows.Next() {
		var inv invalidation
		var live bool
		if err := rows.Scan(&inv.fromID, &inv.toType, &inv.toID, &live); err != nil {
			rows.Close()
			return err
		}
		keep[inv] = true
		if live {
			want[inv] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	have := map[invalidation]string{} // -> edge id
	rows, err = tx.Query(`
		SELECT id, from_id, to_type, to_id FROM graph_edges
		WHERE initiative_id = ? AND from_type = 'assertion' AND edge_type = ?`,
		initiativeID, string(domain.EdgeInvalidates))
	if err != nil {
		return err
	}
	for rows.Next() {
		var edgeID string
		var inv invalidation
		if err := rows.Scan(&edgeID, &inv.fromID, &inv.toType, &inv.toID); err != nil {
			rows.Close()
			return err
		}
		if _, dup := have[inv]; dup {
			// graph_edges has no uniqueness constraint; a duplicate is
			// dropped so the set stays a set.
			if _, err := tx.Exec(`DELETE FROM graph_edges WHERE id = ?`, edgeID); err != nil {
				rows.Close()
				return err
			}
			continue
		}
		have[inv] = edgeID
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	changed := map[string]bool{}
	for inv, edgeID := range have {
		if keep[inv] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE id = ?`, edgeID); err != nil {
			return err
		}
		changed[inv.fromID] = true
	}
	var add []EdgeToWrite
	for inv := range want {
		if _, ok := have[inv]; ok {
			continue
		}
		add = append(add, EdgeToWrite{FromType: "assertion", FromID: inv.fromID, EdgeType: domain.EdgeInvalidates, ToType: inv.toType, ToID: inv.toID})
		changed[inv.fromID] = true
	}
	// Deterministic edge order (created_at ties are broken by insertion).
	sort.Slice(add, func(i, j int) bool {
		if add[i].FromID != add[j].FromID {
			return add[i].FromID < add[j].FromID
		}
		return add[i].ToType+add[i].ToID < add[j].ToType+add[j].ToID
	})
	if err := db.wireEdges(tx, initiativeID, add); err != nil {
		return err
	}

	aids := make([]string, 0, len(changed))
	for aid := range changed {
		aids = append(aids, aid)
	}
	sort.Strings(aids)
	for _, aid := range aids {
		var targets []string
		for inv := range keep {
			if inv.fromID == aid {
				targets = append(targets, inv.toType+":"+inv.toID)
			}
		}
		sort.Strings(targets)
		if err := appendEvent(tx, Event{
			ID:            id.New(),
			AggregateType: "assertion",
			AggregateID:   aid,
			Actor:         actor,
			EventType:     "assertion.invalidates_set",
			Operation:     strings.Join(targets, ","),
			PayloadDigest: "-",
		}); err != nil {
			return err
		}
	}
	return nil
}

// SyncAssertionInvalidations reconciles the initiative's invalidates edges
// (see syncInvalidatesTx). SetAssertionStatus and SetAssertionBasis already
// do this in their own transaction; the PRD stage calls it once more after
// recording a cycle's basis, because a new requirement whose basis matched
// an earlier one writes no basis edge of its own and so never passed through
// SetAssertionBasis.
func (db *DB) SyncAssertionInvalidations(ctx context.Context, initiativeID string) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		return db.syncInvalidatesTx(tx, initiativeID, ActorOrchestrator)
	})
}
