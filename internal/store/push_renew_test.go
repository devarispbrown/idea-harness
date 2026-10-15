package store

import (
	"context"
	"errors"
	"testing"
)

// TestRenewPushSubscriptionNeedsAToken: a row created before migration 48
// has no renewal token, and an empty token must never match it.
func TestRenewPushSubscriptionNeedsAToken(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	legacy, _, err := db.UpsertPushSubscription(ctx, PushSubscription{Endpoint: "https://push.example/a", P256dh: "p", Auth: "a", Events: []string{"verify.failed"}})
	if err != nil {
		t.Fatal(err)
	}
	next := PushSubscription{Endpoint: "https://push.example/b", P256dh: "p2", Auth: "a2"}
	if _, err := db.RenewPushSubscription(ctx, legacy.ID, "", next, "h2"); !errors.Is(err, ErrPushRenewalDenied) {
		t.Fatalf("an empty token against a tokenless row: %v, want denied", err)
	}

	// A re-subscribe with a token makes it renewable, and an Upsert without
	// one leaves the token alone.
	if _, _, err := db.UpsertPushSubscription(ctx, PushSubscription{Endpoint: "https://push.example/a", P256dh: "p", Auth: "a", Events: []string{"verify.failed"}, RenewalTokenHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.UpsertPushSubscription(ctx, PushSubscription{Endpoint: "https://push.example/a", P256dh: "p", Auth: "a", Events: []string{"verify.failed"}}); err != nil {
		t.Fatal(err)
	}
	got, err := db.RenewPushSubscription(ctx, legacy.ID, "h1", next, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != next.Endpoint || got.P256dh != "p2" || got.Auth != "a2" || len(got.Events) != 1 {
		t.Fatalf("renewed row: %+v", got)
	}
	if _, err := db.RenewPushSubscription(ctx, legacy.ID, "h1", next, "h3"); !errors.Is(err, ErrPushRenewalDenied) {
		t.Fatalf("a rotated-out token: %v, want denied", err)
	}
}
