package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// Objectives & outcomes (see domain/outcomes.go for the model, and the
// migration that creates these tables for why the links are graph edges).
//
// Every write here is one store.Apply: the row and the edges that make it
// traceable land in the same transaction as the event that records them, the
// same rule CreateRequirement and CreateTask follow (§26).

// ---------- Objective ----------

// CreateObjective inserts an objective with an initiative-scoped code (OBJ-n).
func (db *DB) CreateObjective(ctx context.Context, o *domain.Objective, actor string) error {
	if strings.TrimSpace(o.Statement) == "" {
		return fmt.Errorf("an objective needs a statement")
	}
	if o.ID == "" {
		o.ID = id.New()
	}
	if o.Status == "" {
		o.Status = domain.ObjectiveActive
	}
	if !o.Status.Valid() {
		return fmt.Errorf("invalid objective status %q (use active, achieved or abandoned)", o.Status)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	now := time.Now().UTC()
	o.CreatedAt, o.UpdatedAt = now, now
	o.RequirementIDs, o.MetricIDs = []string{}, []string{}
	return db.Apply(ctx, Event{
		AggregateType: "objective", AggregateID: o.ID, Actor: actor, EventType: "objective.created",
	}, func(tx *sql.Tx) error {
		if o.Code == "" {
			code, err := nextCode(tx, "objectives", "code", "OBJ", o.InitiativeID)
			if err != nil {
				return err
			}
			o.Code = code
		}
		if _, err := tx.Exec(`
			INSERT INTO objectives (id, initiative_id, code, statement, rationale, status, owner, produced_by_run_id, superseded_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
			o.ID, o.InitiativeID, o.Code, o.Statement, o.Rationale, o.Status, o.Owner, o.ProducedByRun,
			now.UnixMilli(), now.UnixMilli()); err != nil {
			return err
		}
		if o.ProducedByRun == "" {
			return nil
		}
		return db.wireEdges(tx, o.InitiativeID, []EdgeToWrite{
			{FromType: "objective", FromID: o.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: o.ProducedByRun},
		})
	})
}

// UpdateObjective rewrites an objective's statement, rationale, status and
// owner in place -- same id, same code, every edge kept. A non-empty
// ProducedByRun adds a produced_by edge for the revising run, as
// UpdateRequirement does, so every run that shaped it stays in the graph.
func (db *DB) UpdateObjective(ctx context.Context, o *domain.Objective, actor string) error {
	if strings.TrimSpace(o.Statement) == "" {
		return fmt.Errorf("an objective needs a statement")
	}
	if !o.Status.Valid() {
		return fmt.Errorf("invalid objective status %q (use active, achieved or abandoned)", o.Status)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	o.UpdatedAt = time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "objective", AggregateID: o.ID, Actor: actor, EventType: "objective.updated",
		Operation: string(o.Status),
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE objectives SET statement = ?, rationale = ?, status = ?, owner = ?, updated_at = ? WHERE id = ?`,
			o.Statement, o.Rationale, o.Status, o.Owner, o.UpdatedAt.UnixMilli(), o.ID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res, "objective %q not found", o.ID); err != nil {
			return notFound("objective", o.ID)
		}
		if o.ProducedByRun == "" {
			return nil
		}
		if _, err := tx.Exec(`UPDATE objectives SET produced_by_run_id = ? WHERE id = ?`, o.ProducedByRun, o.ID); err != nil {
			return err
		}
		return db.wireEdges(tx, o.InitiativeID, []EdgeToWrite{
			{FromType: "objective", FromID: o.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: o.ProducedByRun},
		})
	})
}

// SupersedeModelObjectivesExcept marks every live, MODEL-authored objective of
// an initiative superseded except those in keepIDs, along with their
// model-authored metrics that have never been observed. Used by a PRD
// revision that dropped an objective.
//
// A human-added objective (produced_by_run_id = '') is never superseded by a
// model revision -- it is the human's to change -- and neither is a metric
// with observations: its history is evidence, and a revision that stopped
// mentioning it does not make the measurements untrue.
func (db *DB) SupersedeModelObjectivesExcept(ctx context.Context, initiativeID, supersededBy string, keepIDs []string) error {
	return db.Apply(ctx, Event{
		AggregateType: "objective", AggregateID: initiativeID, Actor: ActorOrchestrator, EventType: "objectives.superseded",
	}, func(tx *sql.Tx) error {
		args := []any{supersededBy, time.Now().UTC().UnixMilli(), initiativeID}
		q := `UPDATE objectives SET superseded_by = ?, updated_at = ? WHERE initiative_id = ? AND superseded_by = '' AND produced_by_run_id != ''`
		if len(keepIDs) > 0 {
			q += ` AND id NOT IN (` + placeholders(len(keepIDs)) + `)`
			for _, k := range keepIDs {
				args = append(args, k)
			}
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
		_, err := tx.Exec(`
			UPDATE outcome_metrics SET superseded_by = ?, updated_at = ?
			WHERE initiative_id = ? AND superseded_by = '' AND produced_by_run_id != ''
			  AND objective_id IN (SELECT id FROM objectives WHERE initiative_id = ? AND superseded_by != '')
			  AND NOT EXISTS (SELECT 1 FROM metric_observations o WHERE o.metric_id = outcome_metrics.id)`,
			supersededBy, time.Now().UTC().UnixMilli(), initiativeID, initiativeID)
		return err
	})
}

// ListObjectives returns an initiative's objectives, superseded ones
// included (callers filter with Objective.Active), with RequirementIDs and
// MetricIDs attached.
func (db *DB) ListObjectives(ctx context.Context, initiativeID string) ([]domain.Objective, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, code, statement, rationale, status, owner, produced_by_run_id, superseded_by, created_at, updated_at
		FROM objectives WHERE initiative_id = ? ORDER BY created_at ASC, code ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Objective, 0)
	for rows.Next() {
		var o domain.Objective
		var created, updated int64
		if err := rows.Scan(&o.ID, &o.InitiativeID, &o.Code, &o.Statement, &o.Rationale, &o.Status, &o.Owner,
			&o.ProducedByRun, &o.SupersededBy, &created, &updated); err != nil {
			return nil, err
		}
		o.CreatedAt, o.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
		o.RequirementIDs, o.MetricIDs = []string{}, []string{}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	served, err := db.servingRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	metrics, err := db.listMetricRows(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if ids := served[out[i].ID]; len(ids) > 0 {
			out[i].RequirementIDs = ids
		}
		for _, m := range metrics {
			if m.ObjectiveID == out[i].ID && m.SupersededBy == "" {
				out[i].MetricIDs = append(out[i].MetricIDs, m.ID)
			}
		}
	}
	return out, nil
}

// GetObjectiveByRef resolves an objective by id or code (OBJ-n, any case)
// within an initiative.
func (db *DB) GetObjectiveByRef(ctx context.Context, initiativeID, ref string) (*domain.Objective, error) {
	objs, err := db.ListObjectives(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for i := range objs {
		if objs[i].ID == ref || strings.EqualFold(objs[i].Code, strings.TrimSpace(ref)) {
			return &objs[i], nil
		}
	}
	return nil, notFound("objective", ref)
}

// servingRequirements maps objective id -> the ACTIVE requirement ids with a
// serves edge to it.
func (db *DB) servingRequirements(ctx context.Context, initiativeID string) (map[string][]string, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT e.to_id, e.from_id FROM graph_edges e
		JOIN requirements r ON r.id = e.from_id
		WHERE e.initiative_id = ? AND e.edge_type = ? AND e.from_type = 'requirement' AND e.to_type = 'objective'
		  AND r.status != ? AND r.superseded_by = ''
		ORDER BY e.created_at ASC`, initiativeID, string(domain.EdgeServes), domain.RequirementRemoved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	seen := map[string]bool{}
	for rows.Next() {
		var objID, reqID string
		if err := rows.Scan(&objID, &reqID); err != nil {
			return nil, err
		}
		if k := objID + "\x00" + reqID; !seen[k] {
			seen[k] = true
			out[objID] = append(out[objID], reqID)
		}
	}
	return out, rows.Err()
}

// requirementObjectives maps requirement id -> the live objective ids it
// serves. Attached to every requirement ListRequirements returns.
func (db *DB) requirementObjectives(ctx context.Context, initiativeID string) (map[string][]string, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT e.from_id, e.to_id FROM graph_edges e
		JOIN objectives o ON o.id = e.to_id
		WHERE e.initiative_id = ? AND e.edge_type = ? AND e.from_type = 'requirement' AND e.to_type = 'objective'
		  AND o.superseded_by = ''
		ORDER BY o.code ASC`, initiativeID, string(domain.EdgeServes))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	seen := map[string]bool{}
	for rows.Next() {
		var reqID, objID string
		if err := rows.Scan(&reqID, &objID); err != nil {
			return nil, err
		}
		if k := reqID + "\x00" + objID; !seen[k] {
			seen[k] = true
			out[reqID] = append(out[reqID], objID)
		}
	}
	return out, rows.Err()
}

// LinkRequirementObjective records that a requirement serves an objective
// (requirement --serves--> objective). Idempotent: linking twice is one edge.
func (db *DB) LinkRequirementObjective(ctx context.Context, initiativeID, requirementID, objectiveID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement", AggregateID: requirementID, Actor: actor, EventType: "requirement.objective_linked",
		Operation: objectiveID,
	}, func(tx *sql.Tx) error {
		if err := requireOwned(tx, "requirements", initiativeID, requirementID, "requirement"); err != nil {
			return err
		}
		if err := requireOwned(tx, "objectives", initiativeID, objectiveID, "objective"); err != nil {
			return err
		}
		return linkServesTx(db, tx, initiativeID, requirementID, objectiveID)
	})
}

// UnlinkRequirementObjective removes a requirement --serves--> objective edge.
func (db *DB) UnlinkRequirementObjective(ctx context.Context, initiativeID, requirementID, objectiveID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement", AggregateID: requirementID, Actor: actor, EventType: "requirement.objective_unlinked",
		Operation: objectiveID,
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND from_type = 'requirement' AND from_id = ?
			AND edge_type = ? AND to_type = 'objective' AND to_id = ?`,
			initiativeID, requirementID, string(domain.EdgeServes), objectiveID)
		return err
	})
}

// ReplaceRequirementObjectives sets exactly which objectives a requirement
// serves. Used by a PRD revision that named the requirement's objectives; an
// empty list is refused rather than read as "unlink everything", because a
// revision that simply did not say must not erase a human's links.
func (db *DB) ReplaceRequirementObjectives(ctx context.Context, initiativeID, requirementID string, objectiveIDs []string) error {
	if len(objectiveIDs) == 0 {
		return fmt.Errorf("store: ReplaceRequirementObjectives needs at least one objective")
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement", AggregateID: requirementID, Actor: ActorOrchestrator, EventType: "requirement.objectives_replaced",
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND from_type = 'requirement' AND from_id = ?
			AND edge_type = ? AND to_type = 'objective'`, initiativeID, requirementID, string(domain.EdgeServes)); err != nil {
			return err
		}
		for _, oid := range objectiveIDs {
			if err := linkServesTx(db, tx, initiativeID, requirementID, oid); err != nil {
				return err
			}
		}
		return nil
	})
}

func linkServesTx(db *DB, tx *sql.Tx, initiativeID, requirementID, objectiveID string) error {
	return wireEdgeOnceTx(db, tx, initiativeID, EdgeToWrite{
		FromType: "requirement", FromID: requirementID, EdgeType: domain.EdgeServes, ToType: "objective", ToID: objectiveID,
	})
}

// wireEdgeOnceTx writes e unless an identical edge already exists.
func wireEdgeOnceTx(db *DB, tx *sql.Tx, initiativeID string, e EdgeToWrite) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE initiative_id = ? AND from_type = ? AND from_id = ?
		AND edge_type = ? AND to_type = ? AND to_id = ?`,
		initiativeID, e.FromType, e.FromID, string(e.EdgeType), e.ToType, e.ToID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return db.wireEdges(tx, initiativeID, []EdgeToWrite{e})
}

// requireOwned refuses an id that does not name a row of table in this
// initiative -- a link across initiatives would put an edge in one
// initiative's graph pointing at a node no traversal of it can label.
func requireOwned(tx *sql.Tx, table, initiativeID, rowID, kind string) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ? AND initiative_id = ?`, rowID, initiativeID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return notFound(kind, rowID)
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// ---------- OutcomeMetric ----------

// ValidateMetric reports why m cannot be stored, or nil.
func ValidateMetric(m *domain.OutcomeMetric) error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("a metric needs a name")
	}
	if !m.Direction.Valid() {
		return fmt.Errorf("invalid direction %q (use increase, decrease or maintain)", m.Direction)
	}
	if m.Source.Kind == "" {
		m.Source.Kind = domain.MetricSourceManual
	}
	if err := m.Source.Validate(); err != nil {
		return err
	}
	if _, err := domain.ParseCadence(m.Cadence); err != nil {
		return err
	}
	if m.Guardrail != nil && m.Direction == domain.DirectionMaintain && *m.Guardrail < 0 {
		return fmt.Errorf("a maintain metric's guardrail is the allowed distance from the target and cannot be negative")
	}
	return nil
}

// CreateMetric inserts an outcome metric (MET-n) for an objective and wires
// metric --measures--> objective.
func (db *DB) CreateMetric(ctx context.Context, m *domain.OutcomeMetric, actor string) error {
	if err := ValidateMetric(m); err != nil {
		return err
	}
	if m.ID == "" {
		m.ID = id.New()
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	return db.Apply(ctx, Event{
		AggregateType: "metric", AggregateID: m.ID, Actor: actor, EventType: "metric.created",
	}, func(tx *sql.Tx) error {
		if err := requireOwned(tx, "objectives", m.InitiativeID, m.ObjectiveID, "objective"); err != nil {
			return err
		}
		if m.Code == "" {
			code, err := nextCode(tx, "outcome_metrics", "code", "MET", m.InitiativeID)
			if err != nil {
				return err
			}
			m.Code = code
		}
		if _, err := tx.Exec(`
			INSERT INTO outcome_metrics (id, initiative_id, objective_id, code, name, definition, unit, direction, baseline, target, guardrail,
				source_kind, source_command, source_url, source_pointer, cadence, produced_by_run_id, superseded_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
			m.ID, m.InitiativeID, m.ObjectiveID, m.Code, m.Name, m.Definition, m.Unit, m.Direction,
			nullFloat(m.Baseline), m.Target, nullFloat(m.Guardrail),
			m.Source.Kind, m.Source.Command, m.Source.URL, m.Source.Pointer, m.Cadence, m.ProducedByRun,
			now.UnixMilli(), now.UnixMilli()); err != nil {
			return err
		}
		edges := []EdgeToWrite{{FromType: "metric", FromID: m.ID, EdgeType: domain.EdgeMeasures, ToType: "objective", ToID: m.ObjectiveID}}
		if m.ProducedByRun != "" {
			edges = append(edges, EdgeToWrite{FromType: "metric", FromID: m.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: m.ProducedByRun})
		}
		return db.wireEdges(tx, m.InitiativeID, edges)
	})
}

// UpdateMetric rewrites a metric's definition in place. Moving it to another
// objective re-points its measures edge. Its observations are untouched: a
// changed target re-evaluates the same history against the new target.
func (db *DB) UpdateMetric(ctx context.Context, m *domain.OutcomeMetric, actor string) error {
	if err := ValidateMetric(m); err != nil {
		return err
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	m.UpdatedAt = time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "metric", AggregateID: m.ID, Actor: actor, EventType: "metric.updated",
	}, func(tx *sql.Tx) error {
		if err := requireOwned(tx, "objectives", m.InitiativeID, m.ObjectiveID, "objective"); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE outcome_metrics SET objective_id = ?, name = ?, definition = ?, unit = ?, direction = ?, baseline = ?, target = ?, guardrail = ?,
				source_kind = ?, source_command = ?, source_url = ?, source_pointer = ?, cadence = ?, updated_at = ?
			WHERE id = ? AND initiative_id = ?`,
			m.ObjectiveID, m.Name, m.Definition, m.Unit, m.Direction, nullFloat(m.Baseline), m.Target, nullFloat(m.Guardrail),
			m.Source.Kind, m.Source.Command, m.Source.URL, m.Source.Pointer, m.Cadence, m.UpdatedAt.UnixMilli(),
			m.ID, m.InitiativeID)
		if err != nil {
			return err
		}
		if requireOneRow(res, "metric %q not found", m.ID) != nil {
			return notFound("metric", m.ID)
		}
		if _, err := tx.Exec(`DELETE FROM graph_edges WHERE initiative_id = ? AND from_type = 'metric' AND from_id = ? AND edge_type = ?`,
			m.InitiativeID, m.ID, string(domain.EdgeMeasures)); err != nil {
			return err
		}
		edges := []EdgeToWrite{{FromType: "metric", FromID: m.ID, EdgeType: domain.EdgeMeasures, ToType: "objective", ToID: m.ObjectiveID}}
		if m.ProducedByRun != "" {
			if _, err := tx.Exec(`UPDATE outcome_metrics SET produced_by_run_id = ? WHERE id = ?`, m.ProducedByRun, m.ID); err != nil {
				return err
			}
			edges = append(edges, EdgeToWrite{FromType: "metric", FromID: m.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: m.ProducedByRun})
		}
		return db.wireEdges(tx, m.InitiativeID, edges)
	})
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// listMetricRows returns every metric of an initiative (superseded included),
// without drift status.
func (db *DB) listMetricRows(ctx context.Context, initiativeID string) ([]domain.OutcomeMetric, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT m.id, m.initiative_id, m.objective_id, m.code, m.name, m.definition, m.unit, m.direction, m.baseline, m.target, m.guardrail,
			m.source_kind, m.source_command, m.source_url, m.source_pointer, m.cadence, m.produced_by_run_id, m.superseded_by,
			m.created_at, m.updated_at, COALESCE(o.code, '')
		FROM outcome_metrics m LEFT JOIN objectives o ON o.id = m.objective_id
		WHERE m.initiative_id = ? ORDER BY m.created_at ASC, m.code ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.OutcomeMetric, 0)
	for rows.Next() {
		var m domain.OutcomeMetric
		var baseline, guardrail sql.NullFloat64
		var created, updated int64
		if err := rows.Scan(&m.ID, &m.InitiativeID, &m.ObjectiveID, &m.Code, &m.Name, &m.Definition, &m.Unit, &m.Direction,
			&baseline, &m.Target, &guardrail, &m.Source.Kind, &m.Source.Command, &m.Source.URL, &m.Source.Pointer,
			&m.Cadence, &m.ProducedByRun, &m.SupersededBy, &created, &updated, &m.ObjectiveCode); err != nil {
			return nil, err
		}
		if baseline.Valid {
			v := baseline.Float64
			m.Baseline = &v
		}
		if guardrail.Valid {
			v := guardrail.Float64
			m.Guardrail = &v
		}
		m.CreatedAt, m.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMetrics returns an initiative's live metrics (not superseded), each
// with its drift Status evaluated at now.
func (db *DB) ListMetrics(ctx context.Context, initiativeID string, now time.Time) ([]domain.OutcomeMetric, error) {
	all, err := db.listMetricRows(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	obs, err := db.observationsByMetric(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.OutcomeMetric, 0, len(all))
	for _, m := range all {
		if m.SupersededBy != "" {
			continue
		}
		st := domain.EvaluateMetricDrift(m, obs[m.ID], now)
		m.Status = &st
		out = append(out, m)
	}
	return out, nil
}

// GetMetricByRef resolves a live metric by id or code (MET-n, any case),
// with its drift status evaluated at now.
func (db *DB) GetMetricByRef(ctx context.Context, initiativeID, ref string, now time.Time) (*domain.OutcomeMetric, error) {
	ms, err := db.ListMetrics(ctx, initiativeID, now)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		if ms[i].ID == ref || strings.EqualFold(ms[i].Code, strings.TrimSpace(ref)) {
			return &ms[i], nil
		}
	}
	return nil, notFound("metric", ref)
}

// CreateMetricHypothesis records "we believe <statement>" as a hypothesis
// assertion predicting movement in a metric (assertion --predicts--> metric),
// in one transaction.
func (db *DB) CreateMetricHypothesis(ctx context.Context, initiativeID, metricID, statement, source, producedByRun string) (*domain.Assertion, error) {
	if strings.TrimSpace(statement) == "" {
		return nil, fmt.Errorf("a hypothesis needs a statement")
	}
	a := &domain.Assertion{
		ID: id.New(), InitiativeID: initiativeID, Type: domain.AssertionHypothesis, Status: domain.AssertionCandidate,
		Statement: statement, Confidence: domain.ConfidenceLow, Source: source, Provenance: []domain.ProvenanceRef{},
		ProducedByRun: producedByRun, CreatedAt: time.Now().UTC(),
	}
	err := db.Apply(ctx, Event{
		AggregateType: "assertion", AggregateID: a.ID, Actor: ActorOrchestrator, EventType: "assertion.created",
	}, func(tx *sql.Tx) error {
		if err := requireOwned(tx, "outcome_metrics", initiativeID, metricID, "metric"); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO assertions
				(id, initiative_id, type, status, statement, evidence_digest, confidence, source, provenance_json, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, '', ?, ?, '[]', ?, '', ?)`,
			a.ID, a.InitiativeID, a.Type, a.Status, a.Statement, a.Confidence, a.Source, a.ProducedByRun, a.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		edges := []EdgeToWrite{{FromType: "assertion", FromID: a.ID, EdgeType: domain.EdgePredicts, ToType: "metric", ToID: metricID}}
		if producedByRun != "" {
			edges = append(edges, EdgeToWrite{FromType: "assertion", FromID: a.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: producedByRun})
		}
		return db.wireEdges(tx, initiativeID, edges)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// MetricHypotheses returns the hypothesis assertions predicting a metric.
func (db *DB) MetricHypotheses(ctx context.Context, initiativeID, metricID string) ([]domain.Assertion, error) {
	edges, err := db.ListGraphEdgesTo(ctx, initiativeID, "metric", metricID)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgePredicts && e.FromType == "assertion" {
			want[e.FromID] = true
		}
	}
	all, err := db.ListAssertions(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Assertion, 0, len(want))
	for _, a := range all {
		if want[a.ID] {
			out = append(out, a)
		}
	}
	return out, nil
}

// ---------- MetricObservation ----------

// RecordObservation stores one measured value of a metric. In the same
// transaction it computes the outcome attribution -- the change sets applied
// (committed in the apply journal, still applied, and not a no-op) after the
// previous observation and at or before this one; for a metric's first
// observation, after the metric was defined -- stores it on the observation,
// and wires metric --attributed_to--> change_set for each.
//
// When the observation moves the metric's drift INTO off_track or regressed
// from anything else, a `metric.drifted` event (Operation "<from>-><to>") is
// appended alongside `metric.observed`: that is what the integration bus
// turns into an outcome.drifted notification. Staleness is a function of the
// clock, not of a write, so it is never evented -- it is surfaced on read
// (status, guidance, Health) instead.
//
// now is the evaluation time for the before/after drift comparison.
func (db *DB) RecordObservation(ctx context.Context, o *domain.MetricObservation, actor string, now time.Time) (before, after domain.MetricStatus, err error) {
	if actor == "" {
		actor = HumanActor
	}
	if o.ID == "" {
		o.ID = id.New()
	}
	if o.ObservedAt.IsZero() {
		o.ObservedAt = now.UTC()
	}
	if !o.Source.Valid() {
		return before, after, fmt.Errorf("invalid observation source %q", o.Source)
	}
	if o.Provenance == nil {
		o.Provenance = []domain.ProvenanceRef{}
	}
	o.RecordedBy = actor
	o.CreatedAt = time.Now().UTC()

	metric, err := db.GetMetricByRef(ctx, o.InitiativeID, o.MetricID, now)
	if err != nil {
		return before, after, err
	}
	o.MetricID = metric.ID
	before = *metric.Status

	err = db.InTx(ctx, func(tx *sql.Tx) error {
		// The attribution window: (previous observation, this one]. A
		// backdated observation recorded after a later one reads the window
		// from the observation immediately before it in observed_at order.
		// <=, not <: this row is not inserted yet, and an earlier reading in
		// the same millisecond is still the previous one -- with < the window
		// would reach back past it and attribute its change sets twice.
		var prevMS sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(observed_at) FROM metric_observations WHERE metric_id = ? AND observed_at <= ?`,
			metric.ID, o.ObservedAt.UnixMilli()).Scan(&prevMS); err != nil {
			return err
		}
		from := metric.CreatedAt.UnixMilli()
		if prevMS.Valid {
			from = prevMS.Int64
		}
		rows, err := tx.Query(`
			SELECT DISTINCT c.id FROM change_sets c
			JOIN apply_journal j ON j.change_set_id = c.id
			WHERE c.initiative_id = ? AND c.status = ? AND c.no_op = 0
			  AND j.state = ? AND j.applied_at > ? AND j.applied_at <= ?
			ORDER BY c.created_at ASC`,
			o.InitiativeID, domain.ChangeSetApplied, domain.ApplyCommitted, from, o.ObservedAt.UnixMilli())
		if err != nil {
			return err
		}
		o.ChangeSetIDs = []string{}
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return err
			}
			o.ChangeSetIDs = append(o.ChangeSetIDs, cid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO metric_observations (id, initiative_id, metric_id, value, observed_at, source, note, recorded_by, provenance_json, change_set_ids_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.ID, o.InitiativeID, o.MetricID, o.Value, o.ObservedAt.UnixMilli(), o.Source, o.Note, o.RecordedBy,
			mustJSON(o.Provenance), mustJSON(o.ChangeSetIDs), o.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		for _, cid := range o.ChangeSetIDs {
			if err := wireEdgeOnceTx(db, tx, o.InitiativeID, EdgeToWrite{
				FromType: "metric", FromID: o.MetricID, EdgeType: domain.EdgeAttributedTo, ToType: "change_set", ToID: cid,
			}); err != nil {
				return err
			}
		}

		obs, err := scanObservationsTx(tx, o.MetricID)
		if err != nil {
			return err
		}
		after = domain.EvaluateMetricDrift(*metric, obs, now)
		if err := appendEvent(tx, Event{
			ID: id.New(), AggregateType: "metric", AggregateID: o.MetricID, Actor: actor, EventType: "metric.observed",
			Operation: domain.FormatMetricValue(o.Value), PayloadDigest: "-",
		}); err != nil {
			return err
		}
		if (after.Drift == domain.DriftOffTrack || after.Drift == domain.DriftRegressed) && after.Drift != before.Drift {
			return appendEvent(tx, Event{
				ID: id.New(), AggregateType: "metric", AggregateID: o.MetricID, Actor: actor, EventType: "metric.drifted",
				Operation: string(before.Drift) + "->" + string(after.Drift), PayloadDigest: "-",
			})
		}
		return nil
	})
	return before, after, err
}

const observationColumns = `id, initiative_id, metric_id, value, observed_at, source, note, recorded_by, provenance_json, change_set_ids_json, created_at`

func scanObservationRows(rows *sql.Rows) ([]domain.MetricObservation, error) {
	out := make([]domain.MetricObservation, 0)
	for rows.Next() {
		var o domain.MetricObservation
		var observed, created int64
		var prov, cs string
		if err := rows.Scan(&o.ID, &o.InitiativeID, &o.MetricID, &o.Value, &observed, &o.Source, &o.Note, &o.RecordedBy,
			&prov, &cs, &created); err != nil {
			return nil, err
		}
		o.ObservedAt, o.CreatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(created).UTC()
		o.Provenance, o.ChangeSetIDs = []domain.ProvenanceRef{}, []string{}
		_ = json.Unmarshal([]byte(prov), &o.Provenance)
		_ = json.Unmarshal([]byte(cs), &o.ChangeSetIDs)
		out = append(out, o)
	}
	return out, rows.Err()
}

func scanObservationsTx(tx *sql.Tx, metricID string) ([]domain.MetricObservation, error) {
	rows, err := tx.Query(`SELECT `+observationColumns+` FROM metric_observations WHERE metric_id = ? ORDER BY observed_at ASC, created_at ASC`, metricID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservationRows(rows)
}

// ListObservations returns a metric's observations, oldest first.
func (db *DB) ListObservations(ctx context.Context, metricID string) ([]domain.MetricObservation, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+observationColumns+` FROM metric_observations WHERE metric_id = ? ORDER BY observed_at ASC, created_at ASC`, metricID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservationRows(rows)
}

func (db *DB) observationsByMetric(ctx context.Context, initiativeID string) (map[string][]domain.MetricObservation, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT `+observationColumns+` FROM metric_observations WHERE initiative_id = ? ORDER BY observed_at ASC, created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanObservationRows(rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]domain.MetricObservation{}
	for _, o := range all {
		out[o.MetricID] = append(out[o.MetricID], o)
	}
	return out, nil
}

// ---------- Gap & drift queries ----------
//
// These are the clean, read-only questions graph-gap analysis (`idea gaps`)
// and the drift surfaces ask. Each considers only LIVE nodes: an active
// objective (not superseded, status active), a non-superseded metric, and an
// active requirement (not removed, not superseded).

// ObjectivesWithoutMetrics returns the active objectives with no live metric:
// promises nobody can check.
func (db *DB) ObjectivesWithoutMetrics(ctx context.Context, initiativeID string) ([]domain.Objective, error) {
	objs, err := db.ListObjectives(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Objective, 0)
	for _, o := range objs {
		if o.Active() && len(o.MetricIDs) == 0 {
			out = append(out, o)
		}
	}
	return out, nil
}

// ObjectivesWithoutRequirements returns the active objectives no active
// requirement serves: outcomes nothing in the plan is aimed at.
func (db *DB) ObjectivesWithoutRequirements(ctx context.Context, initiativeID string) ([]domain.Objective, error) {
	objs, err := db.ListObjectives(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Objective, 0)
	for _, o := range objs {
		if o.Active() && len(o.RequirementIDs) == 0 {
			out = append(out, o)
		}
	}
	return out, nil
}

// RequirementsWithoutObjectives returns the active requirements that serve no
// live objective: work with no stated outcome.
func (db *DB) RequirementsWithoutObjectives(ctx context.Context, initiativeID string) ([]domain.Requirement, error) {
	reqs, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Requirement, 0)
	for _, r := range reqs {
		if r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
			continue
		}
		if len(r.ObjectiveIDs) == 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

// DriftingMetrics returns the live metrics whose drift at now is off_track,
// regressed or stale (MetricDrift.Drifting), each with its Status.
func (db *DB) DriftingMetrics(ctx context.Context, initiativeID string, now time.Time) ([]domain.OutcomeMetric, error) {
	ms, err := db.ListMetrics(ctx, initiativeID, now)
	if err != nil {
		return nil, err
	}
	out := make([]domain.OutcomeMetric, 0)
	for _, m := range ms {
		if m.Status != nil && m.Status.Drift.Drifting() {
			out = append(out, m)
		}
	}
	return out, nil
}

// RequirementOutcomeDrift returns the "works but fails its objective" signals:
// for every VERIFIED active requirement, each active objective it serves
// whose live metrics include one that is off_track or regressed. Stale alone
// does not count -- not knowing is not the same as failing.
func (db *DB) RequirementOutcomeDrift(ctx context.Context, initiativeID string, now time.Time) ([]domain.RequirementOutcomeDrift, error) {
	reqs, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	objs, err := db.ListObjectives(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	ms, err := db.ListMetrics(ctx, initiativeID, now)
	if err != nil {
		return nil, err
	}
	objByID := map[string]domain.Objective{}
	for _, o := range objs {
		objByID[o.ID] = o
	}
	failing := map[string][]string{} // objective id -> failing metric codes
	for _, m := range ms {
		if m.Status != nil && (m.Status.OffTrack || m.Status.Regressed) {
			failing[m.ObjectiveID] = append(failing[m.ObjectiveID], m.Code)
		}
	}
	out := make([]domain.RequirementOutcomeDrift, 0)
	for _, r := range reqs {
		if r.Status != domain.RequirementVerified || r.SupersededBy != "" {
			continue
		}
		for _, oid := range r.ObjectiveIDs {
			o, ok := objByID[oid]
			codes := failing[oid]
			if !ok || !o.Active() || len(codes) == 0 {
				continue
			}
			sort.Strings(codes)
			out = append(out, domain.RequirementOutcomeDrift{
				RequirementID: r.ID, RequirementCode: r.Code, ObjectiveID: o.ID, ObjectiveCode: o.Code,
				MetricCodes: codes, Signal: domain.SignalWorksButFailsObjective,
				Detail: fmt.Sprintf("%s is verified, but %s's %s %s off track", r.Code, o.Code, strings.Join(codes, ", "), isAre(len(codes))),
			})
		}
	}
	return out, nil
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
