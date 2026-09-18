package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func recordedArgs(t *testing.T, record string) string {
	t.Helper()
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

func TestSandboxedAgentCapabilities(t *testing.T) {
	e := NewOMPExecutor(nil, "", "", AgentProvider{})
	if got := e.Name(); got != "omp" {
		t.Fatalf("Name() = %q, want omp", got)
	}
	caps := e.Capabilities(t.Context())
	if caps.Trust != domain.ExecutorLocalExternal {
		t.Errorf("Trust = %q, want local_external", caps.Trust)
	}
	// The key B2 change: the sandbox mode is now REAL container isolation.
	if caps.SandboxMode != domain.SandboxContainer {
		t.Errorf("SandboxMode = %q, want container (real isolation)", caps.SandboxMode)
	}
	// Network is now TRUE: the agent needs egress to reach its model API.
	if !caps.Network {
		t.Error("Network = false, want true (agent needs model egress)")
	}

	h := NewHermesExecutor(nil, "", "", AgentProvider{})
	if h.Name() != "hermes" {
		t.Errorf("Name() = %q, want hermes", h.Name())
	}
	if h.Capabilities(t.Context()).SandboxMode != domain.SandboxContainer {
		t.Error("Hermes SandboxMode != container")
	}
}

func TestSandboxedOMPArgs(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	e := NewOMPExecutor(newTestBlobStore(t), "anthropic/opus", "alpine:3.20",
		AgentProvider{Name: "anthropic", KeyEnv: "ANTHROPIC_API_KEY", BaseURL: "https://api.anthropic.com/v1"})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	work := t.TempDir()
	a, err := e.Start(t.Context(), Request{TaskStatement: "Add a changelog", WorkRoot: work})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if a.ID == "" || a.Executor != "omp" {
		t.Fatalf("Start() = %+v", a)
	}

	args := recordedArgs(t, record)
	for _, want := range []string{
		"--read-only",
		"--cap-drop ALL",
		"--tmpfs /tmp",
		"--env-file", // single key via env-file, never -e NAME=value (argv leak)
		"-v " + work + ":/work",
		"-w /work",
		"alpine:3.20",
		"omp --print --cwd /work --model anthropic/opus --auto-approve",
		"Add a changelog",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args %q missing %q", args, want)
		}
	}
	// The key value must NOT appear in argv.
	if strings.Contains(args, "sk-ant-test") {
		t.Errorf("provider key leaked into docker argv: %q", args)
	}
	// The agent must NOT be network-isolated (it needs model egress).
	if strings.Contains(args, "--network none") {
		t.Errorf("docker args %q must not contain --network none", args)
	}

	st, err := e.Status(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("Status() = %q, want succeeded", st.Status)
	}
}

func TestSandboxedHermesArgs(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewHermesExecutor(newTestBlobStore(t), "opus", "alpine:3.20", AgentProvider{})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	work := t.TempDir()
	if _, err := e.Start(t.Context(), Request{TaskStatement: "Build the core", WorkRoot: work}); err != nil {
		t.Fatal(err)
	}

	args := recordedArgs(t, record)
	// This used to assert `hermes --in /work --yolo chat --query ... --model ...`
	// -- an argv the pinned image REJECTS at argument parsing, because there is
	// no `--in` flag. The test passed for three releases while the coding-agent
	// path could not have produced a single file, because it asserted the shape
	// of the call rather than that the call works.
	//
	// The form below is the one verified against
	// nousresearch/hermes-agent:v2026.5.16, which parses, initialises, and
	// writes files.
	for _, want := range []string{
		"--read-only",
		"hermes chat --yolo -q Build the core -m opus",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args %q missing %q", args, want)
		}
	}
	if strings.Contains(args, "--network none") {
		t.Errorf("docker args %q must not contain --network none", args)
	}
}

// The container cannot boot without a writable agent home.
//
// The image's HOME is /opt/data and its entrypoint creates and writes there.
// Under --read-only with a host --user that mkdir fails with "Permission
// denied" and NOTHING runs — measured against the pinned image. HERMES_HOME is
// the entrypoint's own knob for relocating it, and dropping it leaves a
// container that starts and immediately dies.
func TestSandboxedHermesGetsAWritableHome(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewHermesExecutor(newTestBlobStore(t), "openai/gpt-5.6-luna", "alpine:3.20",
		AgentProvider{Name: "openai", KeyEnv: "OPENAI_API_KEY", BaseURL: "https://api.openai.com/v1"})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	work := t.TempDir()
	a, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(filepath.Dir(work), agentHomePrefix+a.ID)
	args := recordedArgs(t, record)
	for _, want := range []string{
		"HERMES_HOME=" + containerAgentHome,
		// The mount itself, not just the path: HERMES_HOME alone names a
		// directory the read-only root does not let the entrypoint create.
		"-v " + home + ":" + containerAgentHome,
		// What SweepAgentLeftovers finds a crashed daemon's containers by.
		"--label " + agentLabel + "=" + filepath.Dir(work),
		"--provider idea-openai",
		"-m gpt-5.6-luna",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args missing %q:\n%s", want, args)
		}
	}
	// A non-loopback endpoint must not gain a route to the host.
	if strings.Contains(args, "host-gateway") {
		t.Errorf("a public endpoint was given a host route:\n%s", args)
	}
}

// Ollama is loopback, so it does need one.
func TestSandboxedHermesMapsTheHostForOllama(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewHermesExecutor(newTestBlobStore(t), "ollama/llama3.1", "alpine:3.20",
		AgentProvider{Name: "ollama", BaseURL: "http://localhost:11434/v1"})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	if _, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	args := recordedArgs(t, record)
	if !strings.Contains(args, "host.docker.internal:host-gateway") {
		t.Errorf("ollama got no host route, so the agent dials its own empty port:\n%s", args)
	}
	// And no key file, because ollama needs none.
	if strings.Contains(args, "--env-file") {
		t.Errorf("a keyless provider was given an env-file:\n%s", args)
	}
}

func TestSandboxedAgentUsesBrief(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewHermesExecutor(newTestBlobStore(t), "opus", "alpine:3.20", AgentProvider{})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	work := t.TempDir()
	if _, err := e.Start(t.Context(), Request{TaskStatement: "Build the core", Brief: "Implementation brief\nSummary: write hello.txt", WorkRoot: work}); err != nil {
		t.Fatal(err)
	}

	args := recordedArgs(t, record)
	if !strings.Contains(args, "write hello.txt") {
		t.Errorf("agent argv %q must use the planner brief, not the raw task statement", args)
	}
	if strings.Contains(args, "Build the core") {
		t.Errorf("agent argv %q should not fall back to the task statement when a brief is present", args)
	}
}

// OMP is not Hermes. Replacing its HOME with an empty directory hid whatever
// config or auth an OMP image bakes in, so only Hermes gets an agent home.
func TestOMPKeepsItsOwnHome(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewOMPExecutor(newTestBlobStore(t), "openai/gpt-5.6-luna", "alpine:3.20",
		AgentProvider{Name: "openai", KeyEnv: "OPENAI_API_KEY", BaseURL: "https://api.openai.com/v1"})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)
	if _, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	args := recordedArgs(t, record)
	for _, bad := range []string{"HOME=", containerAgentHome} {
		if strings.Contains(args, bad) {
			t.Errorf("omp was given a Hermes home (%q):\n%s", bad, args)
		}
	}
}

// fakeDockerLog records each call's subcommand as well as its arguments, one
// call per line, so a test can tell `rm -f x` from `run ... x`.
const fakeDockerLog = `#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_RECORD"
case "$1" in
  run) [ -n "$FAKE_DOCKER_RUN_FAIL" ] && exit 125; echo "cid-123" ;;
  inspect) echo "exited 0" ;;
  rm) [ -n "$FAKE_DOCKER_RM_FAIL" ] && exit 1; exit 0 ;;
  ps) echo "leftover-1" ;;
  *) exit 1 ;;
esac
`

func dockerCalls(t *testing.T, record string) []string {
	t.Helper()
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newHermesForLifecycle(t *testing.T) (*SandboxedAgentExecutor, string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	e := NewHermesExecutor(newTestBlobStore(t), "openai/gpt-5.6-luna", "alpine:3.20",
		AgentProvider{Name: "openai", BaseURL: "https://api.openai.com/v1"})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerLog)
	return e, record
}

// Without --rm, removing the container is the executor's job on every path.
// A container left behind keeps the provider key in its config.
func TestCollectRemovesTheContainerAndTheHome(t *testing.T) {
	e, record := newHermesForLifecycle(t)
	work := t.TempDir()
	a, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(filepath.Dir(work), agentHomePrefix+a.ID)
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("no agent home during the run: %v", err)
	}
	if _, err := e.Collect(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if !hasCall(dockerCalls(t, record), "rm -f cid-123") {
		t.Error("Collect did not remove the container")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the agent home outlived the attempt: %v", err)
	}
	// And the attempt is forgotten, so the map does not grow for the life of
	// the daemon holding every request.
	if _, err := e.Status(t.Context(), a.ID); err == nil {
		t.Error("a collected attempt is still tracked")
	}
}

// Collect used to drop the rm error and report success, so the engine skipped
// the Cancel that would have retried, and the container stayed.
func TestCollectReportsAFailedRemoval(t *testing.T) {
	e, _ := newHermesForLifecycle(t)
	work := t.TempDir()
	a, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_RM_FAIL", "1")
	if _, err := e.Collect(t.Context(), a.ID); err == nil {
		t.Fatal("Collect reported success although the container is still there")
	}
	// The attempt is kept so Cancel can retry, and the home with it, which the
	// container may still be using.
	t.Setenv("FAKE_DOCKER_RM_FAIL", "")
	if err := e.Cancel(t.Context(), a.ID); err != nil {
		t.Fatalf("the retry could not find the attempt: %v", err)
	}
}

// A cancelled run is exactly when cleanup must happen, so it must not use the
// cancelled context.
func TestCollectRemovesEvenWithACancelledContext(t *testing.T) {
	e, record := newHermesForLifecycle(t)
	a, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Collect(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if !hasCall(dockerCalls(t, record), "rm -f cid-123") {
		t.Error("a cancelled context skipped the removal")
	}
}

// `docker run -d` can create the container and then fail to start it. No
// attempt is recorded, so Start itself must remove it -- by the name it chose
// before the container existed.
func TestFailedStartRemovesWhatItCreated(t *testing.T) {
	e, record := newHermesForLifecycle(t)
	t.Setenv("FAKE_DOCKER_RUN_FAIL", "1")
	work := t.TempDir()
	if _, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: work}); err == nil {
		t.Fatal("Start succeeded although docker run failed")
	}
	calls := dockerCalls(t, record)
	var name string
	for _, c := range calls {
		f := strings.Fields(c)
		for i := range f {
			if f[i] == "--name" && i+1 < len(f) {
				name = f[i+1]
			}
		}
	}
	if name == "" {
		t.Fatal("the container was not named before it was created")
	}
	if !hasCall(calls, "rm -f "+name) {
		t.Errorf("the half-created container %s was not removed:\n%s", name, strings.Join(calls, "\n"))
	}
	entries, _ := os.ReadDir(filepath.Dir(work))
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), agentHomePrefix) {
			t.Errorf("a failed Start left %s behind", ent.Name())
		}
	}
}

// What a killed daemon leaves behind is found by its agent-home directory, and
// both the container and the directory are removed.
func TestSweepRemovesLeftovers(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	bin := writeExecutable(t, "docker", fakeDockerLog)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	state := t.TempDir()
	workDir := filepath.Join(state, "projects", "p1", "work")
	stale := filepath.Join(workDir, agentHomePrefix+"dead")
	if err := os.MkdirAll(filepath.Join(stale, "locked"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "locked", "state.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The agent runs as the host uid with auto-approve and can do this.
	if err := os.Chmod(filepath.Join(stale, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(stale, "locked"), 0o700) })
	keep := filepath.Join(workDir, "initiative-1")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}

	SweepAgentLeftovers(t.Context(), state)

	if !hasCall(dockerCalls(t, record), "rm -f "+agentContainerName("dead")) {
		t.Error("the leftover container was not removed by name")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a stale agent home survived the sweep: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the sweep removed a work root: %v", err)
	}
}

// A normal start has nothing to sweep and must not call Docker at all: the
// sweep runs before the daemon serves, and a hung Docker would hold it up.
func TestSweepWithNothingLeftDoesNotCallDocker(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	bin := writeExecutable(t, "docker", fakeDockerLog)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "projects", "p1", "work", "initiative-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	SweepAgentLeftovers(t.Context(), state)
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Error("the sweep called docker with nothing to sweep")
	}
}

// OMP gets an attempt directory too -- unmounted -- because that is how the
// sweep finds a killed daemon's OMP containers.
func TestOMPAttemptLeavesAMarker(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	e := NewOMPExecutor(newTestBlobStore(t), "openai/gpt-5.6-luna", "alpine:3.20",
		AgentProvider{Name: "openai", BaseURL: "https://api.openai.com/v1"})
	e.dockerPath = writeExecutable(t, "docker", fakeDockerLog)
	work := t.TempDir()
	a, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(work), agentHomePrefix+a.ID)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("no marker for a running OMP attempt: %v", err)
	}
	// And it goes with the attempt; one left behind would make every daemon
	// start call Docker.
	if _, err := e.Collect(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("the OMP marker outlived its attempt: %v", err)
	}
}
