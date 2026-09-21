package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// impactFixture creates an initiative with one requirement and one task, and
// returns a helper that creates and transitions a change set for that task
// to any status via TransitionChangeSet/MarkChangeSetApplied's own
// primitives -- exactly what Impact/ImpactCounts read.
func impactFixture(t *testing.T) (*Engine, *store.DB, string, string) {
	t.Helper()
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID, task.ID
}

// TestImpactChangeSetLabelIsNotDoublePrefixed is the low-priority review
// item: the change set's FromLabel used to read "CHG " + cs.HumanID, and
// HumanID is already "CHG-3", printing "CHG CHG-3". Mutation-check: put the
// "CHG " prefix back, and this fails.
func TestImpactChangeSetLabelIsNotDoublePrefixed(t *testing.T) {
	eng, db, iniID, taskID := impactFixture(t)
	ctx := context.Background()

	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: taskID, PatchDigest: "d1"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}

	items, err := eng.Impact(ctx, iniID, "task", taskID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if it.FromType == "change_set" && it.FromID == cs.ID {
			found = true
			if it.FromLabel != cs.HumanID {
				t.Errorf("FromLabel = %q, want %q (not double-prefixed)", it.FromLabel, cs.HumanID)
			}
			if strings.Count(it.FromLabel, "CHG") > 1 {
				t.Errorf("FromLabel = %q, CHG appears more than once", it.FromLabel)
			}
		}
	}
	if !found {
		t.Fatal("expected the change set to appear as a dependent of its task")
	}
}

// TestImpactExcludesRejectedAndSupersededChangeSets is the low-priority
// review item's other half: a rejected or superseded change set is dead and
// must not appear in Impact's listing or count toward ImpactCounts'
// "blocks N". Mutation-check: drop the `!cs.Status.HoldsWork()` guards in
// trace.go, and this fails.
func TestImpactExcludesRejectedAndSupersededChangeSets(t *testing.T) {
	eng, db, iniID, taskID := impactFixture(t)
	ctx := context.Background()

	live := &domain.ChangeSet{InitiativeID: iniID, TaskID: taskID, PatchDigest: "d1"}
	if err := db.CreateChangeSet(ctx, live); err != nil {
		t.Fatal(err)
	}

	rejected := &domain.ChangeSet{InitiativeID: iniID, TaskID: taskID, PatchDigest: "d2"}
	if err := db.CreateChangeSet(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, rejected.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, "test"); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, rejected.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, "test"); err != nil {
		t.Fatal(err)
	}

	superseded := &domain.ChangeSet{InitiativeID: iniID, TaskID: taskID, PatchDigest: "d3"}
	if err := db.CreateChangeSet(ctx, superseded); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetChangesRequested},
		{domain.ChangeSetChangesRequested, domain.ChangeSetSuperseded},
	} {
		if err := db.TransitionChangeSet(ctx, superseded.ID, step.from, step.to, "test"); err != nil {
			t.Fatal(err)
		}
	}

	items, err := eng.Impact(ctx, iniID, "task", taskID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if it.FromType == "change_set" {
			seen[it.FromID] = true
		}
	}
	if !seen[live.ID] {
		t.Error("expected the live change set to appear as a dependent")
	}
	if seen[rejected.ID] {
		t.Error("a rejected change set must not appear as a dependent")
	}
	if seen[superseded.ID] {
		t.Error("a superseded change set must not appear as a dependent")
	}

	counts, err := eng.ImpactCounts(ctx, iniID, []NodeRef{{Type: "task", ID: taskID}})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly 1: the live change set. The requirement is task's ancestor, not
	// its dependent, so it does not count here.
	if got := counts["task:"+taskID]; got != 1 {
		t.Fatalf("blocks count = %d, want 1 (only the live change set)", got)
	}
}
