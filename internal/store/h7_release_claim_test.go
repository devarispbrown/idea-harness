package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestReleaseInitiativeClaimDoesNotStompALaterLegitimateWrite is review
// H7(a)'s regression test for ReleaseInitiativeClaim.
//
// apply/execute/revert's release() closures (runtime.go) hand the initiative
// back to a prior status once their real work finishes. Before this, they
// used the unconditional SetInitiativeStatus, so a legitimate write that
// landed in the gap between the claiming operation's engine mutex being
// released and its release() closure actually running -- verify's
// CompleteInitiative, proven reproducible -- was silently overwritten back
// to the pre-apply status, discarding a real completion.
//
// This does not need goroutines to be deterministic: it reproduces the
// interleaving by performing the two writes in the exact order the race
// produces them (claim -> a legitimate write that moves the initiative off
// `running` -> the stale release), and asserts the release is a no-op
// instead of a stomp.
func TestReleaseInitiativeClaimDoesNotStompALaterLegitimateWrite(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d", Status: domain.InitiativePlanApproved}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// apply claims running (priorStatus = plan_approved).
	if err := db.ClaimInitiativeRunning(ctx, ini.ID, domain.InitiativePlanApproved); err != nil {
		t.Fatal(err)
	}

	// While apply's real work is (simulated as) still settling, a
	// legitimate concurrent write -- verify's CompleteInitiative -- lands.
	if err := db.CompleteInitiative(ctx, ini.ID, ActorOrchestrator); err != nil {
		t.Fatalf("CompleteInitiative should succeed while status is running (documented apply-then-verify sequence): %v", err)
	}

	// apply's own release() now runs, unaware that verify has already moved
	// the initiative on.
	if err := db.ReleaseInitiativeClaim(ctx, ini.ID, domain.InitiativePlanApproved, ActorOrchestrator); err != nil {
		t.Fatalf("ReleaseInitiativeClaim returned an error instead of a silent no-op: %v", err)
	}

	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativeComplete {
		t.Fatalf("ReleaseInitiativeClaim stomped a legitimate completion: status = %s, want %s", got.Status, domain.InitiativeComplete)
	}
}

// TestReleaseInitiativeClaimReleasesAGenuineClaim is the positive control:
// when nothing else has moved the initiative on, the release must still
// actually work.
func TestReleaseInitiativeClaimReleasesAGenuineClaim(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d", Status: domain.InitiativePlanApproved}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimInitiativeRunning(ctx, ini.ID, domain.InitiativePlanApproved); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseInitiativeClaim(ctx, ini.ID, domain.InitiativePlanApproved, ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.InitiativePlanApproved {
		t.Fatalf("ReleaseInitiativeClaim did not release a genuine claim: status = %s", got.Status)
	}
}
