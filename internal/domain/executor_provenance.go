package domain

import "time"

// ExecutorProvenance records which coding executor a run actually used, and
// why, on the run manifest.
//
// Requested is what harness.yaml asked for ("auto" when executor.kind is
// unset); Resolved is what ran. They differ only for auto, and the pair is the
// whole point: "requested: auto, resolved: internal" is a run that wrote a
// placeholder instead of code, and Warning says why and how to fix it. Before
// this existed an unset kind silently meant internal and nothing recorded
// that a real agent had never been tried.
type ExecutorProvenance struct {
	Requested string `json:"requested"`
	Resolved  string `json:"resolved"`
	// Image is the container image the agent ran in, as configured (a pinned
	// repo@sha256 reference for the default Hermes image). Empty for internal.
	Image string `json:"image,omitempty"`
	// Server is the remote Agent Server the run executed on (kind
	// openhands): where the work root was sent.
	Server string `json:"server,omitempty"`
	// Warning is non-empty when the run fell back to the placeholder executor.
	Warning string `json:"warning,omitempty"`
	// Permissions is every session/request_permission an ACP agent made, with
	// the answer the harness gave. The container is the sandbox boundary, so
	// each is auto-answered (allow_once when offered) -- and logged here, so
	// what was approved on the operator's behalf is never invisible.
	Permissions []ACPPermission `json:"permissions,omitempty"`
}

// ACPPermission is one permission request an ACP agent made and its answer.
type ACPPermission struct {
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Title      string    `json:"title,omitempty"`
	ToolKind   string    `json:"tool_kind,omitempty"`
	OptionID   string    `json:"option_id,omitempty"`
	Decision   string    `json:"decision"` // allow_once|allow_always|reject_once|reject_always|cancelled
	At         time.Time `json:"at"`
}

// RemoteRun is the durable record of one agent run delegated to a remote
// execution plane (executor kind openhands). It is what lets a daemon restart
// re-attach to a run that kept going on the server instead of starting it a
// second time, and what reconciliation drives to a terminal state.
type RemoteRun struct {
	ID       string `json:"id"`
	Executor string `json:"executor"`
	// IdempotencyKey is deterministic per (task, attempt): a retry after a
	// crash computes the same key and re-attaches.
	IdempotencyKey string `json:"idempotency_key"`
	InitiativeID   string `json:"initiative_id"`
	TaskID         string `json:"task_id"`
	Attempt        int    `json:"attempt"`
	ServerURL      string `json:"server_url"`
	// ConversationID is the remote server's id for the run.
	ConversationID string `json:"conversation_id,omitempty"`
	// RemoteDir is the remote workspace directory the work root was
	// uploaded to.
	RemoteDir string `json:"remote_dir,omitempty"`
	// BaseDigest is the snapshot digest of the work root as uploaded. A
	// finished run is only collected into a work root with the same digest:
	// its files were written against that base, not whatever the workspace
	// holds now.
	BaseDigest string          `json:"base_digest,omitempty"`
	Status     RemoteRunStatus `json:"status"`
	// RemoteStatus is the server's own last-reported status, verbatim.
	RemoteStatus string `json:"remote_status,omitempty"`
	// LastEventID is the event-ingestion cursor: the id of the newest remote
	// event already folded into Progress.
	LastEventID string `json:"last_event_id,omitempty"`
	EventCount  int    `json:"event_count"`
	// Progress is a compact, redacted log of the remote events (the last few
	// hundred lines), for the live view and for a run watched after a
	// restart.
	Progress   string     `json:"progress,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
