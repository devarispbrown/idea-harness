package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ContainerSandbox runs a command inside an isolated Docker container: no
// network, read-only root filesystem, dropped capabilities, bounded memory/pids,
// and a non-root user. The daemon's own UID/GID is not the same as non-root (a
// root daemon's is 0), so it is used only when the daemon is not root; a root
// daemon passes the fixed unprivileged ContainerUID:ContainerGID instead, with
// the work root handed to it for the run (containeruser.go). Only the work
// root (mounted at /work) and a /tmp tmpfs are writable; an optional project
// root is mounted read-only at /repo. This is
// the OS-isolation boundary for external coding agents and deterministic checks
// (§52, review B2/H3).
//
// The /tmp tmpfs mounts are explicitly `exec` (Docker's tmpfs default is
// `noexec`). A compiled toolchain -- `go test`, jest's native transforms,
// cargo build scripts, pytest's compiled extensions -- stages its binary
// under $TMPDIR/os.TempDir() before it fork/execs it; under a noexec /tmp
// that exec fails with "permission denied" even though the compile itself
// succeeded (review defect #1). TMPDIR is set explicitly so every toolchain
// -- not just Go, which already defaults to os.TempDir() -- resolves its
// scratch directory to this same exec-capable tmpfs rather than whatever
// its own default happens to be.
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
	// Network gives the container Docker's default bridge network instead
	// of --network none. Every other restriction stays. Only verify's
	// explicitly configured prepare and scanner commands set it -- never a
	// build/test check, never an agent -- and docs/security-model.md says so.
	Network bool
	// Env adds environment variables (NAME=value) after the defaults, so a
	// caller can move a cache into the work root to survive between two
	// containers (a network prepare phase, then the offline run).
	Env []string
}

// isolatedArgs builds the `docker run` args (after the leading "run") for an
// isolated container running as containerUser(), with writable tmpfs
// caches under /tmp.
func (s *ContainerSandbox) isolatedArgs(opts RunOptions) []string {
	uid := containerUser()
	network := "none"
	if opts.Network {
		network = "bridge"
	}
	args := []string{
		"--network", network,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", uid,
		"--memory", "2g",
		"--pids-limit", "256",
		// rw,exec,nosuid,nodev,size=... : Docker's tmpfs default is noexec,
		// which blocks fork/exec (and PROT_EXEC mmap, i.e. dlopen) of
		// anything staged here. Test/build toolchains compile a binary into
		// $TMPDIR and then run it, so noexec breaks `go test`, jest's native
		// transforms, cargo build scripts, and pytest's compiled
		// extensions alike (review defect #1). Re-enabling exec is no reason
		// to also drop Docker's own tmpfs default of nodev (review LOW #8:
		// an earlier revision dropped it along with noexec) -- nosuid+nodev
		// block a privilege-escalation or device-node vector this tmpfs has
		// no legitimate need for, and size keeps it from being an unbounded
		// -RAM one.
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g",
		"--tmpfs", "/tmp/cache:rw,exec,nosuid,nodev,size=1g",
		"-e", "HOME=/tmp",
		"-e", "TMPDIR=/tmp",
		"-e", "GOCACHE=/tmp/cache/go",
		"-e", "GOMODCACHE=/tmp/cache/gomod",
		"-e", "GOTMPDIR=/tmp",
		"-e", "NPM_CONFIG_CACHE=/tmp/cache/npm",
		"-v", opts.WorkRoot + ":/work",
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
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

	// Under a root daemon the container runs as ContainerUID, so the work
	// root is handed to it for the run. Deferred BEFORE the removal below, so
	// it runs after it: the walk must not race a live container
	// (containeruser.go). RepoRoot is the user's workspace and is never
	// chowned; it is mounted read-only.
	grant, err := grantMounts(opts.WorkRoot)
	if err != nil {
		return "", "", -1, err
	}
	defer func() { _ = grant.Restore() }()

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
