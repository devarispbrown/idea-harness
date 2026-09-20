package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestInheritDispositionFromPriorCarriesForwardAHumanRejection is ICP re-run
// defect A's second half: a re-review of the same subject (Revise,
// RevisePlan/ReopenPhase) must not let a reworded near-duplicate of a
// human-dispositioned finding silently escape that judgment call.
func TestInheritDispositionFromPriorCarriesForwardAHumanRejection(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	prior := &domain.Finding{
		InitiativeID: ini.ID,
		TargetType:   "change_set",
		TargetID:     "cs-1",
		Severity:     domain.SeverityCritical,
		Confidence:   domain.ConfidenceHigh,
		Category:     "injection",
		Path:         "src/handler.go",
		Finding:      "the input is not sanitized before use, which looks like an injection risk",
	}
	if err := db.CreateFinding(ctx, prior); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFindingDisposition(ctx, prior.ID, domain.DispositionRejected, store.HumanActor); err != nil {
		t.Fatal(err)
	}

	fresh := []domain.Finding{
		// A reworded near-duplicate on a DIFFERENT target (a later change
		// set): same type, same path, same category, similar wording.
		{
			TargetType: "change_set",
			TargetID:   "cs-2",
			Severity:   domain.SeverityCritical,
			Category:   "injection",
			Path:       "src/handler.go",
			Finding:    "input is not sanitized before it is used, an injection risk",
		},
		// A genuinely different finding: must NOT inherit anything.
		{
			TargetType: "change_set",
			TargetID:   "cs-2",
			Severity:   domain.SeverityHigh,
			Category:   "performance",
			Path:       "src/other.go",
			Finding:    "this loop is quadratic in the number of items",
		},
	}

	if err := eng.inheritDispositionFromPrior(ctx, ini.ID, fresh); err != nil {
		t.Fatal(err)
	}

	if fresh[0].Disposition != domain.DispositionRejected {
		t.Errorf("near-duplicate finding did not inherit the human's rejected disposition: %+v", fresh[0])
	}
	if fresh[0].DispositionActor != store.HumanActor {
		t.Errorf("inherited disposition did not carry the human actor: %+v", fresh[0])
	}
	if fresh[1].Disposition != "" {
		t.Errorf("an unrelated finding must not inherit a disposition: %+v", fresh[1])
	}
}

// TestInheritDispositionFromPriorIgnoresSystemDispositions proves the other
// half: a SYSTEM-set disposition (e.g. the bulk "fixed" a converged review
// cycle stamps, or a deny-coded secret_sweep rejection) says nothing about
// whether a specific fresh finding is a false positive, and must never be
// copied onto one.
func TestInheritDispositionFromPriorIgnoresSystemDispositions(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	prior := &domain.Finding{
		InitiativeID: ini.ID,
		TargetType:   "change_set",
		TargetID:     "cs-1",
		Severity:     domain.SeverityCritical,
		Category:     "injection",
		Path:         "src/handler.go",
		Finding:      "the input is not sanitized before use, which looks like an injection risk",
		Disposition:  domain.DispositionFixed,
	}
	if err := db.CreateFinding(ctx, prior); err != nil {
		t.Fatal(err)
	}

	fresh := []domain.Finding{{
		TargetType: "change_set",
		TargetID:   "cs-2",
		Severity:   domain.SeverityCritical,
		Category:   "injection",
		Path:       "src/handler.go",
		Finding:    "input is not sanitized before it is used, an injection risk",
	}}

	if err := eng.inheritDispositionFromPrior(ctx, ini.ID, fresh); err != nil {
		t.Fatal(err)
	}
	if fresh[0].Disposition != "" {
		t.Errorf("a system-set disposition must not be inherited: %+v", fresh[0])
	}
}
