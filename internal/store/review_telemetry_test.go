package store

import (
	"context"
	"errors"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Migration 38: one review cycle's convergence record rides on the reviewer's
// own run manifest, and an initiative's history reads back oldest first.
func TestReviewTelemetryRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	var runs []string
	for i := 0; i < 2; i++ {
		runID, err := db.CreateRun(ctx, domain.RunModel, "", "adversarial_reviewer", ini.ID, "review")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateRunManifest(ctx, &domain.RunManifest{RunID: runID, RequestedModel: "demo", ActualModel: "demo"}); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, runID)
	}
	// A manifest with no review record must not appear in the history.
	other, _ := db.CreateRun(ctx, domain.RunModel, "", "product_manager", ini.ID, "product_definition")
	if err := db.CreateRunManifest(ctx, &domain.RunManifest{RunID: other, RequestedModel: "demo", ActualModel: "demo"}); err != nil {
		t.Fatal(err)
	}

	for i, runID := range runs {
		tel := domain.ReviewTelemetry{
			Review: "prd", Cycle: i + 1, Mode: "guarded", OpenBlocking: 2 - i, PrevOpenBlocking: i*3 - 1,
			Raised: map[domain.Severity]int{domain.SeverityHigh: 2}, Demoted: map[string]int{"uncited": i},
			Outcome: []string{"revise", "converged"}[i],
		}
		if err := db.SetRunManifestReview(ctx, runID, tel); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ListReviewTelemetry(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Cycle != 1 || got[1].Outcome != "converged" || got[0].Raised[domain.SeverityHigh] != 2 {
		t.Fatalf("history = %+v", got)
	}
	m, err := db.GetRunManifest(ctx, runs[1])
	if err != nil {
		t.Fatal(err)
	}
	if m.Review == nil || m.Review.Cycle != 2 {
		t.Fatalf("GetRunManifest lost the review record: %+v", m.Review)
	}
	if m2, _ := db.GetRunManifest(ctx, other); m2.Review != nil {
		t.Fatalf("a non-review manifest grew a review record: %+v", m2.Review)
	}
}

// A run with no manifest (a failed call) has nothing to attach telemetry to;
// that is reported, not silently dropped.
func TestSetRunManifestReviewWithoutAManifest(t *testing.T) {
	db := openTestDB(t)
	err := db.SetRunManifestReview(context.Background(), "no-such-run", domain.ReviewTelemetry{Outcome: "revise"})
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}
}
