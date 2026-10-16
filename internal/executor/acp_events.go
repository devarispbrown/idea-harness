package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/textdiff"
)

// acpEvents maps one ACP session onto normalized run events:
//
//	session/update agent_message_chunk  -> message (chunks joined until the
//	                                       next non-message update)
//	session/update agent_thought_chunk  -> thought (once per run of chunks;
//	                                       the text is never kept, §50)
//	session/update tool_call            -> tool_call
//	session/update tool_call_update     -> tool_result (+ file_change for a
//	                                       diff content block)
//	session/update plan                 -> action ("Plan: 2/5 done")
//	session/update usage_update         -> cost (the delta of its running
//	                                       cost; ACP draft, read if present)
//	session/request_permission          -> permission
//	fs/write_text_file                  -> file_change with +/- counts
//	session/prompt response usage       -> cost (ACP draft, read if present)
//
// Everything else (available_commands_update, current_mode_update, an
// update this client does not know) is ignored: the run view is a summary.
type acpEvents struct {
	sink EventSink

	mu       sync.Mutex
	msg      strings.Builder
	thinking bool
	// titles remembers each tool call's title and kind, which a
	// tool_call_update need not repeat.
	titles map[string]acpToolInfo
	// lastCost is the running cost a usage_update last reported, so the
	// event carries the delta.
	lastCost float64
}

type acpToolInfo struct{ title, kind string }

// acpMessageFlushBytes bounds how much agent text is joined into one message
// event before it is sent anyway.
const acpMessageFlushBytes = 2048

func newACPEvents(sink EventSink) *acpEvents {
	return &acpEvents{sink: sink, titles: map[string]acpToolInfo{}}
}

func (m *acpEvents) emit(ev domain.RunEvent) {
	if m == nil || m.sink == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Source == "" {
		ev.Source = domain.RunEventSourceAgent
	}
	m.sink(ev)
}

// flushMessage sends the agent text gathered so far as one message event.
func (m *acpEvents) flushMessage() {
	if m == nil {
		return
	}
	m.mu.Lock()
	text := strings.TrimSpace(m.msg.String())
	m.msg.Reset()
	m.mu.Unlock()
	if text == "" {
		return
	}
	m.emit(domain.RunEvent{Kind: domain.RunEventMessage, Title: text, Detail: text})
}

// acpContainerRel turns an agent-side path (/work/src/x.ts) into the
// work-root-relative one the run view and the diff use (src/x.ts). A path
// outside /work is kept as given: it is what the agent said.
func acpContainerRel(p string) string {
	if p == "" {
		return ""
	}
	c := path.Clean(p)
	if c == acpWorkDir {
		return "."
	}
	if strings.HasPrefix(c, acpWorkDir+"/") {
		return strings.TrimPrefix(c, acpWorkDir+"/")
	}
	return p
}

type acpUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// promptUsage reports a session/prompt response's usage, when the agent sends
// one (an ACP draft extension: absent from protocol version 1 responses, and
// then nothing is emitted).
func (m *acpEvents) promptUsage(u *acpUsage) {
	if u == nil || (u.InputTokens == 0 && u.OutputTokens == 0) {
		return
	}
	m.emit(domain.RunEvent{Kind: domain.RunEventCost, Title: fmt.Sprintf("%d in / %d out tokens", u.InputTokens, u.OutputTokens),
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens})
}

func (m *acpEvents) permission(rec domain.ACPPermission) {
	if m == nil {
		return
	}
	m.flushMessage()
	title := firstNonEmpty(rec.Title, m.toolTitle(rec.ToolCallID), rec.ToolCallID, "a tool call")
	m.emit(domain.RunEvent{Kind: domain.RunEventPermission, Title: title, ToolCallID: rec.ToolCallID,
		ToolKind: rec.ToolKind, Status: rec.Decision, Source: domain.RunEventSourceHarness})
}

func (m *acpEvents) fileWrite(agentPath, prev, next string) {
	if m == nil {
		return
	}
	m.flushMessage()
	rel := acpContainerRel(agentPath)
	added, removed := textdiff.Counts(prev, next)
	m.emit(domain.RunEvent{Kind: domain.RunEventFileChange, Title: "Wrote " + rel, Path: rel, Added: added, Removed: removed})
}

func (m *acpEvents) toolTitle(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.titles[id].title
}

// update maps one session/update notification.
func (m *acpEvents) update(params json.RawMessage) {
	if m == nil || m.sink == nil {
		return
	}
	var n struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			Locations     []struct {
				Path string `json:"path"`
			} `json:"locations"`
			RawInput json.RawMessage `json:"rawInput"`
			Entries  []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
			Cost *struct {
				Amount   float64 `json:"amount"`
				Currency string  `json:"currency"`
			} `json:"cost"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	u := n.Update
	if u.SessionUpdate != "agent_message_chunk" {
		m.flushMessage()
	}
	if u.SessionUpdate != "agent_thought_chunk" {
		m.mu.Lock()
		m.thinking = false
		m.mu.Unlock()
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &c) != nil || c.Type != "text" {
			return
		}
		m.mu.Lock()
		m.msg.WriteString(c.Text)
		full := m.msg.Len() >= acpMessageFlushBytes
		m.mu.Unlock()
		if full {
			m.flushMessage()
		}
	case "agent_thought_chunk":
		m.mu.Lock()
		first := !m.thinking
		m.thinking = true
		m.mu.Unlock()
		if first {
			// The fact only: the thought's text is never stored (§50).
			m.emit(domain.RunEvent{Kind: domain.RunEventThought, Title: "Thinking"})
		}
	case "tool_call":
		info := acpToolInfo{title: u.Title, kind: u.Kind}
		m.mu.Lock()
		m.titles[u.ToolCallID] = info
		m.mu.Unlock()
		ev := domain.RunEvent{Kind: domain.RunEventToolCall, Title: firstNonEmpty(u.Title, u.Kind, "tool call"), ToolCallID: u.ToolCallID,
			ToolKind: u.Kind, Status: firstNonEmpty(u.Status, "pending")}
		if len(u.Locations) > 0 {
			ev.Path = acpContainerRel(u.Locations[0].Path)
		}
		ev.Detail = acpRawInputDetail(u.RawInput)
		m.emit(ev)
		m.diffs(u.Content)
	case "tool_call_update":
		m.mu.Lock()
		info := m.titles[u.ToolCallID]
		if u.Title != "" {
			info.title = u.Title
		}
		if u.Kind != "" {
			info.kind = u.Kind
		}
		m.titles[u.ToolCallID] = info
		m.mu.Unlock()
		ev := domain.RunEvent{Kind: domain.RunEventToolResult, Title: firstNonEmpty(info.title, info.kind, "tool call"), ToolCallID: u.ToolCallID,
			ToolKind: info.kind, Status: u.Status}
		if len(u.Locations) > 0 {
			ev.Path = acpContainerRel(u.Locations[0].Path)
		}
		if u.Status == "failed" {
			ev.Detail = acpContentText(u.Content)
		}
		m.emit(ev)
		m.diffs(u.Content)
	case "plan":
		done := 0
		lines := make([]string, 0, len(u.Entries))
		for _, e := range u.Entries {
			mark := "[ ]"
			switch e.Status {
			case "completed":
				done++
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			lines = append(lines, mark+" "+e.Content)
		}
		m.emit(domain.RunEvent{Kind: domain.RunEventAction, Title: fmt.Sprintf("Plan: %d/%d done", done, len(u.Entries)),
			Detail: strings.Join(lines, "\n"), ToolKind: "plan"})
	case "usage_update":
		// ASSUMED (ACP draft "session usage"): cost is the session's running
		// total; the event carries the delta.
		if u.Cost == nil || u.Cost.Amount <= 0 {
			return
		}
		m.mu.Lock()
		delta := u.Cost.Amount - m.lastCost
		m.lastCost = u.Cost.Amount
		m.mu.Unlock()
		if delta > 0 && (u.Cost.Currency == "" || strings.EqualFold(u.Cost.Currency, "USD")) {
			m.emit(domain.RunEvent{Kind: domain.RunEventCost, Title: fmt.Sprintf("$%.4f", delta), CostUSD: delta})
		}
	}
}

// diffs emits a file_change for each diff content block of a tool call: the
// agent's own account of an edit it made (an ACP agent that edits through
// its own tools, not fs/write_text_file, reports edits this way).
func (m *acpEvents) diffs(content json.RawMessage) {
	if len(content) == 0 {
		return
	}
	var blocks []struct {
		Type    string  `json:"type"`
		Path    string  `json:"path"`
		OldText *string `json:"oldText"`
		NewText string  `json:"newText"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "diff" || b.Path == "" {
			continue
		}
		old := ""
		if b.OldText != nil {
			old = *b.OldText
		}
		added, removed := textdiff.Counts(old, b.NewText)
		rel := acpContainerRel(b.Path)
		m.emit(domain.RunEvent{Kind: domain.RunEventFileChange, Title: "Edited " + rel, Path: rel, Added: added, Removed: removed})
	}
}

// acpContentText joins the text of a tool call's content blocks
// ({"type":"content","content":{"type":"text","text":...}}).
func acpContentText(content json.RawMessage) string {
	var blocks []struct {
		Type    string `json:"type"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "content" && b.Content.Type == "text" && b.Content.Text != "" {
			parts = append(parts, b.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// acpRawInputDetail is a tool call's input as the detail line: its command
// when it has one, else nothing (a file's whole new content is not a detail).
func acpRawInputDetail(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		Command any `json:"command"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Command == nil {
		return ""
	}
	switch c := in.Command.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// EmitsRunEvents: the ACP client maps the agent's own structured updates.
func (e *ACPExecutor) EmitsRunEvents() bool { return true }

// Steer queues a human's note as the agent's next prompt turn (or, with
// interrupt, ends the current turn with session/cancel and sends it at once).
func (e *ACPExecutor) Steer(ctx context.Context, id, note string, interrupt bool) (string, error) {
	a, err := e.attempt(id)
	if err != nil {
		return "", ErrSteeringUnsupported
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.steerClosed || a.status != "running" {
		return "", ErrSteeringUnsupported
	}
	a.notes = append(a.notes, note)
	if !interrupt {
		return domain.SteeringQueued, nil
	}
	a.interrupted = true
	select {
	case a.interruptCh <- struct{}{}:
	default:
	}
	return domain.SteeringDelivered, nil
}
