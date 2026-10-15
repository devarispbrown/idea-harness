package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestPairingCodeBurnBudget pins the per-code guessing bound: a code absorbs
// exactly failureBudget wrong guesses and then cannot be redeemed, a code
// minted afterwards starts at zero, and both the counts and the burn survive
// reopening global.db (a daemon restart).
func TestPairingCodeBurnBudget(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "global.db")
	db, err := OpenGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	const budget = 5
	now := time.Unix(1_700_000_000, 0)
	sess := func(id string) UISession {
		return UISession{ID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	if err := db.CreatePairingCode(ctx, "hunted", "", now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < budget-1; i++ {
		if _, burned, err := db.RedeemPairingCode(ctx, "wrong", sess("x"), "s", now, budget); !errors.Is(err, ErrPairingCodeInvalid) || burned != 0 {
			t.Fatalf("guess %d: burned=%d err=%v", i, burned, err)
		}
	}

	// A restart in the middle of the attack does not reset the count.
	db.Close()
	if db, err = OpenGlobal(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, burned, err := db.RedeemPairingCode(ctx, "wrong", sess("x"), "s", now, budget); !errors.Is(err, ErrPairingCodeInvalid) || burned != 1 {
		t.Fatalf("the budget-th guess: burned=%d err=%v, want the code burned", burned, err)
	}
	if _, _, err := db.RedeemPairingCode(ctx, "hunted", sess("a"), "sa", now, budget); !errors.Is(err, ErrPairingCodeBurned) {
		t.Fatalf("a burned code redeemed: %v", err)
	}

	if err := db.CreatePairingCode(ctx, "fresh", "", now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RedeemPairingCode(ctx, "fresh", sess("b"), "sb", now, budget); err != nil {
		t.Fatalf("a code minted after the burn: %v", err)
	}
	if n, last, err := db.BurnedPairingCodes(ctx, now.Add(-time.Hour)); err != nil || n != 1 || !last.Equal(now) {
		t.Fatalf("burned report: n=%d last=%v err=%v", n, last, err)
	}

	// The burned row outlives its expiry for BurnedCodeRetention, then goes.
	later := now.Add(BurnedCodeRetention - time.Minute)
	if err := db.CreatePairingCode(ctx, "c2", "", later, later.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := db.BurnedPairingCodes(ctx, now.Add(-time.Hour)); n != 1 {
		t.Fatalf("a burned code was pruned within the retention: n=%d", n)
	}
	later = now.Add(BurnedCodeRetention + time.Minute)
	if err := db.CreatePairingCode(ctx, "c3", "", later, later.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := db.BurnedPairingCodes(ctx, now.Add(-time.Hour)); n != 0 {
		t.Fatalf("a burned code outlived the retention: n=%d", n)
	}
}
