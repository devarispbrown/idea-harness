package projection

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func openProjTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestBuildTimelineScoping is the JTBD 6 regression test at the projection
// layer: a change set applied on one initiative must never appear on
// another's timeline.
func TestBuildTimelineScoping(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()

	iniA := &domain.Initiative{Title: "a", Description: "a"}
	iniB := &domain.Initiative{Title: "b", Description: "b"}
	if err := db.CreateInitiative(ctx, iniA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInitiative(ctx, iniB); err != nil {
		t.Fatal(err)
	}
	taskA := &domain.Task{InitiativeID: iniA.ID, Title: "rate limiting", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, taskA, nil, nil); err != nil {
		t.Fatal(err)
	}
	csA := &domain.ChangeSet{InitiativeID: iniA.ID, TaskID: taskA.ID, PatchDigest: "d"}
	if err := db.CreateChangeSet(ctx, csA); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, csA.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, csA.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, csA.ID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, store.HumanActor); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, csA.ID, domain.ChangeSetApproved, domain.ChangeSetApplying, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkChangeSetApplied(ctx, csA.ID, "digest", ""); err != nil {
		t.Fatal(err)
	}

	entriesA, err := BuildTimeline(ctx, db, iniA.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	entriesB, err := BuildTimeline(ctx, db, iniB.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range entriesA {
		if e.Kind == "change_set_applied" {
			found = true
			if e.Ref.Type != "change_set" || e.Ref.ID != csA.ID {
				t.Errorf("ref = %+v, want change_set/%s", e.Ref, csA.ID)
			}
		}
	}
	if !found {
		t.Fatal("iniA's timeline is missing its own change_set_applied entry")
	}
	for _, e := range entriesB {
		if e.Kind == "change_set_applied" {
			t.Fatalf("iniB's timeline leaked iniA's applied change set: %+v", e)
		}
	}
}

// TestBuildTimelineDeniedVsRejected pins the actor-based distinction (C1):
// a system-deny rejection (orchestrator actor) reads "denied"; a human
// reviewer's reject verdict reads "rejected".
func TestBuildTimelineDeniedVsRejected(t *testing.T) {
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

	// System deny: checks_running -> rejected, orchestrator actor.
	denied := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d"}
	if err := db.CreateChangeSet(ctx, denied); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, denied.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, denied.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	// Human reject: review_required -> rejected, human actor.
	rejected := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d2"}
	if err := db.CreateChangeSet(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, rejected.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, rejected.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, rejected.ID, domain.ChangeSetReviewRequired, domain.ChangeSetRejected, store.HumanActor); err != nil {
		t.Fatal(err)
	}

	entries, err := BuildTimeline(ctx, db, ini.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	kindByCS := map[string]string{}
	for _, e := range entries {
		if e.Ref.Type == "change_set" {
			kindByCS[e.Ref.ID] = e.Kind
		}
	}
	if kindByCS[denied.ID] != "change_set_denied" {
		t.Errorf("system-deny reject: kind = %q, want change_set_denied", kindByCS[denied.ID])
	}
	if kindByCS[rejected.ID] != "change_set_rejected" {
		t.Errorf("human reject: kind = %q, want change_set_rejected", kindByCS[rejected.ID])
	}
}

// TestBuildTimelineDecisionAndRunFailed pins decision_changed and run_failed
// entries.
func TestBuildTimelineDecisionAndRunFailed(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "which database?"}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "postgres", store.HumanActor); err != nil {
		t.Fatal(err)
	}
	if err := db.ReopenDecision(ctx, dec.ID, store.HumanActor); err != nil {
		t.Fatal(err)
	}

	opID, err := db.CreateOperation(ctx, "proj", ini.ID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetOperationFailed(ctx, opID, domain.OperationFailed, "provider 401"); err != nil {
		t.Fatal(err)
	}

	entries, err := BuildTimeline(ctx, db, ini.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawResolved, sawReopened, sawRunFailed bool
	for _, e := range entries {
		switch {
		case e.Kind == "decision_changed" && e.Ref.ID == dec.ID:
			if e.Title != "" {
				if contains(e.Title, "reopened") {
					sawReopened = true
				} else if contains(e.Title, "decided") {
					sawResolved = true
				}
			}
		case e.Kind == "run_failed":
			sawRunFailed = true
			if e.Detail != "provider 401" {
				t.Errorf("run_failed detail = %q, want the operation's error", e.Detail)
			}
		}
	}
	if !sawResolved {
		t.Error("missing a decided decision_changed entry")
	}
	if !sawReopened {
		t.Error("missing a reopened decision_changed entry")
	}
	if !sawRunFailed {
		t.Error("missing the run_failed entry")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestBuildTimelineSince pins the ?since filter: an entry at or before the
// given time is excluded.
func TestBuildTimelineSince(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	dec := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q"}
	if err := db.CreateDecision(ctx, dec); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, dec.ID, "r", store.HumanActor); err != nil {
		t.Fatal(err)
	}

	all, err := BuildTimeline(ctx, db, ini.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("expected at least one entry with since=0")
	}
	future := all[0].At.Add(1000000) // strictly after every entry
	filtered, err := BuildTimeline(ctx, db, ini.ID, future.UnixMilli(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 0 {
		t.Fatalf("since in the future should exclude everything, got %d entries", len(filtered))
	}
}
