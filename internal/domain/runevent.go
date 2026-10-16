package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// RunEvent is one normalized step of a running coding agent, whichever
// executor ran it: an ACP session/update, an OpenHands Agent Server event, a
// line of a CLI agent's output, a file the agent wrote. Every executor maps
// its own vocabulary onto this one so the run view, the report and any client
// read one shape.
//
// Seq is monotonic per run (1, 2, 3, ...) and is the resume cursor: a client
// that has seen seq N asks for the events after N and gets only those. Text
// fields are redacted before an event is stored, and bounded (see
// RunEventTitleMax/RunEventDetailMax); Truncated says a field was cut.
type RunEvent struct {
	Seq          int64        `json:"seq"`
	RunID        string       `json:"run_id"`
	OperationID  string       `json:"operation_id,omitempty"`
	InitiativeID string       `json:"initiative_id,omitempty"`
	TaskID       string       `json:"task_id,omitempty"`
	Executor     string       `json:"executor,omitempty"`
	At           time.Time    `json:"at"`
	Kind         RunEventKind `json:"kind"`
	// Source is who the event came from: "agent" (the coding agent itself),
	// "harness" (the executor or engine: phases, file scans, the final
	// diff) or "user" (a steering note).
	Source string `json:"source,omitempty"`
	// Title is the one-line summary the run view shows ("Edit src/x.ts",
	// "npm test exited 1").
	Title string `json:"title"`
	// Detail is the longer text, when there is any: an agent message, a
	// command, an error. Bounded to RunEventDetailMax.
	Detail string `json:"detail,omitempty"`
	// Path is the work-root-relative file the event is about, when it is
	// about one.
	Path string `json:"path,omitempty"`
	// ToolCallID ties a tool_call to its tool_result(s) and permissions.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolKind is the tool's category as the agent reported it (ACP's
	// read|edit|delete|move|search|execute|think|fetch|other, or an
	// OpenHands tool name).
	ToolKind string `json:"tool_kind,omitempty"`
	// Status is the tool call's state (pending|in_progress|completed|
	// failed), a permission's decision, a steering note's delivery, or a
	// status event's lifecycle state.
	Status string `json:"status,omitempty"`
	// Added/Removed are line counts on an authoritative file_change.
	Added   int `json:"added,omitempty"`
	Removed int `json:"removed,omitempty"`
	// Token and cost DELTAS on a cost event (not running totals).
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	// ChangeSetID is set on the status event that ends a run which produced
	// a change set: the run's result, for the run view and report to link.
	ChangeSetID string `json:"change_set_id,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// Event sources.
const (
	RunEventSourceAgent   = "agent"
	RunEventSourceHarness = "harness"
	RunEventSourceUser    = "user"
)

// Bounds on one stored event's text. The run view is a live summary, not an
// archive of the agent's transcript: a 2 MB file write or a pasted log would
// otherwise land in the database and in every client's memory.
const (
	RunEventTitleMax  = 240
	RunEventDetailMax = 4096
	RunEventPathMax   = 1024
)

// Bounded returns the event with its text fields cut to the limits above (on
// a rune boundary), Truncated set when anything was cut, and an invalid kind
// coerced to action so a mapping bug cannot store an event no client knows.
func (e RunEvent) Bounded() RunEvent {
	var cut bool
	e.Title, cut = truncateRunes(strings.TrimSpace(firstLine(e.Title)), RunEventTitleMax)
	e.Truncated = e.Truncated || cut
	e.Detail, cut = truncateRunes(e.Detail, RunEventDetailMax)
	e.Truncated = e.Truncated || cut
	e.Path, cut = truncateRunes(e.Path, RunEventPathMax)
	e.Truncated = e.Truncated || cut
	if !e.Kind.Valid() {
		e.Kind = RunEventAction
	}
	return e
}

// Redacted returns the event with every free-text field passed through
// redact. Kind, ids, counts and the timestamp are structural and kept.
func (e RunEvent) Redacted(redact func(string) string) RunEvent {
	if redact == nil {
		return e
	}
	e.Title = redact(e.Title)
	e.Detail = redact(e.Detail)
	e.Path = redact(e.Path)
	e.ToolKind = redact(e.ToolKind)
	return e
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncateRunes(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…", true
}

// Steering note deliveries.
const (
	SteeringDelivered = "delivered"
	SteeringQueued    = "queued"
	SteeringNextRun   = "next_run"
)

// SteeringNote is a note a human sent to a running agent, recorded on the
// run manifest (ExecutorProvenance.Steering) and as a message event.
// Delivery says what became of it: "delivered" (the agent received it now),
// "queued" (it is sent as the agent's next prompt turn when the current one
// ends) or "next_run" (the executor cannot be steered; it rides on the
// task's next run, like the rerun note on execute).
type SteeringNote struct {
	At       time.Time `json:"at"`
	Note     string    `json:"note"`
	Delivery string    `json:"delivery"`
}
