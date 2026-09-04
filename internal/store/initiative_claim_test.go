package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestOnlyOneClaimWins pins the fix for the DX audit's concurrency finding.
//
// Seven Runtime entry points asked "is this initiative already running?" and
// four of them never set the flag, so two concurrent requests both passed the
// check. A double-clicked "Revise plan" let one goroutine delete and recreate
// the task set while the other wrote dependency edges to task ids that no longer
// existed, after which every later revise was refused.
//
// A Go-level read-then-set cannot close that, which is why this is a conditional
// UPDATE: the check and the write are one statement.
func TestOnlyOneClaimWins(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativePlanApproved, ""); err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	won := make([]bool, racers)
	wg.Add(racers)
	for i := range racers {
		go func(i int) {
			defer wg.Done()
			err := db.ClaimInitiativeRunning(ctx, ini.ID, domain.InitiativePlanApproved)
			won[i] = err == nil
			if err != nil && !errors.Is(err, ErrInitiativeNotClaimable) {
				t.Errorf("racer %d got an unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, w := range won {
		if w {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d of %d concurrent claims succeeded; exactly one must win", winners, racers)
	}
}

// TestClaimRefusesFromAnUnlistedState keeps the claim from becoming a blanket
// "set running": each entry point passes the states it legitimately runs from.
func TestClaimRefusesFromAnUnlistedState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeComplete, ""); err != nil {
		t.Fatal(err)
	}
	err := db.ClaimInitiativeRunning(ctx, ini.ID, domain.InitiativeFailed)
	if !errors.Is(err, ErrInitiativeNotClaimable) {
		t.Errorf("claiming a complete initiative from {failed} = %v, want ErrInitiativeNotClaimable", err)
	}
}
