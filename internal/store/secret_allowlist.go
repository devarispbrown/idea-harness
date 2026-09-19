package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
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
// denying that fingerprint+path again. The delete itself targets the entry's
// (fingerprint, path) pair rather than its id (review M1): the unique index
// added alongside this scoping means there is at most one row per pair going
// forward, but it also cleans up any pre-existing duplicate the dedup-on-
// migration step might have raced with, or that predates this fix.
func (db *DB) RevokeSecretAllowlist(ctx context.Context, id string) error {
	return db.Apply(ctx, Event{
		AggregateType: "secret_allowlist",
		AggregateID:   id,
		Actor:         HumanActor,
		EventType:     "secret_allowlist.revoked",
	}, func(tx *sql.Tx) error {
		var fingerprint, path string
		if err := tx.QueryRow(`SELECT fingerprint, path FROM secret_allowlist WHERE id = ?`, id).Scan(&fingerprint, &path); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("secret allowlist entry", id)
			}
			return err
		}
		if _, err := tx.Exec(`DELETE FROM secret_allowlist WHERE fingerprint = ? AND path = ?`, fingerprint, path); err != nil {
			return err
		}
		return nil
	})
}

// AllowlistKey is the composite key SecretAllowlistFingerprints's set uses,
// and the key a caller (e.g. workflow.filterAllowlisted) must build to look a
// match up in it: an allowlist entry (and therefore a sweep denial it can
// suppress) is scoped to fingerprint AND path together (review H3) -- a
// value allowlisted in one file must still block in another.
func AllowlistKey(fingerprint, path string) string {
	return fingerprint + "\x00" + path
}

// SecretAllowlistFingerprints returns every allowlisted (fingerprint, path)
// pair as a set keyed by allowlistKey, for the sweep to filter its own
// matches against -- one query per sweep rather than one per match.
func (db *DB) SecretAllowlistFingerprints(ctx context.Context) (map[string]bool, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT DISTINCT fingerprint, path FROM secret_allowlist`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fp, path string
		if err := rows.Scan(&fp, &path); err != nil {
			return nil, err
		}
		out[AllowlistKey(fp, path)] = true
	}
	return out, rows.Err()
}

// secretFingerprintKeyName is the project_state row key holding the
// per-project HMAC key Fingerprint uses (review H3).
const secretFingerprintKeyName = "secret_fingerprint_key"

// SecretFingerprintKey returns this project's per-project HMAC key for secret
// fingerprints, generating and persisting a random 32-byte key on first use.
// A fingerprint keyed per-project (rather than a bare sha256 of the value) is
// not the same, precomputable value across every installation that ever saw
// the same secret -- it cannot be used to correlate the same credential
// across unrelated projects or daemons.
func (db *DB) SecretFingerprintKey(ctx context.Context) ([]byte, error) {
	if key, err := db.readSecretFingerprintKey(ctx); err == nil && key != nil {
		return key, nil
	} else if err != nil {
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	hexKey := hex.EncodeToString(buf)
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO project_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		secretFingerprintKeyName, hexKey); err != nil {
		return nil, err
	}
	// Re-read rather than trusting the just-generated key: a concurrent
	// generator racing this one has its insert silently dropped by ON
	// CONFLICT DO NOTHING, and the read-back guarantees every caller ends up
	// with the SAME winning key regardless of who generated it.
	key, err := db.readSecretFingerprintKey(ctx)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, sql.ErrNoRows
	}
	return key, nil
}

func (db *DB) readSecretFingerprintKey(ctx context.Context) ([]byte, error) {
	var hexKey string
	err := db.SQL().QueryRowContext(ctx, `SELECT value FROM project_state WHERE key = ?`, secretFingerprintKeyName).Scan(&hexKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(hexKey)
}
