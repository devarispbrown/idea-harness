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

// TestBuildTimelineDeniedVsRejected pins the DenyCode-based distinction (M3,
// review fix): "denied" is reserved for a genuine system block -- a finding
// that actually carries a DenyCode (secret_sweep, host_exec, ...) -- never
// merely because the orchestrator was the actor. A plain check failure
// (build/tests -- orchestrator actor, but no DenyCode on its finding) and
// the restart-reconcile sweep (orchestrator actor, no findings at all) both
// read "rejected", same as a human reviewer's reject verdict; the review's
// own complaint was that all three used to read "denied" too.
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

	// A genuine system deny: checks_running -> rejected, orchestrator actor,
	// AND a finding carrying a real DenyCode.
	denied := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d"}
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

	// A plain check failure (M3's own bug): orchestrator actor, a finding
	// exists (a failing "tests" check), but it carries no DenyCode.
	checkFailed := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d3"}
	if err := db.CreateChangeSet(ctx, checkFailed); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: checkFailed.ID,
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "tests", Finding: "tests pass — FAIL", Disposition: domain.DispositionRejected,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, checkFailed.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, checkFailed.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	// The restart-reconcile sweep: orchestrator actor, no findings at all.
	interrupted := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d4"}
	if err := db.CreateChangeSet(ctx, interrupted); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, interrupted.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, interrupted.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, store.ActorOrchestrator); err != nil {
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
	detailByCS := map[string]string{}
	for _, e := range entries {
		if e.Ref.Type == "change_set" {
			kindByCS[e.Ref.ID] = e.Kind
			detailByCS[e.Ref.ID] = e.Title
		}
	}
	if kindByCS[denied.ID] != "change_set_denied" {
		t.Errorf("system-deny reject: kind = %q, want change_set_denied", kindByCS[denied.ID])
	}
	if kindByCS[checkFailed.ID] != "change_set_rejected" {
		t.Errorf("plain check failure: kind = %q, want change_set_rejected (never denied)", kindByCS[checkFailed.ID])
	}
	if kindByCS[interrupted.ID] != "change_set_rejected" {
		t.Errorf("restart-reconcile: kind = %q, want change_set_rejected (never denied)", kindByCS[interrupted.ID])
	}
	if !contains(detailByCS[interrupted.ID], "interrupted") {
		t.Errorf("restart-reconcile title = %q, want it to mention the interruption", detailByCS[interrupted.ID])
	}
	if kindByCS[rejected.ID] != "change_set_rejected" {
		t.Errorf("human reject: kind = %q, want change_set_rejected", kindByCS[rejected.ID])
	}
}

// TestBuildTimelinePhaseCompletedSkipsResumeAndFailed pins M3's second fix:
// "->running" (a resume, not a milestone) and "->failed" (already reported
// once as run_failed) never produce their own phase_completed entry.
func TestBuildTimelinePhaseCompletedSkipsResumeAndFailed(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeApproved, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeRunning, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeFailed, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	entries, err := BuildTimeline(ctx, db, ini.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind != "phase_completed" {
			continue
		}
		if contains(e.Title, "resumed") {
			t.Errorf("a ->running transition must not produce a phase_completed entry: %+v", e)
		}
		if contains(e.Title, "Workflow failed") {
			t.Errorf("a ->failed transition must not produce a phase_completed entry (run_failed already reports it): %+v", e)
		}
	}
	// The approved transition is still a real milestone and must survive.
	sawApproved := false
	for _, e := range entries {
		if e.Kind == "phase_completed" && contains(e.Title, "approved") {
			sawApproved = true
		}
	}
	if !sawApproved {
		t.Error("missing the PRD approved phase_completed entry")
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
