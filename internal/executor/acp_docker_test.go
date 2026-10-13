package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shellACPAgent is a minimal ACP agent in POSIX sh: it answers the client's
// first three requests by their known ids (initialize=1, session/new=2,
// session/prompt=3), asks the client to write one file, and writes another
// itself through the /work mount.
const shellACPAgent = `
read -r l; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{},"agentInfo":{"name":"sh-acp","version":"0"}}}'
read -r l; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"s"}}'
read -r l; printf '%s\n' '{"jsonrpc":"2.0","id":"w1","method":"fs/write_text_file","params":{"sessionId":"s","path":"/work/via-client.txt","content":"client"}}'
read -r l; echo direct > /work/via-mount.txt
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'
`

// TestACPOverRealDocker runs the production transport -- `docker run -i` with
// the hardened argv -- against a shell ACP agent in a small local image. It
// proves what the fake-process tests cannot: that the hardened container boots
// attached, that JSON-RPC survives docker's stdio, that the /work mount is the
// work root, and that --rm leaves nothing behind.
//
// Skipped unless Docker answers and alpine:3.20 is already present locally:
// a unit test must not pull images.
func TestACPOverRealDocker(t *testing.T) {
	const image = "alpine:3.20"
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		t.Skipf("%s not present locally (docker pull %s to run this test)", image, image)
	}
	e := NewACPExecutor(ACPOptions{Image: image, Command: []string{"sh", "-c", shellACPAgent}}, AgentProvider{Name: "ollama"})
	// The bind mount must be a path Docker Desktop shares; t.TempDir under
	// /var/folders is, on macOS.
	work := filepath.Join(t.TempDir(), "work", "ini")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	att, err := e.Start(ctx, Request{Brief: "x", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	var st Status
	for {
		st, err = e.Status(ctx, att.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out")
		case <-time.After(100 * time.Millisecond):
		}
	}
	tail := e.OutputTail(ctx, att.ID, 100)
	if st.Status != "succeeded" {
		t.Fatalf("status = %+v\n%s", st, tail)
	}
	for name, want := range map[string]string{"via-client.txt": "client", "via-mount.txt": "direct\n"} {
		got, err := os.ReadFile(filepath.Join(work, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q\n%s", name, got, err, want, tail)
		}
	}
	if _, err := e.Collect(ctx, att.ID); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "name="+agentContainerName(att.ID), "--format", "{{.ID}}").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the agent container is still present after Collect: %s", out)
	}

	// The setup-time probe: same image, --network none, initialize only.
	name, err := ProbeACP(ctx, image, []string{"sh", "-c", shellACPAgent})
	if err != nil || name != "sh-acp 0" {
		t.Fatalf("ProbeACP = %q, %v", name, err)
	}
	if _, err := ProbeACP(ctx, image, []string{"sh", "-c", "echo not an agent >&2; exit 1"}); err == nil || !strings.Contains(err.Error(), "not an agent") {
		t.Fatalf("ProbeACP of a non-agent = %v, want an error carrying its output", err)
	}
}
