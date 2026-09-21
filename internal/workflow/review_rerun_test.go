package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// unreviewedChangeSet creates a change set for task that has reached
// review_required WITHOUT ever going through a completed adversarial review
// -- exactly the persisted shape a real run leaves behind when Execute's own
// call to runChangeSetAdversarialReview errors (e.g. the operation was
// cancelled mid-review) after the checks_running -> review_required
// transition already happened (ICP re-run defect #1). patchDigest/baseDigest
// let a caller reuse an already-persisted patch (e.g. borrowed from a real
// Execute() run) so RerunChangeSetReview's loadPatch succeeds; pass "" for a
// test that never loads the patch.
func unreviewedChangeSet(t *testing.T, eng *Engine, iniID, taskID, patchDigest, baseDigest string) domain.ChangeSet {
	t.Helper()
	ctx := context.Background()
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: taskID, PatchDigest: patchDigest, BaseSnapshotID: baseDigest}
	if err := eng.db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if err := eng.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := eng.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""); err != nil {
		t.Fatal(err)
	}
	got, err := eng.db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewCompleted() {
		t.Fatal("setup: change set should not have a completed review yet")
	}
	return *got
}

// TestApproveRefusesWhenReviewNeverCompleted is ICP re-run defect #1's core
// bug: a change set reaching review_required with only mechanical-check
// observations (no adversarial pass) used to be approvable and appliable
// exactly as if the review had genuinely converged with zero findings.
func TestApproveRefusesWhenReviewNeverCompleted(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	task := addTask(t, db, iniID, "add /health")
	cs := unreviewedChangeSet(t, eng, iniID, task.ID, "unused-digest", "unused-digest")

	err := eng.ReviewChangeSet(context.Background(), iniID, cs.ID, "approved")
	if err == nil {
		t.Fatal("approve succeeded on a change set whose review never completed")
	}
	if !strings.Contains(err.Error(), "rerun") {
		t.Errorf("refusal does not name the way forward (rerun): %v", err)
	}

	// Must not have silently moved anywhere.
	got, gerr := db.GetChangeSet(context.Background(), cs.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("change set moved to %q despite the refusal", got.Status)
	}
}

// TestApproveAcceptFindingsAlsoRefusesWhenReviewNeverCompleted covers the
// --accept-findings escape hatch: it must not be usable to route around the
// same refusal.
func TestApproveAcceptFindingsAlsoRefusesWhenReviewNeverCompleted(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	task := addTask(t, db, iniID, "add /health")
	cs := unreviewedChangeSet(t, eng, iniID, task.ID, "unused-digest", "unused-digest")

	err := eng.ReviewChangeSetAccepting(context.Background(), iniID, cs.ID, "approved")
	if err == nil {
		t.Fatal("--accept-findings approved a change set whose review never completed")
	}
	if !strings.Contains(err.Error(), "rerun") {
		t.Errorf("refusal does not name the way forward (rerun): %v", err)
	}
	_ = db
}

// TestRerunChangeSetReviewCompletesAndAllowsApprove is the way forward
// defect #1 asks for: an explicit, paid rerun of the adversarial review that
// actually completes it, after which approve (the round-3, no-model-call
// path) succeeds.
func TestRerunChangeSetReviewCompletesAndAllowsApprove(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	// A real, fully-reviewed change set, so its patch/base digests are
	// actually loadable from the blob store.
	reviewed := applyOneTask(t, eng, db, iniID)

	broken := addTask(t, db, iniID, "add /status")
	cs := unreviewedChangeSet(t, eng, iniID, broken.ID, reviewed.PatchDigest, reviewed.BaseSnapshotID)

	ctx := context.Background()
	beforeTask, err := db.GetTask(ctx, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeTask.Status == domain.TaskDone {
		t.Fatal("setup: task should not be done before the review ever completed")
	}

	if err := eng.RerunChangeSetReview(ctx, iniID, cs.ID); err != nil {
		t.Fatalf("rerun: %v", err)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReviewCompleted() {
		t.Fatal("rerun did not record a completed review")
	}
	if got.ReviewID == "" {
		t.Error("rerun did not stamp a review id")
	}
	if got.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("rerun changed the change set's status to %q", got.Status)
	}

	afterTask, err := db.GetTask(ctx, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterTask.Status != domain.TaskDone {
		t.Fatalf("expected the task marked done once the review actually completed, got %q", afterTask.Status)
	}

	// The round-3 property survives: approve after a COMPLETED review makes
	// no additional model call. There is no fp reference here (newWorkspaceFixture
	// does not expose it), so this checks the model call's own footprint
	// instead: the review id and the finding set rerun just wrote are exactly
	// what a fresh review call would also rewrite, and approve's plain path
	// (reviewChangeSet's own comment: "NOT skip it -- ... no fresh review, and
	// no EvaluateApproval [call]") never touches either.
	findingsBefore, ferr := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if ferr != nil {
		t.Fatal(ferr)
	}
	reviewIDBefore := got.ReviewID
	reviewCompletedBefore := *got.ReviewCompletedAt

	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("approve after a completed review: %v", err)
	}

	afterApprove, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterApprove.ReviewID != reviewIDBefore || !afterApprove.ReviewCompletedAt.Equal(reviewCompletedBefore) {
		t.Error("approve re-stamped the review after it had already completed -- it ran the reviewer again")
	}
	findingsAfter, ferr := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if len(findingsAfter) != len(findingsBefore) {
		t.Errorf("approve changed the finding count (%d -> %d) after a completed review -- it ran the reviewer again",
			len(findingsBefore), len(findingsAfter))
	}
}

// TestRerunChangeSetReviewRefusesWhenAlreadyCompleted: rerun is a paid,
// explicit action for the one case that needs it -- a review that never
// completed. Once a review has completed (the ordinary path), rerun must
// refuse rather than reintroduce the pre-round-3 "review runs again on every
// call" behavior.
func TestRerunChangeSetReviewRefusesWhenAlreadyCompleted(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(context.Background(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.ReviewCompleted() {
		t.Fatal("setup: a normal Execute() run should complete its review")
	}

	err = eng.RerunChangeSetReview(ctx, iniID, cs.ID)
	if err == nil {
		t.Fatal("rerun succeeded on a change set that already had a completed review")
	}
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("refusal does not say a review already completed: %v", err)
	}
}

// TestExecuteRefusesToRepickTaskWithUnreviewedChangeSet is the other half of
// defect #1's reported symptom: a task whose only change set never finished
// its review stayed `proposed`, so `idea execute` re-picked it and spent
// another implementer run producing a second change set while the first sat
// there unreviewed.
func TestExecuteRefusesToRepickTaskWithUnreviewedChangeSet(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	task := addTask(t, db, iniID, "add /health")
	cs := unreviewedChangeSet(t, eng, iniID, task.ID, "unused-digest", "unused-digest")

	ctx := context.Background()
	_, err := eng.Execute(ctx, iniID)
	if err == nil {
		t.Fatal("execute re-picked a task with an unreviewed open change set")
	}
	if !strings.Contains(err.Error(), "rerun") {
		t.Errorf("refusal does not name the way forward (rerun): %v", err)
	}

	css, lerr := db.ListChangeSetsByTask(ctx, task.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(css) != 1 || css[0].ID != cs.ID {
		t.Fatalf("execute created a duplicate change set for the same task: %+v", css)
	}
}

// TestExecutePreconditionCatchesUnreviewedPriorChangeSet is ICP re-run D5:
// `idea execute` printed "✓ execute started" before this guard existed,
// because Execute is asynchronous and every one of its refusals -- including
// this exact one, a task whose only change set never finished its
// adversarial review -- ran only inside the launchOperation goroutine,
// strictly after the daemon had already answered 202. A real founder-journey
// run hit exactly this case and it failed in under a millisecond, at zero
// model cost, yet still read as a started run.
//
// ExecutePrecondition exists so Runtime.ExecuteInitiativeFreshWithNote can
// run this same check SYNCHRONOUSLY, before ever creating an operation. This
// pins two things: that ExecutePrecondition itself refuses here, and that
// its refusal text matches Execute's own (they must not silently diverge,
// since ExecutePrecondition is documented as mirroring Execute's checks, not
// replacing them).
func TestExecutePreconditionCatchesUnreviewedPriorChangeSet(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	task := addTask(t, db, iniID, "add /health")
	_ = unreviewedChangeSet(t, eng, iniID, task.ID, "unused-digest", "unused-digest")

	ctx := context.Background()
	preErr := eng.ExecutePrecondition(ctx, iniID)
	if preErr == nil {
		t.Fatal("ExecutePrecondition let through a task whose only change set never finished its adversarial review")
	}
	if !strings.Contains(preErr.Error(), "never finished its adversarial review") {
		t.Errorf("ExecutePrecondition refusal missing the reason: %v", preErr)
	}
	if !strings.Contains(preErr.Error(), "rerun") {
		t.Errorf("ExecutePrecondition refusal does not name the way forward (rerun): %v", preErr)
	}

	_, execErr := eng.Execute(ctx, iniID)
	if execErr == nil {
		t.Fatal("Execute let through the same task ExecutePrecondition refused")
	}
	if execErr.Error() != preErr.Error() {
		t.Errorf("ExecutePrecondition's refusal diverged from Execute's own:\n  precondition: %v\n  execute:      %v", preErr, execErr)
	}
}
