package integrations

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// recorder is a fake subscriber. fail, when set, decides each delivery's error.
type recorder struct {
	name  string
	kinds map[Kind]bool // nil = everything
	mu    sync.Mutex
	got   []Event
	fail  func(ev Event, attempt int) error
	tries map[string]int
}

func (r *recorder) Name() string { return r.name }
func (r *recorder) Wants(ev Event) bool {
	return r.kinds == nil || r.kinds[ev.Kind]
}
func (r *recorder) Deliver(_ context.Context, ev Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tries == nil {
		r.tries = map[string]int{}
	}
	r.tries[ev.SourceEventID]++
	if r.fail != nil {
		if err := r.fail(ev, r.tries[ev.SourceEventID]); err != nil {
			return err
		}
	}
	r.got = append(r.got, ev)
	return nil
}
func (r *recorder) kindsGot() []Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Kind{}
	for _, e := range r.got {
		out = append(out, e.Kind)
	}
	return out
}

func openDB(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newInitiative(t *testing.T, db *store.DB, title string) domain.Initiative {
	t.Helper()
	ini := &domain.Initiative{Title: title, Description: title}
	if err := db.CreateInitiative(context.Background(), ini); err != nil {
		t.Fatal(err)
	}
	return *ini
}

func TestKindsMatchConfigVocabulary(t *testing.T) {
	if len(AllKinds) != len(config.IntegrationEventKinds) {
		t.Fatalf("AllKinds has %d, config has %d", len(AllKinds), len(config.IntegrationEventKinds))
	}
	for i, k := range AllKinds {
		if string(k) != config.IntegrationEventKinds[i] {
			t.Errorf("kind %d: %q vs config %q", i, k, config.IntegrationEventKinds[i])
		}
	}
}

// The translation rules, end to end over a real store: what the engine's own
// writes turn into, and -- as importantly -- what they do NOT turn into.
func TestTranslateWorkflowToDomainEvents(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "state.db"))
	rec := &recorder{name: "rec"}
	d := NewDispatcher(db, "p1", []Subscriber{rec}, nil, Options{})
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}

	ini := newInitiative(t, db, "Dark mode")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.NeedsHumanJudgment(ctx, ini.ID, domain.JudgmentPRDReview, store.ActorOrchestrator))
	must(db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeApproved, store.HumanActor))
	must(db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativePlanApproved, store.HumanActor))
	// An execute's claim and release: NOT a second plan approval.
	must(db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativeRunning, store.ActorOrchestrator))
	must(db.SetInitiativeStatus(ctx, ini.ID, domain.InitiativePlanApproved, store.ActorOrchestrator))

	task := &domain.Task{InitiativeID: ini.ID, Title: "Toggle", Statement: "s", Status: domain.TaskReady, Priority: domain.RequirementPriority("must")}
	must(db.CreateTask(ctx, task, "", nil))
	must(db.SetTaskStatus(ctx, task.ID, domain.TaskDone))
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "x"}
	must(db.CreateChangeSet(ctx, cs))
	must(db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, ""))
	must(db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, ""))
	must(db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetReviewRequired, domain.ChangeSetApproved, store.HumanActor))
	must(db.TransitionChangeSet(ctx, cs.ID, domain.ChangeSetApproved, domain.ChangeSetApplying, ""))
	must(db.MarkChangeSetApplied(ctx, cs.ID, "snap", ""))
	must(db.MarkChangeSetReverted(ctx, cs.ID, ""))
	must(db.CompleteInitiative(ctx, ini.ID, store.ActorOrchestrator))
	must(db.FailInitiative(ctx, ini.ID, "model timed out"))

	d.Tick(ctx)
	want := []Kind{
		KindInitiativeCreated, KindNeedsJudgment, KindPRDApproved, KindPlanApproved,
		KindTaskStatusChanged,
		KindChangeSetCreated, KindChangeSetReady, KindChangeSetApproved, KindChangeSetApplied, KindChangeSetReverted,
		KindVerifyPassed, KindRunFailed,
	}
	got := rec.kindsGot()
	if len(got) != len(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %v\nwant %v", i, got, want)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.got[1].Reason != string(domain.JudgmentPRDReview) {
		t.Errorf("needs_judgment reason = %q", rec.got[1].Reason)
	}
	if rec.got[6].ChangeSetRef != "CHG-1" || rec.got[6].TaskID != task.ID || rec.got[6].InitiativeTitle == "" {
		t.Errorf("change set event lacks context: %+v", rec.got[6])
	}
	if rec.got[4].Status != string(domain.TaskDone) {
		t.Errorf("task status = %q", rec.got[4].Status)
	}
	if rec.got[11].Reason != "model timed out" {
		t.Errorf("run.failed reason = %q", rec.got[11].Reason)
	}
}

// Turning an integration on must not replay history into it.
func TestInitStartsAtTheEndOfTheLog(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "state.db"))
	newInitiative(t, db, "old news")
	rec := &recorder{name: "rec"}
	d := NewDispatcher(db, "p1", []Subscriber{rec}, nil, Options{})
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	d.Tick(ctx)
	if n := len(rec.kindsGot()); n != 0 {
		t.Fatalf("history was delivered: %d events", n)
	}
	newInitiative(t, db, "new")
	d.Tick(ctx)
	if got := rec.kindsGot(); len(got) != 1 {
		t.Fatalf("want exactly the new event, got %v", got)
	}
}

// The restart story: an event delivered before a restart is not delivered
// again after it, an event queued but not yet delivered IS delivered after it,
// and nothing is delivered twice.
func TestOutboxIdempotencyAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := &recorder{name: "slack"}
	d1 := NewDispatcher(db1, "p1", []Subscriber{first}, nil, Options{})
	if err := d1.Init(ctx); err != nil {
		t.Fatal(err)
	}
	newInitiative(t, db1, "one")
	d1.Tick(ctx) // delivered
	// Queue a second event but "crash" before delivering it: ingest only.
	newInitiative(t, db1, "two")
	if err := d1.ingest(ctx, first); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2 := openDB(t, path)
	second := &recorder{name: "slack"}
	d2 := NewDispatcher(db2, "p1", []Subscriber{second}, nil, Options{})
	if err := d2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	d2.Tick(ctx)
	d2.Tick(ctx)
	if len(first.got) != 1 || first.got[0].InitiativeTitle != "one" {
		t.Fatalf("before restart: %+v", first.got)
	}
	if len(second.got) != 1 || second.got[0].InitiativeTitle != "two" {
		t.Fatalf("after restart want only the undelivered event, got %+v", second.got)
	}
	st, _ := db2.IntegrationOutboxStats(ctx, "slack")
	if st.Delivered != 2 || st.Pending != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// A failing delivery is retried with backoff, holds back the events behind it
// (order matters), and is dead-lettered after MaxAttempts -- after which the
// queue moves on. A PermanentError dead-letters at once. Nothing here blocks
// the writer: the engine's writes all happened before the first Tick.
func TestRetryBackoffAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "state.db"))
	// Ingest stamps rows with the wall clock, so the fake one starts just after it.
	now := time.Now().Add(time.Second)
	clock := func() time.Time { return now }
	var firstID string
	rec := &recorder{name: "rec", fail: func(ev Event, attempt int) error {
		if ev.SourceEventID == firstID {
			return errors.New("slack is down")
		}
		return nil
	}}
	d := NewDispatcher(db, "p1", []Subscriber{rec}, nil, Options{MaxAttempts: 3, BaseBackoff: time.Second, Now: clock})
	_ = d.Init(ctx)
	newInitiative(t, db, "a")
	newInitiative(t, db, "b")
	evs, _ := db.ListEvents(ctx, 0, 10)
	firstID = evs[0].ID

	d.Tick(ctx) // attempt 1 fails; b is held back
	if len(rec.got) != 0 {
		t.Fatalf("b delivered past a failing head: %+v", rec.got)
	}
	d.Tick(ctx) // still in backoff: no attempt
	if rec.tries[firstID] != 1 {
		t.Fatalf("retried inside backoff: %d tries", rec.tries[firstID])
	}
	now = now.Add(time.Second)
	d.Tick(ctx) // attempt 2
	now = now.Add(2 * time.Second)
	d.Tick(ctx) // attempt 3 -> dead, queue moves on to b
	if rec.tries[firstID] != 3 {
		t.Fatalf("tries = %d; want 3", rec.tries[firstID])
	}
	if len(rec.got) != 1 || rec.got[0].InitiativeTitle != "b" {
		t.Fatalf("after dead-letter want b delivered, got %+v", rec.got)
	}
	st, _ := db.IntegrationOutboxStats(ctx, "rec")
	if st.Dead != 1 || st.Delivered != 1 || st.LastError != "slack is down" {
		t.Fatalf("stats = %+v", st)
	}

	// Permanent: one attempt.
	perm := &recorder{name: "perm", fail: func(Event, int) error { return Permanent(errors.New("invalid_auth")) }}
	d2 := NewDispatcher(db, "p1", []Subscriber{perm}, nil, Options{Now: clock})
	_ = d2.Init(ctx)
	newInitiative(t, db, "c")
	d2.Tick(ctx)
	if st, _ := db.IntegrationOutboxStats(ctx, "perm"); st.Dead != 1 {
		t.Fatalf("permanent error not dead-lettered: %+v", st)
	}
}

func TestRetryAfterHintAndPanic(t *testing.T) {
	d := NewDispatcher(nil, "p", nil, nil, Options{BaseBackoff: time.Second, MaxBackoff: 10 * time.Second})
	if got := d.backoff(1, &RetryAfterError{Err: errors.New("429"), After: 42 * time.Second}); got != 42*time.Second {
		t.Fatalf("hint ignored: %v", got)
	}
	if got := d.backoff(1, &RetryAfterError{Err: errors.New("429"), After: 100 * time.Hour}); got != maxRetryAfter {
		t.Fatalf("hint not capped: %v", got)
	}
	if got := d.backoff(10, errors.New("x")); got != 10*time.Second {
		t.Fatalf("backoff not capped: %v", got)
	}
	err := safeDeliver(context.Background(), panicky{}, Event{})
	if !IsPermanent(err) {
		t.Fatalf("panic not converted: %v", err)
	}
}

type panicky struct{}

func (panicky) Name() string                         { return "p" }
func (panicky) Wants(Event) bool                     { return true }
func (panicky) Deliver(context.Context, Event) error { panic("boom") }

// Run delivers without any explicit Tick, and stops when its context does.
func TestRunDeliversAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := openDB(t, filepath.Join(t.TempDir(), "state.db"))
	rec := &recorder{name: "rec"}
	d := NewDispatcher(db, "p1", []Subscriber{rec}, nil, Options{PollInterval: 10 * time.Millisecond})
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	// Wait for Init to have placed the cursor before writing.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok, _ := db.IntegrationCursor(context.Background(), "rec"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cursor never initialized")
		}
		time.Sleep(5 * time.Millisecond)
	}
	newInitiative(t, db, "x")
	d.Wake()
	for len(rec.kindsGot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run never delivered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}
