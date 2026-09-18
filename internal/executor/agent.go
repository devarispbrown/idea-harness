package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	provider   AgentProvider // resolved by the engine; zero on the capability-listing path
	lookupKey  func(name string) string
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

func newSandboxedAgentExecutor(store *blob.Store, name, model, image string, provider AgentProvider, buildArgs func(Request, string, string) []string) *SandboxedAgentExecutor {
	if image == "" {
		image = "alpine:3.20"
	}
	return &SandboxedAgentExecutor{
		attempts:   make(map[string]*agentAttempt),
		store:      store,
		name:       name,
		model:      model,
		image:      image,
		provider:   provider,
		lookupKey:  os.Getenv,
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

// bareModel strips the `provider/` prefix, and only the first one: an
// OpenRouter id legitimately contains a slash (`openrouter/anthropic/x` becomes
// `anthropic/x`), and an Ollama tag keeps its colon.
func bareModel(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[i+1:]
	}
	return model
}

// SetKeyLookup sets where the provider key's value is read from. The daemon
// passes one that falls back to the project's .env, as its model registry does;
// reading only the process environment gave the agent no key while every other
// model call had one.
func (e *SandboxedAgentExecutor) SetKeyLookup(lookup func(name string) string) {
	if lookup != nil {
		e.lookupKey = lookup
	}
}

// writeProviderKeyFile writes the executor's single provider key (resolved by the
// engine from config, not from a model-prefix switch) to a 0600 temp env-file so
// the secret never appears in the docker run argv. Returns "" when no key is set.
func (e *SandboxedAgentExecutor) writeProviderKeyFile() (string, error) {
	if e.provider.KeyEnv == "" {
		return "", nil
	}
	value := e.lookupKey(e.provider.KeyEnv)
	if value == "" {
		return "", nil
	}
	f, err := os.CreateTemp("", "idea-harness-key-*")
	if err != nil {
		return "", fmt.Errorf("executor: create key env-file: %w", err)
	}
	path := f.Name()
	if _, err := fmt.Fprintf(f, "%s=%s\n", e.provider.KeyEnv, value); err != nil {
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

// NewOMPExecutor returns a sandboxed OMP executor pinned to provider, which the
// engine resolves with ResolveAgentProvider.
func NewOMPExecutor(store *blob.Store, model, image string, provider AgentProvider) *SandboxedAgentExecutor {
	return newSandboxedAgentExecutor(store, "omp", model, image, provider, ompArgs)
}

// NewHermesExecutor returns a sandboxed Hermes (Nous Research) executor.
func NewHermesExecutor(store *blob.Store, model, image string, provider AgentProvider) *SandboxedAgentExecutor {
	return newSandboxedAgentExecutor(store, "hermes", model, image, provider, hermesArgs)
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
func (e *SandboxedAgentExecutor) containerArgs(req Request, attemptID, keyFile, agentHome string) []string {
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	// No `--rm`. The container runs detached and Status polls it with
	// `docker inspect`; with --rm, a fast agent (the smoke writes one file in
	// ~6s) finished and deleted itself between polls, so inspect failed on a run
	// that had SUCCEEDED -- surfacing as "failed (removing)" or
	// "docker inspect: exit status 1".
	//
	// Without --rm, a container nothing removes stays on disk with the provider
	// key in its config, readable with `docker inspect`. So it is NAMED before
	// it exists (Start can remove it even when `docker run` fails after
	// creating it) and LABELLED with its work directory (SweepAgentLeftovers
	// can find what a daemon crash left behind). Collect and Cancel remove it.
	args := []string{
		"run", "-d",
		"--name", agentContainerName(attemptID),
		"--label", agentLabel + "=" + filepath.Dir(req.WorkRoot),
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
	if _, needsGateway := containerBaseURL(e.provider.BaseURL); needsGateway {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	return append(args, e.image)
}

// containerAgentHome is where the generated agent home is mounted.
const containerAgentHome = "/tmp/agent-home"

// agentLabel marks every agent container with the directory its work roots and
// agent homes live in, so leftovers can be found per project.
const agentLabel = "idea-harness.agent"

// agentHomePrefix names agent-home directories, which sit next to the work root.
const agentHomePrefix = ".agent-home-"

func agentContainerName(attemptID string) string { return "idea-agent-" + attemptID }

// writeAgentHome creates the writable home a Hermes agent needs, containing the
// generated config that pins it to one provider and endpoint. Returns "" for
// any other agent: OMP is not Hermes, and replacing its HOME with an empty
// directory would hide whatever config or auth its image bakes in.
//
// Created next to the work root rather than in the system temp directory.
// The work root is already bind-mounted, so its parent is a path Docker can
// see; $TMPDIR is not one under snap-installed Docker (private /tmp) or
// Colima and Lima (only $HOME is shared), where the mount came up empty.
func (e *SandboxedAgentExecutor) writeAgentHome(req Request, attemptID string) (string, error) {
	if e.name != "hermes" {
		return "", nil
	}
	dir := filepath.Join(filepath.Dir(req.WorkRoot), agentHomePrefix+attemptID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("executor: create agent home: %w", err)
	}
	// Without a provider (the capability-listing path) the home still has to
	// exist -- the entrypoint cannot boot without it -- but there is nothing to
	// pin.
	if e.provider.Name == "" {
		return dir, nil
	}
	baseURL, _ := containerBaseURL(agentBaseURL(e.provider.Name, e.provider.BaseURL))
	cfg, err := hermesConfigYAML(e.provider.Name, baseURL, bareModel(e.model), e.provider.KeyEnv)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, hermesConfigPath), []byte(cfg), 0o600)
	}
	if err != nil {
		removeAgentHome(dir)
		return "", fmt.Errorf("executor: write agent config: %w", err)
	}
	return dir, nil
}

// removeAgentHome deletes an agent home. The agent runs as the host uid with
// auto-approve, so it can leave a directory it made unreadable (chmod 000), and
// a plain RemoveAll then fails on it. Permissions are restored first.
func removeAgentHome(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
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
	attemptID := id.New()

	// A writable agent home, seeded with the config that pins the provider.
	//
	// The image's HOME is /opt/data and its entrypoint creates and writes there.
	// Under this container's --read-only root and host --user, that mkdir fails
	// with "Permission denied" and NOTHING runs -- which is the second reason
	// the coding-agent path never worked. HERMES_HOME is the entrypoint's own
	// knob for relocating it.
	agentHome, err := e.writeAgentHome(req, attemptID)
	if err != nil {
		return Attempt{}, err
	}
	// Removed by release(), NOT deferred here. The container runs detached, so
	// Start returns while the agent is still working; deleting the home at that
	// point pulled its config and state out from under it mid-run, and the
	// smoke failed in eight seconds.
	started := false
	defer func() {
		if started {
			return
		}
		// `docker run -d` can create the container and then fail to start it
		// (exit 125), or be killed by ctx while the daemon goes on to create
		// it. Either way no attempt is recorded, so nothing else would ever
		// remove it. Not ctx: it may be the reason we are here.
		cctx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = exec.CommandContext(cctx, e.dockerPath, "rm", "-f", agentContainerName(attemptID)).Run()
		if agentHome != "" {
			removeAgentHome(agentHome)
		}
	}()

	args := e.containerArgs(req, attemptID, keyFile, agentHome)
	args = append(args, e.buildArgs(req, e.model, e.provider.Name)...)
	out, err := exec.CommandContext(ctx, e.dockerPath, args...).Output()
	if err != nil {
		return Attempt{}, fmt.Errorf("executor: %s: docker run: %w", e.name, err)
	}
	cid := strings.TrimSpace(string(out))
	if cid == "" {
		return Attempt{}, fmt.Errorf("executor: %s: docker run returned no container id", e.name)
	}

	e.mu.Lock()
	e.attempts[attemptID] = &agentAttempt{req: req, containerID: cid, agentHome: agentHome}
	e.mu.Unlock()
	started = true

	return Attempt{ID: attemptID, Executor: e.name, Status: "running", Phase: "Planning"}, nil
}

// cleanupContext is for removing what an attempt left behind. It keeps ctx's
// values but not its cancellation -- a cancelled run is exactly when cleanup
// has to happen -- and bounds the removal itself.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
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
	return e.remove(ctx, id)
}

// remove deletes an attempt's container and then its agent home, and forgets
// the attempt. If the container cannot be removed the attempt is kept, so a
// later Cancel can retry, and so is the home, which the container may still be
// using.
func (e *SandboxedAgentExecutor) remove(ctx context.Context, id string) error {
	e.mu.Lock()
	a, ok := e.attempts[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("executor: unknown attempt %q", id)
	}
	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	if err := exec.CommandContext(cctx, e.dockerPath, "rm", "-f", a.containerID).Run(); err != nil {
		return fmt.Errorf("executor: %s: docker rm: %w", e.name, err)
	}
	e.mu.Lock()
	delete(e.attempts, id)
	e.mu.Unlock()
	if a.agentHome != "" {
		if err := removeAgentHome(a.agentHome); err != nil {
			return fmt.Errorf("executor: %s: remove agent home: %w", e.name, err)
		}
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

// Collect cleans up the container and returns an empty Result. The engine
// computes the diff from the work root (B3); the executor's job is only to run
// the agent in isolation.
func (e *SandboxedAgentExecutor) Collect(ctx context.Context, id string) (Result, error) {
	// The error is returned, not dropped. Dropping it reported success while
	// the container -- and the key in its config -- stayed on disk, and the
	// engine then skipped the Cancel that would have retried.
	if err := e.remove(ctx, id); err != nil {
		return Result{}, err
	}
	return Result{}, nil
}

// SweepAgentLeftovers removes agent containers and agent homes that a previous
// daemon left behind in workDir -- the directory holding a project's work
// roots. Collect and Cancel remove them in the normal course; this covers a
// daemon that crashed or was killed between Start and either of them, which
// used to leave the container, and the provider key in its config, on disk
// with nothing that would ever find it.
//
// Call it only when no attempt for this project can be running, which is at
// daemon start. Best effort: without Docker there is nothing to sweep.
func SweepAgentLeftovers(ctx context.Context, workDir string) {
	sctx, cancel := cleanupContext(ctx)
	defer cancel()
	out, err := exec.CommandContext(sctx, "docker", "ps", "-aq",
		"--filter", "label="+agentLabel+"="+workDir).Output()
	if err == nil {
		for _, cid := range strings.Fields(string(out)) {
			_ = exec.CommandContext(sctx, "docker", "rm", "-f", cid).Run()
		}
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() && strings.HasPrefix(ent.Name(), agentHomePrefix) {
			removeAgentHome(filepath.Join(workDir, ent.Name()))
		}
	}
}
