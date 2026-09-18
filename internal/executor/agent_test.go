package executor

import (
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
	e := NewOMPExecutor(nil, "", "", "", "", "")
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

	h := NewHermesExecutor(nil, "", "", "", "", "")
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

	e := NewOMPExecutor(newTestBlobStore(t), "anthropic/opus", "alpine:3.20", "ANTHROPIC_API_KEY", "anthropic", "https://api.anthropic.com/v1")
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

	e := NewHermesExecutor(newTestBlobStore(t), "opus", "alpine:3.20", "", "", "")
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
		"OPENAI_API_KEY", "openai", "https://api.openai.com/v1")
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	if _, err := e.Start(t.Context(), Request{TaskStatement: "x", WorkRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	args := recordedArgs(t, record)
	for _, want := range []string{
		"HERMES_HOME=" + containerAgentHome,
		containerAgentHome,
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
		"", "ollama", "http://localhost:11434/v1")
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

	e := NewHermesExecutor(newTestBlobStore(t), "opus", "alpine:3.20", "", "", "")
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
