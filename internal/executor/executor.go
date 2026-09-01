// Package executor defines the worker abstraction (§48–§50). The deterministic
// `internal` executor is the first implementation; OMP is validated behind this
// same interface as `local_external`.
package executor

import (
	"context"

	"github.com/idea-harness/idea-harness/internal/domain"
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

// Capabilities declares the executor's trust, sandbox mode, and (deterministic,
// not model-granted) capabilities.
type Capabilities struct {
	Trust        domain.ExecutorTrust `json:"trust"`
	SandboxMode  domain.SandboxMode   `json:"sandbox_mode"`
	Network      bool                 `json:"network"`
	Capabilities []string             `json:"capabilities"`
}

// Request is one execution request. The canonical workspace is never passed as a
// writable root — the executor receives an isolated WorkRoot (§37).
type Request struct {
	InitiativeID  string `json:"initiative_id"`
	TaskID        string `json:"task_id"`
	TaskTitle     string `json:"task_title"`
	TaskStatement string `json:"task_statement"`
	WorkRoot      string `json:"work_root"`
}

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
	PatchDigest string           `json:"patch_digest"`
	LogDigest   string           `json:"log_digest,omitempty"`
	Usage       domain.RunUsage  `json:"usage,omitempty"`
}
