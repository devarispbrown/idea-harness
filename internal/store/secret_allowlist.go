package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ListSecretAllowlist returns every "not a secret" entry for this project's
// state.db, newest first, for the Settings page (review changes #1).
func (db *DB) ListSecretAllowlist(ctx context.Context) ([]domain.SecretAllowlistEntry, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, fingerprint, kind, path, actor, created_at
		FROM secret_allowlist ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.SecretAllowlistEntry, 0)
	for rows.Next() {
		var e domain.SecretAllowlistEntry
		var created int64
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.Kind, &e.Path, &e.Actor, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// RevokeSecretAllowlist removes one allowlist entry by id, so the sweep starts
// denying that fingerprint again.
func (db *DB) RevokeSecretAllowlist(ctx context.Context, id string) error {
	return db.Apply(ctx, Event{
		AggregateType: "secret_allowlist",
		AggregateID:   id,
		Actor:         HumanActor,
		EventType:     "secret_allowlist.revoked",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM secret_allowlist WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return notFound("secret allowlist entry", id)
		}
		return nil
	})
}

// SecretAllowlistFingerprints returns every allowlisted fingerprint as a set,
// for the sweep to filter its own matches against -- one query per sweep
// rather than one per match.
func (db *DB) SecretAllowlistFingerprints(ctx context.Context) (map[string]bool, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT DISTINCT fingerprint FROM secret_allowlist`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out[fp] = true
	}
	return out, rows.Err()
}
