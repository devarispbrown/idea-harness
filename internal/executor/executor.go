// Package executor defines the worker abstraction (§48–§50). The deterministic
// `internal` executor is the first implementation; OMP is validated behind this
// same interface as `local_external`.
package executor

import (
	"context"
	"errors"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Executor runs a Task in an isolated worker and returns a patch.
type Executor interface {
	Name() string
	Capabilities(ctx context.Context) Capabilities
	Start(ctx context.Context, req Request) (Attempt, error)
	Status(ctx context.Context, id string) (Status, error)
	Cancel(ctx context.Context, id string) error
	Collect(ctx context.Context, id string) (Result, error)
}

// OutputTailer is implemented by executors that run an external agent and can
// report the end of its own output. The engine uses it to say WHY an attempt
// failed or changed nothing -- a 401, a retired model id -- which the attempt
// status alone does not carry. It must be read before Collect or Cancel, which
// discard the output.
type OutputTailer interface {
	OutputTail(ctx context.Context, id string, lines int) string
}

// Capabilities declares the executor's trust, sandbox mode, and (deterministic,
// not model-granted) capabilities.
type Capabilities struct {
	Trust        domain.ExecutorTrust `json:"trust"`
	SandboxMode  domain.SandboxMode   `json:"sandbox_mode"`
	Network      bool                 `json:"network"`
	Capabilities []string             `json:"capabilities"`
}

// Request is one execution request. The canonical workspace is never passed as a
// writable root — the executor receives an isolated WorkRoot (§37). Brief is the
// planner-produced implementation brief (rendered text) the implementer should
// follow; when empty the executor falls back to TaskStatement.
type Request struct {
	InitiativeID  string `json:"initiative_id"`
	TaskID        string `json:"task_id"`
	TaskTitle     string `json:"task_title"`
	TaskStatement string `json:"task_statement"`
	Brief         string `json:"brief,omitempty"`
	WorkRoot      string `json:"work_root"`
	// RunID is the agent run (runs.id) this attempt belongs to. An executor
	// whose events outlive the attempt in memory (openhands: its watcher
	// survives a daemon restart) records it durably so they still reach the
	// run's event stream.
	RunID string `json:"run_id,omitempty"`
	// Events receives the attempt's normalized run events as they happen.
	// May be nil (a smoke test, a probe); executors call Emit, which is
	// nil-safe. The sink redacts, bounds and persists -- an executor only
	// maps its own vocabulary onto domain.RunEvent, and must not block on it.
	Events EventSink `json:"-"`
}

// EventSink receives one normalized run event. Implementations must be safe
// for concurrent use and must not block for long: an ACP agent's stdout
// reader calls it inline.
type EventSink func(domain.RunEvent)

// Emit sends ev to the request's sink, if it has one.
func (r Request) Emit(ev domain.RunEvent) {
	if r.Events != nil {
		r.Events(ev)
	}
}

// RunEventEmitter is implemented by executors that emit structured run
// events themselves (tool calls, file writes, permissions). For every other
// executor the engine derives coarse events from the agent's output tail and
// a scan of the work root.
type RunEventEmitter interface {
	EmitsRunEvents() bool
}

// Steerer is implemented by executors that can take a human's note while
// the agent runs. Steer returns how the note was delivered:
// domain.SteeringDelivered (the agent has it now, or the moment its current
// turn has been interrupted) or domain.SteeringQueued (it is sent as the
// agent's next turn once the current one ends). interrupt asks for the
// current turn to be ended first where the protocol has a clean way to do
// that; an executor without one ignores it. An executor that cannot steer
// this particular attempt (finished, or not running yet) returns
// ErrSteeringUnsupported.
type Steerer interface {
	Steer(ctx context.Context, attemptID, note string, interrupt bool) (delivery string, err error)
}

// ErrSteeringUnsupported is what a Steerer returns for an attempt it cannot
// deliver a note to (finished, or not running yet).
var ErrSteeringUnsupported = errors.New("executor: this run cannot be steered")

// Attempt is the durable handle returned by Start.
type Attempt struct {
	ID       string `json:"id"`
	Executor string `json:"executor"`
	Status   string `json:"status"` // running|succeeded|failed|cancelled
	Phase    string `json:"phase"`  // Planning|Editing|Testing|Waiting|Reviewing|Finished
}

// Status is a progress snapshot (§50). Never exposes chain-of-thought.
type Status struct {
	Status string `json:"status"`
	Phase  string `json:"phase"`
	Action string `json:"action"`
}

// Result is the collected execution output (content-addressed digests + usage).
type Result struct {
	PatchDigest string          `json:"patch_digest"`
	LogDigest   string          `json:"log_digest,omitempty"`
	Usage       domain.RunUsage `json:"usage,omitempty"`
}
