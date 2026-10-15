package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// defaultHermesImage is the pinned, known-good Hermes Agent image, pinned by
// DIGEST, not only by tag. A tag is mutable: whoever controls the repository
// can point v2026.5.16 at different bytes, and every new install would pull
// them with nothing noticing. The digest is the multi-arch index digest
// `docker buildx imagetools inspect nousresearch/hermes-agent:v2026.5.16`
// reported on 2026-10-13; docker resolves `repo:tag@sha256:…` by the digest
// alone (the tag is kept for humans) and verifies the content against it.
// PullImage additionally checks the pulled image's RepoDigests before
// returning. Override with `idea executors setup --image …` for a custom or
// self-built image.
const defaultHermesImage = "nousresearch/hermes-agent:v2026.5.16@sha256:b6e41c155d6bfce5ad83c5d0fec670086db8a43250e4511c9474134be5482d33"

// DefaultAgentImage returns the pinned default container image for a coding
// agent, or "" when no canonical image exists (the caller should then require an
// explicit --image). Hermes has an official image; OMP and ACP agents do not.
func DefaultAgentImage(kind string) string {
	if kind == "hermes" {
		return defaultHermesImage
	}
	return ""
}

// PullImage ensures image is present locally (docker pull only if absent) and
// returns its image ID so the caller can surface exactly what was downloaded.
//
// For a digest-pinned reference (repo[:tag]@sha256:…) it also refuses an image
// whose RepoDigests do not include that digest for that repository. Docker
// already verifies content pulled by digest; this catches the remaining ways a
// reference can name one thing while the local store holds another (a
// registry mirror or proxy configured in the daemon, a tampered local store,
// a future change to how a tag@digest reference is resolved).
func PullImage(ctx context.Context, image string) (string, error) {
	inspect := func() (string, []string, error) {
		out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}\t{{join .RepoDigests \" \"}}", image).Output()
		if err != nil {
			return "", nil, err
		}
		id, digests, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
		return id, strings.Fields(digests), nil
	}
	id, digests, err := inspect()
	if err != nil {
		if out, perr := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput(); perr != nil {
			return "", fmt.Errorf("executor: docker pull %s: %w: %s", image, perr, strings.TrimSpace(string(out)))
		}
		if id, digests, err = inspect(); err != nil {
			return "", nil
		}
	}
	if err := VerifyPinnedDigest(image, digests); err != nil {
		return "", err
	}
	return id, nil
}

// VerifyPinnedDigest checks that an image pulled by a digest-pinned reference
// carries that digest for that repository among its RepoDigests. A reference
// with no digest passes: there is nothing to verify it against.
func VerifyPinnedDigest(ref string, repoDigests []string) error {
	repo, digest, ok := splitDigestRef(ref)
	if !ok {
		return nil
	}
	for _, rd := range repoDigests {
		r, d, ok := strings.Cut(rd, "@")
		if ok && d == digest && normalizeRepo(r) == normalizeRepo(repo) {
			return nil
		}
	}
	return fmt.Errorf("executor: image %s is pinned to %s, but the local image's digests are %v; refusing to use it "+
		"(remove it with `docker image rm` and pull again, and check for a registry mirror rewriting images)", ref, digest, repoDigests)
}

// splitDigestRef splits repo[:tag]@sha256:… into the repository (no tag) and
// the digest.
func splitDigestRef(ref string) (repo, digest string, ok bool) {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") {
		return "", "", false
	}
	// A tag is the part after the last colon that comes after the last slash
	// (a registry host may carry a port: localhost:5000/x:tag).
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	return name, digest, true
}

// normalizeRepo makes docker.io/library/alpine, library/alpine and alpine
// compare equal, as docker itself treats them.
func normalizeRepo(r string) string {
	r = strings.TrimPrefix(r, "docker.io/")
	r = strings.TrimPrefix(r, "index.docker.io/")
	return strings.TrimPrefix(r, "library/")
}

// dockerProbe caches DockerReachable: the Build page asks on every poll, and
// `docker version` against a stopped Docker Desktop can take seconds.
var dockerProbe struct {
	sync.Mutex
	at time.Time
	ok bool
}

// DockerReachable reports whether a Docker daemon answers, cached for 15 s.
// It is how an unset executor.kind decides between a real agent and the
// placeholder executor, so it must not be slow and must not lie: only a
// server version actually returned counts.
func DockerReachable(ctx context.Context) bool {
	dockerProbe.Lock()
	defer dockerProbe.Unlock()
	if !dockerProbe.at.IsZero() && time.Since(dockerProbe.at) < 15*time.Second {
		return dockerProbe.ok
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	dockerProbe.ok = err == nil && strings.TrimSpace(string(out)) != ""
	dockerProbe.at = time.Now()
	return dockerProbe.ok
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
		"--user", containerUser(),
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
//
// workDir is where the smoke's work root is made. The daemon passes a
// directory under its state root: the system temp directory is not visible
// to Docker under snap-installed Docker or Colima, and a smoke there reported
// "produced no files" for a setup that worked. "" uses the temp directory,
// which is right for the CLI.
func SmokeAgent(ctx context.Context, kind, image, model string, provider AgentProvider, lookupKey func(string) string, workDir string) error {
	var e smokeTarget
	switch kind {
	case "hermes":
		e = NewHermesExecutor(nil, model, image, provider)
	case "omp":
		e = NewOMPExecutor(nil, model, image, provider)
	default:
		return fmt.Errorf("executor: unsupported smoke kind %q (the acp kind uses SmokeACPAgent)", kind)
	}
	return runSmoke(ctx, kind, e, provider, lookupKey, workDir)
}

// SmokeACPAgent is SmokeAgent for the acp kind, whose configuration is more
// than an image and a model: the agent's argv, the key's name inside the
// container, MCP servers. The same trivial task, the same assertion.
func SmokeACPAgent(ctx context.Context, opts ACPOptions, provider AgentProvider, lookupKey func(string) string, workDir string) error {
	return runSmoke(ctx, "acp", NewACPExecutor(opts, provider), provider, lookupKey, workDir)
}

// smokeTarget is what a smoke test drives: an agent executor whose output
// can be read back for the failure message.
type smokeTarget interface {
	Executor
	OutputTailer
	SetKeyLookup(func(string) string)
}

func runSmoke(ctx context.Context, kind string, e smokeTarget, provider AgentProvider, lookupKey func(string) string, workDir string) error {
	// An ACP agent talks to its own vendor's API, not to an OpenAI-compatible
	// base URL, so Validate's endpoint checks do not apply to it.
	// An openhands agent's model calls are made by the server, which maps the
	// provider itself (liteLLMModel), so the endpoint checks do not apply
	// either.
	if kind != "acp" && kind != openHandsName {
		if err := provider.Validate(); err != nil {
			return fmt.Errorf("executor: smoke: %w", err)
		}
	}
	if lookupKey == nil {
		lookupKey = os.Getenv
	}
	// openhands may withhold the key on purpose (the server has its own);
	// its Start refuses a missing key itself when one is needed.
	if kind != openHandsName && provider.KeyEnv != "" && lookupKey(provider.KeyEnv) == "" {
		return fmt.Errorf("executor: smoke requires %s, in the environment or the project's .env", provider.KeyEnv)
	}

	if workDir != "" {
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return err
		}
	}
	work, err := os.MkdirTemp(workDir, "idea-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
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
		case "failed", "cancelled":
			if tail := redactKey(e.OutputTail(ctx, attempt.ID, 40), provider.KeyEnv, lookupKey); tail != "" {
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
	tail := redactKey(e.OutputTail(ctx, attempt.ID, 40), provider.KeyEnv, lookupKey)
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
