package store

import (
	"context"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ArtifactView is an artifact version joined with its family kind.
type ArtifactView struct {
	VersionID     string             `json:"id"`
	ArtifactID    string             `json:"artifact_id"`
	Kind          domain.ArtifactKind `json:"kind"`
	Version       int                `json:"version"`
	Status        string             `json:"status"`
	BodyDigest    string             `json:"body_digest"`
	ProducedByRun string             `json:"produced_by_run"`
	CreatedAt     time.Time          `json:"created_at"`
}

// ListArtifacts returns artifact versions (with kind) for an initiative.
func (db *DB) ListArtifacts(ctx context.Context, initiativeID string) ([]ArtifactView, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT av.id, av.artifact_id, a.kind, av.version, av.status, av.body_digest, av.produced_by_run_id, av.created_at
		FROM artifact_versions av JOIN artifacts a ON a.id = av.artifact_id
		WHERE a.initiative_id = ? ORDER BY av.created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ArtifactView, 0)
	for rows.Next() {
		var v ArtifactView
		var created int64
		if err := rows.Scan(&v.VersionID, &v.ArtifactID, &v.Kind, &v.Version, &v.Status, &v.BodyDigest, &v.ProducedByRun, &created); err != nil {
			return nil, err
		}
		v.CreatedAt = time.UnixMilli(created)
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetArtifactVersion returns one artifact version by its version id.
func (db *DB) GetArtifactVersion(ctx context.Context, versionID string) (ArtifactView, error) {
	var v ArtifactView
	var created int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT av.id, av.artifact_id, a.kind, av.version, av.status, av.body_digest, av.produced_by_run_id, av.created_at
		FROM artifact_versions av JOIN artifacts a ON a.id = av.artifact_id
		WHERE av.id = ?`, versionID).
		Scan(&v.VersionID, &v.ArtifactID, &v.Kind, &v.Version, &v.Status, &v.BodyDigest, &v.ProducedByRun, &created)
	if err != nil {
		return v, err
	}
	v.CreatedAt = time.UnixMilli(created)
	return v, nil
}
