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

// QualityLink is one quality result's `verifies` edge target: a requirement
// id (and its code, for the result's Requirements list).
type QualityLink struct {
	RequirementID   string
	RequirementCode string
}

// RecordQualityMeasurement writes one measurement -- every measured
// dimension's result -- and each result's `verifies` edges to the
// requirements it bears on, in one transaction with one
// `quality.measured` event on the initiative. Either the whole scorecard
// lands or none of it does: a half-written measurement would leave the gate
// reading some dimensions from this run and some from the last.
//
// Results are append-only. The latest per dimension is the scorecard; the one
// before is the performance baseline.
func (db *DB) RecordQualityMeasurement(ctx context.Context, initiativeID string, results []domain.QualityResult, links map[domain.QualityDimension][]QualityLink) error {
	if len(results) == 0 {
		return nil
	}
	measurementID := results[0].MeasurementID
	if measurementID == "" {
		measurementID = id.New()
	}
	now := time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "quality.measured",
		Operation:     measurementID,
	}, func(tx *sql.Tx) error {
		for i := range results {
			r := &results[i]
			if !r.Dimension.Valid() || !r.Status.Valid() || !r.Mode.Valid() {
				return fmt.Errorf("store: invalid quality result %s/%s/%s", r.Dimension, r.Status, r.Mode)
			}
			if r.ID == "" {
				r.ID = id.New()
			}
			r.InitiativeID = initiativeID
			r.MeasurementID = measurementID
			if r.CreatedAt.IsZero() {
				r.CreatedAt = now
			}
			if r.Checks == nil {
				r.Checks = []domain.QualityCheck{}
			}
			checks, err := json.Marshal(r.Checks)
			if err != nil {
				return err
			}
			metrics := []byte("{}")
			if len(r.Metrics) > 0 {
				if metrics, err = json.Marshal(r.Metrics); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`
				INSERT INTO quality_results
					(id, initiative_id, measurement_id, dimension, mode, status, summary, checks_json, metrics_json, input_digest, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.ID, initiativeID, measurementID, r.Dimension, r.Mode, r.Status, r.Summary,
				string(checks), string(metrics), r.InputDigest, r.CreatedAt.UnixMilli()); err != nil {
				return err
			}
			r.Requirements = []string{}
			var edges []EdgeToWrite
			for _, l := range links[r.Dimension] {
				edges = append(edges, EdgeToWrite{FromType: "quality_result", FromID: r.ID, EdgeType: domain.EdgeVerifies, ToType: "requirement", ToID: l.RequirementID})
				r.Requirements = append(r.Requirements, l.RequirementCode)
			}
			if err := db.wireEdges(tx, initiativeID, edges); err != nil {
				return err
			}
		}
		return nil
	})
}

// LatestQualityResults returns the most recent result of each dimension for
// an initiative, keyed by dimension, with each result's linked requirement
// codes filled in from its `verifies` edges.
func (db *DB) LatestQualityResults(ctx context.Context, initiativeID string) (map[domain.QualityDimension]domain.QualityResult, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT q.id, q.initiative_id, q.measurement_id, q.dimension, q.mode, q.status, q.summary, q.checks_json, q.metrics_json, q.input_digest, q.created_at
		FROM quality_results q
		WHERE q.initiative_id = ? AND q.rowid = (
			SELECT q2.rowid FROM quality_results q2
			WHERE q2.initiative_id = q.initiative_id AND q2.dimension = q.dimension
			ORDER BY q2.created_at DESC, q2.rowid DESC LIMIT 1)`, initiativeID)
	if err != nil {
		return nil, err
	}
	list, err := scanQualityResults(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.QualityDimension]domain.QualityResult, len(list))
	for _, r := range list {
		if r.Requirements, err = db.qualityRequirementCodes(ctx, initiativeID, r.ID); err != nil {
			return nil, err
		}
		out[r.Dimension] = r
	}
	return out, nil
}

// ListQualityResults returns every result for an initiative, oldest first
// (the trace label cache reads it; Requirements is left empty).
func (db *DB) ListQualityResults(ctx context.Context, initiativeID string) ([]domain.QualityResult, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, measurement_id, dimension, mode, status, summary, checks_json, metrics_json, input_digest, created_at
		FROM quality_results WHERE initiative_id = ? ORDER BY created_at, rowid`, initiativeID)
	if err != nil {
		return nil, err
	}
	return scanQualityResults(rows)
}

// QualityHistory returns every result for an initiative, oldest first, with
// each result's linked requirement codes filled in -- the measurement history
// the web UI's quality page charts (GET .../quality/history). Kept apart from
// ListQualityResults because the trace label cache calls that on every
// why/impact and has no use for the per-result edge lookups this does.
func (db *DB) QualityHistory(ctx context.Context, initiativeID string) ([]domain.QualityResult, error) {
	list, err := db.ListQualityResults(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Requirements, err = db.qualityRequirementCodes(ctx, initiativeID, list[i].ID); err != nil {
			return nil, err
		}
		if list[i].Checks == nil {
			list[i].Checks = []domain.QualityCheck{}
		}
	}
	return list, nil
}

// GetQualityResult returns one result by id.
func (db *DB) GetQualityResult(ctx context.Context, resultID string) (*domain.QualityResult, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, measurement_id, dimension, mode, status, summary, checks_json, metrics_json, input_digest, created_at
		FROM quality_results WHERE id = ?`, resultID)
	if err != nil {
		return nil, err
	}
	list, err := scanQualityResults(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, notFound("quality result", resultID)
	}
	r := list[0]
	if r.Requirements, err = db.qualityRequirementCodes(ctx, r.InitiativeID, r.ID); err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) qualityRequirementCodes(ctx context.Context, initiativeID, resultID string) ([]string, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT r.code FROM graph_edges e JOIN requirements r ON r.id = e.to_id
		WHERE e.initiative_id = ? AND e.from_type = 'quality_result' AND e.from_id = ? AND e.to_type = 'requirement'
		ORDER BY r.code`, initiativeID, resultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanQualityResults(rows *sql.Rows) ([]domain.QualityResult, error) {
	defer rows.Close()
	var out []domain.QualityResult
	for rows.Next() {
		var r domain.QualityResult
		var checks, metrics string
		var created int64
		if err := rows.Scan(&r.ID, &r.InitiativeID, &r.MeasurementID, &r.Dimension, &r.Mode, &r.Status, &r.Summary,
			&checks, &metrics, &r.InputDigest, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(checks), &r.Checks); err != nil {
			return nil, fmt.Errorf("store: quality result %s checks: %w", r.ID, err)
		}
		if metrics != "" && metrics != "{}" {
			if err := json.Unmarshal([]byte(metrics), &r.Metrics); err != nil {
				return nil, fmt.Errorf("store: quality result %s metrics: %w", r.ID, err)
			}
		}
		r.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
