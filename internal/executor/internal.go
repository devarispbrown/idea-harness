// Package executor defines the worker abstraction (§48–§50). The deterministic
// `internal` executor is the first implementation; OMP is validated behind this
// same interface as `local_external`.
package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// internalProgression is the fixed, deterministic phase sequence the internal
// executor reports. Progression advances with each Status call — never with wall
// time — so the same call sequence always yields the same snapshots (§50).
var internalProgression = []struct {
	status string
	phase  string
	action string
}{
	{"running", "Planning", "planning"},
	{"running", "Editing", "editing"},
	{"running", "Testing", "testing"},
	{"running", "Waiting", "waiting"},
	{"running", "Reviewing", "reviewing"},
	{"succeeded", "Finished", "complete"},
}

// internalAttempt is the in-memory state of one internal execution. It is the
// deterministic substitute for a real worker process; no model output, network,
// or external dependency is ever involved.
type internalAttempt struct {
	req    Request
	status string
	phase  string
	steps  int
}

// InternalExecutor runs tasks deterministically in-process. It is hermetic: it
// performs no network I/O and depends on no external services.
type InternalExecutor struct {
	mu       sync.Mutex
	attempts map[string]*internalAttempt
	store    *blob.Store
}

// NewInternalExecutor returns a ready-to-use InternalExecutor that persists
// produced patch content to the given content-addressed blob store.
func NewInternalExecutor(store *blob.Store) *InternalExecutor {
	return &InternalExecutor{attempts: make(map[string]*internalAttempt), store: store}
}

// Name reports the executor's registered name.
func (e *InternalExecutor) Name() string { return "internal" }

// Capabilities declares the executor's deterministic capabilities. Trust and
// sandbox mode are fixed by construction; the capability set is never
// model-granted (§49, §50).
func (e *InternalExecutor) Capabilities(ctx context.Context) Capabilities {
	return Capabilities{
		Trust:        domain.ExecutorInternal,
		SandboxMode:  domain.SandboxLocalRestricted,
		Network:      false,
		Capabilities: []string{"fs.write"},
	}
}

// Start records a new attempt and returns its durable handle. The isolated work
// root is created so the engine can snapshot it before/after and compute the diff.
func (e *InternalExecutor) Start(ctx context.Context, req Request) (Attempt, error) {
	if req.WorkRoot != "" {
		if err := os.MkdirAll(req.WorkRoot, 0o755); err != nil {
			return Attempt{}, fmt.Errorf("executor: internal: mkdir work root: %w", err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.attempts == nil {
		e.attempts = make(map[string]*internalAttempt)
	}
	attemptID := id.New()
	e.attempts[attemptID] = &internalAttempt{
		req:    req,
		status: "running",
		phase:  "Planning",
	}
	return Attempt{
		ID:       attemptID,
		Executor: "internal",
		Status:   "running",
		Phase:    "Planning",
	}, nil
}

// Status reports the next step of the deterministic progression. It is
// idempotent at the terminal phase and never exposes chain-of-thought.
func (e *InternalExecutor) Status(ctx context.Context, id string) (Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.attempts[id]
	if !ok {
		return Status{}, fmt.Errorf("executor: unknown attempt %q", id)
	}
	if a.status == "cancelled" {
		return Status{Status: "cancelled", Phase: a.phase, Action: "cancelled"}, nil
	}
	if a.status == "succeeded" {
		return Status{Status: "succeeded", Phase: "Finished", Action: "complete"}, nil
	}

	a.steps++
	idx := a.steps
	if idx >= len(internalProgression) {
		idx = len(internalProgression) - 1
	}
	p := internalProgression[idx]
	a.status = p.status
	a.phase = p.phase
	return Status{Status: p.status, Phase: p.phase, Action: p.action}, nil
}

// Cancel marks an attempt cancelled. It is a no-op after the attempt has
// already succeeded or been cancelled.
func (e *InternalExecutor) Cancel(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.attempts[id]
	if !ok {
		return fmt.Errorf("executor: unknown attempt %q", id)
	}
	if a.status == "succeeded" || a.status == "cancelled" {
		return nil
	}
	a.status = "cancelled"
	return nil
}

// Collect writes the deterministic implementation file into the isolated work
// root and returns an empty Result. The engine (orchestrator) snapshots the work
// root before/after and computes the real diff (§36); the executor's job is only
// to produce file changes, never to self-declare the diff.
func (e *InternalExecutor) Collect(ctx context.Context, id string) (Result, error) {
	e.mu.Lock()
	a, ok := e.attempts[id]
	var title string
	if ok {
		title = a.req.TaskTitle
	}
	e.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("executor: unknown attempt %q", id)
	}

	content := []byte("# idea-harness placeholder — configure executor.kind = omp|hermes to generate real code\ntask: " + title + "\n")
	if err := os.WriteFile(filepath.Join(a.req.WorkRoot, "hello.txt"), content, 0o644); err != nil {
		return Result{}, fmt.Errorf("executor: internal: write hello.txt: %w", err)
	}
	return Result{}, nil
}
