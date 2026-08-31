package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// ContainerSandbox runs a command inside an isolated Docker container: no
// network, read-only root filesystem, only the work root (mounted at /work) and
// a /tmp tmpfs are writable (§52). This is the OS-isolation boundary for
// external coding agents (review B2); the capability broker gates harness-
// internal dispatch, not this boundary.
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

// isolatedArgs builds the docker run args for an isolated container.
func isolatedArgs(workRoot, image string, cmd []string) []string {
	args := []string{
		"run",
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp",
		"-v", workRoot + ":/work",
		"-w", "/work",
		image,
	}
	return append(args, cmd...)
}

// Run executes cmd in the sandbox synchronously and returns stdout, stderr, and
// the exit code. A non-zero exit code (e.g. a binary not present in the image)
// is not a Go error; only a failure to launch Docker is.
func (s *ContainerSandbox) Run(ctx context.Context, workRoot string, cmd []string) (stdout, stderr string, exitCode int, err error) {
	c := exec.CommandContext(ctx, s.dockerPath, isolatedArgs(workRoot, s.image, cmd)...)
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	runErr := c.Run()
	exitCode = 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return out.String(), errBuf.String(), -1, fmt.Errorf("executor: sandbox run: %w", runErr)
		}
	}
	return out.String(), errBuf.String(), exitCode, nil
}
