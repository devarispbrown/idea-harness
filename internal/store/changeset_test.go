package store

import (
	"context"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
)

func newTestChangeSet(t *testing.T, db *DB, initiativeID string) *domain.ChangeSet {
	t.Helper()
	cs := &domain.ChangeSet{
		InitiativeID: initiativeID,
		TaskID:       "task-1",
		PatchDigest:  "patch-abc",
	}
	if err := db.CreateChangeSet(context.Background(), cs); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}
	return cs
}

// setChangeSetStatus forces a change set into the given state via direct SQL so
// transition tests can exercise any node of the matrix.
func setChangeSetStatus(t *testing.T, db *DB, id string, status domain.ChangeSetStatus) {
	t.Helper()
	if _, err := db.SQL().Exec(`UPDATE change_sets SET status = ? WHERE id = ?`, status, id); err != nil {
		t.Fatalf("force status %q: %v", status, err)
	}
}

func TestCreateChangeSetAssignsHumanID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	cs1 := newTestChangeSet(t, db, "ini-1")
	if cs1.HumanID != "CHG-1" {
		t.Fatalf("expected CHG-1, got %q", cs1.HumanID)
	}
	if cs1.Status != domain.ChangeSetDraft {
		t.Fatalf("expected draft, got %q", cs1.Status)
	}

	cs2 := newTestChangeSet(t, db, "ini-1")
	if cs2.HumanID != "CHG-2" {
		t.Fatalf("expected CHG-2, got %q", cs2.HumanID)
	}

	// A different initiative gets its own CHG-1.
	cs3 := newTestChangeSet(t, db, "ini-2")
	if cs3.HumanID != "CHG-1" {
		t.Fatalf("expected CHG-1 for second initiative, got %q", cs3.HumanID)
	}

	got, err := db.GetChangeSet(ctx, cs1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HumanID != "CHG-1" || got.PatchDigest != "patch-abc" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	list, err := db.ListChangeSets(ctx, "ini-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 change sets for ini-1, got %d", len(list))
	}
}

func TestLegalChangeSetTransitions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Happy path: draft -> checks_running -> review_required -> approved -> applying -> applied.
	cs := newTestChangeSet(t, db, "ini-1")
	steps := []domain.ChangeSetStatus{
		domain.ChangeSetChecksRunning,
		domain.ChangeSetReviewRequired,
		domain.ChangeSetApproved,
		domain.ChangeSetApplying,
		domain.ChangeSetApplied,
	}
	from := domain.ChangeSetDraft
	for _, to := range steps {
		if err := db.TransitionChangeSet(ctx, cs.ID, from, to, ""); err != nil {
			t.Fatalf("transition %q -> %q: %v", from, to, err)
		}
		from = to
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("expected applied, got %q", got.Status)
	}

	// Branch: checks_running -> rejected.
	cs2 := newTestChangeSet(t, db, "ini-1")
	if err := db.TransitionChangeSet(ctx, cs2.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionChangeSet(ctx, cs2.ID, domain.ChangeSetChecksRunning, domain.ChangeSetRejected, ""); err != nil {
		t.Fatal(err)
	}

	// Branch: review_required -> changes_requested -> superseded.
	cs3 := newTestChangeSet(t, db, "ini-1")
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetChangesRequested},
		{domain.ChangeSetChangesRequested, domain.ChangeSetSuperseded},
	} {
		if err := db.TransitionChangeSet(ctx, cs3.ID, step.from, step.to, ""); err != nil {
			t.Fatalf("transition %q -> %q: %v", step.from, step.to, err)
		}
	}

	// Branch: applying -> conflicted.
	cs4 := newTestChangeSet(t, db, "ini-1")
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
		{domain.ChangeSetApplying, domain.ChangeSetConflicted},
	} {
		if err := db.TransitionChangeSet(ctx, cs4.ID, step.from, step.to, ""); err != nil {
			t.Fatalf("transition %q -> %q: %v", step.from, step.to, err)
		}
	}

	// Branch: applied -> reverted (rollback).
	cs5 := newTestChangeSet(t, db, "ini-1")
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
		{domain.ChangeSetApplying, domain.ChangeSetApplied},
		{domain.ChangeSetApplied, domain.ChangeSetReverted},
	} {
		if err := db.TransitionChangeSet(ctx, cs5.ID, step.from, step.to, ""); err != nil {
			t.Fatalf("transition %q -> %q: %v", step.from, step.to, err)
		}
	}
}

func TestIllegalChangeSetTransitionsRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	illegal := []struct {
		from, to domain.ChangeSetStatus
	}{
		{domain.ChangeSetDraft, domain.ChangeSetApproved},            // skip checks/review
		{domain.ChangeSetDraft, domain.ChangeSetDraft},               // self-loop
		{domain.ChangeSetDraft, domain.ChangeSetApplied},             // skip everything
		{domain.ChangeSetChecksRunning, domain.ChangeSetApproved},    // skip review_required
		{domain.ChangeSetReviewRequired, domain.ChangeSetApplying},   // skip approved
		{domain.ChangeSetReviewRequired, domain.ChangeSetSuperseded}, // superseded only from changes_requested
		{domain.ChangeSetChangesRequested, domain.ChangeSetApproved}, // wrong direction
		{domain.ChangeSetApproved, domain.ChangeSetApplied},          // skip applying
		{domain.ChangeSetApplied, domain.ChangeSetDraft},             // terminal -> anything
		{domain.ChangeSetRejected, domain.ChangeSetDraft},            // terminal -> anything
		{domain.ChangeSetSuperseded, domain.ChangeSetDraft},          // terminal -> anything
		{domain.ChangeSetConflicted, domain.ChangeSetDraft},          // terminal -> anything
	}

	for _, tc := range illegal {
		cs := newTestChangeSet(t, db, "ini-1")
		// Force the change set into the source state so that, if the matrix were
		// buggy and allowed the transition, the UPDATE would succeed and the
		// test would catch it via the nil-error assertion.
		setChangeSetStatus(t, db, cs.ID, tc.from)

		err := db.TransitionChangeSet(ctx, cs.ID, tc.from, tc.to, "")
		if err == nil {
			t.Fatalf("expected illegal transition %q -> %q to fail", tc.from, tc.to)
		}
		got, getErr := db.GetChangeSet(ctx, cs.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got.Status != tc.from {
			t.Fatalf("transition %q -> %q mutated status to %q", tc.from, tc.to, got.Status)
		}
	}
}

func TestTransitionStaleFromRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	cs := newTestChangeSet(t, db, "ini-1")
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err != nil {
		t.Fatal(err)
	}

	// Legal pair, but the change set is no longer in `from`.
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""); err == nil {
		t.Fatal("expected stale from-state transition to fail")
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetChecksRunning {
		t.Fatalf("status should be unchanged, got %q", got.Status)
	}
}

func TestChangeSetReviewAndCommit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	cs := newTestChangeSet(t, db, "ini-1")

	if err := db.SetChangeSetReview(ctx, cs.ID, "review-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChangeSetCommit(ctx, cs.ID, "abc1234", ""); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewID != "review-1" {
		t.Fatalf("expected review id, got %q", got.ReviewID)
	}
	if got.GitCommitSHA != "abc1234" {
		t.Fatalf("expected commit sha, got %q", got.GitCommitSHA)
	}

	// Setting on a missing change set must fail clearly.
	if err := db.SetChangeSetReview(ctx, "missing", "review-2", ""); err == nil {
		t.Fatal("expected SetChangeSetReview on missing id to fail")
	}
	if err := db.SetChangeSetCommit(ctx, "missing", "deadbeef", ""); err == nil {
		t.Fatal("expected SetChangeSetCommit on missing id to fail")
	}
}

func TestJournalAppendAndList(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	cs := newTestChangeSet(t, db, "ini-1")

	e1 := &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: domain.ApplyPrepared}
	if err := db.AppendJournal(ctx, e1); err != nil {
		t.Fatal(err)
	}
	if e1.ID == "" {
		t.Fatal("expected journal id to be assigned")
	}
	e2 := &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: domain.ApplyWorkspaceMutated}
	if err := db.AppendJournal(ctx, e2); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListJournal(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 journal rows, got %d", len(list))
	}
	if list[0].State != domain.ApplyPrepared || list[1].State != domain.ApplyWorkspaceMutated {
		t.Fatalf("unexpected ordering/states: %+v", list)
	}
}

func TestReconcileJournalMarksNonTerminal(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	cs := newTestChangeSet(t, db, "ini-1")

	append := func(state domain.ApplyState) {
		t.Helper()
		if err := db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	append(domain.ApplyPrepared)
	append(domain.ApplyWorkspaceMutated)
	append(domain.ApplyCommitted)
	append(domain.ApplyRolledBack)

	needs, err := db.ReconcileJournal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(needs) != 2 {
		t.Fatalf("expected 2 rows needing recovery, got %d: %+v", len(needs), needs)
	}
	for _, e := range needs {
		if e.State != domain.ApplyRecoveryRequired {
			t.Fatalf("expected recovery_required, got %q", e.State)
		}
		if e.ChangeSetID != cs.ID {
			t.Fatalf("unexpected change set id %q", e.ChangeSetID)
		}
	}

	// The marking must be durable and idempotent: a second pass finds nothing.
	needs2, err := db.ReconcileJournal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(needs2) != 0 {
		t.Fatalf("expected no rows on second reconcile, got %d", len(needs2))
	}

	list, err := db.ListJournal(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("expected 4 journal rows, got %d", len(list))
	}
}
