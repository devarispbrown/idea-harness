package projection

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func TestBuildUnderstandingSummaryAndJudgment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "Recipe sharing", Description: "Share recipes with friends"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRequirement(ctx, &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Share a recipe", Priority: domain.PriorityMust,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDecision(ctx, &domain.Decision{
		InitiativeID: ini.ID, Question: "Public or private?", Type: domain.DecisionProduct,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &domain.Question{InitiativeID: ini.ID, Question: "Retention policy?"}); err != nil {
		t.Fatal(err)
	}

	u, err := BuildUnderstanding(ctx, db, *ini)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.Summary, "Recipe sharing") {
		t.Fatalf("summary should mention the title: %q", u.Summary)
	}
	if !strings.Contains(u.Summary, "decision") {
		t.Fatalf("summary should mention the open decision: %q", u.Summary)
	}
	if len(u.NeedsJudgment) != 2 {
		t.Fatalf("expected 2 judgment items (1 decision + 1 question), got %d", len(u.NeedsJudgment))
	}
}

// TestBuildUnderstandingDedupsReRelistedDecisions is D3's end-to-end wire test:
// the same question asked twice across a revise cycle (once resolved, once
// re-listed and still open) must appear once in BuildUnderstanding's output,
// as the open one.
func TestBuildUnderstandingDedupsReRelistedDecisions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	older := &domain.Decision{InitiativeID: ini.ID, Question: "Public or private sharing?", Type: domain.DecisionProduct, CreatedAt: base}
	if err := db.CreateDecision(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, older.ID, "public", "human"); err != nil {
		t.Fatal(err)
	}
	// A revise cycle re-lists it, reworded, still open, and newer.
	newer := &domain.Decision{InitiativeID: ini.ID, Question: "[Source: revise] Public or PRIVATE sharing?", Type: domain.DecisionProduct, CreatedAt: base.Add(time.Hour)}
	if err := db.CreateDecision(ctx, newer); err != nil {
		t.Fatal(err)
	}

	u, err := BuildUnderstanding(ctx, db, *ini)
	if err != nil {
		t.Fatal(err)
	}
	var matches []DecisionView
	for _, d := range u.Decisions {
		if normalizeDecisionQuestion(d.Question) == normalizeDecisionQuestion(older.Question) {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 decision for the shared question, got %d: %+v", len(matches), matches)
	}
	if matches[0].Status != domain.DecisionOpen {
		t.Errorf("expected the unresolved re-listing to show, got status %q", matches[0].Status)
	}
}

func TestBuildGuidancePrecedence(t *testing.T) {
	// Judgment outranks stage advancement.
	ini := domain.Initiative{Status: domain.InitiativeApproved}
	u := Understanding{}
	if nm := BuildGuidance(ini, u, nil); !strings.Contains(nm.Action, "design & execution plan") {
		t.Fatalf("approved should guide toward M2, got %q", nm.Action)
	}

	ini = domain.Initiative{Status: domain.InitiativePlanApproved}
	if nm := BuildGuidance(ini, u, nil); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("plan_approved should report a done plan, got %q", nm.Action)
	}

	ini = domain.Initiative{Status: domain.InitiativeRunning}
	u = Understanding{Decisions: []DecisionView{{Status: domain.DecisionOpen}}}
	if nm := BuildGuidance(ini, u, nil); !strings.Contains(nm.Action, "decision") {
		t.Fatalf("open decision should outrank, got %q", nm.Action)
	}
}

// TestBuildGuidanceNeverSaysReadyToBuildOverAFreshFailure is review C6: a
// denied, rejected, or failed change set (this codebase funnels all three
// into ChangeSetRejected -- see execute.go's checks-running -> rejected
// transitions) must produce an honest status, never the old unconditional
// "ready to build".
func TestBuildGuidanceNeverSaysReadyToBuildOverAFreshFailure(t *testing.T) {
	ini := domain.Initiative{Status: domain.InitiativePlanApproved}
	u := Understanding{}

	// No change sets yet: the old, unconditional guidance stands.
	if nm := BuildGuidance(ini, u, nil); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("no change sets yet should still say the plan is ready, got %q", nm.Action)
	}

	for _, status := range []domain.ChangeSetStatus{
		domain.ChangeSetRejected, domain.ChangeSetChangesRequested, domain.ChangeSetConflicted,
	} {
		recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-3", Status: status}}
		nm := BuildGuidance(ini, u, recent)
		if strings.Contains(nm.Action, "ready to build") || strings.Contains(nm.Why, "ready to build") ||
			nm.Action == "Execution plan approved" {
			t.Fatalf("status %q: guidance still said the old unconditional thing: %+v", status, nm)
		}
		if !strings.Contains(nm.Why, "CHG-3") {
			t.Fatalf("status %q: guidance did not name the change set: %+v", status, nm)
		}
	}

	// A change set that succeeded (approved/applied) is not a failure --
	// guidance reverts to the ordinary "ready to build" message.
	recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-1", Status: domain.ChangeSetApplied}}
	if nm := BuildGuidance(ini, u, recent); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("a successful latest change set should not trigger the failure path, got %q", nm.Action)
	}

	// Only the LATEST (last in the ascending-by-created_at slice) matters: an
	// earlier rejection followed by a successful retry is not a fresh failure.
	recovered := []domain.ChangeSet{
		{ID: "cs-1", HumanID: "CHG-1", Status: domain.ChangeSetRejected},
		{ID: "cs-2", HumanID: "CHG-2", Status: domain.ChangeSetApplied},
	}
	if nm := BuildGuidance(ini, u, recovered); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("a later successful change set should supersede an earlier rejection, got %q", nm.Action)
	}
}

// TestDedupDecisionsForDisplayShowsTheNewestUnresolvedOverAnyResolvedOne is D3.
//
// A revise cycle can re-list a prior decision, so the same question
// accumulates several rows over time. Before this, every one of them showed
// on the page, so answering a question once still left it listed as open
// however many times it was re-asked. The rule: the newest unresolved
// decision for a question wins even over a newer resolved one -- an
// unresolved decision must never be hidden behind a resolved one asking the
// same thing.
func TestDedupDecisionsForDisplayShowsTheNewestUnresolvedOverAnyResolvedOne(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	decs := []domain.Decision{
		{ID: "d1", Question: "Public or private sharing?", Status: domain.DecisionDecided, Resolution: "public", CreatedAt: base},
		// Newer, same question reworded/re-listed, still unresolved: this is
		// the one that must show, even though d1 (resolved) came before it
		// and d3 (also resolved) comes after it.
		{ID: "d2", Question: "  Public or PRIVATE sharing?  ", Status: domain.DecisionOpen, CreatedAt: base.Add(time.Hour)},
		{ID: "d3", Question: "[Source: revise] Public or private sharing?", Status: domain.DecisionDecided, Resolution: "private", CreatedAt: base.Add(2 * time.Hour)},
		// An unrelated question must survive untouched.
		{ID: "d4", Question: "Which billing model?", Status: domain.DecisionOpen, CreatedAt: base.Add(3 * time.Hour)},
	}
	out := dedupDecisionsForDisplay(decs)
	if len(out) != 2 {
		t.Fatalf("expected 2 rows after dedup, got %d: %+v", len(out), out)
	}
	byID := map[string]domain.Decision{}
	for _, d := range out {
		byID[d.ID] = d
	}
	if _, ok := byID["d2"]; !ok {
		t.Errorf("the unresolved decision (d2) was hidden behind a resolved one: %+v", out)
	}
	if _, ok := byID["d4"]; !ok {
		t.Errorf("the unrelated question (d4) was dropped: %+v", out)
	}
}

// TestDedupDecisionsForDisplayShowsTheNewestResolvedWhenAllAreResolved is D3's
// other branch: once every decision for a question is resolved, show the
// newest one's resolution, not a stale earlier answer.
func TestDedupDecisionsForDisplayShowsTheNewestResolvedWhenAllAreResolved(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	decs := []domain.Decision{
		{ID: "d1", Question: "Which region first?", Status: domain.DecisionDecided, Resolution: "us-east", CreatedAt: base},
		{ID: "d2", Question: "Which region first?", Status: domain.DecisionDecided, Resolution: "eu-west", CreatedAt: base.Add(time.Hour)},
	}
	out := dedupDecisionsForDisplay(decs)
	if len(out) != 1 {
		t.Fatalf("expected 1 row after dedup, got %d: %+v", len(out), out)
	}
	if out[0].ID != "d2" || out[0].Resolution != "eu-west" {
		t.Errorf("expected the newest resolved decision (d2/eu-west), got %+v", out[0])
	}
}

// TestNormalizeDecisionQuestionFoldsCaseWhitespaceAndSourcePrefix pins the
// exact equality rule D3 dedups on.
func TestNormalizeDecisionQuestionFoldsCaseWhitespaceAndSourcePrefix(t *testing.T) {
	cases := []string{
		"Public or private sharing?",
		"  Public or PRIVATE sharing?  ",
		"public   or   private   sharing",
		"[Source: revise] Public or private sharing?",
	}
	want := normalizeDecisionQuestion(cases[0])
	for _, c := range cases[1:] {
		if got := normalizeDecisionQuestion(c); got != want {
			t.Errorf("normalizeDecisionQuestion(%q) = %q, want %q", c, got, want)
		}
	}
}

// TestPlanApprovedGuidanceDoesNotClaimMilestone3IsPending is B5: the plan_approved
// Why used to read "Implementation and ChangeSets arrive in Milestone 3", which
// was already false by the time anyone read it on the Build page -- execute,
// apply and verify had all shipped. A stale "not built yet" message on a phase
// that is fully wired is worse than no message.
func TestPlanApprovedGuidanceDoesNotClaimMilestone3IsPending(t *testing.T) {
	ini := domain.Initiative{Status: domain.InitiativePlanApproved}
	nm := BuildGuidance(ini, Understanding{}, nil)
	if strings.Contains(nm.Why, "Milestone 3") {
		t.Fatalf("plan_approved guidance still claims a future milestone: %q", nm.Why)
	}
}
