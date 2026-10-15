package store

import (
	"context"
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
}

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
			INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, events, label, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Endpoint, s.P256dh, s.Auth, strings.Join(s.Events, ","), s.Label, now.UnixMilli(), now.UnixMilli())
		if err != nil {
			return PushSubscription{}, false, err
		}
		return s, true, nil
	case err != nil:
		return PushSubscription{}, false, err
	}
	_, err = db.sql.ExecContext(ctx, `
		UPDATE push_subscriptions SET p256dh = ?, auth = ?, events = ?, label = ?, updated_at = ?, last_error = '' WHERE id = ?`,
		s.P256dh, s.Auth, strings.Join(s.Events, ","), s.Label, now.UnixMilli(), existing)
	if err != nil {
		return PushSubscription{}, false, err
	}
	got, err := db.GetPushSubscription(ctx, existing)
	return got, false, err
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
