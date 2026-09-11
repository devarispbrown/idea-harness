package store

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
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

func TestGetChangeSetByRefAmbiguousFailsClosed(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Two initiatives each assign their own CHG-1.
	_ = newTestChangeSet(t, db, "ini-1")
	_ = newTestChangeSet(t, db, "ini-2")

	// A bare CHG-1 must not silently resolve to an arbitrary initiative's set.
	if _, err := db.GetChangeSetByRef(ctx, "CHG-1"); err == nil {
		t.Fatal("expected an ambiguity error for cross-initiative CHG-1")
	}

	// A unique CHG-2 still resolves (and the full UUID resolves regardless).
	cs2 := newTestChangeSet(t, db, "ini-1")
	if cs2.HumanID != "CHG-2" {
		t.Fatalf("expected CHG-2, got %q", cs2.HumanID)
	}
	got, err := db.GetChangeSetByRef(ctx, "CHG-2")
	if err != nil {
		t.Fatalf("CHG-2 should resolve uniquely: %v", err)
	}
	if got.ID != cs2.ID {
		t.Fatalf("expected %q, got %q", cs2.ID, got.ID)
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

func TestReconcileChangeSetsOnlyTouchesStuck(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// An interrupted apply: change set in applying with prepared+workspace_mutated.
	applying := newTestChangeSet(t, db, "ini-1")
	setChangeSetStatus(t, db, applying.ID, domain.ChangeSetApplying)
	if err := db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: applying.ID, State: domain.ApplyPrepared}); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: applying.ID, State: domain.ApplyWorkspaceMutated, WorkspaceSnapshotDigest: "pre-digest"}); err != nil {
		t.Fatal(err)
	}

	// A cleanly APPLIED change set keeps its prepared/workspace_mutated rows but
	// must NOT be re-reconciled.
	applied := newTestChangeSet(t, db, "ini-1")
	setChangeSetStatus(t, db, applied.ID, domain.ChangeSetApplied)
	if err := db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: applied.ID, State: domain.ApplyWorkspaceMutated, WorkspaceSnapshotDigest: "applied-digest"}); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: applied.ID, State: domain.ApplyCommitted}); err != nil {
		t.Fatal(err)
	}

	// A checks_running change set.
	checking := newTestChangeSet(t, db, "ini-1")
	setChangeSetStatus(t, db, checking.ID, domain.ChangeSetChecksRunning)

	rec, err := db.ReconcileChangeSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 2 {
		t.Fatalf("expected 2 recoveries (applying + checks_running), got %d: %+v", len(rec), rec)
	}
	byID := map[string]ChangeSetRecovery{}
	for _, r := range rec {
		byID[r.ChangeSetID] = r
	}
	if r, ok := byID[applying.ID]; !ok || r.LatestJournalState != domain.ApplyWorkspaceMutated || r.PreImageDigest != "pre-digest" {
		t.Fatalf("applying change set mis-recovered: %+v", byID[applying.ID])
	}
	if r, ok := byID[checking.ID]; !ok || r.Status != domain.ChangeSetChecksRunning {
		t.Fatalf("checks_running change set mis-recovered: %+v", byID[checking.ID])
	}
	if _, ok := byID[applied.ID]; ok {
		t.Fatalf("applied change set must not be re-reconciled: %+v", byID[applied.ID])
	}
}

func TestReconcileChangeSetsIdempotentPreImageDigest(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	cs := newTestChangeSet(t, db, "ini-1")
	setChangeSetStatus(t, db, cs.ID, domain.ChangeSetApplying)
	if err := db.AppendJournal(ctx, &domain.ApplyJournalEntry{ChangeSetID: cs.ID, State: domain.ApplyWorkspaceMutated, WorkspaceSnapshotDigest: "pre-digest"}); err != nil {
		t.Fatal(err)
	}

	first, err := db.ReconcileChangeSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].PreImageDigest != "pre-digest" {
		t.Fatalf("first reconcile: %+v", first)
	}

	// A second pass (e.g. the daemon crashed before restoring) must still report
	// the pre-image digest so it can be restored, not degrade to approved.
	second, err := db.ReconcileChangeSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].PreImageDigest != "pre-digest" {
		t.Fatalf("second reconcile lost the pre-image digest: %+v", second)
	}
}

func TestApprovedToRejectedRecoveryTransition(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cs := newTestChangeSet(t, db, "ini-1")
	setChangeSetStatus(t, db, cs.ID, domain.ChangeSetApproved)

	// approved→rejected is the system-deny apply-boundary recovery transition.
	if err := db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetApproved, domain.ChangeSetRejected, HumanActor); err != nil {
		t.Fatalf("approved→rejected should be legal: %v", err)
	}
}

// TestAmbiguousChangeSetNamesTheCandidates pins the error TEXT, not just that an
// error happened.
//
// `idea next` prescribes `idea apply CHG-1`. With two initiatives in one project
// each holding a CHG-1 that command always failed, and the refusal said "use the
// full change set id" while no human-readable command printed one — `idea
// changes` is initiative-scoped and prints HumanID only, and the id lived solely
// in `idea changes --json`. `next` kept exiting 0 and prescribing the same
// failing command: a live-lock with no route out.
//
// TestGetChangeSetByRefAmbiguousFailsClosed asserts only `err != nil`, so it
// stays green if the lookup regresses to "not found" — which is why this test
// exists separately and reads the message.
func TestAmbiguousChangeSetNamesTheCandidates(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	one := &domain.Initiative{Title: "Tiered pricing", Status: domain.InitiativeRunning}
	two := &domain.Initiative{Title: "Recipe sharing", Status: domain.InitiativeRunning}
	for _, ini := range []*domain.Initiative{one, two} {
		if err := db.CreateInitiative(ctx, ini); err != nil {
			t.Fatal(err)
		}
	}
	csA := newTestChangeSet(t, db, one.ID)
	csB := newTestChangeSet(t, db, two.ID)

	_, err := db.GetChangeSetByRef(ctx, "CHG-1")
	if err == nil {
		t.Fatal("expected an ambiguity error for cross-initiative CHG-1")
	}
	msg := err.Error()

	// The full ids are the whole point: they are the only thing that resolves,
	// and before this they appeared in no human-readable output anywhere.
	for _, want := range []string{csA.ID, csB.ID} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not name the full id %q, so it prescribes no working command:\n%s", want, msg)
		}
	}
	// The titles are what let a reader tell the two candidates apart.
	for _, want := range []string{"Tiered pricing", "Recipe sharing"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not name initiative %q, so the ids are unattributable:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "ambiguous change set reference") {
		t.Errorf("error no longer reports ambiguity; a regression to not-found would be worse:\n%s", msg)
	}
}

// TestAmbiguousChangeSetSurvivesAMissingInitiative keeps the lookup degrading to
// the id rather than replacing a useful refusal with a database error. The
// fixtures elsewhere in this file create change sets against initiative ids that
// have no row, so this is the shape most of the suite exercises.
func TestAmbiguousChangeSetSurvivesAMissingInitiative(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	csA := newTestChangeSet(t, db, "ini-does-not-exist-1")
	csB := newTestChangeSet(t, db, "ini-does-not-exist-2")

	_, err := db.GetChangeSetByRef(ctx, "CHG-1")
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous change set reference") {
		t.Fatalf("a missing initiative row must not turn the refusal into something else:\n%s", msg)
	}
	for _, want := range []string{csA.ID, csB.ID} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not name %q:\n%s", want, msg)
		}
	}
}
