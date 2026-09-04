package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ContainerSandbox runs a command inside an isolated Docker container: no
// network, read-only root filesystem, dropped capabilities, bounded memory/pids,
// and the daemon's own UID/GID -- which is not the same as non-root. If the
// daemon runs as root the container does too, and nothing refuses to start or
// warns (docs/security-model.md records this under what the gateway does not
// protect against). Only the work root (mounted at /work) and a /tmp tmpfs
// are writable; an optional project root is mounted read-only at /repo. This is
// the OS-isolation boundary for external coding agents and deterministic checks
// (§52, review B2/H3).
type ContainerSandbox struct {
	image      string
	dockerPath string
}

// NewContainerSandbox returns a sandbox using image (defaulting to a minimal
// Alpine image). dockerPath is the Docker CLI (override for tests).
func NewContainerSandbox(image string) *ContainerSandbox {
	if image == "" {
		image = "alpine:3.20"
	}
	return &ContainerSandbox{image: image, dockerPath: "docker"}
}

// RunOptions configures a sandbox execution.
type RunOptions struct {
	WorkRoot string   // writable, mounted at /work
	RepoRoot string   // optional, mounted read-only at /repo (the canonical workspace)
	Cmd      []string // command + args (run with work dir /work)
}

// isolatedArgs builds the `docker run` args (after the leading "run") for an
// isolated container running as the daemon's UID/GID, with writable tmpfs
// caches under /tmp. See the type comment on why that is not "non-root".
func (s *ContainerSandbox) isolatedArgs(opts RunOptions) []string {
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	args := []string{
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", uid,
		"--memory", "2g",
		"--pids-limit", "256",
		"--tmpfs", "/tmp",
		"--tmpfs", "/tmp/cache",
		"-e", "HOME=/tmp",
		"-e", "GOCACHE=/tmp/cache/go",
		"-e", "GOMODCACHE=/tmp/cache/gomod",
		"-e", "NPM_CONFIG_CACHE=/tmp/cache/npm",
		"-v", opts.WorkRoot + ":/work",
	}
	if opts.RepoRoot != "" {
		args = append(args, "-v", opts.RepoRoot+":/repo:ro")
	}
	args = append(args, "-w", "/work", s.image)
	return append(args, opts.Cmd...)
}

// Run executes cmd in the sandbox and returns stdout, stderr, and the exit code.
// The container is started detached so a context timeout/cancellation can be
// enforced by `docker rm -f` (a hung build/test is genuinely killed, not left
// orphaned). A non-zero exit code is not a Go error; only a failure to launch
// Docker is.
func (s *ContainerSandbox) Run(ctx context.Context, opts RunOptions) (stdout, stderr string, exitCode int, err error) {
	name := fmt.Sprintf("idea-harness-check-%d", time.Now().UnixNano())

	// Start detached and capture the container name (we already know it).
	startArgs := append([]string{"run", "-d", "--name", name}, s.isolatedArgs(opts)...)
	if out, err := exec.CommandContext(ctx, s.dockerPath, startArgs...).CombinedOutput(); err != nil {
		return "", string(out), -1, fmt.Errorf("executor: sandbox start: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Always remove the container on the way out (no orphans).
	defer func() {
		_ = exec.Command(s.dockerPath, "rm", "-f", name).Run()
	}()

	// Wait for exit, honoring ctx cancellation.
	waitCh := make(chan int, 1)
	go func() {
		out, _ := exec.Command(s.dockerPath, "wait", name).CombinedOutput()
		code := 0
		if v := strings.TrimSpace(string(out)); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				code = n
			}
		}
		waitCh <- code
	}()

	select {
	case code := <-waitCh:
		exitCode = code
	case <-ctx.Done():
		// Kill and remove the still-running container on timeout/cancel.
		_ = exec.Command(s.dockerPath, "rm", "-f", name).Run()
		return "", "", -1, ctx.Err()
	}

	// Collect the container's combined output.
	var outBuf bytes.Buffer
	logs := exec.Command(s.dockerPath, "logs", name)
	logs.Stdout = &outBuf
	logs.Stderr = &outBuf
	_ = logs.Run()
	return outBuf.String(), "", exitCode, nil
}
