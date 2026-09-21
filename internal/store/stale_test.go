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

// TestMarkStaleKeyedOnTargetAndDecision is review M5: reopening TWO DIFFERENT
// decisions that both bear on the same requirement must yield TWO separate
// marks (keyed on (target, decision_id)), and acknowledging one must leave
// the other active. Before this fix, markStaleTx's idempotency check ignored
// decision_id, so the second decision's reopen found the target already
// marked (by the first decision) and silently dropped its own mark --
// acknowledging the first decision's mark alone then read as "nothing is
// stale here" even though the second decision was never separately
// acknowledged. Mutation-check: drop `AND decision_id = ?` from
// markStaleTx's idempotency SELECT, and this fails.
func TestMarkStaleKeyedOnTargetAndDecision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	decA := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "a?", Options: []string{"x", "y"}}
	if err := db.CreateDecision(ctx, decA); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, decA.ID, "x", "human"); err != nil {
		t.Fatal(err)
	}
	decB := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "b?", Options: []string{"x", "y"}}
	if err := db.CreateDecision(ctx, decB); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, decB.ID, "x", "human"); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, decA.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, req.ID, decB.ID); err != nil {
		t.Fatal(err)
	}

	if err := db.ReopenDecision(ctx, decA.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, decB.ID, "human"); err != nil {
		t.Fatal(err)
	}

	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	var forA, forB *domain.StaleItem
	for i := range items {
		if items[i].TargetType != "requirement" || items[i].TargetID != req.ID {
			continue
		}
		switch items[i].DecisionID {
		case decA.ID:
			forA = &items[i]
		case decB.ID:
			forB = &items[i]
		}
	}
	if forA == nil || forB == nil {
		t.Fatalf("expected one mark per decision on the shared requirement, got %+v", items)
	}

	// Acking decision A's mark must leave decision B's mark active.
	if err := db.ClearStaleItem(ctx, ini.ID, forA.ID, "human"); err != nil {
		t.Fatal(err)
	}
	after, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != forB.ID {
		t.Fatalf("expected only decision B's mark to remain after acking A's, got %+v", after)
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
// RevisePlan/ReopenPhase clears task and change-set marks, but -- review M5
// -- leaves a REQUIREMENT mark active: regenerating the plan does not
// re-verify the requirement itself against the decision that changed, so a
// fresh plan has done nothing to make that requirement-level staleness
// untrue. Mutation-check: drop the `target_type IN (...)` clause from
// ClearStaleItemsForInitiative's UPDATE, and this fails.
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
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}
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

	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}
	before, _ := db.ListStaleItems(ctx, ini.ID)
	if len(before) != 3 {
		t.Fatalf("expected 3 stale items (requirement, task, change_set) before clearing, got %+v", before)
	}

	if err := db.ClearStaleItemsForInitiative(ctx, ini.ID, "orchestrator"); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TargetType != "requirement" || items[0].TargetID != req.ID {
		t.Fatalf("expected only the requirement mark to survive, got %+v", items)
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

// TestResolveDecisionMarksExistingDependentsStale is ICP re-run defect H
// (third clause): quick rigor can auto-approve a plan -- and tasks can
// execute -- while a decision is still open, so a decision's FIRST resolve
// is no longer guaranteed to happen before anything depends on it. A human's
// answer given after plan approval (e.g. "keys in an env var") must mark the
// requirement/task/applied change set built without it stale, exactly as
// ReopenDecision already does for "changed my mind" -- otherwise the answer
// arrived and nothing on the record ever reflected it. Mutation-check: drop
// the markDependentsStaleTx call this adds to ResolveDecision, and this
// fails.
func TestResolveDecisionMarksExistingDependentsStale(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	// The decision is raised but left OPEN -- the quick-rigor-skip scenario --
	// while a requirement, task and applied change set are already built on
	// top of it.
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "where do secrets live?", Options: []string{"env var", "config file"}}
	if err := db.CreateDecision(ctx, dec); err != nil {
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

	// The human answers, resolving the decision for the FIRST time.
	if err := db.ResolveDecision(ctx, dec.ID, "env var", "human"); err != nil {
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
		t.Fatalf("expected requirement, task and the applied change set marked stale after the late resolve, got %+v", items)
	}
}

// TestReopenDecisionSkipsRemovedRequirement reproduces the ICP re-run's own
// defect #4: "DEC-3's reopen marked only removed requirements (REQ-4,
// REQ-13)". A requirement superseded by a PRD revision (SupersedeRequirements
// marks it `removed`, it does not delete the row or its derived_from edge)
// must never be surfaced as a dependent again -- a decision reopened after
// several revise cycles must not read as if a long-dead requirement is what
// still depends on it. Mutation-check: drop the liveReq filter in
// decisionDependents, and this fails.
func TestReopenDecisionSkipsRemovedRequirement(t *testing.T) {
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

	// A PRD revision supersedes every active requirement (this is exactly
	// what a revise cycle does): req is now `removed`, but its derived_from
	// edge to dec is untouched.
	if err := db.SupersedeRequirements(ctx, ini.ID, "run-x"); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].Status != domain.RequirementRemoved {
		t.Fatalf("setup: expected the requirement removed, got %+v", reqs)
	}

	if err := db.ReopenDecision(ctx, dec.ID, "human"); err != nil {
		t.Fatal(err)
	}

	items, err := db.ListStaleItems(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("a removed requirement must not be marked stale on its old decision's reopen, got %+v", items)
	}
}

// TestReopenDecisionMarksLiveRequirementNotARemovedOne pairs with the test
// above: when ONE of two requirements citing a decision is removed and the
// OTHER is still live, reopening the decision must mark only the live one.
func TestReopenDecisionMarksLiveRequirementNotARemovedOne(t *testing.T) {
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
	removed := &domain.Requirement{InitiativeID: ini.ID, Statement: "old", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, removed); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, removed.ID, dec.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeRequirements(ctx, ini.ID, "run-x"); err != nil {
		t.Fatal(err)
	}

	// A live requirement, wired to the same decision AFTER the supersede
	// (mirroring a regenerated requirement in a later revise cycle).
	live := &domain.Requirement{InitiativeID: ini.ID, Statement: "new", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, live); err != nil {
		t.Fatal(err)
	}
	if err := db.WireRequirementDerivedFromDecision(ctx, ini.ID, live.ID, dec.ID); err != nil {
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
	if !got["requirement:"+live.ID] {
		t.Fatalf("expected the live requirement marked stale, got %+v", items)
	}
	if got["requirement:"+removed.ID] {
		t.Fatalf("did not expect the removed requirement marked stale, got %+v", items)
	}
}
