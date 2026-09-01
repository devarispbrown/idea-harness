package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/id"
)

// SandboxedAgentExecutor runs a coding-agent CLI inside an isolated container
// (review B2, amended). The container gives FILESYSTEM/PROCESS isolation —
// read-only root, only the mounted /work and a /tmp tmpfs are writable — but
// NOT network isolation: the agent needs egress to reach its model API, so the
// provider keys are forwarded in and `--network none` is deliberately omitted.
// OMP and Hermes are the two agents; neither runs as a host subprocess. The
// agent writes files under /work (== req.WorkRoot); the engine snapshots that
// directory before/after and computes the diff (B3).
type SandboxedAgentExecutor struct {
	mu         sync.Mutex
	attempts   map[string]*agentAttempt
	store      *blob.Store
	name       string
	image      string
	model      string
	dockerPath string
	buildArgs  func(req Request, model string) []string
}

type agentAttempt struct {
	req         Request
	containerID string
}

func newSandboxedAgentExecutor(store *blob.Store, name, model, image string, buildArgs func(Request, string) []string) *SandboxedAgentExecutor {
	if image == "" {
		image = "alpine:3.20"
	}
	return &SandboxedAgentExecutor{
		attempts:  make(map[string]*agentAttempt),
		store:     store,
		name:      name,
		model:     model,
		image:     image,
		dockerPath: "docker",
		buildArgs: buildArgs,
	}
}

func ompArgs(req Request, model string) []string {
	stmt := req.TaskStatement
	if stmt == "" {
		stmt = req.TaskTitle
	}
	// --print runs the coding loop non-interactively (writes files, then exits);
	// --auto-approve skips the write/command approval prompts so a headless
	// container never hangs waiting on a TTY (review B3).
	return []string{"omp", "--print", "--cwd", "/work", "--model", model, "--auto-approve", stmt}
}

func hermesArgs(req Request, model string) []string {
	stmt := req.TaskStatement
	if stmt == "" {
		stmt = req.TaskTitle
	}
	// --in/--yolo are global options (before `chat`); --yolo is auto-approve mode
	// so the agent writes files headlessly instead of prompting.
	return []string{"hermes", "--in", "/work", "--yolo", "chat", "--query", stmt, "--model", model}
}

// providerKeyEnvs are the provider API-key env vars forwarded into the agent
// container so the agent can authenticate to its model API. Only keys that are
// set on the host are forwarded.
var providerKeyEnvs = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"GOOGLE_API_KEY",
	"OPENROUTER_API_KEY",
	"DEEPSEEK_API_KEY",
}

// forwardProviderKeys returns the `-e NAME=value` docker args for every set
// provider key, so the agent has a credential inside the sandbox.
func forwardProviderKeys() []string {
	var args []string
	for _, name := range providerKeyEnvs {
		if v := os.Getenv(name); v != "" {
			args = append(args, "-e", name+"="+v)
		}
	}
	return args
}

// NewOMPExecutor returns a sandboxed OMP executor. image must contain the omp
// CLI; the thin slice defaults to Alpine so tests can exercise the adapter
// without a real agent image.
func NewOMPExecutor(store *blob.Store, model, image string) *SandboxedAgentExecutor {
	return newSandboxedAgentExecutor(store, "omp", model, image, ompArgs)
}

// NewHermesExecutor returns a sandboxed Hermes (Nous Research) executor.
func NewHermesExecutor(store *blob.Store, model, image string) *SandboxedAgentExecutor {
	return newSandboxedAgentExecutor(store, "hermes", model, image, hermesArgs)
}

// Name reports the executor's registered name.
func (e *SandboxedAgentExecutor) Name() string { return e.name }

// Capabilities declares the agent's capabilities. The sandbox mode is REAL
// filesystem/process isolation (container, read-only root); Network is true
// because the agent needs egress to reach its model API.
func (e *SandboxedAgentExecutor) Capabilities(ctx context.Context) Capabilities {
	return Capabilities{
		Trust:        domain.ExecutorLocalExternal,
		SandboxMode:  domain.SandboxContainer,
		Network:      true,
		Capabilities: []string{"fs.write"},
	}
}

// Start launches the agent inside the isolated container (detached) and records
// the container id. The provider keys are forwarded so the agent can reach its
// model; `--network none` is intentionally omitted (the agent must egress to the
// model API — this is a filesystem/process sandbox, not a network sandbox).
func (e *SandboxedAgentExecutor) Start(ctx context.Context, req Request) (Attempt, error) {
	if err := os.MkdirAll(req.WorkRoot, 0o755); err != nil {
		return Attempt{}, fmt.Errorf("executor: %s: mkdir work root: %w", e.name, err)
	}
	args := []string{
		"run", "-d",
		"--read-only",
		"--tmpfs", "/tmp",
		"-v", req.WorkRoot + ":/work",
		"-w", "/work",
	}
	args = append(args, forwardProviderKeys()...)
	args = append(args, e.image)
	args = append(args, e.buildArgs(req, e.model)...)
	out, err := exec.CommandContext(ctx, e.dockerPath, args...).Output()
	if err != nil {
		return Attempt{}, fmt.Errorf("executor: %s: docker run: %w", e.name, err)
	}
	cid := strings.TrimSpace(string(out))
	if cid == "" {
		return Attempt{}, fmt.Errorf("executor: %s: docker run returned no container id", e.name)
	}

	attemptID := id.New()
	e.mu.Lock()
	e.attempts[attemptID] = &agentAttempt{req: req, containerID: cid}
	e.mu.Unlock()

	return Attempt{ID: attemptID, Executor: e.name, Status: "running", Phase: "Planning"}, nil
}

// Status reports the container's run state by inspecting the Docker daemon.
func (e *SandboxedAgentExecutor) Status(ctx context.Context, id string) (Status, error) {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return Status{}, fmt.Errorf("executor: unknown attempt %q", id)
	}
	out, err := exec.CommandContext(ctx, e.dockerPath, "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", a.containerID).Output()
	if err != nil {
		return Status{}, fmt.Errorf("executor: %s: docker inspect: %w", e.name, err)
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return Status{}, fmt.Errorf("executor: %s: unexpected inspect output %q", e.name, strings.TrimSpace(string(out)))
	}
	switch f[0] {
	case "running":
		return Status{Status: "running", Phase: "Editing", Action: "working"}, nil
	case "exited":
		if f[1] == "0" {
			return Status{Status: "succeeded", Phase: "Finished", Action: "complete"}, nil
		}
		return Status{Status: "failed", Phase: "Finished", Action: "exit " + f[1]}, nil
	default:
		return Status{Status: "failed", Phase: "Finished", Action: f[0]}, nil
	}
}

// Cancel removes the container, terminating the agent.
func (e *SandboxedAgentExecutor) Cancel(ctx context.Context, id string) error {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("executor: unknown attempt %q", id)
	}
	if err := exec.CommandContext(ctx, e.dockerPath, "rm", "-f", a.containerID).Run(); err != nil {
		return fmt.Errorf("executor: %s: docker rm: %w", e.name, err)
	}
	return nil
}

// Collect cleans up the container and returns an empty Result. The engine
// computes the diff from the work root (B3); the executor's job is only to run
// the agent in isolation.
func (e *SandboxedAgentExecutor) Collect(ctx context.Context, id string) (Result, error) {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("executor: unknown attempt %q", id)
	}
	_ = exec.CommandContext(ctx, e.dockerPath, "rm", "-f", a.containerID).Run()
	return Result{}, nil
}
