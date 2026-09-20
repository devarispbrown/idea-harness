package store

import (
	"context"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRunManifestAndStatusFailOnCancelledContext pins the mechanism behind
// the MEDIUM fix at execute.go's CreateRunManifest/SetRunStatus call sites: a
// context already cancelled (a Stop landing right after a real, billed
// implementer run finishes, before its manifest/status is recorded) makes
// BOTH calls fail outright. Execute switched these two calls to
// context.WithoutCancel(ctx) specifically so a Stop at that exact moment
// cannot lose the spend/provenance record of a run that, in fact, already
// completed. This test proves the cancellation would otherwise break them
// (the bug), and that WithoutCancel insulates them (the fix) -- see
// TestRunManifestAndStatusSucceedWithoutCancel below.
func TestRunManifestAndStatusFailOnCancelledContext(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	runID, err := db.CreateRun(ctx, domain.RunAgent, "", "implementer", "", "")
	if err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := db.CreateRunManifest(cancelled, &domain.RunManifest{
		RunID:      runID,
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}); err == nil {
		t.Fatal("CreateRunManifest succeeded with an already-cancelled context; " +
			"this test's premise (cancellation breaks it) no longer holds -- re-check the WithoutCancel fix is still needed")
	}

	if err := db.SetRunStatus(cancelled, runID, domain.RunSucceeded, "implementer"); err == nil {
		t.Fatal("SetRunStatus succeeded with an already-cancelled context")
	}
}

// TestRunManifestAndStatusSucceedWithoutCancel is the fix's other half: the
// same two calls, given context.WithoutCancel of an already-cancelled
// context (exactly what execute.go now passes), still succeed.
func TestRunManifestAndStatusSucceedWithoutCancel(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	runID, err := db.CreateRun(ctx, domain.RunAgent, "", "implementer", "", "")
	if err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runCtx := context.WithoutCancel(cancelled)

	if err := db.CreateRunManifest(runCtx, &domain.RunManifest{
		RunID:      runID,
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRunManifest with context.WithoutCancel: %v", err)
	}
	if err := db.SetRunStatus(runCtx, runID, domain.RunSucceeded, "implementer"); err != nil {
		t.Fatalf("SetRunStatus with context.WithoutCancel: %v", err)
	}

	got, err := db.GetRunManifest(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != runID {
		t.Errorf("GetRunManifest run id = %q, want %q", got.RunID, runID)
	}
}
