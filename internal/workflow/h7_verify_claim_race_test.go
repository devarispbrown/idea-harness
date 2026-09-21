package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestVerifyInitiativeBlocksOnAConcurrentlyHeldEngineMutex is review H7(a)'s
// regression test for the root-cause fix.
//
// VerifyInitiative used to run with no synchronization of its own.
// store.CompleteInitiative's CAS excludes `failed` but deliberately not
// `running` (the documented apply-then-verify sequence relies on verify
// succeeding while the row still reads `running`), so nothing stopped
// VerifyInitiative from reaching CompleteInitiative WHILE
// Execute/ApplyChangeSet/RevertChangeSet were still actively mutating files
// under mu. Proven: an apply mid-file-write let a concurrent verify complete
// the initiative, after which a third request (revise, which claims
// `running` FROM `complete`) claimed the initiative out from under the
// still-running apply and started superseding requirements while apply's
// files were still landing.
//
// This forces the exact interleaving deterministically by holding mu
// externally (exactly what Execute/ApplyChangeSet/RevertChangeSet hold for
// their whole duration) and confirming VerifyInitiative blocks until it is
// released, rather than proceeding to read/write requirement and initiative
// state underneath it.
func TestVerifyInitiativeBlocksOnAConcurrentlyHeldEngineMutex(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t", Description: "d", Status: domain.InitiativeApproved}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	// A waived requirement reaches the terminal write on the first call
	// (same shape TestVerifyInitiativeDoesNotCompleteAFailedInitiative uses),
	// so if VerifyInitiative were NOT blocked, it would run to completion
	// almost instantly.
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "s", Status: domain.RequirementWaived,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "1", Condition: "c"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Hold the SAME mutex Execute/ApplyChangeSet/RevertChangeSet hold for
	// their entire duration, simulating one of them still actively running.
	eng.mu.Lock()

	done := make(chan error, 1)
	go func() {
		done <- eng.VerifyInitiative(ctx, ini.ID)
	}()

	// Give a buggy (unsynchronized) VerifyInitiative every chance to run to
	// completion before checking that it has NOT: it needs only a couple of
	// fast local DB calls. A correct one blocks on mu.Lock() regardless of
	// how long we wait here, so this sleep can only ever expose a
	// regression, never mask one.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		eng.mu.Unlock()
		t.Fatal("VerifyInitiative returned while mu was held externally -- it is not taking the engine mutex (review H7)")
	default:
	}

	// Confirm no write landed while mu was held.
	ini2, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		eng.mu.Unlock()
		t.Fatal(err)
	}
	if ini2.Status == domain.InitiativeComplete {
		eng.mu.Unlock()
		t.Fatal("initiative reached `complete` before mu was released -- VerifyInitiative is not excluded by the engine mutex")
	}

	eng.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("VerifyInitiative after unlock: %v", err)
	}
	ini3, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ini3.Status != domain.InitiativeComplete {
		t.Fatalf("VerifyInitiative should have completed the initiative once mu was released, got status %s", ini3.Status)
	}
}
