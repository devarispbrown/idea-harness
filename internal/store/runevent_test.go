package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func ev(kind domain.RunEventKind, title string) domain.RunEvent {
	return domain.RunEvent{Kind: kind, Title: title}
}

// seq is per run and monotonic, starting at 1, across separate appends; the
// run id on the event is the one appended under.
func TestRunEventsSeqIsPerRunAndMonotonic(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	a1, err := db.AppendRunEvents(ctx, "run-a", []domain.RunEvent{ev(domain.RunEventStatus, "one"), ev(domain.RunEventAction, "two")})
	if err != nil {
		t.Fatal(err)
	}
	b1, err := db.AppendRunEvents(ctx, "run-b", []domain.RunEvent{{Kind: domain.RunEventAction, Title: "b", RunID: "spoofed"}})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := db.AppendRunEvents(ctx, "run-a", []domain.RunEvent{ev(domain.RunEventMessage, "three")})
	if err != nil {
		t.Fatal(err)
	}
	if a1[0].Seq != 1 || a1[1].Seq != 2 || a2[0].Seq != 3 || b1[0].Seq != 1 {
		t.Fatalf("seqs: a=%d,%d,%d b=%d; want 1,2,3 and 1", a1[0].Seq, a1[1].Seq, a2[0].Seq, b1[0].Seq)
	}
	if b1[0].RunID != "run-b" {
		t.Fatalf("run id = %q, want the run it was appended under", b1[0].RunID)
	}
	if a1[0].At.IsZero() {
		t.Fatal("a stored event has no timestamp")
	}

	page, err := db.ListRunEvents(ctx, "run-a", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Title != "two" || page.FirstSeq != 1 || page.LastSeq != 3 || page.More {
		t.Fatalf("page after 1 = %+v", page)
	}
}

// Pages: limit bounds a page and More says there is another; after= the last
// seq of a page continues exactly where it stopped.
func TestRunEventsPaging(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	var evs []domain.RunEvent
	for i := 0; i < 7; i++ {
		evs = append(evs, ev(domain.RunEventAction, fmt.Sprint(i)))
	}
	if _, err := db.AppendRunEvents(ctx, "r", evs); err != nil {
		t.Fatal(err)
	}
	var got []string
	after := int64(0)
	for pages := 0; pages < 10; pages++ {
		page, err := db.ListRunEvents(ctx, "r", after, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			got = append(got, e.Title)
		}
		if !page.More {
			break
		}
		after = page.Events[len(page.Events)-1].Seq
	}
	if strings.Join(got, ",") != "0,1,2,3,4,5,6" {
		t.Fatalf("paged = %v", got)
	}
	all, err := db.ListAllRunEvents(ctx, "r")
	if err != nil || len(all) != 7 {
		t.Fatalf("all = %d (%v)", len(all), err)
	}
}

// Text is bounded at the store whatever the caller sent, and a kind no
// client knows is coerced rather than stored.
func TestRunEventsAreBounded(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	out, err := db.AppendRunEvents(ctx, "r", []domain.RunEvent{{Kind: "bogus", Title: strings.Repeat("t", 1000) + "\nsecond line",
		Detail: strings.Repeat("d", domain.RunEventDetailMax*3)}})
	if err != nil {
		t.Fatal(err)
	}
	e := out[0]
	if e.Kind != domain.RunEventAction || !e.Truncated || strings.Contains(e.Title, "second line") ||
		len(e.Title) > domain.RunEventTitleMax+len("…") || len(e.Detail) > domain.RunEventDetailMax+len("…") {
		t.Fatalf("stored = kind %q truncated %v title %d detail %d", e.Kind, e.Truncated, len(e.Title), len(e.Detail))
	}
}

// The per-run cap drops the oldest events but never reuses a seq, so a
// client's cursor stays meaningful and FirstSeq reports the gap.
func TestRunEventsPerRunCap(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	evs := make([]domain.RunEvent, RunEventsPerRunMax+5)
	for i := range evs {
		evs[i] = ev(domain.RunEventAction, fmt.Sprint(i))
	}
	if _, err := db.AppendRunEvents(ctx, "r", evs); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListRunEvents(ctx, "r", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.FirstSeq != 6 || page.LastSeq != int64(RunEventsPerRunMax+5) || page.Events[0].Seq != 6 {
		t.Fatalf("after cap: first %d last %d first event %d; want 6, %d, 6", page.FirstSeq, page.LastSeq, page.Events[0].Seq, RunEventsPerRunMax+5)
	}
	next, err := db.AppendRunEvents(ctx, "r", []domain.RunEvent{ev(domain.RunEventAction, "next")})
	if err != nil || next[0].Seq != int64(RunEventsPerRunMax+6) {
		t.Fatalf("next seq = %+v (%v)", next, err)
	}
}

// Only the newest RunEventsKeepRuns runs keep events.
func TestRunEventsKeepOnlyRecentRuns(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	for i := 0; i <= RunEventsKeepRuns; i++ {
		if _, err := db.AppendRunEvents(ctx, fmt.Sprintf("run-%03d", i), []domain.RunEvent{ev(domain.RunEventStatus, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	oldest, err := db.ListRunEvents(ctx, "run-000", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldest.Events) != 0 {
		t.Fatalf("the oldest run still has %d events; only %d runs keep theirs", len(oldest.Events), RunEventsKeepRuns)
	}
	newest, _ := db.ListRunEvents(ctx, fmt.Sprintf("run-%03d", RunEventsKeepRuns), 0, 0)
	second, _ := db.ListRunEvents(ctx, "run-001", 0, 0)
	if len(newest.Events) != 1 || len(second.Events) != 1 {
		t.Fatalf("recent runs lost events: newest %d, run-001 %d", len(newest.Events), len(second.Events))
	}
}

// RunEventsChanged is closed by the next append: a stream that took it before
// reading wakes for an append that lands after its read.
func TestRunEventsChangedSignalsAppends(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	ch := db.RunEventsChanged()
	select {
	case <-ch:
		t.Fatal("signalled before any append")
	default:
	}
	if _, err := db.AppendRunEvents(ctx, "r", []domain.RunEvent{ev(domain.RunEventStatus, "x")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("an append did not signal")
	}
	if db.RunEventsChanged() == ch {
		t.Fatal("a fresh wait got the already-closed channel")
	}
}

// The project-wide id orders events across runs for the project stream.
func TestRunEventsAfterIDAndLatestRun(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	if _, err := db.AppendRunEvents(ctx, "r1", []domain.RunEvent{{Kind: domain.RunEventStatus, Title: "a", InitiativeID: "ini"}}); err != nil {
		t.Fatal(err)
	}
	mark, _ := db.MaxRunEventID(ctx)
	if _, err := db.AppendRunEvents(ctx, "r2", []domain.RunEvent{{Kind: domain.RunEventStatus, Title: "b", InitiativeID: "ini"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListRunEventsAfterID(ctx, mark, 0)
	if err != nil || len(rows) != 1 || rows[0].Event.RunID != "r2" {
		t.Fatalf("after id %d: %+v (%v)", mark, rows, err)
	}
	latest, err := db.LatestEventRunForInitiative(ctx, "ini")
	if err != nil || latest != "r2" {
		t.Fatalf("latest run = %q (%v), want r2", latest, err)
	}
	if none, _ := db.LatestEventRunForInitiative(ctx, "other"); none != "" {
		t.Fatalf("latest run of an initiative with none = %q", none)
	}
}

// Remote progress and its run events share one transaction and one cursor
// guard: a racing second ingester of the same page stores neither, so the
// event stream is exactly-once like the progress log. Without a run id on
// the remote run, the progress is written and the events dropped.
func TestRemoteRunProgressEventsAreExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	r, _, err := db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k", InitiativeID: "ini", TaskID: "t", Attempt: 1, ServerURL: "https://x"})
	if err != nil {
		t.Fatal(err)
	}
	evs := []domain.RunEvent{ev(domain.RunEventToolCall, "terminal ls")}
	if _, err := db.AppendRemoteRunProgressEvents(ctx, r.ID, "", "e1", 1, []string{"→ ls"}, evs); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.MaxRunEventID(ctx); n != 0 {
		t.Fatalf("events stored with no agent run recorded: max id %d", n)
	}
	if err := db.SetRemoteRunAgentRun(ctx, r.ID, "agent-run"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetRemoteRunByAgentRun(ctx, "agent-run"); got == nil || got.ID != r.ID {
		t.Fatalf("by agent run = %+v", got)
	}
	ok, err := db.AppendRemoteRunProgressEvents(ctx, r.ID, "e1", "e2", 1, []string{"→ cat"}, evs)
	if err != nil || !ok {
		t.Fatalf("first ingest: %v %v", ok, err)
	}
	ok, err = db.AppendRemoteRunProgressEvents(ctx, r.ID, "e1", "e2", 1, []string{"→ cat"}, evs)
	if err != nil || ok {
		t.Fatalf("racing ingest of the same page applied=%v err=%v", ok, err)
	}
	page, _ := db.ListRunEvents(ctx, "agent-run", 0, 0)
	if len(page.Events) != 1 || page.Events[0].RunID != "agent-run" {
		t.Fatalf("agent run events = %+v, want exactly one", page.Events)
	}
}

func TestRunEventCostTotals(t *testing.T) {
	ctx := context.Background()
	db, _ := openRemoteRunDB(t)
	if _, err := db.AppendRunEvents(ctx, "r", []domain.RunEvent{
		{Kind: domain.RunEventCost, InputTokens: 10, OutputTokens: 2, CostUSD: 0.5},
		{Kind: domain.RunEventCost, InputTokens: 5, CostUSD: 0.25},
		{Kind: domain.RunEventAction, Title: "not a cost", InputTokens: 999},
	}); err != nil {
		t.Fatal(err)
	}
	in, out, cost, err := db.RunEventCostTotals(ctx, "r")
	if err != nil || in != 15 || out != 2 || cost != 0.75 {
		t.Fatalf("totals = %d %d %v (%v), want 15 2 0.75", in, out, cost, err)
	}
}
