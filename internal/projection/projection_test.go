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
	if nm := BuildGuidance(ini, u, nil, false); !strings.Contains(nm.Action, "design & execution plan") {
		t.Fatalf("approved should guide toward M2, got %q", nm.Action)
	}

	ini = domain.Initiative{Status: domain.InitiativePlanApproved}
	if nm := BuildGuidance(ini, u, nil, false); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("plan_approved should report a done plan, got %q", nm.Action)
	}

	ini = domain.Initiative{Status: domain.InitiativeRunning}
	u = Understanding{Decisions: []DecisionView{{Status: domain.DecisionOpen}}}
	if nm := BuildGuidance(ini, u, nil, false); !strings.Contains(nm.Action, "decision") {
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
	if nm := BuildGuidance(ini, u, nil, false); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("no change sets yet should still say the plan is ready, got %q", nm.Action)
	}

	for _, status := range []domain.ChangeSetStatus{
		domain.ChangeSetRejected, domain.ChangeSetChangesRequested, domain.ChangeSetConflicted,
	} {
		recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-3", Status: status}}
		nm := BuildGuidance(ini, u, recent, false)
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
	if nm := BuildGuidance(ini, u, recent, false); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("a successful latest change set should not trigger the failure path, got %q", nm.Action)
	}

	// Only the task's own LATEST change set matters: an earlier rejection
	// followed by a successful retry (same task) is not a fresh failure. Per
	// review M7, this reduction (LatestChangeSetsPerActiveTask) is the
	// caller's job, not BuildGuidance's -- so this constructs the input the
	// way the real caller (handlers.go) does, rather than handing
	// BuildGuidance raw, unreduced history directly.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sameTaskHistory := []domain.ChangeSet{
		{ID: "cs-1", HumanID: "CHG-1", TaskID: "task-1", Status: domain.ChangeSetRejected, CreatedAt: base},
		{ID: "cs-2", HumanID: "CHG-2", TaskID: "task-1", Status: domain.ChangeSetApplied, CreatedAt: base.Add(time.Hour)},
	}
	activeTask := []domain.Task{{ID: "task-1", Status: domain.TaskProposed}}
	reduced := LatestChangeSetsPerActiveTask(sameTaskHistory, activeTask)
	if nm := BuildGuidance(ini, u, reduced, false); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("a later successful change set should supersede an earlier rejection, got %q", nm.Action)
	}
}

// TestLatestChangeSetsPerActiveTaskExcludesCancelledOrDeletedTasks and
// TestLatestChangeSetsPerActiveTaskDoesNotHideOneTasksRejectionBehindAnothers
// are review M7's own two bugs: guidance used to look only at the globally
// last change set, so a rejected change set on one task was hidden the
// moment ANY other task's change set was created afterward, and stayed
// "stuck" forever once its own task was cancelled or deleted.
func TestLatestChangeSetsPerActiveTaskExcludesCancelledOrDeletedTasks(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	changeSets := []domain.ChangeSet{
		{ID: "cs-1", HumanID: "CHG-1", TaskID: "task-1", Status: domain.ChangeSetRejected, CreatedAt: base},
	}
	// task-1 was cancelled after CHG-1 was rejected: its rejection must no
	// longer be reported (there is nothing left to "resolve" on a task that
	// no longer exists as active work).
	tasks := []domain.Task{{ID: "task-1", Status: domain.TaskCancelled}}
	got := LatestChangeSetsPerActiveTask(changeSets, tasks)
	if len(got) != 0 {
		t.Fatalf("expected no change sets for a cancelled task, got %+v", got)
	}

	// A task not present at all (deleted) is excluded the same way.
	got2 := LatestChangeSetsPerActiveTask(changeSets, nil)
	if len(got2) != 0 {
		t.Fatalf("expected no change sets for a deleted task, got %+v", got2)
	}
}

func TestLatestChangeSetsPerActiveTaskDoesNotHideOneTasksRejectionBehindAnothers(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	changeSets := []domain.ChangeSet{
		// TASK-2's change set is rejected...
		{ID: "cs-2", HumanID: "CHG-2", TaskID: "task-2", Status: domain.ChangeSetRejected, CreatedAt: base},
		// ...but a LATER change set on a completely different, healthy task
		// (task-3) must not hide it.
		{ID: "cs-3", HumanID: "CHG-3", TaskID: "task-3", Status: domain.ChangeSetApplied, CreatedAt: base.Add(time.Hour)},
	}
	tasks := []domain.Task{
		{ID: "task-2", Status: domain.TaskProposed},
		{ID: "task-3", Status: domain.TaskProposed},
	}
	reduced := LatestChangeSetsPerActiveTask(changeSets, tasks)

	ini := domain.Initiative{Status: domain.InitiativePlanApproved}
	nm := BuildGuidance(ini, Understanding{}, reduced, false)
	if !strings.Contains(nm.Why, "CHG-2") {
		t.Fatalf("expected guidance to still report CHG-2's rejection despite a later change set on another task, got %+v", nm)
	}
}

// TestBuildGuidancePendingChangeSetApprovalBeatsNextTask is ICP re-run defect
// G: guidance said "Run the next task" (the InitiativePlanApproved fallback)
// while CHG-1 sat in review_required awaiting the human's own approval --
// pending judgment on a change set must outrank "run the next task", exactly
// as it already does for a rejected/changes_requested/conflicted one (review
// C6). Mutation check: delete either new branch in BuildGuidance's
// InitiativePlanApproved case and this fails.
func TestBuildGuidancePendingChangeSetApprovalBeatsNextTask(t *testing.T) {
	ini := domain.Initiative{Status: domain.InitiativePlanApproved}
	u := Understanding{}

	// ReviewCompletedAt set: this is the ordinary "awaiting a human's
	// approval" state, not defect #1's interrupted-review one (which
	// BuildGuidance now reports as "Rerun the review" instead -- see
	// TestBuildGuidancePointsAtRerunWhenReviewNeverCompleted).
	reviewedAt := time.Now()
	recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-1", Status: domain.ChangeSetReviewRequired, ReviewCompletedAt: &reviewedAt}}
	nm := BuildGuidance(ini, u, recent, false)
	if !strings.Contains(nm.Action, "CHG-1") || !strings.Contains(nm.Action, "Review") {
		t.Fatalf("expected guidance to point at reviewing CHG-1, got %+v", nm)
	}
	if nm.Action == "Execution plan approved" {
		t.Fatalf("guidance still said to run the next task over a pending review: %+v", nm)
	}

	recent = []domain.ChangeSet{{ID: "cs-2", HumanID: "CHG-2", Status: domain.ChangeSetApproved}}
	nm = BuildGuidance(ini, u, recent, false)
	if !strings.Contains(nm.Action, "CHG-2") || !strings.Contains(nm.Action, "Apply") {
		t.Fatalf("expected guidance to point at applying CHG-2, got %+v", nm)
	}
}

// TestBuildGuidancePointsAtRerunWhenReviewNeverCompleted is ICP re-run defect
// #1: a change set stuck in review_required with no completed adversarial
// review (Execute's own review call errored, most often cancelled
// mid-review) is not simply "awaiting your review" -- `idea review CHG-1
// approve` refuses it. Guidance must name the actual way forward (rerun),
// not point at a command that will just fail. Mutation check: delete the
// !ReviewCompleted() branch in BuildGuidance's InitiativePlanApproved case
// and this fails.
func TestBuildGuidancePointsAtRerunWhenReviewNeverCompleted(t *testing.T) {
	ini := domain.Initiative{Status: domain.InitiativePlanApproved}
	u := Understanding{}

	recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-1", Status: domain.ChangeSetReviewRequired}}
	nm := BuildGuidance(ini, u, recent, false)
	if !strings.Contains(nm.Action, "CHG-1") || !strings.Contains(nm.Action, "Rerun") {
		t.Fatalf("expected guidance to point at rerunning CHG-1's review, got %+v", nm)
	}
	if strings.Contains(nm.Why, "review CHG-1 approve") {
		t.Errorf("guidance still tells the human to approve, which refuses on an incomplete review: %+v", nm)
	}
	if !strings.Contains(nm.Why, "rerun") {
		t.Errorf("guidance does not name the `rerun` command: %+v", nm)
	}
}

// TestBuildGuidancePointsToVerifyOnceAllTasksAreDone is ICP re-run defect G:
// status stayed plan_approved after every task was done and applied, and
// guidance never pointed to Verify -- it kept saying "run the next task"
// forever with nothing left to run. Mutation check: delete the allTasksDone
// branch in BuildGuidance's InitiativePlanApproved case and this fails.
func TestBuildGuidancePointsToVerifyOnceAllTasksAreDone(t *testing.T) {
	ini := domain.Initiative{Status: domain.InitiativePlanApproved}
	u := Understanding{}

	// Not all done: the ordinary fallback stands.
	if nm := BuildGuidance(ini, u, nil, false); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("expected the ordinary fallback while work remains, got %q", nm.Action)
	}

	nm := BuildGuidance(ini, u, nil, true)
	if !strings.Contains(nm.Action, "verify") {
		t.Fatalf("expected guidance to point at `idea verify` once every task is done, got %+v", nm)
	}

	// A pending change set still outranks "verify", even if some OTHER task's
	// change set is applied and allTasksDone was (incorrectly) passed true.
	recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-1", Status: domain.ChangeSetReviewRequired}}
	nm = BuildGuidance(ini, u, recent, true)
	if !strings.Contains(nm.Action, "CHG-1") {
		t.Fatalf("expected a pending review to still outrank verify, got %+v", nm)
	}
}

// TestLatestChangeSetsPerActiveTaskIncludesDoneTasksAwaitingReview is ICP
// re-run defect G: Execute marks a task `done` as soon as its change set
// passes checks -- before it is reviewed, approved, or applied -- so a task
// sitting on a change set awaiting human review/apply is `done` the entire
// time it waits. Excluding `done` tasks here hid exactly that change set from
// BuildGuidance.
func TestLatestChangeSetsPerActiveTaskIncludesDoneTasksAwaitingReview(t *testing.T) {
	changeSets := []domain.ChangeSet{
		{ID: "cs-1", HumanID: "CHG-1", TaskID: "task-1", Status: domain.ChangeSetReviewRequired},
	}
	tasks := []domain.Task{{ID: "task-1", Status: domain.TaskDone}}
	got := LatestChangeSetsPerActiveTask(changeSets, tasks)
	if len(got) != 1 || got[0].ID != "cs-1" {
		t.Fatalf("expected the done task's pending change set to still be reported, got %+v", got)
	}
}

// TestBuildGuidanceSurfacesOpenJudgmentBeforeProceeding is ICP re-run defect H
// (second clause): a founder-journey run reached plan_approved with DEC-3/
// DEC-4 still open (quick rigor auto-approved without requiring them
// resolved) and nothing anywhere said so -- guidance just said "ready to
// build" as if nothing was outstanding. The caveat is advisory (it must not
// block or change the recommended Action -- an open decision/question is a
// normal, expected part of an in-progress initiative), but it must be
// visible on every "proceed" message, not just the generic open-items nudge
// that only fires for OTHER statuses.
func TestBuildGuidanceSurfacesOpenJudgmentBeforeProceeding(t *testing.T) {
	open := Understanding{
		Decisions: []DecisionView{{Status: domain.DecisionOpen}},
		Questions: []QuestionView{{Status: domain.QuestionOpen}, {Status: domain.QuestionOpen}},
	}

	// InitiativeApproved: about to advance into design + the execution plan.
	nm := BuildGuidance(domain.Initiative{Status: domain.InitiativeApproved}, open, nil, false)
	if nm.Action != "Proceed to design & execution plan" {
		t.Fatalf("an open decision/question must not change the action, got %q", nm.Action)
	}
	if !strings.Contains(nm.Why, "1 decision(s)") || !strings.Contains(nm.Why, "2 question(s)") {
		t.Fatalf("expected the open decision/question count surfaced, got %q", nm.Why)
	}

	// InitiativePlanApproved, nothing pending: the "ready to build" message.
	nm = BuildGuidance(domain.Initiative{Status: domain.InitiativePlanApproved}, open, nil, false)
	if nm.Action != "Execution plan approved" {
		t.Fatalf("an open decision/question must not change the action, got %q", nm.Action)
	}
	if !strings.Contains(nm.Why, "1 decision(s)") || !strings.Contains(nm.Why, "2 question(s)") {
		t.Fatalf("expected the open decision/question count surfaced, got %q", nm.Why)
	}

	// InitiativePlanApproved, everything done: the new "run verify" message.
	nm = BuildGuidance(domain.Initiative{Status: domain.InitiativePlanApproved}, open, nil, true)
	if !strings.Contains(nm.Why, "1 decision(s)") || !strings.Contains(nm.Why, "2 question(s)") {
		t.Fatalf("expected the open decision/question count surfaced on the verify message, got %q", nm.Why)
	}

	// Nothing open: no caveat text at all.
	clean := Understanding{}
	nm = BuildGuidance(domain.Initiative{Status: domain.InitiativePlanApproved}, clean, nil, false)
	if strings.Contains(nm.Why, "still open") {
		t.Fatalf("expected no open-judgment caveat when nothing is open, got %q", nm.Why)
	}

	// A pending change-set review still outranks the caveat's own message
	// (it belongs on the "proceed" messages only): the review action fires
	// unconditionally regardless of open items.
	recent := []domain.ChangeSet{{ID: "cs-1", HumanID: "CHG-1", Status: domain.ChangeSetReviewRequired}}
	nm = BuildGuidance(domain.Initiative{Status: domain.InitiativePlanApproved}, open, recent, false)
	if !strings.Contains(nm.Action, "CHG-1") {
		t.Fatalf("expected the pending review to still take precedence, got %+v", nm)
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
	nm := BuildGuidance(ini, Understanding{}, nil, false)
	if strings.Contains(nm.Why, "Milestone 3") {
		t.Fatalf("plan_approved guidance still claims a future milestone: %q", nm.Why)
	}
}
