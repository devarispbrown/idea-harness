package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func openRemoteRunDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

// The idempotency key is the claim: a second create with the same key --
// a retry after a crash, or two dispatches racing -- returns the first row
// untouched instead of inserting another, across a reopen of the database.
func TestRemoteRunCreateIsIdempotentAcrossReopen(t *testing.T) {
	ctx := context.Background()
	db, path := openRemoteRunDB(t)
	run := domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k1", InitiativeID: "ini", TaskID: "task", Attempt: 1, ServerURL: "https://oh.example"}
	first, created, err := db.CreateRemoteRun(ctx, run)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	if first.Status != domain.RemoteRunDispatching {
		t.Fatalf("new run status = %q, want dispatching", first.Status)
	}
	conv := "conv-1"
	if err := db.UpdateRemoteRun(ctx, first.ID, RemoteRunUpdate{ConversationID: &conv}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	again, created, err := db2.CreateRemoteRun(ctx, run)
	if err != nil || created {
		t.Fatalf("second create: created=%v err=%v (want the existing row)", created, err)
	}
	if again.ID != first.ID || again.ConversationID != "conv-1" {
		t.Fatalf("second create returned %+v, want the first row with its conversation", again)
	}
	all, err := db2.ListRemoteRuns(ctx, false, 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("rows = %d (%v), want 1", len(all), err)
	}
}

// A terminal status is sticky: a slow poller's late "running" must not
// resurrect a cancelled run, and finished_at is stamped once.
func TestRemoteRunTerminalStatusIsSticky(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	r, _, err := db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k", TaskID: "t", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	set := func(s domain.RemoteRunStatus) {
		t.Helper()
		if err := db.UpdateRemoteRun(ctx, r.ID, RemoteRunUpdate{Status: &s}); err != nil {
			t.Fatal(err)
		}
	}
	set(domain.RemoteRunRunning)
	set(domain.RemoteRunCancelled)
	set(domain.RemoteRunRunning)
	got, err := db.GetRemoteRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RemoteRunCancelled || got.FinishedAt == nil {
		t.Fatalf("status = %q finished_at = %v, want cancelled with a finish time", got.Status, got.FinishedAt)
	}
	active, err := db.ListRemoteRuns(ctx, true, 0)
	if err != nil || len(active) != 0 {
		t.Fatalf("active runs = %d (%v), want 0", len(active), err)
	}
	if n, err := db.CountTerminalRemoteRuns(ctx, "openhands", "t"); err != nil || n != 1 {
		t.Fatalf("terminal count = %d (%v), want 1", n, err)
	}
	bad := domain.RemoteRunStatus("bogus")
	if err := db.UpdateRemoteRun(ctx, r.ID, RemoteRunUpdate{Status: &bad}); err == nil {
		t.Fatal("an invalid status was accepted")
	}
	if err := db.UpdateRemoteRun(ctx, "missing", RemoteRunUpdate{}); !errors.Is(err, ErrRemoteRunNotFound) {
		t.Fatalf("update of a missing run: %v, want ErrRemoteRunNotFound", err)
	}
}

// Progress ingestion is guarded on the cursor the caller read: two
// ingesters over the same events append them once, and the log is bounded.
func TestRemoteRunProgressCursorGuard(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	r, _, err := db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k", TaskID: "t", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := db.AppendRemoteRunProgress(ctx, r.ID, "", "e2", 2, []string{"one", "two"})
	if err != nil || !ok {
		t.Fatalf("first append: ok=%v err=%v", ok, err)
	}
	// The loser of a race read the same old cursor.
	ok, err = db.AppendRemoteRunProgress(ctx, r.ID, "", "e2", 2, []string{"one", "two"})
	if err != nil || ok {
		t.Fatalf("stale-cursor append: ok=%v err=%v, want ok=false", ok, err)
	}
	got, _ := db.GetRemoteRun(ctx, r.ID)
	if got.Progress != "one\ntwo\n" || got.LastEventID != "e2" || got.EventCount != 2 {
		t.Fatalf("after race: progress=%q cursor=%q count=%d", got.Progress, got.LastEventID, got.EventCount)
	}

	long := strings.Repeat("x", 1000)
	lines := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		lines = append(lines, long)
	}
	if _, err := db.AppendRemoteRunProgress(ctx, r.ID, "e2", "e3", 100, lines); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetRemoteRun(ctx, r.ID)
	if len(got.Progress) > RemoteRunProgressMax {
		t.Fatalf("progress is %d bytes, over the %d cap", len(got.Progress), RemoteRunProgressMax)
	}
	if strings.HasPrefix(got.Progress, "one") || !strings.HasPrefix(got.Progress, long) {
		t.Fatal("trimming should drop the oldest lines and keep whole lines")
	}
}
