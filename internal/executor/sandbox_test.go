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
