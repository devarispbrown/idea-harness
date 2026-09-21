package daemon

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestReconcileStaleOperationsRecoversReapplyToReverted is H3, exercised at
// the daemon's real startup-reconciliation path (reconcileStaleOperations ->
// store.ReconcileChangeSets): a change set applied once, reverted, and then
// crashed mid-re-apply before any byte was written must recover back to
// `reverted`, not the old hardcoded `approved` -- CHANGELOG.md's own
// documented promise, and the reverted->applying edge
// (changeset.go:legalChangeSetTransition) exists specifically so this
// recovery is legal.
func TestReconcileStaleOperationsRecoversReapplyToReverted(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: "d"}
	if err := pr.db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pr.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""))
	must(pr.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""))
	must(pr.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, ""))

	// First apply: fully committed.
	must(pr.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetApproved, domain.ChangeSetApplying, ""))
	must(pr.db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: domain.ApplyPrepared}))
	must(pr.db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: domain.ApplyWorkspaceMutated, WorkspaceSnapshotDigest: "first-apply-digest"}))
	must(pr.db.MarkChangeSetApplied(ctx, cs.ID, "first-apply-digest", ""))

	// Reverted.
	must(pr.db.MarkChangeSetReverted(ctx, cs.ID, ""))

	// Re-apply begins and crashes before any byte is written for THIS attempt.
	must(pr.db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetReverted, domain.ChangeSetApplying, ""))
	must(pr.db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: domain.ApplyPrepared}))

	rt.reconcileStaleOperations(ctx, pr)

	got, err := pr.db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReverted {
		t.Fatalf("H3: crash-recovery left the change set %q, want reverted "+
			"(it was re-applying from reverted, not a fresh apply from approved)", got.Status)
	}
}
