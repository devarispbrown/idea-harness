package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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
	}
	if cli == "hermes" {
		// Through the image's entrypoint, NOT `--entrypoint ""`.
		//
		// The pinned Hermes image installs its CLI at
		// /opt/hermes/.venv/bin/hermes, which is not on the default PATH -- the
		// entrypoint is what makes `hermes` resolvable. Probing with the
		// entrypoint cleared therefore reported "CLI not found in image" for an
		// image that contains it, and `idea executors setup --smoke` could never
		// succeed against the very image it downloads by default.
		//
		// HERMES_HOME for the same reason the executor sets it: the entrypoint
		// creates and writes its home, and under --read-only with a host --user
		// the default /opt/data is not writable.
		args = append(args,
			"-e", "HERMES_HOME="+containerAgentHome,
			"-e", "HOME="+containerAgentHome)
	} else {
		// Any other agent image is probed as before, entrypoint cleared, so an
		// image whose ENTRYPOINT is the CLI itself does not run `omp omp
		// --version`.
		args = append(args, "--entrypoint", "")
	}
	args = append(args, image, cli, "--version")
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

// SmokeAgent runs a trivial coding task through the agent in an isolated
// container and asserts it produced at least one file in the work root. It
// verifies the full CLI contract end-to-end (the exact agent invocation), not
// just that the binary exists. It requires the provider API key for model to be
// present in the environment (the agent needs it to call its model).
//
// provider is resolved by the caller from the project's provider view, so the
// smoke calls the endpoint the real executor will -- including a custom
// provider or an Ollama moved to another port -- rather than a preset's.
//
// lookupKey reads the provider key; the CLI passes one that falls back to the
// project's .env, as the daemon does, so a key `idea execute` would accept is
// not refused here.
func SmokeAgent(ctx context.Context, kind, image, model string, provider AgentProvider, lookupKey func(string) string) error {
	if err := provider.Validate(); err != nil {
		return fmt.Errorf("executor: smoke: %w", err)
	}
	if lookupKey == nil {
		lookupKey = os.Getenv
	}
	if provider.KeyEnv != "" && lookupKey(provider.KeyEnv) == "" {
		return fmt.Errorf("executor: smoke requires %s, in the environment or the project's .env", provider.KeyEnv)
	}

	work, err := os.MkdirTemp("", "idea-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	var e *SandboxedAgentExecutor
	switch kind {
	case "hermes":
		e = NewHermesExecutor(nil, model, image, provider)
	case "omp":
		e = NewOMPExecutor(nil, model, image, provider)
	default:
		return fmt.Errorf("executor: unsupported smoke kind %q", kind)
	}
	e.SetKeyLookup(lookupKey)

	attempt, err := e.Start(ctx, Request{TaskTitle: "smoke", TaskStatement: "write a single file named smoke.txt containing the text 'smoke ok'", WorkRoot: work})
	if err != nil {
		return err
	}
	defer e.Cancel(context.Background(), attempt.ID)

	for {
		st, err := e.Status(ctx, attempt.ID)
		if err != nil {
			return err
		}
		switch st.Status {
		case "succeeded":
			goto done
		case "failed":
			if tail := redactKey(e.tailLogs(ctx, attempt.ID, 40), provider.KeyEnv, lookupKey); tail != "" {
				return fmt.Errorf("executor: smoke agent %s failed (%s); agent output:\n%s", kind, st.Action, tail)
			}
			return fmt.Errorf("executor: smoke agent %s failed (%s)", kind, st.Action)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
done:
	// Read the output BEFORE Collect removes the container. Hermes exits 0 even
	// when the model call fails -- a 401, a 404, a billing error all end the
	// session "normally" -- so a clean exit with no file is the common failure,
	// and without the agent's own output the only message was "produced no
	// files (CLI contract mismatch?)", which is wrong about the cause.
	tail := redactKey(e.tailLogs(ctx, attempt.ID, 40), provider.KeyEnv, lookupKey)
	if _, err := e.Collect(ctx, attempt.ID); err != nil {
		return fmt.Errorf("executor: smoke agent %s finished but its container could not be removed: %w", kind, err)
	}

	entries, err := os.ReadDir(work)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if tail != "" {
			return fmt.Errorf("executor: smoke agent %s exited without writing a file; agent output:\n%s", kind, tail)
		}
		return fmt.Errorf("executor: smoke agent %s produced no files (CLI contract mismatch?)", kind)
	}
	return nil
}

// redactKey hides the provider key in agent output the smoke prints. An agent
// can echo its own environment, and this goes straight to a terminal.
func redactKey(s, keyEnv string, lookupKey func(string) string) string {
	if keyEnv == "" {
		return s
	}
	if v := lookupKey(keyEnv); len(v) >= 8 {
		s = strings.ReplaceAll(s, v, "[redacted "+keyEnv+"]")
	}
	return s
}
