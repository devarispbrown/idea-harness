package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor/ohfake"
)

// remoteDirOf is the run's directory on the (fake) server: what the agent's
// own events name files under.
func (h *ohHarness) remoteDirOf() string {
	runs, err := h.db.ListRemoteRuns(context.Background(), false, 1)
	if err != nil || len(runs) == 0 {
		h.t.Fatalf("no remote run: %v", err)
	}
	return runs[0].RemoteDir
}

func (h *ohHarness) runEvents(runID string) []domain.RunEvent {
	h.t.Helper()
	evs, err := h.db.ListAllRunEvents(context.Background(), runID)
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

// The server's events reach the agent run's event stream, mapped and
// redacted, with paths relative to the work root: an edit with line counts,
// a failed command, the agent's words, a cost delta, an error. The brief
// (the user's own first message) and the agent's thought are not events.
func TestOpenHandsEventsReachTheRunStream(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		remote := h.remoteDirOf()
		c.Emit(map[string]any{"kind": "ActionEvent", "source": "agent", "tool_name": "file_editor", "tool_call_id": "call-1",
			"thought": []map[string]string{{"type": "text", "text": "SECRET THOUGHT"}},
			"action":  map[string]any{"command": "str_replace", "path": remote + "/a.txt", "old_str": "A\n", "new_str": "A1\nA2\n"}})
		c.Emit(map[string]any{"kind": "ObservationEvent", "source": "environment", "tool_name": "file_editor", "tool_call_id": "call-1",
			"observation": map[string]any{"is_error": false}})
		c.Emit(map[string]any{"kind": "ActionEvent", "source": "agent", "tool_name": "terminal", "tool_call_id": "call-2",
			"action": map[string]any{"command": "npm test"}})
		c.Emit(map[string]any{"kind": "ObservationEvent", "source": "environment", "tool_name": "terminal", "tool_call_id": "call-2",
			"observation": map[string]any{"exit_code": 1}})
		c.Emit(map[string]any{"kind": "ConversationStateUpdateEvent", "source": "environment", "key": "stats",
			"value": map[string]any{"usage_to_metrics": map[string]any{"agent": map[string]any{"accumulated_cost": 0.02,
				"accumulated_token_usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 50}}}}})
		c.Emit(map[string]any{"kind": "ConversationStateUpdateEvent", "source": "environment", "key": "stats",
			"value": map[string]any{"usage_to_metrics": map[string]any{"agent": map[string]any{"accumulated_cost": 0.05,
				"accumulated_token_usage": map[string]any{"prompt_tokens": 1500, "completion_tokens": 80}}}}})
		c.Emit(map[string]any{"kind": "AgentErrorEvent", "source": "agent", "error": "provider said no to " + testProviderKey})
		c.Say("done; the key was " + testProviderKey)
	}
	ctx := context.Background()
	req := h.req()
	req.RunID = "agent-run-1"
	att, err := h.ex.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if st := h.waitStatus(h.ex, att.ID); st.Status != "succeeded" {
		t.Fatalf("status = %+v", st)
	}
	evs := h.runEvents("agent-run-1")
	if len(evs) == 0 {
		t.Fatal("no run events from the remote run")
	}
	var call1 *domain.RunEvent
	var edit, failed, msg, errEv bool
	var costUSD float64
	var inTok int64
	for i, ev := range evs {
		blob, _ := json.Marshal(ev)
		if strings.Contains(string(blob), testProviderKey) || strings.Contains(string(blob), testSessionKey) {
			t.Fatalf("a key reached the event stream: %s", blob)
		}
		if strings.Contains(string(blob), "SECRET THOUGHT") || strings.Contains(string(blob), "the brief") {
			t.Fatalf("a thought or the brief became an event: %s", blob)
		}
		if ev.Executor != "openhands" || ev.InitiativeID != "ini-1" || ev.TaskID != "task-1" {
			t.Fatalf("event not stamped with the run's identity: %+v", ev)
		}
		switch {
		case ev.Kind == domain.RunEventToolCall && ev.ToolCallID == "call-1":
			call1 = &evs[i]
		case ev.Kind == domain.RunEventFileChange && ev.Path == "a.txt" && ev.Added == 2 && ev.Removed == 1:
			edit = true
		case ev.Kind == domain.RunEventToolResult && ev.ToolCallID == "call-2" && ev.Status == "failed" && ev.Title == "terminal exited 1":
			failed = true
		case ev.Kind == domain.RunEventMessage && strings.HasPrefix(ev.Detail, "done; the key was"):
			msg = true
		case ev.Kind == domain.RunEventError:
			errEv = true
		case ev.Kind == domain.RunEventCost:
			costUSD += ev.CostUSD
			inTok += ev.InputTokens
		}
	}
	if call1 == nil || call1.Path != "a.txt" || call1.ToolKind != "file_editor" {
		t.Fatalf("the file_editor call = %+v, want path a.txt relative to the work root", call1)
	}
	if !edit || !failed || !msg || !errEv {
		t.Fatalf("missing mapped events (edit %v, failed result %v, message %v, error %v) in %+v", edit, failed, msg, errEv, evs)
	}
	if costUSD < 0.0499 || costUSD > 0.0501 || inTok != 1500 {
		t.Fatalf("cost deltas sum to $%v / %d tokens, want $0.05 / 1500 (running totals turned into deltas)", costUSD, inTok)
	}
	if _, err := h.ex.Collect(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
}

// Across a daemon restart, the re-attached watcher continues the SAME run's
// event stream from the stored cursor: every server event once, none twice.
func TestOpenHandsRunEventsAreExactlyOnceAcrossRestart(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	step := make(chan struct{})
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		c.Say("before the restart")
		<-step
		c.Say("after the restart")
	}
	ctx := context.Background()
	req := h.req()
	req.RunID = "agent-run-2"
	att, err := h.ex.Start(ctx, req)
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
	h.sup.Detach()
	_ = h.ex.Cancel(ctx, att.ID)
	h.restart()
	close(step)
	conv := h.srv.OnlyConv(t)
	for conv.Status() != "finished" {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.sup.Reconcile(ctx, h.db, OpenHandsReconcile{SessionKey: func(string) string { return testSessionKey }}); err != nil {
		t.Fatal(err)
	}
	h.waitRecord(att.ID, domain.RemoteRunFinished)
	var before, after int
	for _, ev := range h.runEvents("agent-run-2") {
		switch ev.Detail {
		case "before the restart":
			before++
		case "after the restart":
			after++
		}
	}
	if before != 1 || after != 1 {
		t.Fatalf("messages before/after the restart = %d/%d, want 1/1", before, after)
	}
}

// A note to a running OpenHands run is a user message into its conversation
// (SendMessageRequest), delivered at once; a run that is over refuses.
func TestOpenHandsSteerSendsAMessageToTheConversation(t *testing.T) {
	h := newOHHarness(t, testSessionKey)
	h.write("a.txt", "A\n")
	got := make(chan struct{})
	h.srv.Agent = func(c *ohfake.Conv, dir string) {
		deadline := time.After(10 * time.Second)
		for len(c.Messages()) == 0 {
			select {
			case <-c.Released():
				return
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		close(got)
	}
	ctx := context.Background()
	att, err := h.ex.Start(ctx, h.req())
	if err != nil {
		t.Fatal(err)
	}
	h.waitRecord(att.ID, domain.RemoteRunRunning)
	delivery, err := h.ex.Steer(ctx, att.ID, "use the v2 endpoint", false)
	if err != nil || delivery != domain.SteeringDelivered {
		t.Fatalf("Steer = %q, %v; want delivered", delivery, err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never saw the message")
	}
	if msgs := h.srv.OnlyConv(t).Messages(); len(msgs) != 1 || msgs[0] != "use the v2 endpoint" {
		t.Fatalf("messages = %v", msgs)
	}
	if n := h.srv.Count("POST /api/conversations/{id}/events"); n != 1 {
		t.Fatalf("send-message calls = %d, want exactly 1 (never retried)", n)
	}
	if st := h.waitStatus(h.ex, att.ID); st.Status != "succeeded" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := h.ex.Collect(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ex.Steer(ctx, att.ID, "too late", false); !errors.Is(err, ErrSteeringUnsupported) {
		t.Fatalf("Steer after collect = %v, want ErrSteeringUnsupported", err)
	}
}

func TestOhRunEventsMapping(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	if evs := ohRunEvents("MessageEvent", "user", at, raw(map[string]any{"llm_message": map[string]any{"content": []map[string]string{{"type": "text", "text": "brief"}}}})); len(evs) != 0 {
		t.Fatalf("a user message became %+v", evs)
	}
	evs := ohRunEvents("ActionEvent", "agent", at, raw(map[string]any{"tool_name": "file_editor", "tool_call_id": "c",
		"action": map[string]any{"command": "create", "path": "/w/new.go", "file_text": "a\nb\nc\n"}}))
	if len(evs) != 2 || evs[1].Kind != domain.RunEventFileChange || evs[1].Added != 3 || !evs[0].At.Equal(at) {
		t.Fatalf("create = %+v", evs)
	}
	if evs := ohRunEvents("ActionEvent", "agent", at, raw(map[string]any{"tool_name": "file_editor", "action": map[string]any{"command": "view", "path": "/w/x"}})); len(evs) != 1 {
		t.Fatalf("a view is one tool call and no file change: %+v", evs)
	}
	if evs := ohRunEvents("ConversationStateUpdateEvent", "environment", at, raw(map[string]any{"key": "execution_status", "value": "running"})); len(evs) != 1 || evs[0].Status != "running" {
		t.Fatalf("status = %+v", evs)
	}
	if evs := ohRunEvents("ConversationErrorEvent", "environment", at, raw(map[string]any{"code": "E1", "detail": "boom"})); len(evs) != 1 || evs[0].Kind != domain.RunEventError {
		t.Fatalf("conversation error = %+v", evs)
	}
	if evs := ohRunEvents("SomethingNew", "agent", at, raw(map[string]any{})); evs != nil {
		t.Fatalf("an unknown kind = %+v, want nothing", evs)
	}
	if _, _, _, ok := ohStatsTotals(raw(map[string]any{"unexpected": 1})); ok {
		t.Fatal("an unknown stats shape produced totals")
	}
}

func TestConversationURL(t *testing.T) {
	if got := ConversationURL("https://oh.example.com/", "abc"); got != "https://oh.example.com/conversations/abc" {
		t.Fatalf("got %q", got)
	}
	if ConversationURL("", "abc") != "" || ConversationURL("https://oh.example.com", "") != "" {
		t.Fatal("a link was made without a UI or a conversation")
	}
}
