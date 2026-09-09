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

// TestApproveDispositionsWhatItAccepts pins the defect that made `idea approve`
// a dead end one step further along than the one it fixed.
//
// Approve moved the status and left every blocking finding undisposed.
// hasOpenBlockingFindings refuses `complete` while any undisposed critical/high
// exists, and nothing in the CLI can disposition one -- so the command offered
// as the way out of a non-converged PRD review handed back an initiative that
// reached a change set and could never finish. The README's last step,
// `idea next` printing "✓ initiative complete", stayed unreachable.
func TestApproveDispositionsWhatItAccepts(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	for _, sev := range []domain.Severity{domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium} {
		if err := db.CreateFinding(ctx, &domain.Finding{
			InitiativeID: iniID, TargetType: "prd", TargetID: iniID,
			Severity: sev, Confidence: domain.ConfidenceHigh,
			Category: "c", Finding: "f", Recommendation: "r",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.NeedsHumanJudgment(ctx, iniID, domain.JudgmentPRDReview, "test"); err != nil {
		t.Fatal(err)
	}

	if err := eng.Approve(ctx, iniID); err != nil {
		t.Fatal(err)
	}

	blocking, err := eng.hasOpenBlockingFindings(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if blocking {
		t.Fatal("approve left blocking findings undisposed; the initiative can reach a change set and never complete")
	}

	// accepted_risk specifically: it is the disposition the reviewer is
	// forbidden to set, so it reads unambiguously as a human's signature rather
	// than a claim that anything was fixed.
	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Disposition != domain.DispositionAcceptedRisk {
			t.Errorf("finding %s disposition = %q, want accepted_risk", f.Severity, f.Disposition)
		}
	}
}

// TestApproveRefusesJudgmentReasonsItDoesNotOwn is the other half of the
// disposition above, and the reason it is safe.
//
// All four JudgmentReasons share one status, so a status-only gate let
// `idea approve` -- help text "accept the reviewed PRD", and offered by
// `idea next` only for a stalled PRD review -- fire on an initiative parked for
// the plan or for verification, signing off findings the human never saw with
// `accepted_risk` in their name. The worst case is `plan_dependencies`, where
// the undisposed finding IS the guard: advance.go says three times that
// approving there locks in a dependency graph known to be incomplete.
func TestApproveRefusesJudgmentReasonsItDoesNotOwn(t *testing.T) {
	for _, reason := range []domain.JudgmentReason{
		domain.JudgmentPlanDependencies, domain.JudgmentPlanReview, domain.JudgmentVerification,
	} {
		t.Run(string(reason), func(t *testing.T) {
			eng, db, iniID := newVerifyFixture(t)
			ctx := context.Background()
			if err := db.CreateFinding(ctx, &domain.Finding{
				InitiativeID: iniID, TargetType: "initiative", TargetID: iniID,
				Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
				Category: "plan_dependency", Finding: "f", Recommendation: "r",
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.NeedsHumanJudgment(ctx, iniID, reason, "test"); err != nil {
				t.Fatal(err)
			}

			if err := eng.Approve(ctx, iniID); err == nil {
				t.Fatalf("approve accepted an initiative parked on %s", reason)
			}

			got, err := db.GetInitiative(ctx, iniID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != domain.InitiativeNeedsHumanJudgment {
				t.Errorf("a refused approve moved the status to %q", got.Status)
			}
			findings, err := db.ListFindingsByInitiative(ctx, iniID)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range findings {
				if f.Disposition != "" {
					t.Errorf("a refused approve dispositioned a %s finding as %q", reason, f.Disposition)
				}
			}
		})
	}
}

// TestApprovePlanDispositionsWhatItApproves is the plan-path twin of
// TestApproveDispositionsWhatItAccepts, and the defect was identical.
//
// runExecutionPlanReview's success path set the status and left every blocking
// finding open, so a plan review that blocked once and passed on a later run
// produced a `plan_approved` initiative that could never reach `complete`.
// Nothing in the CLI could clear them: revise-plan and reopen-phase both refuse
// once a change set exists, and `approve` refuses any reason but prd_review.
// `idea approve-plan` therefore succeeded and quietly made the initiative
// unfinishable.
func TestApprovePlanDispositionsWhatItApproves(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	// A blocking finding from an earlier, blocked plan review.
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "execution_plan", TargetID: iniID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "vague acceptance criteria", Finding: "f", Recommendation: "r",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.NeedsHumanJudgment(ctx, iniID, domain.JudgmentPlanReview, "test"); err != nil {
		t.Fatal(err)
	}

	// The demo reviewer returns no findings, so this run converges.
	if _, err := eng.ApprovePlan(ctx, iniID); err != nil {
		t.Fatal(err)
	}

	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativePlanApproved {
		t.Fatalf("status = %q, want plan_approved", ini.Status)
	}
	blocking, err := eng.hasOpenBlockingFindings(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if blocking {
		t.Fatal("approve-plan succeeded and left blocking findings open; the initiative " +
			"can reach a change set and never complete, and no CLI command can clear them")
	}

	// The value and the actor, not just "some disposition". hasOpenBlockingFindings
	// only checks for a non-empty one, so every enum value clears the gate equally
	// and any of them would satisfy the assertion above.
	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Disposition != domain.DispositionAcceptedRisk {
			t.Errorf("disposition = %q, want accepted_risk: nothing was fixed and the plan was "+
				"not regenerated — a human accepted findings they were shown", f.Disposition)
		}
	}
}

