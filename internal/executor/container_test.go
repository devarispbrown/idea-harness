package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/domain"
)

// fakeDockerScript is a minimal Docker CLI stand-in: it records its arguments
// and returns canned responses so the adapter can be tested hermetically.
const fakeDockerScript = `#!/usr/bin/env bash
sub="$1"
shift
printf '%s\n' "$@" >> "$FAKE_DOCKER_RECORD"
case "$sub" in
  run) echo "cid-123" ;;
  inspect) echo "exited 0" ;;
  rm) exit 0 ;;
  *) exit 1 ;;
esac
`

func writeExecutable(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func newTestBlobStore(t *testing.T) *blob.Store {
	t.Helper()
	s, err := blob.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("blob.New: %v", err)
	}
	return s
}

func TestContainerExecutorCapabilities(t *testing.T) {
	e := NewContainerExecutor(nil, "")
	if got := e.Name(); got != "container" {
		t.Fatalf("Name() = %q, want %q", got, "container")
	}
	caps := e.Capabilities(t.Context())
	if caps.Trust != domain.ExecutorLocalExternal {
		t.Errorf("Trust = %q, want %q", caps.Trust, domain.ExecutorLocalExternal)
	}
	if caps.SandboxMode != domain.SandboxContainer {
		t.Errorf("SandboxMode = %q, want %q", caps.SandboxMode, domain.SandboxContainer)
	}
	if caps.Network {
		t.Error("Network = true, want false (--network none)")
	}
}

func TestContainerExecutorStartStatusCollect(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)

	e := NewContainerExecutor(newTestBlobStore(t), "alpine:3.20")
	e.dockerPath = writeExecutable(t, "docker", fakeDockerScript)

	work := t.TempDir()
	req := Request{TaskTitle: "Add changelog", WorkRoot: work}

	a, err := e.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if a.ID == "" || a.Executor != "container" {
		t.Fatalf("Start() = %+v, want non-empty id and executor container", a)
	}

	// The recorded args must include the no-network, mount, and env flags.
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	args := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"-d --network none",
		"-v " + work + ":/work",
		"-e TASK_TITLE=Add changelog",
		"alpine:3.20",
		"sh -c " + containerScript,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args %q missing %q", args, want)
		}
	}

	st, err := e.Status(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("Status() = %q, want succeeded", st.Status)
	}

	// Collect reads the hand-off file the container wrote.
	if err := os.MkdirAll(filepath.Join(work, "result"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "result", "patch.txt"), []byte("change: Add changelog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.Collect(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if res.PatchDigest != blob.Digest([]byte("change: Add changelog\n")) {
		t.Fatalf("PatchDigest = %q, want content digest", res.PatchDigest)
	}
}

func TestContainerExecutorStatusFailure(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	script := `#!/usr/bin/env bash
sub="$1"
shift
printf '%s\n' "$@" >> "$FAKE_DOCKER_RECORD"
case "$sub" in
  run) echo "cid-123" ;;
  inspect) echo "exited 3" ;;
  rm) exit 0 ;;
  *) exit 1 ;;
esac
`
	e := NewContainerExecutor(nil, "")
	e.dockerPath = writeExecutable(t, "docker", script)

	a, err := e.Start(t.Context(), Request{WorkRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.Status(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "failed" {
		t.Fatalf("Status() = %q, want failed", st.Status)
	}
}
