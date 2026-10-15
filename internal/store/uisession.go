package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UISession is one browser session of the web UI, signed in remotely with a
// pairing code (global.db, migration 2). The session secret itself is never
// stored -- only its hash -- so this record can be listed without care.
type UISession struct {
	ID         string     `json:"id"`
	Label      string     `json:"label,omitempty"`
	UserAgent  string     `json:"user_agent,omitempty"`
	RemoteAddr string     `json:"remote_addr,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// ErrPairingCodeInvalid is returned when a pairing code is unknown, expired
// or already used. One error for all three on purpose: telling a guesser
// which of them it hit is telling it something.
var ErrPairingCodeInvalid = errors.New("pairing code is invalid, expired or already used")

// CreatePairingCode stores the hash of a new one-time pairing code.
func (db *DB) CreatePairingCode(ctx context.Context, codeHash, label string, now, expires time.Time) error {
	// Expired codes are dead weight with no audit value; drop them here
	// rather than on a timer.
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM ui_pairing_codes WHERE expires_at < ?`, now.UnixMilli()); err != nil {
		return err
	}
	_, err := db.SQL().ExecContext(ctx,
		`INSERT INTO ui_pairing_codes (code_hash, label, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		codeHash, label, now.UnixMilli(), expires.UnixMilli())
	return err
}

// RedeemPairingCode consumes a pairing code and creates the session it pays
// for, in one transaction: a code can be spent once, and a crash between
// "spent" and "session exists" must not leave a used code with nothing to
// show for it.
func (db *DB) RedeemPairingCode(ctx context.Context, codeHash string, s UISession, secretHash string, now time.Time) (UISession, error) {
	tx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return UISession{}, err
	}
	defer tx.Rollback()
	var label string
	err = tx.QueryRowContext(ctx,
		`SELECT label FROM ui_pairing_codes WHERE code_hash = ? AND used_at IS NULL AND expires_at >= ?`,
		codeHash, now.UnixMilli()).Scan(&label)
	if errors.Is(err, sql.ErrNoRows) {
		return UISession{}, ErrPairingCodeInvalid
	}
	if err != nil {
		return UISession{}, err
	}
	// The conditional UPDATE is the real claim: two logins racing with the
	// same code both pass the SELECT, only one changes a row.
	res, err := tx.ExecContext(ctx, `UPDATE ui_pairing_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`, now.UnixMilli(), codeHash)
	if err != nil {
		return UISession{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return UISession{}, ErrPairingCodeInvalid
	}
	if s.Label == "" {
		s.Label = label
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ui_sessions WHERE expires_at < ?`, now.UnixMilli()); err != nil {
		return UISession{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ui_sessions (id, secret_hash, label, user_agent, remote_addr, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, secretHash, s.Label, s.UserAgent, s.RemoteAddr, s.CreatedAt.UnixMilli(), s.LastSeenAt.UnixMilli(), s.ExpiresAt.UnixMilli()); err != nil {
		return UISession{}, err
	}
	return s, tx.Commit()
}

// InvalidatePairingCodes expires every unused pairing code. The login rate
// limiter calls it when the failure budget is spent: someone is guessing, and
// the codes they were guessing at are the thing to take away.
func (db *DB) InvalidatePairingCodes(ctx context.Context, now time.Time) (int64, error) {
	res, err := db.SQL().ExecContext(ctx, `UPDATE ui_pairing_codes SET expires_at = ? WHERE used_at IS NULL AND expires_at >= ?`, now.UnixMilli()-1, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LookupUISession returns the live (unrevoked, unexpired) session whose
// secret hashes to secretHash, or ok=false.
func (db *DB) LookupUISession(ctx context.Context, secretHash string, now time.Time) (UISession, bool, error) {
	row := db.SQL().QueryRowContext(ctx, `
		SELECT id, label, user_agent, remote_addr, created_at, last_seen_at, expires_at, revoked_at
		FROM ui_sessions WHERE secret_hash = ? AND revoked_at IS NULL AND expires_at >= ?`, secretHash, now.UnixMilli())
	s, err := scanUISession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return UISession{}, false, nil
	}
	if err != nil {
		return UISession{}, false, err
	}
	return s, true, nil
}

// TouchUISession records activity on a session.
func (db *DB) TouchUISession(ctx context.Context, id string, at time.Time) error {
	_, err := db.SQL().ExecContext(ctx, `UPDATE ui_sessions SET last_seen_at = ? WHERE id = ?`, at.UnixMilli(), id)
	return err
}

// ListUISessions returns every session not yet pruned, newest first.
func (db *DB) ListUISessions(ctx context.Context) ([]UISession, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, label, user_agent, remote_addr, created_at, last_seen_at, expires_at, revoked_at
		FROM ui_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UISession{}
	for rows.Next() {
		s, err := scanUISession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeUISession revokes one session; ok=false when no live session has id.
func (db *DB) RevokeUISession(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := db.SQL().ExecContext(ctx, `UPDATE ui_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, at.UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RevokeAllUISessions revokes every live session and returns how many.
func (db *DB) RevokeAllUISessions(ctx context.Context, at time.Time) (int64, error) {
	res, err := db.SQL().ExecContext(ctx, `UPDATE ui_sessions SET revoked_at = ? WHERE revoked_at IS NULL`, at.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type rowScanner interface{ Scan(...any) error }

func scanUISession(r rowScanner) (UISession, error) {
	var s UISession
	var created, seen, expires int64
	var revoked sql.NullInt64
	if err := r.Scan(&s.ID, &s.Label, &s.UserAgent, &s.RemoteAddr, &created, &seen, &expires, &revoked); err != nil {
		return UISession{}, err
	}
	s.CreatedAt = time.UnixMilli(created).UTC()
	s.LastSeenAt = time.UnixMilli(seen).UTC()
	s.ExpiresAt = time.UnixMilli(expires).UTC()
	if revoked.Valid {
		t := time.UnixMilli(revoked.Int64).UTC()
		s.RevokedAt = &t
	}
	return s, nil
}
