package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
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
	keyEnv     string // provider API-key env var name (resolved by the engine)
	provider   string // provider key, e.g. "openai" (resolved by the engine)
	baseURL    string // that provider's API root (resolved by the engine)
	dockerPath string
	buildArgs  func(req Request, model, provider string) []string
}

type agentAttempt struct {
	req         Request
	containerID string
	// agentHome is bind-mounted into the running container, which is detached
	// (`docker run -d`), so it must outlive Start and go away with the attempt.
	agentHome string
}

func newSandboxedAgentExecutor(store *blob.Store, name, model, image, keyEnv, provider, baseURL string, buildArgs func(Request, string, string) []string) *SandboxedAgentExecutor {
	if image == "" {
		image = "alpine:3.20"
	}
	return &SandboxedAgentExecutor{
		attempts:   make(map[string]*agentAttempt),
		store:      store,
		name:       name,
		model:      model,
		image:      image,
		keyEnv:     keyEnv,
		provider:   provider,
		baseURL:    baseURL,
		dockerPath: "docker",
		buildArgs:  buildArgs,
	}
}

func ompArgs(req Request, model, provider string) []string {
	stmt := req.Brief
	if stmt == "" {
		stmt = req.TaskStatement
	}
	if stmt == "" {
		stmt = req.TaskTitle
	}
	// --print runs the coding loop non-interactively (writes files, then exits);
	// --auto-approve skips the write/command approval prompts so a headless
	// container never hangs waiting on a TTY (review B3).
	return []string{"omp", "--print", "--cwd", "/work", "--model", model, "--auto-approve", stmt}
}

func hermesArgs(req Request, model, provider string) []string {
	stmt := req.Brief
	if stmt == "" {
		stmt = req.TaskStatement
	}
	if stmt == "" {
		stmt = req.TaskTitle
	}
	// `hermes chat --yolo -q <stmt> -m <model> --provider <slug>`.
	//
	// This was `hermes --in /work --yolo chat --query ... --model ...`, which the
	// pinned image rejects at argument parsing -- there is no `--in` flag, so
	// Hermes printed its usage and exited and the coding-agent path could never
	// have produced a file. The working directory comes from `-w /work` on the
	// container, so `--in` was never needed.
	//
	// The model is passed BARE. The provider is pinned separately by a generated
	// custom_provider, so a `provider/model` spec here would be handed to the
	// endpoint verbatim and rejected.
	args := []string{"hermes", "chat", "--yolo", "-q", stmt, "-m", bareModel(model)}
	if provider != "" {
		args = append(args, "--provider", hermesProviderSlug(provider))
	}
	return args
}

// bareModel strips the `provider/` prefix. OpenRouter is the exception: its
// model ids legitimately contain a slash (`anthropic/claude-opus-5`), and the
// prefix IS part of the id there.
func bareModel(model string) string {
	i := strings.Index(model, "/")
	if i <= 0 {
		return model
	}
	if model[:i] == "openrouter" {
		return model[i+1:]
	}
	return model[i+1:]
}

// writeProviderKeyFile writes the executor's single provider key (resolved by the
// engine from config, not from a model-prefix switch) to a 0600 temp env-file so
// the secret never appears in the docker run argv. Returns "" when no key is set.
func (e *SandboxedAgentExecutor) writeProviderKeyFile() (string, error) {
	if e.keyEnv == "" {
		return "", nil
	}
	value := os.Getenv(e.keyEnv)
	if value == "" {
		return "", nil
	}
	f, err := os.CreateTemp("", "idea-harness-key-*")
	if err != nil {
		return "", fmt.Errorf("executor: create key env-file: %w", err)
	}
	path := f.Name()
	if _, err := fmt.Fprintf(f, "%s=%s\n", e.keyEnv, value); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// NewOMPExecutor returns a sandboxed OMP executor. keyEnv is the provider
// API-key env var name resolved by the engine (empty for keyless providers).
func NewOMPExecutor(store *blob.Store, model, image, keyEnv, provider, baseURL string) *SandboxedAgentExecutor {
	return newSandboxedAgentExecutor(store, "omp", model, image, keyEnv, provider, baseURL, ompArgs)
}

// NewHermesExecutor returns a sandboxed Hermes (Nous Research) executor.
func NewHermesExecutor(store *blob.Store, model, image, keyEnv, provider, baseURL string) *SandboxedAgentExecutor {
	return newSandboxedAgentExecutor(store, "hermes", model, image, keyEnv, provider, baseURL, hermesArgs)
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

// containerArgs builds the `docker run` argv for an agent container.
//
// Named rather than inline so docs/security-model.md's isolation table can be
// asserted against it: that table said "no network" and a read-only /repo mount
// for three releases, describing ContainerSandbox rather than this, and no check
// in the repo could see the difference.
func (e *SandboxedAgentExecutor) containerArgs(req Request, keyFile, agentHome string) []string {
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	// No `--rm`. The container runs detached and Status polls it with
	// `docker inspect`; with --rm, a fast agent (the smoke writes one file in
	// ~6s) finished and deleted itself between polls, so inspect failed on a run
	// that had SUCCEEDED -- surfacing as "failed (removing)" or
	// "docker inspect: exit status 1". Collect and Cancel remove it explicitly,
	// and the engine always calls one of them.
	args := []string{
		"run", "-d",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", uid,
		"--memory", "2g",
		"--pids-limit", "256",
		"--tmpfs", "/tmp",
		"-v", req.WorkRoot + ":/work",
		"-w", "/work",
	}
	if keyFile != "" {
		args = append(args, "--env-file", keyFile)
	}
	if agentHome != "" {
		args = append(args,
			"-v", agentHome+":"+containerAgentHome,
			"-e", "HERMES_HOME="+containerAgentHome,
			"-e", "HOME="+containerAgentHome)
	}
	// host.docker.internal is built in on Docker Desktop and absent on Linux,
	// where it has to be mapped explicitly. Added only when the endpoint is
	// actually a loopback one, i.e. Ollama, so nothing else gains a route to the
	// host it did not have.
	if _, needsGateway := containerBaseURL(e.baseURL); needsGateway {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	return append(args, e.image)
}

// containerAgentHome is where the generated agent home is mounted.
const containerAgentHome = "/tmp/agent-home"

// writeAgentHome creates a writable home directory for the agent, containing
// the generated config that pins it to one provider and endpoint. Returns ""
// when this executor has no provider to pin (the capability-listing path).
func (e *SandboxedAgentExecutor) writeAgentHome() (string, error) {
	if e.provider == "" || e.baseURL == "" {
		return "", nil
	}
	baseURL, _ := containerBaseURL(agentBaseURL(e.provider, e.baseURL))
	cfg, err := hermesConfigYAML(e.provider, baseURL, bareModel(e.model), e.keyEnv)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "idea-harness-agent-home-*")
	if err != nil {
		return "", fmt.Errorf("executor: create agent home: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, hermesConfigPath), []byte(cfg), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("executor: write agent config: %w", err)
	}
	return dir, nil
}

// Start launches the agent inside the isolated container (detached) and records
// the container id. The provider keys are forwarded so the agent can reach its
// model; `--network none` is intentionally omitted (the agent must egress to the
// model API — this is a filesystem/process sandbox, not a network sandbox).
func (e *SandboxedAgentExecutor) Start(ctx context.Context, req Request) (Attempt, error) {
	if err := os.MkdirAll(req.WorkRoot, 0o755); err != nil {
		return Attempt{}, fmt.Errorf("executor: %s: mkdir work root: %w", e.name, err)
	}
	// Forward only the agent's own provider key via a 0600 env-file (not argv).
	keyFile, err := e.writeProviderKeyFile()
	if err != nil {
		return Attempt{}, err
	}
	if keyFile != "" {
		defer os.Remove(keyFile)
	}

	// A writable agent home, seeded with the config that pins the provider.
	//
	// The image's HOME is /opt/data and its entrypoint creates and writes there.
	// Under this container's --read-only root and host --user, that mkdir fails
	// with "Permission denied" and NOTHING runs -- which is the second reason
	// the coding-agent path never worked. HERMES_HOME is the entrypoint's own
	// knob for relocating it.
	agentHome, err := e.writeAgentHome()
	if err != nil {
		return Attempt{}, err
	}
	// Removed by release(), NOT deferred here. The container runs detached, so
	// Start returns while the agent is still working; deleting the home at that
	// point pulled its config and state out from under it mid-run, and the
	// smoke failed in eight seconds.
	started := false
	defer func() {
		if !started && agentHome != "" {
			os.RemoveAll(agentHome)
		}
	}()

	args := e.containerArgs(req, keyFile, agentHome)
	args = append(args, e.buildArgs(req, e.model, e.provider)...)
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
	e.attempts[attemptID] = &agentAttempt{req: req, containerID: cid, agentHome: agentHome}
	e.mu.Unlock()
	started = true

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
	err := exec.CommandContext(ctx, e.dockerPath, "rm", "-f", a.containerID).Run()
	e.release(a)
	if err != nil {
		return fmt.Errorf("executor: %s: docker rm: %w", e.name, err)
	}
	return nil
}

// tailLogs returns the last n lines of an attempt's container output, for a
// failure message. Best effort: an empty string when there is nothing to read.
//
// A failed agent reported only `exit 1`. The reason -- a 401, an unknown
// provider, a model the endpoint does not serve -- is always in the agent's own
// output, and the container is still present until Collect or Cancel removes
// it, so it can be read.
func (e *SandboxedAgentExecutor) tailLogs(ctx context.Context, id string, n int) string {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return ""
	}
	out, _ := exec.CommandContext(ctx, e.dockerPath, "logs", "--tail", fmt.Sprint(n), a.containerID).CombinedOutput()
	return strings.TrimSpace(scrubAgentLog(string(out)))
}

// scrubAgentLog drops the image's per-start skill-sync noise and blank lines,
// which otherwise fill the tail and push the actual error out of it.
func scrubAgentLog(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "+ ") || strings.HasPrefix(t, "Syncing bundled skills") ||
			strings.HasPrefix(t, "Done: ") || t == "Dropping root privileges" {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// release removes the attempt's host-side agent home once its container is
// gone. Safe to call more than once.
func (e *SandboxedAgentExecutor) release(a *agentAttempt) {
	e.mu.Lock()
	home := a.agentHome
	a.agentHome = ""
	e.mu.Unlock()
	if home != "" {
		os.RemoveAll(home)
	}
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
	e.release(a)
	return Result{}, nil
}
