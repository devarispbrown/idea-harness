package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
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
		"-e", "HERMES_HOME=" + containerAgentHome,
		"-e", "HOME=" + containerAgentHome,
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

// KeyEnvForModel maps a "provider/model" spec to its provider API-key env var
// name (built-in presets only; custom providers are resolved by the engine at
// runtime). Used by the smoke check to confirm the required key is present.
func KeyEnvForModel(model string) string {
	_, keyEnv, _ := ProviderForModel(model)
	return keyEnv
}

// ProviderForModel splits a `provider/model` spec and returns the provider key,
// its API-key env var (empty for a keyless provider such as Ollama) and its
// base URL, from config.Presets.
//
// This used to be a switch here duplicating that table, which meant two places
// had to agree about six providers and only one of them was checked. It also
// silently omitted Ollama, so a keyless provider looked identical to an
// unresolvable one -- the distinction the executor now turns on.
func ProviderForModel(model string) (provider, keyEnv, baseURL string) {
	provider = model
	if i := strings.Index(model, "/"); i > 0 {
		provider = model[:i]
	}
	if p := config.PresetFor(provider); p != nil {
		return provider, p.DefaultKeyEnv, p.BaseURL
	}
	return provider, "", ""
}

// SmokeAgent runs a trivial coding task through the agent in an isolated
// container and asserts it produced at least one file in the work root. It
// verifies the full CLI contract end-to-end (the exact agent invocation), not
// just that the binary exists. It requires the provider API key for model to be
// present in the environment (the agent needs it to call its model).
func SmokeAgent(ctx context.Context, kind, image, model string) error {
	provider, keyEnv, baseURL := ProviderForModel(model)
	if keyEnv != "" && os.Getenv(keyEnv) == "" {
		return fmt.Errorf("executor: smoke requires %s in the environment", keyEnv)
	}

	work, err := os.MkdirTemp("", "idea-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	var e *SandboxedAgentExecutor
	switch kind {
	case "hermes":
		e = NewHermesExecutor(nil, model, image, keyEnv, provider, baseURL)
	case "omp":
		e = NewOMPExecutor(nil, model, image, keyEnv, provider, baseURL)
	default:
		return fmt.Errorf("executor: unsupported smoke kind %q", kind)
	}

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
			if tail := e.tailLogs(ctx, attempt.ID, 40); tail != "" {
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
	tail := e.tailLogs(ctx, attempt.ID, 40)
	_, _ = e.Collect(ctx, attempt.ID)

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
