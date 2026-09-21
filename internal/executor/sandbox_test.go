package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestContainerSandboxIsolation runs a payload inside a real isolated container
// and asserts it cannot write outside the work root or open a network socket
// (review B2). Skipped when Docker or the daemon is unavailable.
func TestContainerSandboxIsolation(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}

	s := NewContainerSandbox("alpine:3.20")
	work := t.TempDir()

	script := `
if echo pwn > /etc/pwned 2>/dev/null; then echo ROOT_WRITE_SUCCEEDED; else echo ROOT_WRITE_BLOCKED; fi
echo ok > /work/inside.txt && echo WORK_WRITE_OK
if wget -q -T 3 -O /dev/null http://example.com 2>/dev/null; then echo NETWORK_OPEN; else echo NETWORK_BLOCKED; fi
`
	stdout, stderr, code, err := s.Run(t.Context(), RunOptions{WorkRoot: work, Cmd: []string{"sh", "-c", script}})
	if err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr)
	}
	if code != 0 {
		t.Fatalf("Run() exit = %d, want 0 (stderr=%s)", code, stderr)
	}

	for _, want := range []string{"ROOT_WRITE_BLOCKED", "WORK_WRITE_OK", "NETWORK_BLOCKED"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("payload output missing %q; stdout=%q stderr=%q", want, stdout, stderr)
		}
	}
	if strings.Contains(stdout, "ROOT_WRITE_SUCCEEDED") {
		t.Errorf("container wrote to read-only root; stdout=%q", stdout)
	}

	// The work-root write must have landed on the host.
	data, err := os.ReadFile(filepath.Join(work, "inside.txt"))
	if err != nil {
		t.Fatalf("read inside.txt: %v", err)
	}
	if string(data) != "ok\n" {
		t.Errorf("inside.txt = %q, want %q", string(data), "ok\n")
	}
}

// TestContainerSandboxGoTestExecutes is a regression test for review defect
// #1: `go test` writes a compiled test binary to $TMPDIR/os.TempDir() and
// then fork/execs it. With Docker's default `--tmpfs /tmp` (noexec), that
// exec fails with "permission denied" even though compilation succeeded --
// so `go test` could never pass inside the check sandbox. This runs a real
// `go test` for a tiny module inside the sandbox and asserts it passes.
// Skipped when Docker, the daemon, or a Go toolchain image is unavailable.
func TestContainerSandboxGoTestExecutes(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	const image = "golang:1.25"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		if err := exec.Command("docker", "pull", image).Run(); err != nil {
			t.Skipf("go toolchain image %s not available and could not be pulled: %v", image, err)
		}
	}

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "go.mod"), "module tmpexec\n\ngo 1.22\n")
	writeFile(t, filepath.Join(work, "tmpexec_test.go"), `package tmpexec

import "testing"

func TestOK(t *testing.T) {
	// A compiled test binary; the sandbox must be able to fork/exec it
	// after writing it into $TMPDIR (review defect #1).
}
`)

	s := NewContainerSandbox(image)
	stdout, stderr, code, err := s.Run(t.Context(), RunOptions{
		WorkRoot: work,
		Cmd:      []string{"sh", "-c", "go test ./..."},
	})
	if err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr)
	}
	out := stdout + stderr
	if strings.Contains(out, "permission denied") {
		t.Fatalf("go test hit a noexec /tmp: %s", out)
	}
	if code != 0 {
		t.Fatalf("go test exit = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected passing test output, got:\n%s", out)
	}
}

// TestContainerSandboxTmpfsKeepsNodev is review LOW #8: re-enabling exec on
// the /tmp tmpfs mounts (review defect #1, above) must not also drop
// Docker's own tmpfs default of nodev -- moot without CAP_MKNOD (already
// dropped by --cap-drop ALL), but free to keep, and an earlier revision
// dropped it right along with noexec when adding exec. No Docker needed:
// this asserts against the argv isolatedArgs builds, not a running
// container.
func TestContainerSandboxTmpfsKeepsNodev(t *testing.T) {
	s := NewContainerSandbox("alpine:3.20")
	args := s.isolatedArgs(RunOptions{WorkRoot: "/tmp/work"})
	found := 0
	for i, a := range args {
		if a != "--tmpfs" || i+1 >= len(args) {
			continue
		}
		mount := args[i+1]
		if !strings.HasPrefix(mount, "/tmp") {
			continue
		}
		found++
		opts := strings.Split(mount, ":")
		if len(opts) < 2 {
			t.Errorf("--tmpfs %q has no mount options", mount)
			continue
		}
		for _, want := range []string{"exec", "nosuid", "nodev"} {
			if !strings.Contains(opts[1], want) {
				t.Errorf("--tmpfs %q is missing %q", mount, want)
			}
		}
	}
	if found == 0 {
		t.Fatal("no /tmp --tmpfs mount found in isolatedArgs; the fixture and the code have drifted")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
