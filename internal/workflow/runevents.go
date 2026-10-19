package workflow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/textdiff"
)

// ---------- the recorder ----------

// runRecorder is the event sink an Execute hands its executor: it stamps
// each event with the run's identity, redacts it, and persists it. Emit
// never blocks -- an ACP agent's stdout reader calls it inline, and a slow
// disk must not stall the agent -- so events go through a buffered channel
// to one writer goroutine that batches them. When the buffer is full an
// event is dropped and counted; Close records how many.
type runRecorder struct {
	db     *store.DB
	log    *slog.Logger
	redact func(string) string
	base   domain.RunEvent

	ch      chan domain.RunEvent
	done    chan struct{}
	dropped atomic.Int64
	closed  atomic.Bool
	once    sync.Once
	// sendMu orders Emit's send against Close's close(ch).
	sendMu sync.RWMutex
}

// runRecorderBuffer is how many events may wait for the writer.
const runRecorderBuffer = 1024

func (e *Engine) newRunRecorder(runID, operationID, initiativeID, taskID, executorName string) *runRecorder {
	r := &runRecorder{
		db:     e.db,
		log:    e.log,
		redact: e.redactRemote,
		base: domain.RunEvent{RunID: runID, OperationID: operationID, InitiativeID: initiativeID, TaskID: taskID,
			Executor: executorName},
		ch:   make(chan domain.RunEvent, runRecorderBuffer),
		done: make(chan struct{}),
	}
	go r.loop()
	return r
}

// Emit records ev. Safe for concurrent use; never blocks; a no-op once the
// recorder is closed.
func (r *runRecorder) Emit(ev domain.RunEvent) {
	if r == nil {
		return
	}
	r.sendMu.RLock()
	defer r.sendMu.RUnlock()
	if r.closed.Load() {
		return
	}
	select {
	case r.ch <- ev:
	default:
		r.dropped.Add(1)
	}
}

// Sink is Emit as an executor.EventSink.
func (r *runRecorder) Sink() executor.EventSink {
	if r == nil {
		return nil
	}
	return r.Emit
}

func (r *runRecorder) stamp(ev domain.RunEvent) domain.RunEvent {
	ev.RunID, ev.OperationID, ev.InitiativeID, ev.TaskID = r.base.RunID, r.base.OperationID, r.base.InitiativeID, r.base.TaskID
	if ev.Executor == "" {
		ev.Executor = r.base.Executor
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Source == "" {
		ev.Source = domain.RunEventSourceHarness
	}
	// Bound first, so the redactor never runs over megabytes of a file write
	// the store would cut anyway; redaction can only shorten or keep length
	// close, and the store bounds again.
	return ev.Bounded().Redacted(r.redact)
}

func (r *runRecorder) loop() {
	defer close(r.done)
	for ev := range r.ch {
		batch := []domain.RunEvent{r.stamp(ev)}
	drain:
		for len(batch) < 100 {
			select {
			case more, ok := <-r.ch:
				if !ok {
					break drain
				}
				batch = append(batch, r.stamp(more))
			default:
				break drain
			}
		}
		// Detached: a Stop cancels the run's context, and the events that
		// say what happened up to the Stop are exactly the ones wanted.
		if _, err := r.db.AppendRunEvents(context.Background(), r.base.RunID, batch); err != nil && r.log != nil {
			r.log.Warn("could not record run events", "run", r.base.RunID, "events", len(batch), "error", err)
		}
	}
}

// Close flushes what is buffered and stops the writer. Idempotent.
func (r *runRecorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.sendMu.Lock()
		r.closed.Store(true)
		close(r.ch)
		r.sendMu.Unlock()
		<-r.done
		r.closeOpenToolCalls()
		if n := r.dropped.Load(); n > 0 {
			_, _ = r.db.AppendRunEvents(context.Background(), r.base.RunID, []domain.RunEvent{r.stamp(domain.RunEvent{
				Kind: domain.RunEventStatus, Title: fmt.Sprintf("%d events were dropped: the agent produced them faster than they could be stored", n),
			})})
		}
	})
}

// ToolCallCancelled is the status closeOpenToolCalls gives a tool call the
// run ended without finishing.
const ToolCallCancelled = "cancelled"

// closeOpenToolCalls records, for every tool call of the run still pending
// or in progress, a tool_result with status cancelled. Whatever ended the run
// -- Stop, a failure, a timeout, or an agent that never reported the call's
// end -- the call is not running any more, and without this it read "in
// progress" in the run panel and the report forever. Read from the store,
// not from what passed through Emit, so calls recorded by an executor's own
// ingest are closed too. Grouping and "open" mirror the UI's
// (web/src/runEvents.ts groupToolCalls): by tool_call_id, the latest
// non-empty status wins, and no status, pending or in_progress is open.
func (r *runRecorder) closeOpenToolCalls() {
	evs, err := r.db.ListAllRunEvents(context.Background(), r.base.RunID)
	if err != nil {
		return
	}
	type call struct {
		status string
		ev     domain.RunEvent
	}
	calls := map[string]*call{}
	var order []string
	for _, ev := range evs {
		if (ev.Kind != domain.RunEventToolCall && ev.Kind != domain.RunEventToolResult) || ev.ToolCallID == "" {
			continue
		}
		c := calls[ev.ToolCallID]
		if c == nil {
			c = &call{status: "pending", ev: ev}
			calls[ev.ToolCallID] = c
			order = append(order, ev.ToolCallID)
		}
		if ev.Kind == domain.RunEventToolCall {
			c.ev = ev
		}
		if ev.Status != "" {
			c.status = ev.Status
		}
	}
	var closing []domain.RunEvent
	for _, id := range order {
		c := calls[id]
		if c.status != "pending" && c.status != "in_progress" {
			continue
		}
		closing = append(closing, r.stamp(domain.RunEvent{
			Kind: domain.RunEventToolResult, Source: domain.RunEventSourceHarness, ToolCallID: id,
			ToolKind: c.ev.ToolKind, Status: ToolCallCancelled, Title: "Did not finish: the run ended",
		}))
	}
	if len(closing) == 0 {
		return
	}
	if _, err := r.db.AppendRunEvents(context.Background(), r.base.RunID, closing); err != nil && r.log != nil {
		r.log.Warn("could not close the run's open tool calls", "run", r.base.RunID, "error", err)
	}
}

// ---------- live run identity and steering ----------

// liveRun is the steering side of a live attempt: which run it is, which
// executor runs it, and the notes sent to it. Guarded by e.liveMu.
type liveRun struct {
	runID     string
	exec      executor.Executor
	attemptID string
	rec       *runRecorder
	steering  []domain.SteeringNote
}

// LiveRunInfo describes an initiative's running agent run for clients.
type LiveRunInfo struct {
	RunID    string `json:"run_id"`
	Executor string `json:"executor,omitempty"`
	// Steerable reports whether a note reaches the running agent itself;
	// false means it is held for the task's next run.
	Steerable bool `json:"steerable"`
}

// ErrNoLiveRun is returned when a note is sent to a run that is not running.
var ErrNoLiveRun = errors.New("workflow: that run is not running")

func (e *Engine) liveRunFor(initiativeID string) *liveRun {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if a := e.firstLiveFor(initiativeID); a != nil {
		return a.run
	}
	return nil
}

// LiveRun returns the initiative's running agent run, if one is past the
// point of having a run id.
func (e *Engine) LiveRun(initiativeID string) (LiveRunInfo, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	a := e.firstLiveFor(initiativeID)
	if a == nil || a.run == nil || a.run.runID == "" {
		return LiveRunInfo{}, false
	}
	info := LiveRunInfo{RunID: a.run.runID}
	if a.run.exec != nil {
		info.Executor = a.run.exec.Name()
		_, info.Steerable = a.run.exec.(executor.Steerer)
	}
	return info, true
}

// registerLiveRun attaches the run's identity, executor and recorder to the
// initiative's live entry (created by setLiveStarting). The run exists only
// once the planner has written the brief, so there is nothing to steer
// before then: the run view shows the run as starting.
func (e *Engine) registerLiveRun(initiativeID, taskID, runID string, exec executor.Executor, rec *runRecorder) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if e.live == nil {
		e.live = map[string]*liveAttempt{}
	}
	key := liveKey(initiativeID, taskID)
	a := e.live[key]
	if a == nil {
		a = &liveAttempt{redactor: e.redactor}
		e.live[key] = a
	}
	a.run = &liveRun{runID: runID, exec: exec, rec: rec}
}

// takePendingNotes returns, and marks consumed by taskID's run, the steering
// notes held for the initiative's next run (sent to a run that could not take
// them): this run's brief carries them. Stored, not in memory, so a daemon
// restart between the note and this run does not drop them; the store's
// take is one statement, so each note rides exactly one run. A failure to
// read them is logged, not fatal: the run goes ahead without them, and they
// stay held for the one after.
func (e *Engine) takePendingNotes(ctx context.Context, initiativeID, taskID string) []string {
	held, err := e.db.TakeSteeringNotes(ctx, initiativeID, taskID)
	if err != nil {
		if e.log != nil {
			e.log.Warn("could not read held steering notes", "initiative", initiativeID, "error", err)
		}
		return nil
	}
	out := make([]string, 0, len(held))
	for _, n := range held {
		out = append(out, n.Note)
	}
	return out
}

// holdNote stores a steering note for the initiative's next run.
func (e *Engine) holdNote(ctx context.Context, initiativeID string, lr *liveRun, note string) error {
	n := store.HeldSteeringNote{InitiativeID: initiativeID, RunID: lr.runID, Note: e.redact(note)}
	if lr.rec != nil {
		n.TaskID = lr.rec.base.TaskID
	}
	// Detached from the request: the caller is told the note is held, so a
	// client hanging up mid-call must not lose it.
	return e.db.HoldSteeringNote(context.WithoutCancel(ctx), n)
}

// setLiveAttempt records the executor's attempt id once Start returned.
func (e *Engine) setLiveAttempt(initiativeID, taskID, attemptID string) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if a := e.live[liveKey(initiativeID, taskID)]; a != nil && a.run != nil {
		a.run.attemptID = attemptID
	}
}

// liveSteering returns the notes sent to the initiative's live run.
func (e *Engine) liveSteering(initiativeID string) []domain.SteeringNote {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if a := e.firstLiveFor(initiativeID); a != nil && a.run != nil {
		return append([]domain.SteeringNote(nil), a.run.steering...)
	}
	return nil
}

// SteerResult is what became of a steering note.
type SteerResult struct {
	RunID    string `json:"run_id"`
	Executor string `json:"executor,omitempty"`
	// Delivery is delivered | queued | next_run (domain.Steering*).
	Delivery string `json:"delivery"`
	// Message is a sentence for the person who sent it.
	Message string `json:"message"`
}

// SteerRun sends a human's note to the initiative's running agent run.
// runID, when set, must name that run (a stale UI must not steer a newer
// run it never saw). How the note travels depends on the executor:
//
//   - an executor.Steerer (acp, openhands): Steer -- delivered or queued as
//     the agent's next turn;
//   - any other executor (internal, container, hermes, omp): the agent
//     cannot be reached mid-run, so the note is held and appended to the
//     task's next run's brief -- next_run.
//
// Every note is recorded as a message event (source user) on the run, and on
// the run manifest's executor provenance.
func (e *Engine) SteerRun(ctx context.Context, initiativeID, runID, note string, interrupt bool) (SteerResult, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return SteerResult{}, fmt.Errorf("workflow: steer: the note is empty")
	}
	e.liveMu.Lock()
	var a *liveAttempt
	if runID != "" {
		// A runID names one task's run; find it among the initiative's entries.
		for _, cand := range e.live {
			if cand.run != nil && cand.run.runID == runID {
				a = cand
				break
			}
		}
	} else {
		a = e.firstLiveFor(initiativeID)
	}
	if a == nil || a.run == nil {
		e.liveMu.Unlock()
		return SteerResult{}, ErrNoLiveRun
	}
	lr := a.run
	res := SteerResult{RunID: lr.runID}
	var steerer executor.Steerer
	if s, ok := lr.exec.(executor.Steerer); ok {
		steerer = s
		res.Executor = lr.exec.Name()
	} else {
		if lr.exec != nil {
			res.Executor = lr.exec.Name()
		}
		res.Delivery = domain.SteeringNextRun
		res.Message = fmt.Sprintf("steering not supported by %s; your note will be added to this task's next run", firstNonEmptyStr(res.Executor, "this executor"))
	}
	attemptID := lr.attemptID
	e.liveMu.Unlock()

	if steerer == nil {
		if err := e.holdNote(ctx, initiativeID, lr, note); err != nil {
			return SteerResult{}, err
		}
	} else {
		// Between the run's creation and the agent's start there is no
		// session to reach yet: held for the next run, like a finished one.
		d, err := "", executor.ErrSteeringUnsupported
		if attemptID != "" {
			d, err = steerer.Steer(ctx, attemptID, note, interrupt)
		}
		switch {
		case errors.Is(err, executor.ErrSteeringUnsupported):
			// The agent's session is ending (or already over): hold the note
			// for the next run rather than lose it.
			if err := e.holdNote(ctx, initiativeID, lr, note); err != nil {
				return SteerResult{}, err
			}
			res.Delivery = domain.SteeringNextRun
			res.Message = "The agent is finishing and can no longer take a note; it will be added to this task's next run."
		case err != nil:
			return SteerResult{}, err
		default:
			res.Delivery = d
			if d == domain.SteeringDelivered {
				res.Message = "Sent to the agent."
			} else {
				res.Message = "The agent will get your note as its next instruction, once its current step ends."
			}
		}
	}

	sn := domain.SteeringNote{At: time.Now().UTC(), Note: e.redact(note), Delivery: res.Delivery}
	e.liveMu.Lock()
	lr.steering = append(lr.steering, sn)
	e.liveMu.Unlock()
	lr.rec.Emit(domain.RunEvent{Kind: domain.RunEventMessage, Source: domain.RunEventSourceUser, Status: res.Delivery,
		Title: "Note from you", Detail: note})
	return res, nil
}

// PendingNotes returns steering notes held for the initiative's next run,
// oldest first (nil when none, or when they cannot be read).
func (e *Engine) PendingNotes(ctx context.Context, initiativeID string) []string {
	held, err := e.db.PendingSteeringNotes(ctx, initiativeID)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range held {
		out = append(out, n.Note)
	}
	return out
}

func firstNonEmptyStr(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// emitStage records a stage of the live run as a status event (setStage).
func (e *Engine) emitStage(initiativeID, stage string) {
	if lr := e.liveRunFor(initiativeID); lr != nil && lr.rec != nil {
		lr.rec.Emit(domain.RunEvent{Kind: domain.RunEventStatus, Title: stage, Status: "running"})
	}
}

// ---------- coarse events for executors without their own ----------

// runEventPollInterval paces the output pump and the work-root scan.
var runEventPollInterval = 2 * time.Second

// pumpCoarseEvents derives events for an executor that emits none of its own
// (hermes, omp, container): each new line of the agent's output tail is an
// action event, and each file the work root scan finds changed is a
// file_change (without counts; the final diff has them). It returns when ctx
// ends.
func (e *Engine) pumpCoarseEvents(ctx context.Context, exec executor.Executor, attemptID, workRoot string, rec *runRecorder) {
	tailer, _ := exec.(executor.OutputTailer)
	scan := newWorkRootScanner(workRoot)
	var prev []string
	t := time.NewTicker(runEventPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if tailer != nil {
			tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			out := tailer.OutputTail(tctx, attemptID, 200)
			cancel()
			cur := splitNonEmptyLines(out)
			for _, line := range newTailLines(prev, cur, 50) {
				rec.Emit(domain.RunEvent{Kind: domain.RunEventAction, Source: domain.RunEventSourceAgent, Title: line})
			}
			prev = cur
		}
		for _, p := range scan.changed() {
			rec.Emit(domain.RunEvent{Kind: domain.RunEventFileChange, Source: domain.RunEventSourceHarness, Title: "Changed " + p, Path: p})
		}
	}
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// newTailLines returns the lines of cur that were not in prev, where both
// are tails (the last N lines) of one growing log: cur's start overlaps
// prev's end. The longest suffix of prev that is a prefix of cur is the
// overlap; what follows it is new. No overlap (more than N lines arrived
// between two reads) means all of cur is new. At most max lines are
// returned, the newest.
func newTailLines(prev, cur []string, max int) []string {
	best := 0
	for k := min(len(prev), len(cur)); k > 0; k-- {
		match := true
		for i := 0; i < k; i++ {
			if prev[len(prev)-k+i] != cur[i] {
				match = false
				break
			}
		}
		if match {
			best = k
			break
		}
	}
	out := cur[best:]
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// workRootScanner notices files that change in a work root, by size and
// modification time -- a stat walk, no hashing, so it is cheap enough to run
// every couple of seconds. It skips version-control and dependency
// directories, and gives up on a tree larger than workRootScanMax files
// (the final diff still reports everything).
type workRootScanner struct {
	root     string
	seen     map[string]fileStamp
	reported map[string]fileStamp
	disabled bool
}

type fileStamp struct {
	size int64
	mod  time.Time
}

const workRootScanMax = 20000

var workRootScanSkip = map[string]bool{".git": true, "node_modules": true, ".venv": true, "vendor": true, "target": true, "dist": true, ".next": true}

func newWorkRootScanner(root string) *workRootScanner {
	s := &workRootScanner{root: root, reported: map[string]fileStamp{}}
	s.seen, s.disabled = s.walk()
	return s
}

func (s *workRootScanner) walk() (map[string]fileStamp, bool) {
	out := map[string]fileStamp{}
	over := false
	_ = filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != s.root && workRootScanSkip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if len(out) >= workRootScanMax {
			over = true
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = fileStamp{size: info.Size(), mod: info.ModTime()}
		return nil
	})
	return out, over
}

// changed returns the paths whose size or mtime moved since the scanner
// started, each once per change.
func (s *workRootScanner) changed() []string {
	if s.disabled || s.root == "" {
		return nil
	}
	now, over := s.walk()
	if over {
		s.disabled = true
		return nil
	}
	var out []string
	for p, st := range now {
		if base, ok := s.seen[p]; ok && base == st {
			continue
		}
		if r, ok := s.reported[p]; ok && r == st {
			continue
		}
		s.reported[p] = st
		out = append(out, p)
	}
	for p := range s.seen {
		if _, ok := now[p]; !ok {
			if _, done := s.reported[p]; !done {
				s.reported[p] = fileStamp{size: -1}
				out = append(out, p)
			}
		}
	}
	return out
}

// ---------- the authoritative file list ----------

// fileChangeCountMax bounds the files whose line counts are computed for the
// final file list; past it the rest are listed without counts.
const fileChangeCountMax = 200

// emitFinalFileChanges records the orchestrator's own diff of the run as
// file_change events with +/- line counts: the authoritative file list,
// whatever the executor reported live.
func (e *Engine) emitFinalFileChanges(rec *runRecorder, workRoot string, files []domain.FileChange) {
	if rec == nil {
		return
	}
	for i, fc := range files {
		ev := domain.RunEvent{Kind: domain.RunEventFileChange, Source: domain.RunEventSourceHarness, Path: fc.Path, Status: string(fc.ChangeType)}
		switch fc.ChangeType {
		case domain.FileAdded:
			ev.Title = "Added " + fc.Path
		case domain.FileDeleted:
			ev.Title = "Deleted " + fc.Path
		default:
			ev.Title = "Modified " + fc.Path
		}
		if i < fileChangeCountMax {
			ev.Added, ev.Removed = e.lineCounts(workRoot, fc)
		}
		rec.Emit(ev)
	}
	rec.Emit(domain.RunEvent{Kind: domain.RunEventStatus, Status: "agent_finished",
		Title: fmt.Sprintf("The agent finished: %d file(s) changed", len(files))})
}

// lineCounts is fc's +/- line count, or 0/0 for a binary or oversized file
// or one whose base cannot be recovered.
func (e *Engine) lineCounts(workRoot string, fc domain.FileChange) (int, int) {
	const maxBytes = 1 << 20
	var next string
	if fc.ChangeType != domain.FileDeleted {
		b, err := os.ReadFile(filepath.Join(workRoot, filepath.FromSlash(fc.Path)))
		if err != nil || len(b) > maxBytes || strings.IndexByte(string(b), 0) >= 0 {
			return 0, 0
		}
		next = string(b)
	}
	prev, ok := e.baseContent(fc)
	if !ok || len(prev) > maxBytes || strings.IndexByte(prev, 0) >= 0 {
		return 0, 0
	}
	return textdiff.Counts(prev, next)
}
