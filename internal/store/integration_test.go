package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// The outbox is the restart-safety story for integrations: a row's
// idempotency key is unique, so re-ingesting the same event (a crash between
// "rows inserted" and "cursor advanced" can only ever re-offer rows) never
// queues a second delivery, and a delivered row never comes back.
func TestOutboxIdempotentAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.IntegrationCursor(ctx, "slack"); err != nil || ok {
		t.Fatalf("fresh cursor: ok=%v err=%v", ok, err)
	}
	row := OutboxInsert{Subscriber: "slack", IdempotencyKey: "slack:initiative.created:e1", EventSeq: 5, Kind: "initiative.created", PayloadJSON: `{}`}
	if err := db.IngestOutbox(ctx, "slack", 5, []OutboxInsert{row}); err != nil {
		t.Fatal(err)
	}
	next, err := db.NextOutbox(ctx, "slack")
	if err != nil || next == nil {
		t.Fatalf("next: %v %v", next, err)
	}
	if err := db.MarkOutboxDelivered(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// "Restart": a new handle on the same file re-offers the same event.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if seq, ok, _ := db.IntegrationCursor(ctx, "slack"); !ok || seq != 5 {
		t.Fatalf("cursor after reopen = %d,%v; want 5,true", seq, ok)
	}
	if err := db.IngestOutbox(ctx, "slack", 5, []OutboxInsert{row}); err != nil {
		t.Fatal(err)
	}
	if next, _ := db.NextOutbox(ctx, "slack"); next != nil {
		t.Fatalf("a delivered event was re-queued after restart: %+v", next)
	}
	st, err := db.IntegrationOutboxStats(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 1 || st.Pending != 0 || st.LastDeliveredAt == nil {
		t.Fatalf("stats = %+v", st)
	}
	// A cursor never moves backwards.
	if err := db.IngestOutbox(ctx, "slack", 2, nil); err != nil {
		t.Fatal(err)
	}
	if seq, _, _ := db.IntegrationCursor(ctx, "slack"); seq != 5 {
		t.Fatalf("cursor moved backwards to %d", seq)
	}
}

func TestOutboxFailureAndDead(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.IngestOutbox(ctx, "linear", 1, []OutboxInsert{
		{Subscriber: "linear", IdempotencyKey: "a", EventSeq: 1, Kind: "k", PayloadJSON: "{}"},
		{Subscriber: "linear", IdempotencyKey: "b", EventSeq: 2, Kind: "k", PayloadJSON: "{}"},
	}); err != nil {
		t.Fatal(err)
	}
	head, _ := db.NextOutbox(ctx, "linear")
	if head.IdempotencyKey != "a" {
		t.Fatalf("head = %s; want the lowest seq", head.IdempotencyKey)
	}
	if err := db.MarkOutboxFailed(ctx, head.ID, "boom", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	// Still the head: in-order delivery holds the row behind it.
	again, _ := db.NextOutbox(ctx, "linear")
	if again.ID != head.ID || again.Attempts != 1 || again.LastError != "boom" {
		t.Fatalf("after failure: %+v", again)
	}
	if err := db.MarkOutboxFailed(ctx, head.ID, "boom twice", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	next, _ := db.NextOutbox(ctx, "linear")
	if next == nil || next.IdempotencyKey != "b" {
		t.Fatalf("dead head should release the queue, got %+v", next)
	}
	st, _ := db.IntegrationOutboxStats(ctx, "linear")
	if st.Dead != 1 || st.Pending != 1 || st.LastError != "boom twice" {
		t.Fatalf("stats = %+v", st)
	}
}

func TestIntegrationLinks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if l, err := db.GetIntegrationLink(ctx, "linear", "task", "t1"); err != nil || l != nil {
		t.Fatalf("missing link: %v %v", l, err)
	}
	if err := db.PutIntegrationLink(ctx, IntegrationLink{Integration: "linear", Kind: "task", LocalID: "t1", RemoteID: "iss-1", RemoteKey: "ENG-1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutIntegrationLink(ctx, IntegrationLink{Integration: "linear", Kind: "task", LocalID: "t1", RemoteID: "iss-1", RemoteKey: "ENG-1", RemoteState: "canceled"}); err != nil {
		t.Fatal(err)
	}
	l, err := db.GetIntegrationLinkByRemote(ctx, "linear", "task", "iss-1")
	if err != nil || l == nil || l.LocalID != "t1" || l.RemoteState != "canceled" {
		t.Fatalf("by remote: %+v %v", l, err)
	}
	all, _ := db.ListIntegrationLinks(ctx, "linear", "")
	if len(all) != 1 {
		t.Fatalf("upsert produced %d rows", len(all))
	}
}

// PreviousInitiativeStatus skips `running` claims, which is what lets the bus
// tell a claim release (plan_approved -> running -> plan_approved) from a real
// transition into plan_approved.
func TestPreviousInitiativeStatusSkipsClaims(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.InitiativeStatus{domain.InitiativePlanApproved, domain.InitiativeRunning, domain.InitiativePlanApproved} {
		if err := db.SetInitiativeStatus(ctx, ini.ID, st, ActorOrchestrator); err != nil {
			t.Fatal(err)
		}
	}
	maxSeq, _ := db.MaxEventSeq(ctx)
	prev, err := db.PreviousInitiativeStatus(ctx, ini.ID, maxSeq, true)
	if err != nil {
		t.Fatal(err)
	}
	if prev != string(domain.InitiativePlanApproved) {
		t.Fatalf("previous = %q; want plan_approved (the running claim is skipped)", prev)
	}
	if err := db.RecordExternalNote(ctx, ini.ID, "linear", "ENG-1 canceled in Linear"); err != nil {
		t.Fatal(err)
	}
	evs, _ := db.ListEvents(ctx, maxSeq, 10)
	if len(evs) != 1 || evs[0].EventType != "initiative.external_note" {
		t.Fatalf("external note event = %+v", evs)
	}
}
