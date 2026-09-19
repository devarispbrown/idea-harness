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
