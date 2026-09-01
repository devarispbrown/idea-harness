package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/id"
)

// containerScript is the deterministic work the container performs for the M3
// thin slice: it writes the task title to a hand-off file the host reads back.
// A real coding-agent image would mount the workspace and produce an actual
// file diff here; the adapter boundary is identical.
const containerScript = `mkdir -p /work/result && printf 'change: %s\n' "$TASK_TITLE" > /work/result/patch.txt`

// ContainerExecutor runs a task inside a Docker container with no network and a
// read-only-by-convention workspace mount, collecting the produced patch from a
// mounted hand-off file (§52 SandboxContainer).
type ContainerExecutor struct {
	mu         sync.Mutex
	attempts   map[string]*containerAttempt
	store      *blob.Store
	image      string
	dockerPath string
}

type containerAttempt struct {
	req         Request
	containerID string
}

// NewContainerExecutor returns a ContainerExecutor that persists produced patch
// content to the given blob store. image defaults to a minimal Alpine image when
// empty; dockerPath is the Docker CLI (override for tests).
func NewContainerExecutor(store *blob.Store, image string) *ContainerExecutor {
	if image == "" {
		image = "alpine:3.20"
	}
	return &ContainerExecutor{
		attempts:   make(map[string]*containerAttempt),
		store:      store,
		image:      image,
		dockerPath: "docker",
	}
}

// Name reports the executor's registered name.
func (e *ContainerExecutor) Name() string { return "container" }

// Capabilities declares the container executor's deterministic capabilities.
func (e *ContainerExecutor) Capabilities(ctx context.Context) Capabilities {
	return Capabilities{
		Trust:        domain.ExecutorLocalExternal,
		SandboxMode:  domain.SandboxContainer,
		Network:      false,
		Capabilities: []string{"fs.write"},
	}
}

// Start launches the task in a detached container and records its container id.
func (e *ContainerExecutor) Start(ctx context.Context, req Request) (Attempt, error) {
	if err := os.MkdirAll(filepath.Join(req.WorkRoot, "result"), 0o700); err != nil {
		return Attempt{}, fmt.Errorf("executor: container: mkdir work root: %w", err)
	}
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	args := []string{
		"run", "-d",
		"--rm",
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", uid,
		"--memory", "2g",
		"--pids-limit", "256",
		"--tmpfs", "/tmp",
		"-v", req.WorkRoot + ":/work",
		"-e", "TASK_TITLE=" + req.TaskTitle,
		e.image,
		"sh", "-c", containerScript,
	}
	out, err := exec.CommandContext(ctx, e.dockerPath, args...).Output()
	if err != nil {
		return Attempt{}, fmt.Errorf("executor: container: docker run: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		return Attempt{}, fmt.Errorf("executor: container: docker run returned no container id")
	}

	attemptID := id.New()
	e.mu.Lock()
	e.attempts[attemptID] = &containerAttempt{req: req, containerID: containerID}
	e.mu.Unlock()

	return Attempt{ID: attemptID, Executor: "container", Status: "running", Phase: "Planning"}, nil
}

// Status reports the container's run state by inspecting the Docker daemon.
func (e *ContainerExecutor) Status(ctx context.Context, id string) (Status, error) {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return Status{}, fmt.Errorf("executor: unknown attempt %q", id)
	}

	out, err := exec.CommandContext(ctx, e.dockerPath, "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", a.containerID).Output()
	if err != nil {
		return Status{}, fmt.Errorf("executor: container: docker inspect: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return Status{}, fmt.Errorf("executor: container: unexpected inspect output %q", strings.TrimSpace(string(out)))
	}
	switch fields[0] {
	case "running":
		return Status{Status: "running", Phase: "Editing", Action: "working"}, nil
	case "exited":
		if fields[1] == "0" {
			return Status{Status: "succeeded", Phase: "Finished", Action: "complete"}, nil
		}
		return Status{Status: "failed", Phase: "Finished", Action: "exit " + fields[1]}, nil
	default:
		return Status{Status: "failed", Phase: "Finished", Action: fields[0]}, nil
	}
}

// Cancel removes the container, terminating the attempt.
func (e *ContainerExecutor) Cancel(ctx context.Context, id string) error {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("executor: unknown attempt %q", id)
	}
	if err := exec.CommandContext(ctx, e.dockerPath, "rm", "-f", a.containerID).Run(); err != nil {
		return fmt.Errorf("executor: container: docker rm: %w", err)
	}
	return nil
}

// Collect reads the produced patch from the mounted hand-off file, cleans up the
// container, and persists the content to the blob store.
func (e *ContainerExecutor) Collect(ctx context.Context, id string) (Result, error) {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("executor: unknown attempt %q", id)
	}

	content, err := os.ReadFile(filepath.Join(a.req.WorkRoot, "result", "patch.txt"))
	if err != nil {
		return Result{}, fmt.Errorf("executor: container: read patch: %w", err)
	}

	// Best-effort cleanup; a failure here must not fail the collect.
	_ = exec.CommandContext(ctx, e.dockerPath, "rm", "-f", a.containerID).Run()

	patchDigest := blob.Digest(content)
	if e.store != nil {
		d, err := e.store.Put(content)
		if err != nil {
			return Result{}, fmt.Errorf("executor: container: persist patch: %w", err)
		}
		patchDigest = d
	}
	return Result{PatchDigest: patchDigest}, nil
}
