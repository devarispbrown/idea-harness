package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// An Execute on the internal executor leaves a complete event stream on its
// agent run: the start, the executor's synthetic phases, the stages after
// the agent, the authoritative file list with line counts, and a finish that
// names the change set. Text is redacted on the way in.
func TestExecuteRecordsTheRunsEvents(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	const secret = "sk-proj-runeventsecret0123456789"
	eng.SetRedactor(security.NewRedactor(secret))
	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	eng.SetNoteOverride(iniID, "use the key "+secret)
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := db.LatestEventRunForInitiative(ctx, iniID)
	if err != nil || runID == "" {
		t.Fatalf("no run with events (%v)", err)
	}
	evs, err := db.ListAllRunEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq %d at position %d: seqs must be 1..n with no gap", ev.Seq, i)
		}
		if ev.RunID != runID || ev.InitiativeID != iniID || ev.TaskID != task.ID || ev.Executor != "internal" {
			t.Fatalf("event not stamped with its run: %+v", ev)
		}
		blob, _ := json.Marshal(ev)
		if strings.Contains(string(blob), secret) {
			t.Fatalf("a known secret reached the event stream: %s", blob)
		}
		titles = append(titles, string(ev.Kind)+":"+ev.Title)
	}
	joined := strings.Join(titles, "\n")
	for _, want := range []string{
		"status:Starting " + task.Code + " on the internal executor",
		"message:Note for this run",
		"status:Started the internal placeholder executor",
		"action:Editing",
		"file_change:Added hello.txt",
		"status:The agent finished: 1 file(s) changed",
		"status:Running checks",
		"status:Running adversarial review",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in the run's events:\n%s", want, joined)
		}
	}
	last := evs[len(evs)-1]
	if last.Status != "finished" || last.ChangeSetID != cs.ID {
		t.Fatalf("last event = %+v, want the finish naming %s", last, cs.ID)
	}
	for _, ev := range evs {
		if ev.Kind == domain.RunEventFileChange && ev.Status == "add" && (ev.Path != "hello.txt" || ev.Added != 2 || ev.Removed != 0) {
			t.Fatalf("authoritative file change = %+v, want hello.txt +2", ev)
		}
	}

	rep, err := eng.BuildRunReport(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ChangeSet == nil || rep.ChangeSet.ID != cs.ID || rep.Run.TaskCode != task.Code || rep.Run.Executor != "internal" {
		t.Fatalf("report header = %+v / %+v", rep.Run, rep.ChangeSet)
	}
	if len(rep.Files) != 1 || rep.Files[0].Path != "hello.txt" || rep.Files[0].Added != 2 || rep.Files[0].Change != "add" {
		t.Fatalf("report files = %+v", rep.Files)
	}
	if len(rep.Events) != len(evs) || rep.Manifest == nil {
		t.Fatalf("report has %d events (want %d), manifest %v", len(rep.Events), len(evs), rep.Manifest != nil)
	}
	md := rep.Markdown()
	for _, want := range []string{"# Run report", "## Files changed (1)", "`hello.txt` add +2 −0", "## Events ("} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, secret) {
		t.Fatal("the markdown report carries a secret")
	}
}

// fakeExec is a do-nothing executor; fakeSteerer adds Steer.
type fakeExec struct{ name string }

func (f *fakeExec) Name() string { return f.name }
func (f *fakeExec) Capabilities(context.Context) executor.Capabilities {
	return executor.Capabilities{}
}
func (f *fakeExec) Start(context.Context, executor.Request) (executor.Attempt, error) {
	return executor.Attempt{ID: "att"}, nil
}
func (f *fakeExec) Status(context.Context, string) (executor.Status, error) {
	return executor.Status{Status: "running"}, nil
}
func (f *fakeExec) Cancel(context.Context, string) error { return nil }
func (f *fakeExec) Collect(context.Context, string) (executor.Result, error) {
	return executor.Result{}, nil
}

type fakeSteerer struct {
	fakeExec
	mu       sync.Mutex
	notes    []string
	delivery string
	err      error
}

func (f *fakeSteerer) Steer(_ context.Context, attemptID, note string, interrupt bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.notes = append(f.notes, attemptID+":"+note)
	return f.delivery, nil
}

// liveFixture registers a live run the way Execute does, for SteerRun.
func liveFixture(t *testing.T, eng *Engine, iniID string, exec executor.Executor) *runRecorder {
	t.Helper()
	eng.setLiveStarting(iniID, "t")
	rec := eng.newRunRecorder("run-live", "", iniID, "task-x", exec.Name())
	eng.registerLiveRun(iniID, "t", "run-live", exec, rec)
	eng.setLiveAttempt(iniID, "t", "att-1")
	t.Cleanup(func() { rec.Close(); eng.clearLive(iniID, "t") })
	return rec
}

// A steerable executor gets the note; the note is a user message event and
// is kept for the manifest.
func TestSteerRunDeliversToASteerer(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ex := &fakeSteerer{fakeExec: fakeExec{name: "acp"}, delivery: domain.SteeringQueued}
	rec := liveFixture(t, eng, iniID, ex)
	res, err := eng.SteerRun(context.Background(), iniID, "run-live", "  also the README  ", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Delivery != domain.SteeringQueued || res.Executor != "acp" || len(ex.notes) != 1 || ex.notes[0] != "att-1:also the README" {
		t.Fatalf("result %+v, notes %v", res, ex.notes)
	}
	if got := eng.liveSteering(iniID); len(got) != 1 || got[0].Delivery != domain.SteeringQueued {
		t.Fatalf("manifest steering = %+v", got)
	}
	rec.Close()
	evs, _ := db.ListAllRunEvents(context.Background(), "run-live")
	if len(evs) != 1 || evs[0].Source != domain.RunEventSourceUser || evs[0].Detail != "also the README" || evs[0].Status != domain.SteeringQueued {
		t.Fatalf("events = %+v", evs)
	}
	if info, ok := eng.LiveRun(iniID); !ok || !info.Steerable || info.RunID != "run-live" {
		t.Fatalf("LiveRun = %+v %v", info, ok)
	}
}

// An executor that cannot be steered answers "steering not supported by
// <kind>", and the note is held for -- and consumed by -- the task's next run.
func TestSteerRunHoldsTheNoteForTheNextRunWhenUnsupported(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	liveFixture(t, eng, iniID, &fakeExec{name: "hermes"})
	res, err := eng.SteerRun(ctx, iniID, "", "prefer small commits", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Delivery != domain.SteeringNextRun || !strings.Contains(res.Message, "steering not supported by hermes") {
		t.Fatalf("result = %+v", res)
	}
	if info, _ := eng.LiveRun(iniID); info.Steerable {
		t.Fatal("hermes reported steerable")
	}
	if got := eng.PendingNotes(context.Background(), iniID); len(got) != 1 || got[0] != "prefer small commits" {
		t.Fatalf("pending = %v", got)
	}
	eng.clearLive(iniID, "t")

	// A daemon restart between the note and the next run: a new engine over
	// the same database still has it (held notes used to live in memory).
	restarted := New(db, eng.blobs, eng.registry, eng.cfg, nil)
	restarted.SetRawBlobs(eng.blobs)
	restarted.SetWorkRoot(eng.workRoot)
	restarted.SetWorkspaceRoot(eng.workspaceRoot)
	eng = restarted
	if got := eng.PendingNotes(ctx, iniID); len(got) != 1 || got[0] != "prefer small commits" {
		t.Fatalf("pending after restart = %v", got)
	}

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Execute(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	var note string
	if err := db.SQL().QueryRow(`SELECT note FROM runs WHERE initiative_id = ? AND stage = ?`, iniID, "agent:"+task.Code).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "prefer small commits") {
		t.Fatalf("next run's note = %q, want the held steering note", note)
	}
	if got := eng.PendingNotes(context.Background(), iniID); len(got) != 0 {
		t.Fatalf("the held note was not consumed: %v", got)
	}
}

// A steerer whose session is ending refuses; the note is held, not lost.
func TestSteerRunFallsBackWhenTheSessionIsEnding(t *testing.T) {
	eng, _, iniID := newExecuteFixture(t)
	liveFixture(t, eng, iniID, &fakeSteerer{fakeExec: fakeExec{name: "acp"}, err: executor.ErrSteeringUnsupported})
	res, err := eng.SteerRun(context.Background(), iniID, "run-live", "late note", true)
	if err != nil || res.Delivery != domain.SteeringNextRun {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := eng.PendingNotes(context.Background(), iniID); len(got) != 1 {
		t.Fatalf("pending = %v", got)
	}
}

func TestSteerRunRefusesARunThatIsNotLive(t *testing.T) {
	eng, _, iniID := newExecuteFixture(t)
	if _, err := eng.SteerRun(context.Background(), iniID, "", "x", false); !errors.Is(err, ErrNoLiveRun) {
		t.Fatalf("nothing running: %v", err)
	}
	liveFixture(t, eng, iniID, &fakeExec{name: "internal"})
	if _, err := eng.SteerRun(context.Background(), iniID, "some-older-run", "x", false); !errors.Is(err, ErrNoLiveRun) {
		t.Fatalf("a different run id: %v", err)
	}
	if _, err := eng.SteerRun(context.Background(), iniID, "run-live", "   ", false); err == nil {
		t.Fatal("an empty note was accepted")
	}
}

// fakeTailer is an executor with growing output and no events of its own.
type fakeTailer struct {
	fakeExec
	mu  sync.Mutex
	out []string
}

func (f *fakeTailer) OutputTail(_ context.Context, _ string, n int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := f.out
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (f *fakeTailer) add(l string) {
	f.mu.Lock()
	f.out = append(f.out, l)
	f.mu.Unlock()
}

// For an executor with no events of its own, each new output line becomes
// one action event (never a line twice) and a file written in the work root
// becomes a file_change.
func TestCoarseEventsFromOutputAndWorkRoot(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	old := runEventPollInterval
	runEventPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { runEventPollInterval = old })
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &fakeTailer{fakeExec: fakeExec{name: "hermes"}}
	rec := eng.newRunRecorder("run-coarse", "", iniID, "t", "hermes")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.pumpCoarseEvents(ctx, ex, "att", work, rec); close(done) }()
	ex.add("reading the code")
	time.Sleep(60 * time.Millisecond)
	ex.add("writing main.go")
	// Written whole (a rename), so a scan cannot catch it half-written and
	// rightly report it twice.
	if err := os.WriteFile(filepath.Join(work, ".main.go.tmp"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(work, ".main.go.tmp"), filepath.Join(work, "main.go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done
	rec.Close()
	evs, _ := db.ListAllRunEvents(context.Background(), "run-coarse")
	counts := map[string]int{}
	for _, ev := range evs {
		counts[string(ev.Kind)+":"+ev.Title]++
	}
	if counts["action:reading the code"] != 1 || counts["action:writing main.go"] != 1 {
		t.Fatalf("output lines as events: %v", counts)
	}
	if counts["file_change:Changed main.go"] != 1 || counts["file_change:Changed keep.txt"] != 0 {
		t.Fatalf("work root changes as events: %v", counts)
	}
}

func TestNewTailLines(t *testing.T) {
	cases := []struct {
		prev, cur, want []string
	}{
		{nil, []string{"a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b"}, []string{"a", "b", "c"}, []string{"c"}},
		{[]string{"a", "b", "c"}, []string{"b", "c", "d"}, []string{"d"}},
		{[]string{"a", "b"}, []string{"a", "b"}, nil},
		{[]string{"x"}, []string{"y", "z"}, []string{"y", "z"}},
	}
	for _, c := range cases {
		got := newTailLines(c.prev, c.cur, 50)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("newTailLines(%v, %v) = %v, want %v", c.prev, c.cur, got, c.want)
		}
	}
	if got := newTailLines(nil, []string{"1", "2", "3"}, 2); strings.Join(got, ",") != "2,3" {
		t.Errorf("cap keeps the newest: %v", got)
	}
}

// The recorder never blocks its caller and records what it could not keep.
func TestRunRecorderCountsDroppedEvents(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	rec := eng.newRunRecorder("run-flood", "", iniID, "t", "acp")
	for i := 0; i < runRecorderBuffer*4; i++ {
		rec.Emit(domain.RunEvent{Kind: domain.RunEventAction, Title: "x"})
	}
	rec.Close()
	rec.Emit(domain.RunEvent{Kind: domain.RunEventAction, Title: "after close"})
	evs, _ := db.ListAllRunEvents(context.Background(), "run-flood")
	total := len(evs)
	var dropped bool
	for _, ev := range evs {
		if strings.Contains(ev.Title, "events were dropped") {
			dropped = true
			total--
		}
		if ev.Title == "after close" {
			t.Fatal("an event emitted after Close was stored")
		}
	}
	if total+0 > runRecorderBuffer*4 || (total < runRecorderBuffer*4 && !dropped) {
		t.Fatalf("stored %d of %d with dropped notice %v", total, runRecorderBuffer*4, dropped)
	}
}

// A run that ends with tool calls still pending or in progress (Stop, a
// failure, an agent that never reported the end) closes each with a
// cancelled tool_result on the same call id; finished calls, and calls with
// no id, are left alone, and closing twice adds nothing.
func TestRunRecorderClosesOpenToolCallsAsCancelled(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	rec := eng.newRunRecorder("run-stop", "", iniID, "t", "acp")
	rec.Emit(domain.RunEvent{Kind: domain.RunEventToolCall, ToolCallID: "done", Status: "in_progress", Title: "read a"})
	rec.Emit(domain.RunEvent{Kind: domain.RunEventToolResult, ToolCallID: "done", Status: "completed"})
	rec.Emit(domain.RunEvent{Kind: domain.RunEventToolCall, ToolCallID: "running", ToolKind: "execute", Status: "in_progress", Title: "npm test"})
	rec.Emit(domain.RunEvent{Kind: domain.RunEventToolCall, ToolCallID: "pending", Title: "edit b"})
	rec.Emit(domain.RunEvent{Kind: domain.RunEventToolCall, ToolCallID: "failed", Status: "failed"})
	rec.Emit(domain.RunEvent{Kind: domain.RunEventToolCall, Status: "in_progress", Title: "no id"})
	rec.Close()
	rec.Close()
	evs, err := db.ListAllRunEvents(ctx, "run-stop")
	if err != nil {
		t.Fatal(err)
	}
	cancelled := map[string]domain.RunEvent{}
	for _, ev := range evs {
		if ev.Status == ToolCallCancelled {
			if _, dup := cancelled[ev.ToolCallID]; dup {
				t.Fatalf("call %q closed twice", ev.ToolCallID)
			}
			cancelled[ev.ToolCallID] = ev
		}
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %v, want exactly the running and pending calls", cancelled)
	}
	r, ok := cancelled["running"]
	if !ok || r.Kind != domain.RunEventToolResult || r.ToolKind != "execute" || r.Source != domain.RunEventSourceHarness || r.RunID != "run-stop" {
		t.Fatalf("closing event for the running call = %+v", r)
	}
	if _, ok := cancelled["pending"]; !ok {
		t.Fatal("the pending call was not closed")
	}
}

// A run's cost is the sum of its cost events (deltas) when its manifest has
// no figure; an agent's text cannot inject Markdown structure into the
// report.
func TestRunReportSumsCostEventsAndEscapesText(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	runID, err := db.CreateRun(ctx, domain.RunAgent, "", "implementer", iniID, "agent:TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AppendRunEvents(ctx, runID, []domain.RunEvent{
		{Kind: domain.RunEventCost, InputTokens: 100, OutputTokens: 10, CostUSD: 0.01},
		{Kind: domain.RunEventCost, InputTokens: 50, CostUSD: 0.02},
		{Kind: domain.RunEventFileChange, Source: domain.RunEventSourceAgent, Path: "a.go", Title: "Wrote a.go", Added: 4},
		{Kind: domain.RunEventFileChange, Source: domain.RunEventSourceHarness, Path: "a.go", Title: "Modified a.go", Status: "modify", Added: 2, Removed: 1},
		{Kind: domain.RunEventMessage, Source: domain.RunEventSourceUser, Title: "Note from you", Detail: "see [here](javascript:x) # now", Status: "next_run"},
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := eng.BuildRunReport(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cost.InputTokens != 150 || rep.Cost.OutputTokens != 10 || rep.Cost.CostUSD < 0.0299 || rep.Cost.CostUSD > 0.0301 {
		t.Fatalf("cost = %+v, want the sum of the deltas", rep.Cost)
	}
	if len(rep.Files) != 1 || rep.Files[0].Added != 2 || rep.Files[0].Removed != 1 || rep.Files[0].Change != "modify" {
		t.Fatalf("files = %+v, want the orchestrator's diff to win over the live count", rep.Files)
	}
	if len(rep.Steering) != 1 || rep.Steering[0].Delivery != "next_run" {
		t.Fatalf("steering from events (no manifest) = %+v", rep.Steering)
	}
	md := rep.Markdown()
	if strings.Contains(md, "[here](javascript:x)") || !strings.Contains(md, `\[here\](javascript:x) \# now`) {
		t.Fatalf("an agent's link survived into the markdown:\n%s", md)
	}
}
