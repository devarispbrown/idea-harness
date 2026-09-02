package store

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ArtifactRollup is the minimal artifact info phase derivation needs.
type ArtifactRollup struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
}

// InitiativeRollup is a cross-initiative summary: the phase-derivation inputs
// (artifacts/tasks/change sets) plus the open-item counts for the attention
// inbox, gathered with a handful of grouped queries instead of one round trip
// per initiative per entity (the ProductHome N+1).
type InitiativeRollup struct {
	ID                string                  `json:"id"`
	Title             string                  `json:"title"`
	Status            domain.InitiativeStatus `json:"status"`
	Artifacts         []ArtifactRollup        `json:"artifacts"`
	TaskStatuses      []string                `json:"task_statuses"`
	ChangeSetStatuses []string                `json:"change_set_statuses"`
	OpenDecisions      int                     `json:"open_decisions"`
	OpenQuestions      int                     `json:"open_questions"`
	OpenFindings       int                     `json:"open_findings"`
	VerifiedRequirements int                   `json:"verified_requirements"`
}

// ListInitiativeRollups returns one rollup per initiative in the project DB.
func (db *DB) ListInitiativeRollups(ctx context.Context) ([]InitiativeRollup, error) {
	inis, err := db.ListInitiatives(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InitiativeRollup, 0, len(inis))
	byID := make(map[string]int, len(inis))
	for _, ini := range inis {
		out = append(out, InitiativeRollup{
			ID:                ini.ID,
			Title:             ini.Title,
			Status:            ini.Status,
			Artifacts:         []ArtifactRollup{},
			TaskStatuses:      []string{},
			ChangeSetStatuses: []string{},
		})
		byID[ini.ID] = len(out) - 1
	}

	// Artifacts (kind + version) per initiative, oldest first.
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT a.initiative_id, a.kind, av.version
		FROM artifact_versions av JOIN artifacts a ON a.id = av.artifact_id
		ORDER BY a.initiative_id, av.created_at ASC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var iniID, kind string
		var version int
		if err := rows.Scan(&iniID, &kind, &version); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := byID[iniID]; ok {
			out[i].Artifacts = append(out[i].Artifacts, ArtifactRollup{Kind: kind, Version: version})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Task statuses.
	rows, err = db.SQL().QueryContext(ctx, `SELECT initiative_id, status FROM tasks`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var iniID, status string
		if err := rows.Scan(&iniID, &status); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := byID[iniID]; ok {
			out[i].TaskStatuses = append(out[i].TaskStatuses, status)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Change-set statuses.
	rows, err = db.SQL().QueryContext(ctx, `SELECT initiative_id, status FROM change_sets`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var iniID, status string
		if err := rows.Scan(&iniID, &status); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := byID[iniID]; ok {
			out[i].ChangeSetStatuses = append(out[i].ChangeSetStatuses, status)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Open-item counts, grouped by initiative.
	if err := db.rollupCounts(ctx, byID, out, `
		SELECT initiative_id, COUNT(*) FROM decisions WHERE status = ? GROUP BY initiative_id`,
		[]any{domain.DecisionOpen}, func(i int, n int) { out[i].OpenDecisions = n }); err != nil {
		return nil, err
	}
	if err := db.rollupCounts(ctx, byID, out, `
		SELECT initiative_id, COUNT(*) FROM questions WHERE status = ? GROUP BY initiative_id`,
		[]any{domain.QuestionOpen}, func(i int, n int) { out[i].OpenQuestions = n }); err != nil {
		return nil, err
	}
	if err := db.rollupCounts(ctx, byID, out, `
		SELECT initiative_id, COUNT(*) FROM findings
		WHERE (disposition IS NULL OR disposition = '') AND severity != ? GROUP BY initiative_id`,
		[]any{domain.SeverityObservation}, func(i int, n int) { out[i].OpenFindings = n }); err != nil {
		return nil, err
	}
	if err := db.rollupCounts(ctx, byID, out, `
		SELECT initiative_id, COUNT(*) FROM requirements WHERE status = ? GROUP BY initiative_id`,
		[]any{domain.RequirementVerified}, func(i int, n int) { out[i].VerifiedRequirements = n }); err != nil {
		return nil, err
	}

	return out, nil
}

// rollupCounts runs a grouped count query and applies each count to its
// initiative's rollup. The query must select (initiative_id, count) in that
// order. apply receives the rollup index and the count.
func (db *DB) rollupCounts(ctx context.Context, byID map[string]int, out []InitiativeRollup, query string, args []any, apply func(int, int)) error {
	rows, err := db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var iniID string
		var n int
		if err := rows.Scan(&iniID, &n); err != nil {
			return err
		}
		if i, ok := byID[iniID]; ok {
			apply(i, n)
		}
	}
	return rows.Err()
}
