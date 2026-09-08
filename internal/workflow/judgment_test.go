package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestNextActionNamesWhyJudgmentIsNeeded pins the P0 from DX audit 6.
//
// Six code paths set needs_human_judgment and NextAction returned the bare token
// "resolve" for all of them, which the CLI rendered as "resolve open
// decisions/questions/findings — idea decisions". In the case that actually
// happens on the README's own quick start, `idea decisions` prints "No decisions
// yet." and no CLI command could move the initiative at all.
func TestNextActionNamesWhyJudgmentIsNeeded(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		reason domain.JudgmentReason
		want   string
	}{
		{domain.JudgmentPRDReview, "resolve prd"},
		{domain.JudgmentPlanDependencies, "resolve plan-dependencies"},
		{domain.JudgmentPlanReview, "resolve plan"},
		{domain.JudgmentVerification, "resolve verification"},
	} {
		if err := db.NeedsHumanJudgment(ctx, iniID, tc.reason, "test"); err != nil {
			t.Fatal(err)
		}
		got, err := eng.NextAction(ctx, iniID)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("reason %q -> %q, want %q", tc.reason, got, tc.want)
		}
	}

	// An initiative parked before the reason was recorded. The bare token is the
	// honest answer; inventing a specific instruction would name a command that
	// may be wrong for its actual cause.
	if err := db.NeedsHumanJudgment(ctx, iniID, "", "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ := eng.NextAction(ctx, iniID); got != "resolve" {
		t.Errorf("unrecorded reason -> %q, want bare resolve", got)
	}
}

// TestApproveIsACompareAndSwap covers audit 6 / F-1.
//
// Approve was an unconditional SetInitiativeStatus with no check of any kind, so
// it overwrote whatever the initiative was doing -- including releasing the claim
// a running advance held, after which a second advance was accepted and two ran
// concurrently on one initiative.
func TestApproveIsACompareAndSwap(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	// Running: the state the concurrency bug was about.
	if err := db.SetInitiativeStatus(ctx, iniID, domain.InitiativeRunning, "test"); err != nil {
		t.Fatal(err)
	}
	if err := eng.Approve(ctx, iniID); err == nil {
		t.Error("approve accepted a running initiative; it releases the claim a running step holds")
	}
	if ini, _ := db.GetInitiative(ctx, iniID); ini.Status != domain.InitiativeRunning {
		t.Errorf("running initiative was moved to %q by a refused approve", ini.Status)
	}

	// Walking the state machine backwards is the same defect resume had.
	for _, st := range []domain.InitiativeStatus{
		domain.InitiativePlanApproved, domain.InitiativeComplete, domain.InitiativeFailed,
	} {
		if err := db.SetInitiativeStatus(ctx, iniID, st, "test"); err != nil {
			t.Fatal(err)
		}
		if err := eng.Approve(ctx, iniID); err == nil {
			t.Errorf("approve accepted status %q; it walks the state machine backwards", st)
		}
	}

	// The state it exists for.
	if err := db.NeedsHumanJudgment(ctx, iniID, domain.JudgmentPRDReview, "test"); err != nil {
		t.Fatal(err)
	}
	if err := eng.Approve(ctx, iniID); err != nil {
		t.Fatalf("approve refused the state it exists for: %v", err)
	}
	ini, _ := db.GetInitiative(ctx, iniID)
	if ini.Status != domain.InitiativeApproved {
		t.Fatalf("status = %q, want approved", ini.Status)
	}
	// And the reason does not linger to be read the next time it parks.
	if ini.JudgmentReason != "" {
		t.Errorf("judgment_reason %q survived the transition out of needs_human_judgment", ini.JudgmentReason)
	}
	// Idempotent: a second approve is a no-op, not an error.
	if err := eng.Approve(ctx, iniID); err != nil {
		t.Errorf("second approve returned %v, want no-op", err)
	}
}

// TestResumeReleasesItsClaimOnError covers the regression the claim introduced.
//
// Resume takes a claim and has no operation row, so a claim it fails to release
// is invisible to reconcileStaleOperations: the initiative answers "already
// running" to everything until a daemon restart, which then sweeps it to
// `failed` -- past the point `idea approve` can take it. runReviewLoop returns
// an error without writing the status on every failure path, and a provider 401
// is the likeliest of them on the endpoint you reach for after a model
// misbehaved.
func TestResumeReleasesItsClaimOnError(t *testing.T) {
	fp := model.NewFakeProvider()
	// A schema-parse failure: one of the error paths runReviewLoop returns
	// without writing a status, and the class the reviewer is most likely to hit
	// on a real provider.
	fp.SetResponse("review", `{not json`)
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network: config.NetworkDisabled,
		Review:  config.ReviewConfig{MaxCycles: 3},
	})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	iniID := ini.ID

	if err := db.NeedsHumanJudgment(ctx, iniID, domain.JudgmentPRDReview, "test"); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Resume(ctx, iniID); err == nil {
		t.Fatal("expected the seeded model failure to reach the caller")
	}

	got, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.InitiativeRunning {
		t.Fatal("resume stranded the initiative in `running`: no operation row exists for " +
			"reconcileStaleOperations to find, so every later command answers `already running`")
	}
	if got.Status != domain.InitiativeNeedsHumanJudgment {
		t.Errorf("status = %q, want the state resume was called from", got.Status)
	}
	if got.JudgmentReason != domain.JudgmentPRDReview {
		t.Errorf("judgment_reason = %q, want it restored to %q", got.JudgmentReason, domain.JudgmentPRDReview)
	}
}
