package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// eventLog is a test EventSink.
type eventLog struct {
	mu  sync.Mutex
	evs []domain.RunEvent
}

func (l *eventLog) sink(ev domain.RunEvent) {
	l.mu.Lock()
	l.evs = append(l.evs, ev)
	l.mu.Unlock()
}

func (l *eventLog) all() []domain.RunEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domain.RunEvent(nil), l.evs...)
}

func (l *eventLog) ofKind(k domain.RunEventKind) []domain.RunEvent {
	var out []domain.RunEvent
	for _, e := range l.all() {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// Every session/update the run view uses maps onto a normalized event, with
// the agent's own paths made work-root-relative; thought text is never kept.
func TestACPSessionUpdatesMapToRunEvents(t *testing.T) {
	e := newTestACP(t, "events", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	work := workRootFor(t)
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "src", "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var log eventLog
	att, err := e.Start(context.Background(), Request{TaskTitle: "t", Brief: "b", WorkRoot: work, Events: log.sink})
	if err != nil {
		t.Fatal(err)
	}
	if st := waitACP(t, e, att.ID, 20*time.Second); st.Status != "succeeded" {
		t.Fatalf("status = %+v\n%s", st, e.OutputTail(context.Background(), att.ID, 100))
	}
	_, _ = e.Collect(context.Background(), att.ID)

	for _, ev := range log.all() {
		if strings.Contains(ev.Title+ev.Detail, "private reasoning") {
			t.Fatalf("thought text reached an event: %+v", ev)
		}
		if ev.At.IsZero() {
			t.Fatalf("event without a timestamp: %+v", ev)
		}
	}
	if th := log.ofKind(domain.RunEventThought); len(th) != 1 || th[0].Title != "Thinking" {
		t.Fatalf("thought events = %+v, want one per run of thought chunks", th)
	}
	msgs := log.ofKind(domain.RunEventMessage)
	if len(msgs) != 1 || msgs[0].Detail != "I will edit src/a.txt." {
		t.Fatalf("message events = %+v, want the chunks joined into one", msgs)
	}
	calls := log.ofKind(domain.RunEventToolCall)
	if len(calls) != 2 || calls[0].ToolCallID != "t1" || calls[0].ToolKind != "execute" || calls[0].Detail != "npm test" || calls[0].Status != "pending" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[1].Path != "src/b.txt" {
		t.Fatalf("tool call path = %q, want it relative to the work root", calls[1].Path)
	}
	results := log.ofKind(domain.RunEventToolResult)
	if len(results) != 2 || results[0].Status != "failed" || results[0].Title != "Run tests" || results[0].Detail != "1 test failed" {
		t.Fatalf("tool results = %+v (a failure carries its title from the tool_call and its text)", results)
	}
	files := log.ofKind(domain.RunEventFileChange)
	byPath := map[string]domain.RunEvent{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if b := byPath["src/b.txt"]; b.Added != 2 || b.Removed != 1 {
		t.Fatalf("diff content file_change = %+v, want +2 -1", b)
	}
	if a := byPath["src/a.txt"]; a.Added != 1 || a.Removed != 1 {
		t.Fatalf("fs write file_change = %+v, want +1 -1 against the previous content", a)
	}
	if perms := log.ofKind(domain.RunEventPermission); len(perms) != 1 || perms[0].Status != "allow_once" || perms[0].Title != "Write src/a.txt" {
		t.Fatalf("permission events = %+v", perms)
	}
	var plan bool
	for _, a := range log.ofKind(domain.RunEventAction) {
		if a.Title == "Plan: 1/3 done" && strings.Contains(a.Detail, "[~] edit") {
			plan = true
		}
	}
	if !plan {
		t.Fatalf("no plan event among %+v", log.ofKind(domain.RunEventAction))
	}
	costs := log.ofKind(domain.RunEventCost)
	var usd float64
	var in int64
	for _, c := range costs {
		usd += c.CostUSD
		in += c.InputTokens
	}
	if usd < 0.0299 || usd > 0.0301 || in != 100 {
		t.Fatalf("cost events = %+v; want deltas summing to $0.03 and 100 input tokens", costs)
	}
	if !e.EmitsRunEvents() {
		t.Fatal("the ACP executor must report its own structured events")
	}
}

// A note sent while a turn runs is queued and becomes the next prompt turn
// in the same session once that turn ends -- ACP has no concurrent prompts.
func TestACPSteerQueuesTheNoteAsTheNextTurn(t *testing.T) {
	e := newTestACP(t, "steer", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	work := workRootFor(t)
	att, err := e.Start(context.Background(), Request{TaskTitle: "t", Brief: "b", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	waitAction(t, e, att.ID, "working")
	delivery, err := e.Steer(context.Background(), att.ID, "also update the README", false)
	if err != nil || delivery != domain.SteeringQueued {
		t.Fatalf("Steer = %q, %v; want queued", delivery, err)
	}
	if st := waitACP(t, e, att.ID, 20*time.Second); st.Status != "succeeded" {
		t.Fatalf("status = %+v\n%s", st, e.OutputTail(context.Background(), att.ID, 100))
	}
	got, err := os.ReadFile(filepath.Join(work, "turn2.txt"))
	if err != nil {
		t.Fatalf("no second turn: %v\n%s", err, e.OutputTail(context.Background(), att.ID, 100))
	}
	if !strings.Contains(string(got), "also update the README") {
		t.Fatalf("second turn prompt = %q, want the note", got)
	}
	// The session is over: a note now is refused, not silently dropped.
	if _, err := e.Steer(context.Background(), att.ID, "too late", false); !errors.Is(err, ErrSteeringUnsupported) {
		t.Fatalf("Steer after the session ended = %v, want ErrSteeringUnsupported", err)
	}
	_, _ = e.Collect(context.Background(), att.ID)
}

// interrupt ends the running turn with session/cancel (the agent answers
// "cancelled") and sends the note at once; the run still succeeds.
func TestACPSteerInterruptCancelsTheTurnAndSendsTheNote(t *testing.T) {
	e := newTestACP(t, "steer-interrupt", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	work := workRootFor(t)
	att, err := e.Start(context.Background(), Request{TaskTitle: "t", Brief: "b", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	waitAction(t, e, att.ID, "working")
	delivery, err := e.Steer(context.Background(), att.ID, "stop and use the v2 API", true)
	if err != nil || delivery != domain.SteeringDelivered {
		t.Fatalf("Steer = %q, %v; want delivered", delivery, err)
	}
	if st := waitACP(t, e, att.ID, 20*time.Second); st.Status != "succeeded" {
		t.Fatalf("status = %+v, want succeeded after the interrupted turn\n%s", st, e.OutputTail(context.Background(), att.ID, 100))
	}
	got, err := os.ReadFile(filepath.Join(work, "turn2.txt"))
	if err != nil || !strings.Contains(string(got), "stop and use the v2 API") {
		t.Fatalf("turn 2 = %q (%v), want the note", got, err)
	}
	if tail := e.OutputTail(context.Background(), att.ID, 100); !strings.Contains(tail, "interrupting the turn for a steering note") {
		t.Fatalf("the interrupt is not in the output:\n%s", tail)
	}
	_, _ = e.Collect(context.Background(), att.ID)
}

// A real Stop is not a steering interrupt: the run ends cancelled, and the
// queued note is not sent.
func TestACPStopWithAQueuedNoteStillCancels(t *testing.T) {
	e := newTestACP(t, "hang", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	work := workRootFor(t)
	att, err := e.Start(context.Background(), Request{TaskTitle: "t", Brief: "b", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	waitAction(t, e, att.ID, "working")
	if _, err := e.Steer(context.Background(), att.ID, "queued note", false); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(context.Background(), att.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Status(context.Background(), att.ID); !errors.Is(err, ErrUnknownAttempt) {
		t.Fatalf("status after cancel: %v", err)
	}
}

// waitAction polls Status until the attempt reports action.
func waitAction(t *testing.T, e *ACPExecutor, id, action string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := e.Status(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if st.Action == action {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("attempt never reached action %q", action)
}
