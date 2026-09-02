package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// defaultHermesImage is the pinned, known-good Hermes Agent image. It is a
// versioned tag (never `latest`) so the auto-downloaded artifact is reproducible
// and auditable; override with `idea executors setup --image …` for a custom or
// self-built image.
const defaultHermesImage = "nousresearch/hermes-agent:v2026.5.16"

// DefaultAgentImage returns the pinned default container image for a coding
// agent, or "" when no canonical image exists (the caller should then require an
// explicit --image). Hermes has an official image; OMP does not.
func DefaultAgentImage(kind string) string {
	if kind == "hermes" {
		return defaultHermesImage
	}
	return ""
}

// PullImage ensures image is present locally (docker pull only if absent) and
// returns its image ID so the caller can surface exactly what was downloaded.
func PullImage(ctx context.Context, image string) (string, error) {
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput(); err != nil {
		return "", fmt.Errorf("executor: docker pull %s: %w: %s", image, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", nil
}

// VerifyAgentCLI checks that an agent CLI is present in a container image,
// WITHOUT pulling and with the same hardened flags the sandbox uses at runtime
// (no network, read-only root, dropped capabilities, non-root, bounded memory/
// pids). It is a UX presence check only — trust must come from the image source,
// not this probe (review: v1 agent setup). The image must already be present
// locally; a missing image is a distinct error, not a CLI-missing result.
func VerifyAgentCLI(ctx context.Context, image, cli string) error {
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
		return fmt.Errorf("executor: image %q is not present locally (run `docker pull %s` first)", image, image)
	}

	args := []string{
		"run", "--rm",
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--memory", "2g",
		"--pids-limit", "256",
		"--tmpfs", "/tmp",
		"--entrypoint", "",
		image, cli, "--version",
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		switch ee.ExitCode() {
		case 127:
			return fmt.Errorf("executor: %q CLI not found in image %q", cli, image)
		case 125:
			return fmt.Errorf("executor: docker daemon or image error (exit 125): %s", strings.TrimSpace(string(out)))
		default:
			return fmt.Errorf("executor: %q --version failed (exit %d): %s", cli, ee.ExitCode(), strings.TrimSpace(string(out)))
		}
	}
	return fmt.Errorf("executor: docker unavailable: %w", err)
}
