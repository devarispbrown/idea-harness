package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/textdiff"
)

// ohRunEvents maps one OpenHands Agent Server event (openhands-sdk
// event/*.py) onto normalized run events:
//
//	MessageEvent (source agent)          -> message
//	ActionEvent                          -> tool_call (+ file_change for a
//	                                        file_editor create/str_replace/
//	                                        insert, with +/- counts)
//	ObservationEvent                     -> tool_result (failed on a nonzero
//	                                        exit code or is_error)
//	AgentErrorEvent, ConversationError   -> error
//	ConversationStateUpdateEvent         -> status (execution_status)
//	PauseEvent, InterruptEvent           -> status
//
// The user's own MessageEvents are skipped: the first is the brief the
// harness sent, and a steering note is recorded by the engine when it is
// sent. An ActionEvent's thought/reasoning is never kept (§50).
func ohRunEvents(kind, source string, at time.Time, raw json.RawMessage) []domain.RunEvent {
	ev := func(e domain.RunEvent) domain.RunEvent {
		e.At = at
		if e.Source == "" {
			e.Source = domain.RunEventSourceAgent
		}
		return e
	}
	switch kind {
	case "MessageEvent":
		if source != "agent" {
			return nil
		}
		var m struct {
			LLMMessage struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"llm_message"`
		}
		_ = json.Unmarshal(raw, &m)
		var parts []string
		for _, c := range m.LLMMessage.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		text := strings.TrimSpace(strings.Join(parts, "\n"))
		if text == "" {
			return nil
		}
		return []domain.RunEvent{ev(domain.RunEvent{Kind: domain.RunEventMessage, Title: text, Detail: text})}
	case "ActionEvent":
		var a struct {
			ToolName   string `json:"tool_name"`
			ToolCallID string `json:"tool_call_id"`
			Summary    string `json:"summary"`
			Action     struct {
				Command  string  `json:"command"`
				Path     string  `json:"path"`
				FileText *string `json:"file_text"`
				OldStr   *string `json:"old_str"`
				NewStr   *string `json:"new_str"`
			} `json:"action"`
		}
		_ = json.Unmarshal(raw, &a)
		tool := firstNonEmpty(a.ToolName, "action")
		title := a.Summary
		if title == "" {
			title = strings.TrimSpace(tool + " " + firstNonEmpty(a.Action.Path, a.Action.Command))
		}
		call := domain.RunEvent{Kind: domain.RunEventToolCall, Title: title, ToolCallID: a.ToolCallID, ToolKind: tool,
			Status: "in_progress", Path: a.Action.Path}
		if a.Action.Command != "" && a.Action.Path == "" {
			// A terminal command: the command is the detail.
			call.Detail = a.Action.Command
		}
		out := []domain.RunEvent{ev(call)}
		if tool == "file_editor" || tool == "str_replace_editor" {
			if fc, ok := ohFileChange(a.Action.Command, a.Action.Path, a.Action.FileText, a.Action.OldStr, a.Action.NewStr); ok {
				out = append(out, ev(fc))
			}
		}
		return out
	case "ObservationEvent":
		var o struct {
			ToolName    string `json:"tool_name"`
			ToolCallID  string `json:"tool_call_id"`
			Observation struct {
				ExitCode *int `json:"exit_code"`
				Error    bool `json:"is_error"`
			} `json:"observation"`
		}
		_ = json.Unmarshal(raw, &o)
		r := domain.RunEvent{Kind: domain.RunEventToolResult, Title: firstNonEmpty(o.ToolName, "tool"), ToolCallID: o.ToolCallID,
			ToolKind: o.ToolName, Status: "completed"}
		switch {
		case o.Observation.ExitCode != nil && *o.Observation.ExitCode != 0:
			r.Status = "failed"
			r.Title = fmt.Sprintf("%s exited %d", r.Title, *o.Observation.ExitCode)
		case o.Observation.Error:
			r.Status = "failed"
			r.Title += " failed"
		}
		return []domain.RunEvent{ev(r)}
	case "AgentErrorEvent":
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return []domain.RunEvent{ev(domain.RunEvent{Kind: domain.RunEventError, Title: firstNonEmpty(e.Error, "agent error"), Detail: e.Error})}
	case "ConversationErrorEvent":
		var e struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Code + ": " + e.Detail)
		return []domain.RunEvent{ev(domain.RunEvent{Kind: domain.RunEventError, Title: msg, Detail: e.Detail, Source: domain.RunEventSourceHarness})}
	case "ConversationStateUpdateEvent":
		var s struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		_ = json.Unmarshal(raw, &s)
		if s.Key == "execution_status" {
			var v string
			if json.Unmarshal(s.Value, &v) == nil && v != "" {
				return []domain.RunEvent{ev(domain.RunEvent{Kind: domain.RunEventStatus, Title: "Server: " + v, Status: v, Source: domain.RunEventSourceHarness})}
			}
		}
		return nil
	case "PauseEvent":
		return []domain.RunEvent{ev(domain.RunEvent{Kind: domain.RunEventStatus, Title: "Paused", Status: "paused", Source: domain.RunEventSourceHarness})}
	case "InterruptEvent":
		return []domain.RunEvent{ev(domain.RunEvent{Kind: domain.RunEventStatus, Title: "Interrupted", Status: "interrupted", Source: domain.RunEventSourceHarness})}
	}
	return nil
}

// ohFileChange is the file_change an OpenHands file_editor action makes,
// with line counts where the action carries the text (create: the whole new
// file; str_replace: old and new; insert: the inserted lines). view and
// undo_edit change nothing countable here.
func ohFileChange(command, path string, fileText, oldStr, newStr *string) (domain.RunEvent, bool) {
	if path == "" {
		return domain.RunEvent{}, false
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	var added, removed int
	switch command {
	case "create":
		added, removed = textdiff.Counts("", str(fileText))
	case "str_replace":
		added, removed = textdiff.Counts(str(oldStr), str(newStr))
	case "insert":
		added, removed = textdiff.Counts("", str(newStr))
	default:
		return domain.RunEvent{}, false
	}
	return domain.RunEvent{Kind: domain.RunEventFileChange, Title: "Edited " + path, Path: path, Added: added, Removed: removed}, true
}

// ohStatsTotals reads the running token and cost totals from a
// ConversationStateUpdateEvent whose key is "stats".
//
// ASSUMED: the value is openhands-sdk's ConversationStats,
// {"usage_to_metrics": {"<usage id>": {"accumulated_cost": float,
// "accumulated_token_usage": {"prompt_tokens": int, "completion_tokens":
// int}}}}, summed over usage ids. Anything else reads as no totals, and no
// cost event is emitted -- never a guessed one.
func ohStatsTotals(value json.RawMessage) (in, out int64, cost float64, ok bool) {
	var st struct {
		UsageToMetrics map[string]struct {
			AccumulatedCost       float64 `json:"accumulated_cost"`
			AccumulatedTokenUsage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"accumulated_token_usage"`
		} `json:"usage_to_metrics"`
	}
	if json.Unmarshal(value, &st) != nil || len(st.UsageToMetrics) == 0 {
		return 0, 0, 0, false
	}
	for _, m := range st.UsageToMetrics {
		cost += m.AccumulatedCost
		if m.AccumulatedTokenUsage != nil {
			in += m.AccumulatedTokenUsage.PromptTokens
			out += m.AccumulatedTokenUsage.CompletionTokens
		}
	}
	return in, out, cost, true
}

// ohRemoteRel turns a server-side path under the run's remote directory into
// the work-root-relative one.
func ohRemoteRel(remoteDir, p string) string {
	if remoteDir != "" && strings.HasPrefix(p, remoteDir+"/") {
		return strings.TrimPrefix(p, remoteDir+"/")
	}
	return p
}

// ohTimestamp parses an event's timestamp (the server writes naive ISO 8601
// in UTC); now when it is missing or unreadable.
func ohTimestamp(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.999999", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// EmitsRunEvents: the watcher maps the server's own structured events.
func (e *OpenHandsExecutor) EmitsRunEvents() bool { return true }

// Steer sends a human's note into the running conversation as a user
// message. The server's agent loop reads it at its next step, so it is
// delivered, not queued; interrupt is not needed for that and is ignored.
func (e *OpenHandsExecutor) Steer(ctx context.Context, runID, note string, _ bool) (string, error) {
	a, err := e.attempt(runID)
	if err != nil {
		return "", ErrSteeringUnsupported
	}
	rec, err := e.runs.GetRemoteRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if rec.Status != domain.RemoteRunRunning {
		return "", ErrSteeringUnsupported
	}
	if err := a.client.sendMessage(ctx, a.convID, note); err != nil {
		if errors.Is(err, ErrOpenHandsNotFound) {
			return "", ErrSteeringUnsupported
		}
		return "", fmt.Errorf("executor: openhands: send the note: %s", e.scrub(err.Error(), a.secrets))
	}
	return domain.SteeringDelivered, nil
}

// ConversationURL is the run's page in an OpenHands web UI, when the
// operator configured one (executor.openhands.ui_url): <ui_url>/conversations/
// <conversation id>. Empty without a configured UI -- the Agent Server
// itself serves no UI, and its address is not guessed at.
func ConversationURL(uiURL, conversationID string) string {
	if uiURL == "" || conversationID == "" {
		return ""
	}
	return strings.TrimRight(uiURL, "/") + "/conversations/" + conversationID
}
