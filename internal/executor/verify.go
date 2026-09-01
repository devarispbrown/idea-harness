package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
