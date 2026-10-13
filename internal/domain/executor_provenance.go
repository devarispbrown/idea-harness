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
