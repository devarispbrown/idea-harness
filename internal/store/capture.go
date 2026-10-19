package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/devarispbrown/idea-harness/internal/id"
)

// CaptureToken is one channel's capability to create draft initiatives in a
// project (migration 53). Only metadata is ever read back; the token itself is
// SHA-256-hashed and never listed.
type CaptureToken struct {
	ID         string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// ErrCaptureTokenUnknown is returned for an unknown or wrong capture token — one
// error, so the unauthenticated capture route says nothing about which tokens
// exist (the same single-sourcing as push renewal).
var ErrCaptureTokenUnknown = errors.New("capture token unknown")

// CreateCaptureToken stores a token's hash for a project and returns the row id.
func (db *DB) CreateCaptureToken(ctx context.Context, projectID, tokenHash, label string) (string, error) {
	tokID := id.New()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO capture_tokens (id, project_id, token_hash, label, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		tokID, projectID, tokenHash, label, time.Now().UTC().UnixMilli())
	if err != nil {
		return "", err
	}
	return tokID, nil
}

// LookupCaptureToken returns the project a token authorizes, or
// ErrCaptureTokenUnknown. It also records the use, best-effort.
func (db *DB) LookupCaptureToken(ctx context.Context, tokenHash string) (string, error) {
	var projectID string
	err := db.sql.QueryRowContext(ctx,
		`SELECT project_id FROM capture_tokens WHERE token_hash = ?`, tokenHash).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCaptureTokenUnknown
	}
	if err != nil {
		return "", err
	}
	_, _ = db.sql.ExecContext(ctx, `UPDATE capture_tokens SET last_used_at = ? WHERE token_hash = ?`,
		time.Now().UTC().UnixMilli(), tokenHash)
	return projectID, nil
}

// ListCaptureTokens returns metadata for a project's tokens, never the hashes.
func (db *DB) ListCaptureTokens(ctx context.Context, projectID string) ([]CaptureToken, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, label, created_at, last_used_at FROM capture_tokens
		WHERE project_id = ? ORDER BY created_at ASC, id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaptureToken{}
	for rows.Next() {
		var t CaptureToken
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Label, &created, &last); err != nil {
			return nil, err
		}
		t.CreatedAt = time.UnixMilli(created).UTC()
		if last.Valid {
			lu := time.UnixMilli(last.Int64).UTC()
			t.LastUsedAt = &lu
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
