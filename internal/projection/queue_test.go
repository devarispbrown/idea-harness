package projection

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestBuildQueueRanksBlockingFirst pins D-b: an open decision on an
// initiative parked at needs_human_judgment is blocking (lower rank) than
// one on a running initiative, regardless of creation order.
func TestBuildQueueRanksBlockingFirst(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()

	blockedIni := &domain.Initiative{Title: "blocked", Description: "d"}
	if err := db.CreateInitiative(ctx, blockedIni); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, blockedIni.ID, domain.InitiativeNeedsHumanJudgment, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	blockedDec := &domain.Decision{InitiativeID: blockedIni.ID, Type: domain.DecisionProduct, Question: "blocking one"}
	if err := db.CreateDecision(ctx, blockedDec); err != nil {
		t.Fatal(err)
	}

	runningIni := &domain.Initiative{Title: "running", Description: "d"}
	if err := db.CreateInitiative(ctx, runningIni); err != nil {
		t.Fatal(err)
	}
	// Running is the default status CreateInitiative sets; an open decision on
	// it is live but not blocking anything right now.
	runningDec := &domain.Decision{InitiativeID: runningIni.ID, Type: domain.DecisionProduct, Question: "non-blocking one"}
	if err := db.CreateDecision(ctx, runningDec); err != nil {
		t.Fatal(err)
	}

	items, err := BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(items), items)
	}
	// Pre-sorted by rank ascending: the blocking one must come first.
	if items[0].InitiativeID != blockedIni.ID {
		t.Fatalf("first item is %+v, want the blocking decision on %s", items[0], blockedIni.ID)
	}
	if items[1].InitiativeID != runningIni.ID {
		t.Fatalf("second item is %+v, want the non-blocking decision on %s", items[1], runningIni.ID)
	}
	if items[0].Rank >= items[1].Rank {
		t.Fatalf("blocking item's rank (%d) is not lower (more urgent) than the non-blocking one's (%d)", items[0].Rank, items[1].Rank)
	}
}

// TestBuildQueueShortTitleFallsBackToTitle pins the short_title||title rule
// on queue rows for an initiative created before short titles existed (empty
// column) -- exercised directly rather than through GetInitiative's own lazy
// backfill, which BuildQueue's ListInitiatives call also benefits from.
func TestBuildQueueShortTitleFallsBackToTitle(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "The Full Title", Description: ""}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q"}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	items, err := BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].ShortTitle != "The Full Title" {
		t.Errorf("ShortTitle = %q, want fallback to Title", items[0].ShortTitle)
	}
}

// TestBuildQueueChangeSetStates pins review/conflict/denial rows.
func TestBuildQueueChangeSetStates(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}

	review := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d1"}
	if err := db.CreateChangeSet(ctx, review); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, review.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, review.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}

	denied := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d2"}
	if err := db.CreateChangeSet(ctx, denied); err != nil {
		t.Fatal(err)
	}
	// A system-deny finding (H3, review fix): a denial only stays in the
	// queue when a finding actually carries a DenyCode -- a bare status
	// transition with no findings (a plain human reject, or the
	// restart-reconcile sweep) must not.
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: denied.ID,
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "secret_sweep", Code: domain.DenyCodeSecretSweep, Finding: "credential detected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, denied.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, denied.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	items, err := BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, it := range items {
		kinds[it.Kind] = true
	}
	if !kinds["review"] {
		t.Errorf("missing a review item: %+v", items)
	}
	if !kinds["denial"] {
		t.Errorf("missing a denial item: %+v", items)
	}
}

// TestBuildQueueDenialHiddenOncePlainRejectOrSuperseded pins the rest of H3:
// a plain human reject or check failure (no DenyCode) never shows as a
// denial, and a rejected change set superseded by a LATER attempt on the
// SAME task drops out once that later attempt exists -- the reviewer's own
// complaint was that a denial stayed "at the top tier permanently" even
// after a successful re-run.
func TestBuildQueueDenialHiddenOncePlainRejectOrSuperseded(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}

	// A plain check failure: rejected, orchestrator actor, a finding exists
	// but carries no DenyCode.
	plainFail := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d1"}
	if err := db.CreateChangeSet(ctx, plainFail); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: plainFail.ID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass — FAIL", Disposition: domain.DispositionRejected,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, plainFail.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, plainFail.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	items, err := BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == "denial" {
			t.Errorf("a plain check failure (no DenyCode) must not show as a denial: %+v", it)
		}
	}

	// A different task: a genuine system deny, later superseded by a
	// successful applied change set on that same task.
	task2 := &domain.Task{InitiativeID: ini.ID, Title: "t2", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task2, nil, nil); err != nil {
		t.Fatal(err)
	}
	denied := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task2.ID, PatchDigest: "d2"}
	if err := db.CreateChangeSet(ctx, denied); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: denied.ID,
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "secret_sweep", Code: domain.DenyCodeSecretSweep, Finding: "credential detected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, denied.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, denied.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	items, err = BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	sawDenial := false
	for _, it := range items {
		if it.Kind == "denial" {
			sawDenial = true
		}
	}
	if !sawDenial {
		t.Fatal("the only (and latest) change set on task2 is a genuine system deny; it must show")
	}

	// A later, successful attempt on the same task supersedes it.
	rerun := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task2.ID, PatchDigest: "d3"}
	if err := db.CreateChangeSet(ctx, rerun); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, rerun.ID, step.from, step.to, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, rerun.ID, "digest", ""); err != nil {
		t.Fatal(err)
	}
	items, err = BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == "denial" {
			t.Errorf("task2's denial is superseded by a later applied attempt and must no longer show: %+v", it)
		}
	}
}

// TestBuildQueueRankIsASmallOrdinal pins H6: Rank is 1..N, not the internal
// tier*age sort key (which routinely exceeds JavaScript's safe-integer
// range and rendered as a garbled number in the UI).
func TestBuildQueueRankIsASmallOrdinal(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		ini := &domain.Initiative{Title: "i", Description: "i"}
		if err := db.CreateInitiative(ctx, ini); err != nil {
			t.Fatal(err)
		}
		dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q"}
		if err := db.CreateDecision(ctx, dec); err != nil {
			t.Fatal(err)
		}
	}
	items, err := BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	for i, it := range items {
		want := int64(i + 1)
		if it.Rank != want {
			t.Errorf("item %d: rank = %d, want the ordinal %d", i, it.Rank, want)
		}
	}
}

// TestBuildQueueVerifyOnlyOffersApprovedRequirements pins H4: the "ready to
// confirm" item mirrors confirm_passing_tests' own eligibility -- only an
// `approved` requirement, never draft/verified/failed/waived/removed, even
// with all-passing evidence.
func TestBuildQueueVerifyOnlyOffersApprovedRequirements(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", HumanID: "CHG-1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass", Evidence: "ok 1 - REQ-1 works\nok 2 - REQ-2 works\n",
	}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, cs.ID, step.from, step.to, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, cs.ID, "digest", ""); err != nil {
		t.Fatal(err)
	}

	approved := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-1", Statement: "s", Priority: domain.PriorityMust, Status: domain.RequirementApproved}
	failed := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-2", Statement: "s", Priority: domain.PriorityMust, Status: domain.RequirementFailed}
	if err := db.CreateRequirement(ctx, approved); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRequirement(ctx, failed); err != nil {
		t.Fatal(err)
	}

	items, err := BuildQueue(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, it := range items {
		if it.Kind == "verify" {
			labels[it.Label] = true
		}
	}
	sawREQ1, sawREQ2 := false, false
	for l := range labels {
		if len(l) >= 5 && l[:5] == "REQ-1" {
			sawREQ1 = true
		}
		if len(l) >= 5 && l[:5] == "REQ-2" {
			sawREQ2 = true
		}
	}
	if !sawREQ1 {
		t.Errorf("REQ-1 (approved, all tests passed) must be offered: %+v", items)
	}
	if sawREQ2 {
		t.Errorf("REQ-2 (failed, not approved) must never be offered, even with passing evidence: %+v", items)
	}
}
