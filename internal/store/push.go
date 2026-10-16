package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/id"
)

// PushSubscription is one browser's Web Push subscription for a project
// (migration 46). Endpoint, P256dh and Auth are what a push service needs to
// reach that browser; the API never returns them.
type PushSubscription struct {
	ID              string
	Endpoint        string
	P256dh          string
	Auth            string
	Events          []string
	Label           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastDeliveredAt *time.Time
	LastError       string
	// RenewalTokenHash is the SHA-256 (hex) of the subscription's renewal
	// token (migration 49). Write-only here: Upsert stores it when set, and
	// no read returns it.
	RenewalTokenHash string
}

// ErrPushRenewalDenied is returned by RenewPushSubscription for an unknown
// subscription or a wrong renewal token -- one error for both, so the
// unauthenticated renewal route says nothing about which ids exist.
var ErrPushRenewalDenied = errors.New("push subscription renewal refused")

// PushSubscriberName is the integration-bus subscriber name of a push
// subscription: its cursor and outbox rows are keyed by it.
func PushSubscriberName(subscriptionID string) string { return "webpush:" + subscriptionID }

// UpsertPushSubscription stores a subscription, keyed by endpoint: the same
// browser subscribing again (a new tab, a re-grant) refreshes its keys and
// events rather than creating a second row that would notify it twice.
func (db *DB) UpsertPushSubscription(ctx context.Context, s PushSubscription) (PushSubscription, bool, error) {
	now := time.Now().UTC()
	var existing string
	err := db.sql.QueryRowContext(ctx, `SELECT id FROM push_subscriptions WHERE endpoint = ?`, s.Endpoint).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.ID = id.New()
		s.CreatedAt, s.UpdatedAt = now, now
		_, err = db.sql.ExecContext(ctx, `
			INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, events, label, created_at, updated_at, renewal_token_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Endpoint, s.P256dh, s.Auth, strings.Join(s.Events, ","), s.Label, now.UnixMilli(), now.UnixMilli(), s.RenewalTokenHash)
		if err != nil {
			return PushSubscription{}, false, err
		}
		s.RenewalTokenHash = ""
		return s, true, nil
	case err != nil:
		return PushSubscription{}, false, err
	}
	// A re-subscribe hands the page a fresh renewal token, so the old one
	// (wherever it was kept) stops working.
	_, err = db.sql.ExecContext(ctx, `
		UPDATE push_subscriptions SET p256dh = ?, auth = ?, events = ?, label = ?, updated_at = ?, last_error = '',
			renewal_token_hash = CASE WHEN ? = '' THEN renewal_token_hash ELSE ? END
		WHERE id = ?`,
		s.P256dh, s.Auth, strings.Join(s.Events, ","), s.Label, now.UnixMilli(), s.RenewalTokenHash, s.RenewalTokenHash, existing)
	if err != nil {
		return PushSubscription{}, false, err
	}
	got, err := db.GetPushSubscription(ctx, existing)
	return got, false, err
}

// RenewPushSubscription re-points subscription subID at a new endpoint and
// keys -- what a browser's `pushsubscriptionchange` asks for -- provided
// tokenHash matches its stored renewal token, and rotates that token to
// newTokenHash. Events and label are kept: renewal is the same browser.
//
// If the new endpoint already has a row of its own (the page re-subscribed
// before the worker got here), that row survives with the new token and
// subID is deleted, outbox and cursor included; the survivor is returned
// either way. The token comparison is on hashes of 256-bit secrets, so a
// timing difference reveals nothing usable.
func (db *DB) RenewPushSubscription(ctx context.Context, subID, tokenHash string, s PushSubscription, newTokenHash string) (PushSubscription, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return PushSubscription{}, err
	}
	defer tx.Rollback()
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT renewal_token_hash FROM push_subscriptions WHERE id = ?`, subID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(tokenHash)) != 1)) {
		return PushSubscription{}, ErrPushRenewalDenied
	}
	if err != nil {
		return PushSubscription{}, err
	}
	now := time.Now().UTC().UnixMilli()
	survivor := subID
	var other string
	err = tx.QueryRowContext(ctx, `SELECT id FROM push_subscriptions WHERE endpoint = ? AND id <> ?`, s.Endpoint, subID).Scan(&other)
	switch {
	case err == nil:
		survivor = other
		if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, subID); err != nil {
			return PushSubscription{}, err
		}
		name := PushSubscriberName(subID)
		if _, err := tx.ExecContext(ctx, `DELETE FROM integration_outbox WHERE subscriber = ?`, name); err != nil {
			return PushSubscription{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM integration_cursors WHERE subscriber = ?`, name); err != nil {
			return PushSubscription{}, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return PushSubscription{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE push_subscriptions SET endpoint = ?, p256dh = ?, auth = ?, renewal_token_hash = ?, updated_at = ?, last_error = ''
		WHERE id = ?`, s.Endpoint, s.P256dh, s.Auth, newTokenHash, now, survivor); err != nil {
		return PushSubscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return PushSubscription{}, err
	}
	return db.GetPushSubscription(ctx, survivor)
}

// GetPushSubscription returns one subscription.
func (db *DB) GetPushSubscription(ctx context.Context, subID string) (PushSubscription, error) {
	row := db.sql.QueryRowContext(ctx, pushSelect+` WHERE id = ?`, subID)
	s, err := scanPush(row)
	if err != nil {
		return PushSubscription{}, notFoundIfNoRows(err, "push subscription", subID)
	}
	return s, nil
}

// ListPushSubscriptions returns every subscription, oldest first.
func (db *DB) ListPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := db.sql.QueryContext(ctx, pushSelect+` ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSubscription{}
	for rows.Next() {
		s, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetPushSubscriptionEvents replaces the event kinds a subscription wants.
func (db *DB) SetPushSubscriptionEvents(ctx context.Context, subID string, events []string) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE push_subscriptions SET events = ?, updated_at = ? WHERE id = ?`,
		strings.Join(events, ","), time.Now().UTC().UnixMilli(), subID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("push subscription", subID)
	}
	return nil
}

// DeletePushSubscription removes a subscription together with its bus
// cursor and outbox rows: a subscriber that no longer exists must not leave
// pending deliveries nothing will ever drain.
func (db *DB) DeletePushSubscription(ctx context.Context, subID string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, subID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("push subscription", subID)
	}
	name := PushSubscriberName(subID)
	if _, err := tx.ExecContext(ctx, `DELETE FROM integration_outbox WHERE subscriber = ?`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM integration_cursors WHERE subscriber = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPushResult records the outcome of a delivery attempt (lastErr "" on
// success), for the Settings list.
func (db *DB) RecordPushResult(ctx context.Context, subID, lastErr string) error {
	now := time.Now().UTC().UnixMilli()
	var err error
	if lastErr == "" {
		_, err = db.sql.ExecContext(ctx, `UPDATE push_subscriptions SET last_delivered_at = ?, last_error = '' WHERE id = ?`, now, subID)
	} else {
		_, err = db.sql.ExecContext(ctx, `UPDATE push_subscriptions SET last_error = ? WHERE id = ?`, truncateErr(lastErr), subID)
	}
	return err
}

const pushSelect = `SELECT id, endpoint, p256dh, auth, events, label, created_at, updated_at, last_delivered_at, last_error FROM push_subscriptions`

func scanPush(r interface{ Scan(...any) error }) (PushSubscription, error) {
	var s PushSubscription
	var events string
	var created, updated int64
	var delivered sql.NullInt64
	if err := r.Scan(&s.ID, &s.Endpoint, &s.P256dh, &s.Auth, &events, &s.Label, &created, &updated, &delivered, &s.LastError); err != nil {
		return PushSubscription{}, err
	}
	if events != "" {
		s.Events = strings.Split(events, ",")
	} else {
		s.Events = []string{}
	}
	s.CreatedAt = time.UnixMilli(created).UTC()
	s.UpdatedAt = time.UnixMilli(updated).UTC()
	if delivered.Valid {
		t := time.UnixMilli(delivered.Int64).UTC()
		s.LastDeliveredAt = &t
	}
	return s, nil
}
