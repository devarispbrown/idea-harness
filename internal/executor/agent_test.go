package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
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
	e := NewOMPExecutor(nil, "", "")
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

	h := NewHermesExecutor(nil, "", "")
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

	e := NewOMPExecutor(newTestBlobStore(t), "opus", "alpine:3.20")
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
		"--tmpfs /tmp",
		"-v " + work + ":/work",
		"-w /work",
		"-e ANTHROPIC_API_KEY=sk-ant-test",
		"alpine:3.20",
		"omp --print --cwd /work --model opus --auto-approve",
		"Add a changelog",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args %q missing %q", args, want)
		}
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

	e := NewHermesExecutor(newTestBlobStore(t), "opus", "alpine:3.20")
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	work := t.TempDir()
	if _, err := e.Start(t.Context(), Request{TaskStatement: "Build the core", WorkRoot: work}); err != nil {
		t.Fatal(err)
	}

	args := recordedArgs(t, record)
	for _, want := range []string{
		"--read-only",
		"hermes --in /work --yolo chat --query Build the core --model opus",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args %q missing %q", args, want)
		}
	}
	if strings.Contains(args, "--network none") {
		t.Errorf("docker args %q must not contain --network none", args)
	}
}

func TestSandboxedAgentUsesBrief(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewHermesExecutor(newTestBlobStore(t), "opus", "alpine:3.20")
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
