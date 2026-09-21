package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRunModelAccumulatesCostAcrossRetries pins M5, review fix: the run
// manifest used to record only the LAST attempt's cost, even though
// chargeUSD bills every retry attempt against the budget -- a real charge
// incurred by an earlier attempt that then failed validation and was
// retried simply vanished from GET .../costs. The first "idea_brief" call
// here returns invalid JSON (forcing a retry) and reports $0.01; the second,
// successful call reports $0.02. The persisted manifest must show $0.03.
func TestRunModelAccumulatesCostAcrossRetries(t *testing.T) {
	fp := scriptedProvider()
	// Consumed first, before scriptedProvider's own valid default response.
	fp.EnqueueResponse("idea_brief", "not valid json at all")
	fp.EnqueueCost("idea_brief", 0.01)
	fp.EnqueueCost("idea_brief", 0.02)

	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListModelPhaseCosts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	var framingUSD float64
	found := false
	for _, r := range rows {
		if r.Stage == StageFraming {
			framingUSD = r.CostUSD
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q phase in cost rows: %+v", StageFraming, rows)
	}
	if framingUSD < 0.0299 || framingUSD > 0.0301 {
		t.Errorf("framing phase cost = %v, want ~0.03 (0.01 retried attempt + 0.02 successful attempt)", framingUSD)
	}
}

// TestRunModelWritesAManifestOnStructuredOutputFailure pins M5's other half:
// a model run that ultimately fails (every retry attempt produces invalid
// structured output) still incurred real, billed cost across those
// attempts, and that spend must not vanish just because the run itself
// failed.
func TestRunModelWritesAManifestOnStructuredOutputFailure(t *testing.T) {
	fp := scriptedProvider()
	// Every attempt (maxAttempts = 3 in runModel) returns invalid JSON, so
	// the run exhausts its retries and fails outright.
	for i := 0; i < 3; i++ {
		fp.EnqueueResponse("idea_brief", "still not valid json")
		fp.EnqueueCost("idea_brief", 0.05)
	}

	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Run(ctx, ini.ID, ini.Description); err == nil {
		t.Fatal("expected the run to fail: every attempt returned invalid structured output")
	}

	rows, err := db.ListModelPhaseCosts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	var framingUSD float64
	found := false
	for _, r := range rows {
		if r.Stage == StageFraming {
			framingUSD = r.CostUSD
			found = true
		}
	}
	if !found {
		t.Fatalf("a failed run's real, billed cost must still produce a manifest -- got no %q phase at all: %+v", StageFraming, rows)
	}
	if framingUSD < 0.1499 || framingUSD > 0.1501 {
		t.Errorf("framing phase cost = %v, want ~0.15 (3 attempts x $0.05)", framingUSD)
	}
}
