package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor/ohfake"
	"github.com/devarispbrown/idea-harness/internal/store"
)

const (
	testSessionKey  = "oh-session-key-0123456789"
	testProviderKey = "sk-ant-test-provider-key-abcdefghijklmnop"
)

// ohHarness wires an executor to a fake server, a real state database and a
// fast supervisor -- the pieces a "daemon" has. restart() builds a new
// supervisor and executor over the same database, the way a new daemon
// process would.
type ohHarness struct {
	t        *testing.T
	srv      *ohfake.Server
	db       *store.DB
	sup      *RemoteSupervisor
	ex       *OpenHandsExecutor
	opts     OpenHandsOptions
	workRoot string
	env      map[string]string
}

func newOHHarness(t *testing.T, serverKey string) *ohHarness {
	t.Helper()
	srv := ohfake.New(t, serverKey)
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &ohHarness{t: t, srv: srv, db: db, workRoot: filepath.Join(t.TempDir(), "work"),
		env:  map[string]string{"OPENHANDS_API_KEY": serverKey, "ANTHROPIC_API_KEY": testProviderKey},
		opts: OpenHandsOptions{URL: srv.URL(), Model: "anthropic/claude-sonnet-5", Timeout: time.Minute}}
	if err := os.MkdirAll(h.workRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	h.restart()
	return h
}

// restart is a daemon restart: the old supervisor detaches (its runs keep
// going on the server), and a new supervisor and executor take over.
func (h *ohHarness) restart() {
	if h.sup != nil {
		h.sup.Detach()
	}
	h.sup = NewRemoteSupervisor(nil)
	h.sup.SetPollIntervalForTest(5 * time.Millisecond)
	h.t.Cleanup(h.sup.Detach)
	h.ex = h.newExecutor()
}

func (h *ohHarness) newExecutor() *OpenHandsExecutor {
	ex := NewOpenHandsExecutor(h.opts, AgentProvider{Name: "anthropic", Type: config.ProviderAnthropic, KeyEnv: "ANTHROPIC_API_KEY",
		BaseURL: "https://api.anthropic.com/v1"}, h.db, h.sup)
	ex.SetKeyLookup(func(n string) string { return h.env[n] })
	ex.newClient = func(u, k string) (*ohClient, error) {
		c, err := newOHClient(u, k)
		if c != nil {
			c.retry = ohRetry{attempts: 4, base: time.Millisecond, max: 2 * time.Millisecond}
		}
		return c, err
	}
	return ex
}

func (h *ohHarness) write(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.workRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *ohHarness) read(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(h.workRoot, filepath.FromSlash(rel)))
	return string(b), err == nil
}

func (h *ohHarness) req() Request {
	return Request{InitiativeID: "ini-1", TaskID: "task-1", TaskTitle: "t", Brief: "edit the files", WorkRoot: h.workRoot}
}

// waitStatus polls Status until it leaves "running".
func (h *ohHarness) waitStatus(ex *OpenHandsExecutor, id string) Status {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := ex.Status(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		if st.Status != "running" {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("run did not settle")
	return Status{}
}

func (h *ohHarness) waitRecord(id string, want domain.RemoteRunStatus) *domain.RemoteRun {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := h.db.GetRemoteRun(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		if rec.Status == want {
			return rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, _ := h.db.GetRemoteRun(context.Background(), id)
	h.t.Fatalf("run status = %q (error %q), want %q", rec.Status, rec.Error, want)
	return nil
}

// editingAgent edits a.txt, deletes b.txt, adds sub/c.txt, and says so.
func editingAgent(c *ohfake.Conv, dir string) {
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A, edited remotely\n"), 0o644)
	_ = os.Remove(filepath.Join(dir, "b.txt"))
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("new\n"), 0o644)
	c.Emit(map[string]any{"kind": "ActionEvent", "source": "agent", "tool_name": "file_editor", "summary": "edit a.txt",
		"thought": []map[string]string{{"type": "text", "text": "SECRET THOUGHT"}}})
	c.Say("done editing")
}

// The happy path: the work root goes up, the agent edits it on the server,
// and the edits -- a change, a deletion, an addition -- come back into the
// local work root, where the engine's own snapshot will diff them.
func TestOpenHandsHappyPathRoundTripsFileChanges(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.write("b.txt", "B\n")
	h.write("keep.txt", "unchanged\n")
	h.srv.Agent = editingAgent

	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	if st := h.waitStatus(h.ex, att.ID); st.Status != "succeeded" {
		t.Fatalf("status = %+v, want succeeded", st)
	}
	tail := h.ex.OutputTail(ctx, att.ID, 50)
	if !strings.Contains(tail, "agent: done editing") || !strings.Contains(tail, "→ file_editor edit a.txt") {
		t.Fatalf("output tail lacks the agent's events:\n%s", tail)
	}
	if strings.Contains(tail, "SECRET THOUGHT") || strings.Contains(tail, "the brief") {
		t.Fatalf("output tail kept a thought or the user's brief:\n%s", tail)
	}
	if _, err := h.ex.Collect(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.read("a.txt"); got != "A, edited remotely\n" {
		t.Errorf("a.txt = %q", got)
	}
	if _, ok := h.read("b.txt"); ok {
		t.Error("b.txt was deleted on the server but survived locally")
	}
	if got, _ := h.read("sub/c.txt"); got != "new\n" {
		t.Errorf("sub/c.txt = %q", got)
	}
	if got, _ := h.read("keep.txt"); got != "unchanged\n" {
		t.Errorf("keep.txt = %q", got)
	}
	// The archive route's own metadata file is not part of the directory.
	if _, ok := h.read("archive_manifest.json"); ok {
		t.Error("the server's archive_manifest.json was mirrored into the work root")
	}
	rec, _ := h.db.GetRemoteRun(ctx, att.ID)
	if rec.Status != domain.RemoteRunCollected || rec.FinishedAt == nil {
		t.Fatalf("record = %+v, want collected", rec)
	}

	// What was sent: the OpenHands agent, the model in LiteLLM form, the
	// key, the run's own directory, and no confirmation policy override.
	if len(h.srv.Creates()) != 1 {
		t.Fatalf("creates = %d, want 1", len(h.srv.Creates()))
	}
	body := h.srv.Creates()[0]
	agent := body["agent"].(map[string]any)
	llm := agent["llm"].(map[string]any)
	if agent["kind"] != "Agent" || llm["model"] != "anthropic/claude-sonnet-5" || llm["api_key"] != testProviderKey {
		t.Fatalf("agent spec = %v", agent)
	}
	if _, ok := llm["base_url"]; ok {
		t.Errorf("a preset provider should not send base_url: %v", llm)
	}
	if body["conversation_id"] != rec.ConversationID || body["workspace"].(map[string]any)["working_dir"] != rec.RemoteDir {
		t.Fatalf("conversation id/dir mismatch: %v vs %+v", body, rec)
	}
	if _, ok := body["confirmation_policy"]; ok {
		t.Error("confirmation_policy should be left to the server's NeverConfirm default")
	}
	if !strings.HasPrefix(rec.RemoteDir, config.DefaultOpenHandsWorkspaceDir+"/") {
		t.Errorf("remote dir %q is not under the default workspace dir", rec.RemoteDir)
	}
}

// A retry after a crash re-attaches: the same (task, attempt) key finds the
// same row and the same conversation, and nothing is uploaded or created a
// second time -- including when the crash fell between "conversation
// created" and "status recorded".
func TestOpenHandsRedispatchIsIdempotent(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.write("b.txt", "B\n")
	release := make(chan struct{})
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		select {
		case <-release:
		case <-c.Released():
		}
		editingAgent(c, dir)
	}
	ctx := context.Background()
	first, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.waitRecord(first.ID, domain.RemoteRunRunning)

	// Crash between create and the status write: the row says dispatching.
	st := domain.RemoteRunDispatching
	if err := h.db.UpdateRemoteRun(ctx, first.ID, store.RemoteRunUpdate{Status: &st}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	second, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-dispatch made a new run %s, want %s", second.ID, first.ID)
	}
	if n := h.srv.Count("POST /api/conversations"); n != 1 {
		t.Fatalf("conversation creates = %d, want 1", n)
	}
	if n := h.srv.Count("POST /api/file/upload"); n != 1 {
		t.Fatalf("uploads = %d, want 1", n)
	}
	// And a third, plain re-attach to a running row.
	h.restart()
	third, err := h.ex.Start(ctx, h.req())
	if err != nil || third.ID != first.ID {
		t.Fatalf("third start = %v, %v", third, err)
	}
	close(release)
	if st := h.waitStatus(h.ex, third.ID); st.Status != "succeeded" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := h.ex.Collect(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	if n := h.srv.Count("POST /api/conversations"); n != 1 {
		t.Fatalf("conversation creates = %d, want 1", n)
	}
	// The next attempt of the task is a new run.
	h.write("a.txt", "A again\n")
	fourth, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	if fourth.ID == first.ID {
		t.Fatal("a collected run was re-attached to instead of starting attempt 2")
	}
	rec, _ := h.db.GetRemoteRun(ctx, fourth.ID)
	if rec.Attempt != 2 || rec.IdempotencyKey != IdempotencyKey("task-1", 2) {
		t.Fatalf("attempt 2 record = %+v", rec)
	}
}

// The daemon restarts mid-run: the old daemon detaches without stopping the
// run, the new one's reconciliation catches up on the events it missed from
// the stored cursor (each exactly once) and follows the run to the end, and
// the task's next execute collects the result instead of starting again.
func TestOpenHandsDaemonRestartReconcilesAndCollects(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.write("b.txt", "B\n")
	step := make(chan struct{})
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		c.Say("before the restart")
		<-step
		c.Say("after the restart")
		editingAgent(c, dir)
	}
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.ex.OutputTail(ctx, att.ID, 50), "before the restart") {
		if time.Now().After(deadline) {
			t.Fatal("never ingested the first event")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Shutdown: the operation is cancelled, which calls Cancel -- and must
	// NOT stop the run on the server.
	h.sup.Detach()
	if err := h.ex.Cancel(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	if n := h.srv.Count("POST /api/conversations/{id}/interrupt"); n != 0 {
		t.Fatalf("shutdown interrupted the remote run (%d calls)", n)
	}
	h.restart()

	close(step) // the agent carries on while no daemon is attached
	conv := h.srv.OnlyConv(t)
	for conv.Status() != "finished" {
		time.Sleep(5 * time.Millisecond)
	}

	n, err := h.sup.Reconcile(ctx, h.db, OpenHandsReconcile{SessionKey: func(string) string { return testSessionKey }})
	if err != nil || n != 1 {
		t.Fatalf("reconcile = %d, %v; want 1 run re-attached", n, err)
	}
	rec := h.waitRecord(att.ID, domain.RemoteRunFinished)
	if rec.EventCount != conv.EventCount() {
		t.Fatalf("ingested %d events, the server has %d (lost or duplicated)", rec.EventCount, conv.EventCount())
	}
	if strings.Count(rec.Progress, "before the restart") != 1 || !strings.Contains(rec.Progress, "after the restart") {
		t.Fatalf("progress after catch-up:\n%s", rec.Progress)
	}

	// The retry: a fresh seed of the same workspace has the same digest, so
	// the finished run is collected, not dispatched again.
	if err := os.RemoveAll(h.workRoot); err != nil {
		t.Fatal(err)
	}
	h.write("a.txt", "A\n")
	h.write("b.txt", "B\n")
	again, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != att.ID {
		t.Fatalf("retry started run %s, want to collect %s", again.ID, att.ID)
	}
	if st := h.waitStatus(h.ex, again.ID); st.Status != "succeeded" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := h.ex.Collect(ctx, again.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.read("a.txt"); got != "A, edited remotely\n" {
		t.Fatalf("a.txt = %q after collecting the reconciled run", got)
	}
	if n := h.srv.Count("POST /api/conversations"); n != 1 {
		t.Fatalf("conversation creates = %d, want 1", n)
	}
}

// A retry against a workspace that changed since dispatch does not collect
// the stale result: the old run is retired and a new attempt starts.
func TestOpenHandsStaleBaseStartsNewAttempt(t *testing.T) {
	h := newOHHarness(t, "")
	h.write("a.txt", "A\n")
	block := make(chan struct{})
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		select {
		case <-block:
		case <-c.Released():
		}
	}
	ctx := context.Background()
	first, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.write("a.txt", "A, edited by the human meanwhile\n")
	second, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("re-attached to a run dispatched against a different workspace")
	}
	old := h.waitRecord(first.ID, domain.RemoteRunFailed)
	if !strings.Contains(old.Error, "workspace changed") {
		t.Fatalf("retired run's reason = %q", old.Error)
	}
	if h.srv.Count("POST /api/conversations/{id}/interrupt") == 0 {
		t.Fatal("the stale run was not stopped on the server")
	}
	close(block)
}

// The conversation disappears from the server mid-run: the run fails with a
// reason that says so, and a reconciler finding it gone says the same.
func TestOpenHandsRemoteVanished(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.srv.Agent = func(c *ohfake.Conv, dir string) { <-c.Released() }
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.waitRecord(att.ID, domain.RemoteRunRunning)
	h.srv.DeleteConv(h.srv.OnlyConv(t).ID())
	st := h.waitStatus(h.ex, att.ID)
	if st.Status != "failed" || !strings.Contains(st.Action, "no longer exists") {
		t.Fatalf("status = %+v, want failed: vanished", st)
	}

	// The same, discovered by reconciliation after a restart.
	h2 := newOHHarness(t, testSessionKey)
	h2.write("a.txt", "A\n")
	h2.srv.Agent = func(c *ohfake.Conv, dir string) { <-c.Released() }
	att2, err := h2.ex.Start(ctx, h2.req())
	if err != nil {
		t.Fatal(err)
	}
	h2.waitRecord(att2.ID, domain.RemoteRunRunning)
	h2.restart()
	h2.srv.DeleteConv(h2.srv.OnlyConv(t).ID())
	if _, err := h2.sup.Reconcile(ctx, h2.db, OpenHandsReconcile{SessionKey: func(string) string { return testSessionKey }}); err != nil {
		t.Fatal(err)
	}
	rec := h2.waitRecord(att2.ID, domain.RemoteRunFailed)
	if !strings.Contains(rec.Error, "no longer exists") {
		t.Fatalf("reconciled reason = %q", rec.Error)
	}
}

// Reconciliation of a run the daemon never finished dispatching, with no
// conversation on the server, fails it: nothing ran remotely.
func TestOpenHandsReconcileUndispatchedRun(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	ctx := context.Background()
	rec, _, err := h.db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: IdempotencyKey("t", 1), TaskID: "t", Attempt: 1,
		ServerURL: h.srv.URL(), ConversationID: conversationIDFor(IdempotencyKey("t", 1)), RemoteDir: "/workspace/idea-harness/x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.sup.Reconcile(ctx, h.db, OpenHandsReconcile{SessionKey: func(string) string { return testSessionKey }}); err != nil {
		t.Fatal(err)
	}
	got := h.waitRecord(rec.ID, domain.RemoteRunFailed)
	if !strings.Contains(got.Error, "nothing ran remotely") {
		t.Fatalf("reason = %q", got.Error)
	}
}

// Cancel (idea cancel / Stop) interrupts the run on the server, records it
// cancelled, and brings back what the agent had written so Stop keeps it.
func TestOpenHandsCancelInterruptsAndKeepsPartialWork(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	wrote := make(chan struct{})
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		_ = os.WriteFile(filepath.Join(dir, "partial.txt"), []byte("half done\n"), 0o644)
		close(wrote)
		<-c.Released()
	}
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	<-wrote
	h.waitRecord(att.ID, domain.RemoteRunRunning)
	if err := h.ex.Cancel(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	if n := h.srv.Count("POST /api/conversations/{id}/interrupt"); n != 1 {
		t.Fatalf("interrupt calls = %d, want 1", n)
	}
	if s := h.srv.OnlyConv(t).Status(); s != "paused" {
		t.Fatalf("remote status = %q, want paused", s)
	}
	rec, _ := h.db.GetRemoteRun(ctx, att.ID)
	if rec.Status != domain.RemoteRunCancelled {
		t.Fatalf("record = %q, want cancelled", rec.Status)
	}
	if got, _ := h.read("partial.txt"); got != "half done\n" {
		t.Fatalf("partial work not brought back: %q", got)
	}
	// The watcher sees the remote pause but must not overwrite cancelled.
	time.Sleep(50 * time.Millisecond)
	rec, _ = h.db.GetRemoteRun(ctx, att.ID)
	if rec.Status != domain.RemoteRunCancelled {
		t.Fatalf("cancelled run became %q", rec.Status)
	}
	if _, err := h.ex.Status(ctx, att.ID); !errors.Is(err, ErrUnknownAttempt) {
		t.Fatalf("Status after Cancel: %v, want ErrUnknownAttempt", err)
	}
}

// Transient failures (503s) on dispatch and on following are retried with
// backoff; the run completes.
func TestOpenHandsRetriesTransient5xx(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.write("b.txt", "B\n")
	h.srv.Agent = editingAgent
	h.srv.FailNext("POST /api/file/upload", 2)
	h.srv.FailNext("POST /api/conversations", 3)
	h.srv.FailNext("GET /api/conversations/{id}/events/search", 2)
	h.srv.FailNext("GET /api/file/archive", 1)
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	if st := h.waitStatus(h.ex, att.ID); st.Status != "succeeded" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := h.ex.Collect(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.read("a.txt"); got != "A, edited remotely\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if n := h.srv.Count("POST /api/conversations"); n != 4 {
		t.Fatalf("create calls = %d, want 3 refused + 1", n)
	}
	if len(h.srv.Creates()) != 1 {
		t.Fatalf("conversations created = %d, want 1", len(h.srv.Creates()))
	}

	// A server that stays down exhausts the retries and says so.
	h2 := newOHHarness(t, testSessionKey)
	h2.write("a.txt", "A\n")
	h2.srv.FailNext("POST /api/file/upload", 100)
	if _, err := h2.ex.Start(ctx, h2.req()); err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "after 4 attempts") {
		t.Fatalf("persistent 503: %v", err)
	}
}

// A wrong session key is an auth failure, reported as one, never retried,
// and the key never appears in an error.
func TestOpenHandsAuthFailure(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.env["OPENHANDS_API_KEY"] = "wrong-session-key-123456"
	h.ex = h.newExecutor()
	_, err := h.ex.Start(context.Background(), h.req())
	if !errors.Is(err, ErrOpenHandsAuth) {
		t.Fatalf("Start with a wrong key: %v, want ErrOpenHandsAuth", err)
	}
	if strings.Contains(err.Error(), "wrong-session-key") {
		t.Fatal("the session key leaked into the error")
	}
	if n := h.srv.Count("GET /api/conversations/{id}"); n != 1 {
		t.Fatalf("auth failure was retried: %d calls", n)
	}
	if _, err := ProbeOpenHands(context.Background(), h.srv.URL(), "wrong-session-key-123456"); err == nil || !strings.Contains(err.Error(), "refused the session API key") {
		t.Fatalf("probe with a wrong key: %v", err)
	}
	if v, err := ProbeOpenHands(context.Background(), h.srv.URL(), testSessionKey); err != nil || v != "1.54.0" {
		t.Fatalf("probe = %q, %v", v, err)
	}
}

// Plain http to a non-loopback host is refused before anything is sent.
func TestOpenHandsRefusesNonHTTPS(t *testing.T) {
	h := newOHHarness(t, "")
	h.write("a.txt", "A\n")
	h.opts.URL = "http://agents.example.com"
	h.ex = h.newExecutor()
	_, err := h.ex.Start(context.Background(), h.req())
	if err == nil || !strings.Contains(err.Error(), "use https") {
		t.Fatalf("Start over plain http: %v", err)
	}
	if runs, _ := h.db.ListRemoteRuns(context.Background(), false, 0); len(runs) != 0 {
		t.Fatal("a run was recorded for a refused URL")
	}
	if _, err := ProbeOpenHands(context.Background(), "http://agents.example.com", ""); err == nil {
		t.Fatal("probe accepted plain http to a remote host")
	}
	// Loopback over http is fine (the fake server is one).
	if err := config.ValidateOpenHandsURL(h.srv.URL()); err != nil {
		t.Fatal(err)
	}
}

// A missing provider key stops the run before anything leaves the machine;
// withholding it sends none.
func TestOpenHandsProviderKeyHandling(t *testing.T) {
	h := newOHHarness(t, "")
	h.write("a.txt", "A\n")
	delete(h.env, "ANTHROPIC_API_KEY")
	h.ex = h.newExecutor()
	if _, err := h.ex.Start(context.Background(), h.req()); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("missing key: %v", err)
	}
	if h.srv.Count("POST /api/file/upload") != 0 {
		t.Fatal("the work root was uploaded before the key check")
	}
	h.opts.WithholdProviderKey = true
	h.ex = h.newExecutor()
	att, err := h.ex.Start(context.Background(), h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(h.ex, att.ID)
	llm := h.srv.Creates()[0]["agent"].(map[string]any)["llm"].(map[string]any)
	if _, ok := llm["api_key"]; ok {
		t.Fatal("a withheld key was sent")
	}
}

// agent: acp starts an ACPAgent with the configured argv and hands the
// provider key over as a conversation secret.
func TestOpenHandsACPAgentSpec(t *testing.T) {
	h := newOHHarness(t, "")
	h.write("a.txt", "A\n")
	h.opts.Agent, h.opts.ACPCommand = "acp", []string{"claude-agent-acp"}
	h.ex = h.newExecutor()
	att, err := h.ex.Start(context.Background(), h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(h.ex, att.ID)
	body := h.srv.Creates()[0]
	agent := body["agent"].(map[string]any)
	if agent["kind"] != "ACPAgent" || agent["acp_command"].([]any)[0] != "claude-agent-acp" {
		t.Fatalf("agent = %v", agent)
	}
	sec := body["secrets"].(map[string]any)["ANTHROPIC_API_KEY"].(map[string]any)
	if sec["kind"] != "StaticSecret" || sec["value"] != testProviderKey {
		t.Fatalf("secrets = %v", body["secrets"])
	}
}

// A run that outlives its timeout is interrupted on the server and failed.
func TestOpenHandsTimeoutInterrupts(t *testing.T) {
	h := newOHHarness(t, "")
	h.write("a.txt", "A\n")
	h.opts.Timeout = 50 * time.Millisecond
	h.ex = h.newExecutor()
	h.srv.Agent = func(c *ohfake.Conv, dir string) { <-c.Released() }
	att, err := h.ex.Start(context.Background(), h.req())
	if err != nil {
		t.Fatal(err)
	}
	st := h.waitStatus(h.ex, att.ID)
	if st.Status != "failed" || !strings.Contains(st.Action, "did not finish within") {
		t.Fatalf("status = %+v", st)
	}
	if h.srv.Count("POST /api/conversations/{id}/interrupt") == 0 {
		t.Fatal("timed-out run was not interrupted on the server")
	}
}

// An error status on the server fails the run with the agent's own error,
// and secrets in that error are redacted.
func TestOpenHandsAgentErrorIsReportedRedacted(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		c.Emit(map[string]any{"kind": "AgentErrorEvent", "source": "agent", "error": "401 from provider with key " + testProviderKey})
		c.Say("my key is " + testProviderKey + " and the session is " + testSessionKey)
		c.SetStatus("error")
	}
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	st := h.waitStatus(h.ex, att.ID)
	if st.Status != "failed" || !strings.Contains(st.Action, "401 from provider") {
		t.Fatalf("status = %+v", st)
	}
	rec, _ := h.db.GetRemoteRun(ctx, att.ID)
	for _, s := range []string{rec.Progress, rec.Error, st.Action, h.ex.OutputTail(ctx, att.ID, 50)} {
		if strings.Contains(s, testProviderKey) || strings.Contains(s, testSessionKey) {
			t.Fatalf("a secret reached stored or shown output: %q", s)
		}
	}
}

// Archive handling: entries that escape, links, and control-plane paths are
// never written; a file the archive route leaves out (its default excludes)
// but that still exists is fetched on its own rather than read as deleted.
func TestOpenHandsArchiveSafety(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		h.Mode = 0o644
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	add(&tar.Header{Name: "conv-1/ok.txt"}, "fine")
	add(&tar.Header{Name: "conv-1/archive_manifest.json"}, `{"format":"tar.gz"}`)
	add(&tar.Header{Name: "../escape.txt"}, "x")
	add(&tar.Header{Name: "/abs.txt"}, "x")
	add(&tar.Header{Name: ".git/hooks/pre-commit"}, "x")
	add(&tar.Header{Name: ".idea-harness/harness.yaml"}, "x")
	add(&tar.Header{Name: "node_modules/pkg/index.js"}, "x")
	add(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	_ = tw.Close()
	_ = gz.Close()

	staging := t.TempDir()
	files, skipped, err := unpackArchive(buf.Bytes(), staging, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !files["ok.txt"] {
		t.Fatalf("files = %v, want only ok.txt", files)
	}
	if len(skipped) != 6 {
		t.Fatalf("skipped = %v, want the six unsafe entries", skipped)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(staging), "escape.txt")); err == nil {
		t.Fatal("an escaping entry was written")
	}

	// Excluded-but-present: the fake leaves "data.log" out of archives.
	h := newOHHarness(t, "")
	h.write("a.txt", "A\n")
	h.write("data.log", "kept\n")
	h.srv.Exclude("data.log")
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A2\n"), 0o644)
		// A real file that shares the metadata file's name is kept.
		_ = os.WriteFile(filepath.Join(dir, "archive_manifest.json"), []byte("{\"mine\":true}\n"), 0o644)
	}
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(h.ex, att.ID)
	if _, err := h.ex.Collect(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.read("data.log"); !ok || got != "kept\n" {
		t.Fatalf("an excluded file was treated as deleted: %q %v", got, ok)
	}
	if got, _ := h.read("archive_manifest.json"); got != "{\"mine\":true}\n" {
		t.Fatalf("the agent's own archive_manifest.json = %q", got)
	}
	if got, _ := h.read("a.txt"); got != "A2\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestLiteLLMModelMapping(t *testing.T) {
	cases := []struct {
		spec      string
		p         AgentProvider
		wantModel string
		wantBase  string
	}{
		{"openai/gpt-5.6-luna", AgentProvider{Name: "openai", BaseURL: "https://api.openai.com/v1"}, "openai/gpt-5.6-luna", ""},
		{"openrouter/anthropic/claude-sonnet-5", AgentProvider{Name: "openrouter"}, "openrouter/anthropic/claude-sonnet-5", ""},
		{"ollama/qwen3.8:latest", AgentProvider{Name: "ollama", BaseURL: "http://localhost:11434/v1"}, "ollama/qwen3.8:latest", "http://localhost:11434"},
		{"openai-work/gpt-5.6-luna", AgentProvider{Name: "openai-work", BaseURL: "https://api.openai.com/v1"}, "openai/gpt-5.6-luna", ""},
		{"corp/llama-4", AgentProvider{Name: "corp", Type: config.ProviderOpenAI, BaseURL: "https://llm.corp.example/v1"}, "openai/llama-4", "https://llm.corp.example/v1"},
	}
	for _, tc := range cases {
		m, b := liteLLMModel(tc.spec, tc.p)
		if m != tc.wantModel || b != tc.wantBase {
			t.Errorf("liteLLMModel(%q) = %q, %q; want %q, %q", tc.spec, m, b, tc.wantModel, tc.wantBase)
		}
	}
}

// The smoke runs the shared trivial task through the server and passes only
// when a file comes back; an agent that writes nothing fails it with the
// agent's own output.
func TestSmokeOpenHands(t *testing.T) {
	srv := ohfake.New(t, testSessionKey)
	srv.Agent = func(c *ohfake.Conv, dir string) {
		_ = os.WriteFile(filepath.Join(dir, "smoke.txt"), []byte("smoke ok\n"), 0o644)
		c.Say("wrote smoke.txt")
	}
	env := map[string]string{"OPENHANDS_API_KEY": testSessionKey, "ANTHROPIC_API_KEY": testProviderKey}
	opts := OpenHandsOptions{URL: srv.URL(), Model: "anthropic/claude-sonnet-5", Timeout: time.Minute}
	provider := AgentProvider{Name: "anthropic", Type: config.ProviderAnthropic, KeyEnv: "ANTHROPIC_API_KEY"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := SmokeOpenHands(ctx, opts, provider, func(n string) string { return env[n] }, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	srv.Agent = func(c *ohfake.Conv, dir string) { c.Say("I refuse") }
	err := SmokeOpenHands(ctx, opts, provider, func(n string) string { return env[n] }, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "I refuse") {
		t.Fatalf("an agent that wrote nothing: %v", err)
	}
}

func TestOpenHandsCompatIsUntestedNotBlocked(t *testing.T) {
	c := CheckModelCompat("openhands", "anthropic/claude-sonnet-5")
	if c.Blocked || !strings.Contains(c.Message, "--kind openhands --url") {
		t.Fatalf("compat = %+v", c)
	}
}

func TestConversationIDIsADeterministicUUID(t *testing.T) {
	a, b := conversationIDFor(IdempotencyKey("t", 1)), conversationIDFor(IdempotencyKey("t", 1))
	if a != b {
		t.Fatal("not deterministic")
	}
	if a == conversationIDFor(IdempotencyKey("t", 2)) {
		t.Fatal("attempts share a conversation id")
	}
	if len(a) != 36 || a[14] != '8' || strings.Count(a, "-") != 4 {
		t.Fatalf("%q is not a version-8 UUID", a)
	}
}
