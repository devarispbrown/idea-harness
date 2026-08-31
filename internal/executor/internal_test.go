package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
)

func TestInternalExecutorNameAndCapabilities(t *testing.T) {
	e := NewInternalExecutor(nil)
	if got := e.Name(); got != "internal" {
		t.Fatalf("Name() = %q, want %q", got, "internal")
	}

	caps := e.Capabilities(t.Context())
	if caps.Trust != domain.ExecutorInternal {
		t.Errorf("Trust = %q, want %q", caps.Trust, domain.ExecutorInternal)
	}
	if caps.SandboxMode != domain.SandboxLocalRestricted {
		t.Errorf("SandboxMode = %q, want %q", caps.SandboxMode, domain.SandboxLocalRestricted)
	}
	if caps.Network {
		t.Error("Network = true, want false (hermetic)")
	}
	if len(caps.Capabilities) != 1 || caps.Capabilities[0] != "fs.write" {
		t.Errorf("Capabilities = %v, want [fs.write]", caps.Capabilities)
	}
}

func TestInternalExecutorStart(t *testing.T) {
	e := NewInternalExecutor(nil)
	a, err := e.Start(t.Context(), Request{TaskTitle: "Add changelog"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if a.ID == "" {
		t.Fatal("Start() returned empty ID")
	}
	if a.Executor != "internal" {
		t.Errorf("Executor = %q, want %q", a.Executor, "internal")
	}
	if a.Status != "running" {
		t.Errorf("Status = %q, want %q", a.Status, "running")
	}
	if a.Phase != "Planning" {
		t.Errorf("Phase = %q, want %q", a.Phase, "Planning")
	}
}

func TestInternalExecutorDeterministicProgression(t *testing.T) {
	e := NewInternalExecutor(nil)
	a, err := e.Start(t.Context(), Request{TaskTitle: "Add changelog"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	want := []struct {
		status string
		phase  string
	}{
		{"running", "Editing"},
		{"running", "Testing"},
		{"running", "Waiting"},
		{"running", "Reviewing"},
		{"succeeded", "Finished"},
	}

	for i, w := range want {
		st, err := e.Status(t.Context(), a.ID)
		if err != nil {
			t.Fatalf("Status() call %d error = %v", i+1, err)
		}
		if st.Status != w.status || st.Phase != w.phase {
			t.Fatalf("Status() call %d = (%q, %q), want (%q, %q)",
				i+1, st.Status, st.Phase, w.status, w.phase)
		}
	}

	// Terminal state is idempotent: further calls stay Finished/succeeded.
	for i := 0; i < 3; i++ {
		st, err := e.Status(t.Context(), a.ID)
		if err != nil {
			t.Fatalf("terminal Status() error = %v", err)
		}
		if st.Status != "succeeded" || st.Phase != "Finished" {
			t.Fatalf("terminal Status() = (%q, %q), want (succeeded, Finished)",
				st.Status, st.Phase)
		}
	}
}

func TestInternalExecutorCancel(t *testing.T) {
	e := NewInternalExecutor(nil)
	a, err := e.Start(t.Context(), Request{TaskTitle: "Add changelog"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := e.Cancel(t.Context(), a.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	st, err := e.Status(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("Status() after Cancel() error = %v", err)
	}
	if st.Status != "cancelled" {
		t.Errorf("Status() after Cancel() = %q, want %q", st.Status, "cancelled")
	}
}

func TestInternalExecutorCollectWritesRealFile(t *testing.T) {
	e := NewInternalExecutor(nil)
	work := t.TempDir()
	a, err := e.Start(t.Context(), Request{TaskTitle: "Add changelog", WorkRoot: work})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	res, err := e.Collect(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if res.PatchDigest != "" {
		t.Errorf("PatchDigest = %q, want empty (the engine computes the diff)", res.PatchDigest)
	}

	data, err := os.ReadFile(filepath.Join(work, "hello.txt"))
	if err != nil {
		t.Fatalf("read hello.txt: %v", err)
	}
	if string(data) != "task: Add changelog\n" {
		t.Errorf("hello.txt = %q, want %q", string(data), "task: Add changelog\n")
	}
}

func TestInternalExecutorUnknownID(t *testing.T) {
	e := NewInternalExecutor(nil)
	ctx := t.Context()

	if _, err := e.Status(ctx, "does-not-exist"); err == nil {
		t.Error("Status() for unknown id = nil error, want error")
	}
	if err := e.Cancel(ctx, "does-not-exist"); err == nil {
		t.Error("Cancel() for unknown id = nil error, want error")
	}
	if _, err := e.Collect(ctx, "does-not-exist"); err == nil {
		t.Error("Collect() for unknown id = nil error, want error")
	}
}

var _ Executor = (*InternalExecutor)(nil)
