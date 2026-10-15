package executor

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// openHandsImage is the pinned Agent Server image this client targets.
const openHandsImage = "ghcr.io/openhands/agent-server:" + OpenHandsAPIVersion + "-python"

// TestOpenHandsAgainstRealAgentServer runs the executor against the real
// Agent Server image, without any LLM key: the server starts and accepts
// conversations without one, and the agent's first model call then fails. That
// is enough to check, against the real server rather than the fake, every part
// docs/agents.md lists as assumed or confirmed only from source: the session
// key header and websocket auth, the upload + `tar -xzf` unpack (tar present in
// the image), idempotent create with a client-chosen id, the event search
// paging from a cursor, the run settling on a terminal status, and the archive
// route's tarball layout on the way back.
//
// Skipped unless Docker answers and the image is already present locally (a
// unit test must not pull a multi-gigabyte image): docker pull
// ghcr.io/openhands/agent-server:1.54.0-python to run it.
func TestOpenHandsAgainstRealAgentServer(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if exec.Command("docker", "image", "inspect", openHandsImage).Run() != nil {
		t.Skipf("%s not present locally (docker pull %s to run this test)", openHandsImage, openHandsImage)
	}
	const key = "real-server-session-key-0123"
	name := "idea-oh-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	_ = exec.Command("docker", "rm", "-f", name).Run()
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::8000",
		"-e", "OH_SESSION_API_KEYS_0="+key, openHandsImage, "--host", "0.0.0.0", "--port", "8000").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	portOut, err := exec.Command("docker", "port", name, "8000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	hostPort := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	base := "http://" + hostPort
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, err := http.Get(base + "/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("server never became ready:\n%s", logs)
		}
		time.Sleep(time.Second)
	}

	ctx := context.Background()
	if v, err := ProbeOpenHands(ctx, base, key); err != nil || v == "" {
		t.Fatalf("probe = %q, %v", v, err)
	}
	if _, err := ProbeOpenHands(ctx, base, "wrong-key-000000"); err == nil {
		t.Fatal("the real server accepted a wrong session key")
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sup := NewRemoteSupervisor(nil)
	sup.SetPollIntervalForTest(500 * time.Millisecond)
	defer sup.Detach()
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644)

	ex := NewOpenHandsExecutor(OpenHandsOptions{URL: base, Model: "openai/gpt-4o-mini", Timeout: 3 * time.Minute, MaxIterations: 3},
		AgentProvider{Name: "openai", Type: config.ProviderOpenAI, KeyEnv: "OPENAI_API_KEY"}, db, sup)
	ex.SetKeyLookup(func(n string) string {
		return map[string]string{"OPENHANDS_API_KEY": key, "OPENAI_API_KEY": "sk-not-a-real-key-for-the-docker-test"}[n]
	})
	req := Request{TaskID: "docker-task", Brief: "add a line to README.md", WorkRoot: work}
	att, err := ex.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// Idempotent re-dispatch against the real server: the same run back.
	again, err := NewOpenHandsExecutor(ex.opts, ex.provider, db, sup).withLookup(ex.lookupKey).Start(ctx, req)
	if err != nil || again.ID != att.ID {
		t.Fatalf("re-dispatch = %v, %v; want %s", again, err, att.ID)
	}

	var rec *domain.RemoteRun
	deadline = time.Now().Add(3 * time.Minute)
	for {
		rec, err = db.GetRemoteRun(ctx, att.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status.Terminal() || rec.Status == domain.RemoteRunFinished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never settled: %+v", rec)
		}
		time.Sleep(time.Second)
	}
	t.Logf("real server settled the run as %s (%s); %d events; progress:\n%s", rec.Status, rec.Error, rec.EventCount, rec.Progress)
	if rec.EventCount == 0 {
		t.Fatal("no events were ingested from the real server")
	}
	if strings.Contains(rec.Error, "no longer exists") {
		t.Fatalf("the real server's conversation was reported vanished: %s", rec.Error)
	}

	// The way back: the archive route's real tarball layout mirrors cleanly,
	// and an unchanged tree changes nothing locally.
	c, _ := newOHClient(base, key)
	data, err := c.archive(ctx, rec.RemoteDir)
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := packWorkRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	res, err := mirrorInto(ctx, c, data, work, rec.RemoteDir, m)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 || res.Written < 2 {
		t.Fatalf("mirror from the real archive = %+v (skipped %v)", res, res.Skipped)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "src", "main.go")); string(b) != "package main\n" {
		t.Fatalf("src/main.go after the round trip = %q", b)
	}
	// The real route appends archive_manifest.json to every tarball; it must
	// not land in the work root as an "added" file.
	if _, err := os.Stat(filepath.Join(work, archiveManifestName)); err == nil {
		t.Fatal("the real server's archive_manifest.json was mirrored into the work root")
	}
}

// withLookup is SetKeyLookup returning the executor, for a one-liner above.
func (e *OpenHandsExecutor) withLookup(l func(string) string) *OpenHandsExecutor {
	e.SetKeyLookup(l)
	return e
}
