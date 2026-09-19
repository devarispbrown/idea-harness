package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestMarkDecisionDependentsStaleNoOpWhenNothingDependsOnIt proves the common
// case (review A4 #5): a decision nothing cites yet reopens cleanly with no
// stale marks written.
func TestMarkDecisionDependentsStaleNoOpWhenNothingDependsOnIt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q?", Options: []string{"a", "b"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no stale items when nothing depends on the decision, got %+v", items)
	}
}

// TestMarkStaleIdempotent proves reopening the same decision twice does not
// pile up duplicate active marks for the same target.
func TestMarkStaleIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q?", Options: []string{"a", "b"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, dec.ID); err != nil {
		t.Fatal(err)
	}

	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "b", "human"); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range items {
		if it.TargetType == "requirement" && it.TargetID == req.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 active stale mark for the requirement after two reopens, got %d (%+v)", n, items)
	}
}

// TestClearStaleItem proves explicit acknowledgement clears one item without
// touching others (review A4 #5: "explicitly acknowledging the item clears
// the stale state").
func TestClearStaleItem(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q?", Options: []string{"a", "b"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	req1 := &domain.Requirement{InitiativeID: ini.ID, Statement: "r1", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req1); err != nil {
		t.Fatal(err)
	}
	req2 := &domain.Requirement{InitiativeID: ini.ID, Statement: "r2", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req2); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req1.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req2.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 stale items, got %+v", items)
	}
	if err := db.ClearStaleItem(ctx, ini.ID, items[0].ID, "human"); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != items[1].ID {
		t.Fatalf("expected exactly the other item to remain active, got %+v", remaining)
	}

	// Clearing an already-cleared (or unknown) id is refused, not a silent
	// no-op -- mirrors ResolveDecision/ReopenDecision's own state guard.
	if err := db.ClearStaleItem(ctx, ini.ID, items[0].ID, "human"); err == nil {
		t.Fatal("expected clearing an already-cleared stale item to fail")
	}
}

// TestClearStaleItemsForInitiative proves the bulk-clear path used by
// RevisePlan/ReopenPhase.
func TestClearStaleItemsForInitiative(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q?", Options: []string{"a", "b"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if items, _ := db.ListStaleItems(ctx, ini.ID); len(items) == 0 {
		t.Fatal("expected at least one stale item before clearing")
	}

	if err := db.ClearStaleItemsForInitiative(ctx, ini.ID, "orchestrator"); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected all stale items cleared, got %+v", items)
	}
}

// TestReopenDecisionMarksTaskAndAppliedChangeSetStale proves the full
// dependent chain (requirement -> task -> applied change set) at the store
// layer, complementing the workflow-package Impact-focused test.
func TestReopenDecisionMarksTaskAndAppliedChangeSetStale(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q?", Options: []string{"a", "b"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}

	// One applied change set and one still-draft change set: only the applied
	// one should be marked stale.
	applied := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d1"}
	if err := db.CreateChangeSet(ctx, applied); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, applied.ID, step.from, step.to, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, applied.ID, "digest", "test"); err != nil {
		t.Fatal(err)
	}
	draft := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d2"}
	if err := db.CreateChangeSet(ctx, draft); err != nil {
		t.Fatal(err)
	}

	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.TargetType+":"+it.TargetID] = true
	}
	if !got["requirement:"+req.ID] || !got["task:"+task.ID] || !got["change_set:"+applied.ID] {
		t.Fatalf("expected requirement, task and the applied change set marked stale, got %+v", items)
	}
	if got["change_set:"+draft.ID] {
		t.Fatalf("did not expect the draft (unapplied) change set to be marked stale, got %+v", items)
	}
}
