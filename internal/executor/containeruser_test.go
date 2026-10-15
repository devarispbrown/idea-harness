package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// chownLog records the chowns a faked-root daemon would have made.
type chownLog struct {
	mu    sync.Mutex
	calls map[string][2]int // last owner set per path
	order []string
}

func (c *chownLog) owner(path string) ([2]int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.calls[path]
	return o, ok
}

// fakeRootDaemon makes this package believe it runs as root, and records
// chowns instead of making them (the test process cannot chown to 65532).
func fakeRootDaemon(t *testing.T) *chownLog {
	t.Helper()
	log := &chownLog{calls: map[string][2]int{}}
	prevEUID, prevChown := daemonEUID, lchown
	daemonEUID = func() int { return 0 }
	lchown = func(path string, uid, gid int) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.calls[path] = [2]int{uid, gid}
		log.order = append(log.order, path)
		return nil
	}
	t.Cleanup(func() { daemonEUID, lchown = prevEUID, prevChown })
	return log
}

const unprivileged = "65532:65532"

// fakeOwners makes ownerOf report these owners; any other path is missing.
func fakeOwners(t *testing.T, owners map[string][2]int) {
	t.Helper()
	prev := ownerOf
	ownerOf = func(path string) (int, int, error) {
		if o, ok := owners[path]; ok {
			return o[0], o[1], nil
		}
		return 0, 0, os.ErrNotExist
	}
	t.Cleanup(func() { ownerOf = prev })
}

func TestContainerUser(t *testing.T) {
	fakeOwners(t, map[string][2]int{
		"/home/ann/proj": {1000, 1000},
		"/srv/rootproj":  {0, 0},
		"/srv/wheel":     {1001, 0},
		"/srv/rootuser":  {0, 1002},
	})
	if os.Geteuid() != 0 {
		own := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
		for _, root := range []string{"", "/home/ann/proj", "/srv/rootproj"} {
			if got := containerUser(root); got != own {
				t.Fatalf("a non-root daemon, project %q: --user %s, want its own %s (unchanged)", root, got, own)
			}
		}
	}
	fakeRootDaemon(t)
	for root, want := range map[string]string{
		"":               unprivileged, // a probe: no project
		"/home/ann/proj": "1000:1000",  // the project's owner
		"/srv/rootproj":  unprivileged, // owned by root: never root
		"/srv/rootuser":  unprivileged,
		"/srv/wheel":     "1001:65532", // never the root group
		"/missing":       unprivileged, // owner unknown
	} {
		if got := containerUser(root); got != want {
			t.Errorf("a root daemon, project %q: --user %s, want %s", root, got, want)
		}
	}
}

// TestRootDaemonContainersRunAsProjectOwner: every site that works for a
// project passes the project owner's uid:gid under a root daemon, and hands
// the writable mounts to that owner rather than to 65532.
func TestRootDaemonContainersRunAsProjectOwner(t *testing.T) {
	const owner = "1000:1000"
	proj := t.TempDir()
	fakeOwners(t, map[string][2]int{proj: {1000, 1000}})
	log := fakeRootDaemon(t)

	e := &SandboxedAgentExecutor{name: "hermes", image: "img", dockerPath: "docker"}
	if args := e.containerArgs(Request{WorkRoot: "/tmp/work", ProjectRoot: proj}, "a", "", "/tmp/home"); !hasFlagValue(args, "--user", owner) {
		t.Fatalf("hermes argv: %v", args)
	}
	acp := NewACPExecutor(ACPOptions{Image: "img", Command: []string{"agent"}}, AgentProvider{Name: "ollama"})
	if args := acp.containerArgs(acpLaunch{AttemptID: "a", WorkRoot: "/tmp/work", User: owner, Argv: []string{"agent"}}); !hasFlagValue(args, "--user", owner) {
		t.Fatalf("acp argv: %v", args)
	}
	sb := NewContainerSandbox("img")
	if args := sb.isolatedArgs(RunOptions{WorkRoot: "/tmp/work", RepoRoot: proj}); !hasFlagValue(args, "--user", owner) {
		t.Fatalf("check argv (RepoRoot): %v", args)
	}
	if args := sb.isolatedArgs(RunOptions{WorkRoot: "/tmp/work", ProjectRoot: proj}); !hasFlagValue(args, "--user", owner) {
		t.Fatalf("check argv (ProjectRoot, no /repo): %v", args)
	}

	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	bin := writeExecutable(t, "docker", `#!/bin/sh
echo "$*" >> "$FAKE_DOCKER_RECORD"
case "$1" in
  run) echo cid-1 ;;
  wait) echo 0 ;;
esac
exit 0
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	work := t.TempDir()
	sb.dockerPath = bin
	if _, _, _, err := sb.Run(ctx, RunOptions{WorkRoot: work, RepoRoot: proj, Cmd: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	// Granted to the owner for the run (then restored to the daemon).
	granted := false
	log.mu.Lock()
	for _, p := range log.order {
		if p == work {
			granted = true
		}
	}
	log.mu.Unlock()
	if !granted {
		t.Fatal("the sandbox did not hand its work root over")
	}
	ce := NewContainerExecutor(nil, "img")
	ce.dockerPath = bin
	cwork := t.TempDir()
	if _, err := ce.Start(ctx, Request{WorkRoot: cwork, ProjectRoot: proj, TaskTitle: "t"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := log.owner(cwork); o != [2]int{1000, 1000} {
		t.Fatalf("container executor handed its work root to %v, want the project owner", o)
	}
	for _, c := range dockerCalls(t, record) {
		if strings.HasPrefix(c, "run ") && !strings.Contains(c, "--user "+owner) {
			t.Errorf("a docker run for a project owned by 1000 without --user %s:\n%s", owner, c)
		}
	}
}

// TestRootDaemonContainersAreUnprivileged is the argv half of audit 3 / S6:
// every container start site passes --user 65532:65532 under a root daemon.
func TestRootDaemonContainersAreUnprivileged(t *testing.T) {
	fakeRootDaemon(t)

	t.Run("hermes/omp agent", func(t *testing.T) {
		e := &SandboxedAgentExecutor{name: "hermes", image: "img", dockerPath: "docker"}
		if args := e.containerArgs(Request{WorkRoot: "/tmp/work"}, "a", "", "/tmp/home"); !hasFlagValue(args, "--user", unprivileged) {
			t.Fatalf("argv: %v", args)
		}
	})
	t.Run("acp agent", func(t *testing.T) {
		e := NewACPExecutor(ACPOptions{Image: "img", Command: []string{"agent"}}, AgentProvider{Name: "ollama"})
		if args := e.containerArgs(acpLaunch{AttemptID: "a", WorkRoot: "/tmp/work", Argv: []string{"agent"}}); !hasFlagValue(args, "--user", unprivileged) {
			t.Fatalf("argv: %v", args)
		}
	})
	t.Run("check sandbox", func(t *testing.T) {
		if args := NewContainerSandbox("img").isolatedArgs(RunOptions{WorkRoot: "/tmp/work"}); !hasFlagValue(args, "--user", unprivileged) {
			t.Fatalf("argv: %v", args)
		}
	})

	// The sites that call docker themselves: record their argv through a fake
	// CLI on PATH (or dockerPath).
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	bin := writeExecutable(t, "docker", `#!/bin/sh
echo "$*" >> "$FAKE_DOCKER_RECORD"
case "$1" in
  run) echo cid-1 ;;
  wait) echo 0 ;;
esac
exit 0
`)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := VerifyAgentCLI(ctx, "img", "omp"); err != nil {
		t.Fatalf("VerifyAgentCLI: %v", err)
	}
	_, _ = ProbeACP(ctx, "img", []string{"agent"}) // the fake is no agent; only its argv matters

	ce := NewContainerExecutor(nil, "img")
	ce.dockerPath = bin
	if _, err := ce.Start(ctx, Request{WorkRoot: t.TempDir(), TaskTitle: "t"}); err != nil {
		t.Fatal(err)
	}
	sb := NewContainerSandbox("img")
	sb.dockerPath = bin
	if _, _, _, err := sb.Run(ctx, RunOptions{WorkRoot: t.TempDir(), Cmd: []string{"true"}}); err != nil {
		t.Fatal(err)
	}

	runs := 0
	for _, c := range dockerCalls(t, record) {
		if !strings.HasPrefix(c, "run ") {
			continue
		}
		runs++
		if !strings.Contains(c, "--user "+unprivileged) {
			t.Errorf("a docker run without --user %s under a root daemon:\n%s", unprivileged, c)
		}
	}
	if runs != 4 {
		t.Fatalf("recorded %d docker runs, want 4 (verify, probe, container, sandbox)", runs)
	}
}

// TestGrantMountsAndRestore: under a root daemon the writable mounts are handed
// to 65532 before the run, and afterwards every path goes back to the owner
// it had -- anything the container created to the daemon. Symlinks are
// chowned as links and never followed.
func TestGrantMountsAndRestore(t *testing.T) {
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-secret")
	os.WriteFile(outside, []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(work, "src"), 0o755)
	os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main"), 0o644)
	os.Symlink(outside, filepath.Join(work, "link"))

	log := fakeRootDaemon(t)
	g, err := grantMounts(unprivilegedIdentity, work, "", filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{work, filepath.Join(work, "src"), filepath.Join(work, "src", "main.go"), filepath.Join(work, "link")} {
		if o, ok := log.owner(p); !ok || o != [2]int{ContainerUID, ContainerGID} {
			t.Errorf("%s not handed to the container user: %v %v", p, o, ok)
		}
	}
	if _, ok := log.owner(outside); ok {
		t.Fatal("a symlink was followed out of the work root")
	}

	// The "container" writes a new file.
	created := filepath.Join(work, "src", "new.go")
	os.WriteFile(created, []byte("x"), 0o644)

	if err := g.Restore(); err != nil {
		t.Fatal(err)
	}
	me := [2]int{os.Getuid(), os.Getgid()}
	if o, _ := log.owner(created); o != me {
		t.Errorf("a file the container created goes to the daemon: %v, want %v", o, me)
	}
	if o, _ := log.owner(filepath.Join(work, "src", "main.go")); o != me {
		// Created by this test, i.e. by the daemon's own uid.
		t.Errorf("a pre-existing file goes back to its owner: %v, want %v", o, me)
	}
	if _, ok := log.owner(outside); ok {
		t.Fatal("restore followed a symlink out of the work root")
	}
	if err := g.Restore(); err != nil {
		t.Fatalf("a second Restore must be a no-op: %v", err)
	}
	var nilGrant *mountGrant
	if err := nilGrant.Restore(); err != nil {
		t.Fatal(err)
	}
}

func TestNonRootDaemonChownsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	called := false
	prev := lchown
	lchown = func(string, int, int) error { called = true; return nil }
	t.Cleanup(func() { lchown = prev })
	g, err := grantMounts(unprivilegedIdentity, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = g.Restore()
	_ = g.handOver("/w", "/w/a/b")
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "projects", "p", "work", "ini"), 0o755)
	os.MkdirAll(filepath.Join(state, "projects", "p", "quality-1"), 0o755)
	if n, err := RecoverWorkRoots(state); n != 0 || err != nil {
		t.Fatalf("RecoverWorkRoots on a non-root daemon: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(state, "projects", "p", "quality-1")); err != nil {
		t.Fatal("a non-root daemon removed a quality copy")
	}
	if called {
		t.Fatal("a non-root daemon chowned something")
	}
}

func TestHandOverStaysInsideTheRoot(t *testing.T) {
	log := fakeRootDaemon(t)
	g := &mountGrant{to: identity{1000, 1000}}
	if err := g.handOver("/w", "/w/a/b/c.txt"); err != nil {
		t.Fatal(err)
	}
	for p, o := range log.calls {
		if o != [2]int{1000, 1000} {
			t.Errorf("%s handed to %v, want the grant's user", p, o)
		}
	}
	var nilGrant *mountGrant
	if err := nilGrant.handOver("/w", "/w/x"); err != nil || log.calls["/w/x"] != [2]int{} {
		t.Fatal("a nil grant handed something over")
	}
	var got []string
	for p := range log.calls {
		got = append(got, p)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "/w/a,/w/a/b,/w/a/b/c.txt" {
		t.Fatalf("handOver chowned %v; want the file and its parents below the root only", got)
	}
}

// TestRecoverWorkRoots: a root daemon starting after one was killed mid-run
// hands every foreign-owned path in a work root back to itself, removes the
// disposable quality copies, and touches nothing else: not agent homes, not a
// symlinked root, never a symlink's target.
func TestRecoverWorkRoots(t *testing.T) {
	state := t.TempDir()
	proj := filepath.Join(state, "projects", "p")
	work := filepath.Join(proj, "work", "ini")
	os.MkdirAll(filepath.Join(work, "src"), 0o755)
	os.WriteFile(filepath.Join(work, "src", "a.go"), []byte("x"), 0o644)
	outside := filepath.Join(t.TempDir(), "host-file")
	os.WriteFile(outside, []byte("x"), 0o600)
	os.Symlink(outside, filepath.Join(work, "link"))
	home := filepath.Join(proj, "work", agentHomePrefix+"A1")
	os.MkdirAll(home, 0o700)
	elsewhere := t.TempDir()
	os.Symlink(elsewhere, filepath.Join(proj, "work", "linked-root"))
	quality := filepath.Join(proj, "quality-123")
	os.MkdirAll(filepath.Join(quality, "d"), 0o755)
	os.WriteFile(filepath.Join(quality, "d", "f"), []byte("x"), 0o644)
	metric := filepath.Join(proj, "metric-456")
	os.MkdirAll(metric, 0o755)
	notOurs := filepath.Join(state, "elsewhere", "work", "ini")
	os.MkdirAll(notOurs, 0o755)

	log := fakeRootDaemon(t)
	// Every file here belongs to the test user; a daemon that is someone
	// else sees all of them as left behind by a container.
	prevIDs := daemonIDs
	daemonIDs = func() (int, int) { return 4242, 4242 }
	t.Cleanup(func() { daemonIDs = prevIDs })

	n, err := RecoverWorkRoots(state)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("recovered %d roots, want 3 (the work root, the quality copy, the metric root)", n)
	}
	for _, p := range []string{work, filepath.Join(work, "src"), filepath.Join(work, "src", "a.go"), filepath.Join(work, "link")} {
		if o, ok := log.owner(p); !ok || o != [2]int{4242, 4242} {
			t.Errorf("%s not handed back to the daemon: %v %v", p, o, ok)
		}
	}
	for _, p := range []string{outside, home, elsewhere, filepath.Join(proj, "work", "linked-root"), notOurs, proj, filepath.Join(proj, "work")} {
		if _, ok := log.owner(p); ok {
			t.Errorf("%s was chowned; only work roots are", p)
		}
	}
	if _, err := os.Stat(quality); !os.IsNotExist(err) {
		t.Errorf("the quality copy was not removed: %v", err)
	}
	if _, err := os.Stat(metric); !os.IsNotExist(err) {
		t.Errorf("the metric root was not removed: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("an agent home was removed (that is the sweep's job): %v", err)
	}

	// Already the daemon's: nothing to do.
	daemonIDs = prevIDs
	log.mu.Lock()
	log.calls = map[string][2]int{}
	log.mu.Unlock()
	if n, err := RecoverWorkRoots(state); n != 0 || err != nil || len(log.calls) != 0 {
		t.Fatalf("a clean state root: %d %v, chowned %v", n, err, log.calls)
	}
}

// ---------- as real root ----------

// TestRootOwnerIdentityForReal: as real root, a project owned by an ordinary
// user gives that user's uid:gid, and a root-owned one gives 65532.
func TestRootOwnerIdentityForReal(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs a root daemon (run as root, e.g. in a Linux container)")
	}
	proj := t.TempDir()
	if got := containerUser(proj); got != unprivileged {
		t.Fatalf("a root-owned project: --user %s, want %s", got, unprivileged)
	}
	if err := os.Chown(proj, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if got := containerUser(proj); got != "1000:1000" {
		t.Fatalf("a project owned by 1000: --user %s, want 1000:1000", got)
	}
}

// TestRootRecoverWorkRootsForReal: files a container left owned by 65532
// (the daemon was killed before Restore) are the daemon's again after
// RecoverWorkRoots, even inside a directory the container locked with chmod 0.
func TestRootRecoverWorkRootsForReal(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs a root daemon (run as root, e.g. in a Linux container)")
	}
	state := t.TempDir()
	work := filepath.Join(state, "projects", "p", "work", "ini")
	os.MkdirAll(filepath.Join(work, "locked"), 0o755)
	os.WriteFile(filepath.Join(work, "f"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(work, "locked", "g"), []byte("x"), 0o600)
	_ = filepath.Walk(work, func(p string, _ os.FileInfo, _ error) error {
		return os.Lchown(p, ContainerUID, ContainerGID)
	})
	os.Chmod(filepath.Join(work, "locked"), 0)
	if _, err := RecoverWorkRoots(state); err != nil {
		t.Fatal(err)
	}
	assertOwnedByDaemon(t, work)
}

// TestRootRemapOwnershipForReal runs only as root (CI in a Linux container,
// `sudo go test`): it hands a work root to 65532, writes into it AS 65532 --
// a new file, a new directory, an edit, and a directory locked with chmod 0 --
// and checks that afterwards everything belongs to the daemon again.
func TestRootRemapOwnershipForReal(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs a root daemon (run as root, e.g. in a Linux container)")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	// A container reaches its mount through the bind mount; this process
	// reaches it through the path, so the parents must be traversable.
	work := filepath.Join(t.TempDir(), "work")
	os.Mkdir(work, 0o755)
	for p := filepath.Dir(work); strings.HasPrefix(p, os.TempDir()) && p != filepath.Dir(p); p = filepath.Dir(p) {
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o001 == 0 {
			os.Chmod(p, fi.Mode().Perm()|0o011)
		}
	}
	os.WriteFile(filepath.Join(work, "seed.txt"), []byte("seed\n"), 0o644)
	os.Mkdir(filepath.Join(work, "private"), 0o700)
	g, err := grantMounts(unprivilegedIdentity, work)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", `set -e
echo more >> seed.txt
echo new > new.txt
mkdir -p made/deeper && echo x > made/deeper/f
echo p > private/p
mkdir locked && echo s > locked/s && chmod 0 locked`)
	cmd.Dir = work
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: ContainerUID, Gid: ContainerGID}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writing as %d: %v\n%s", ContainerUID, err, out)
	}
	if err := g.Restore(); err != nil {
		t.Fatal(err)
	}
	assertOwnedByDaemon(t, work)
	if b, err := os.ReadFile(filepath.Join(work, "seed.txt")); err != nil || string(b) != "seed\nmore\n" {
		t.Fatalf("seed.txt = %q, %v", b, err)
	}
}

// TestRootDaemonSandboxOverRealDocker is the same through a real container:
// ContainerSandbox.Run as a root daemon, the check writing into /work as
// 65532, and every file usable by the daemon afterwards. Root and Docker and
// alpine:3.20 already present, or skipped (a unit test must not pull).
func TestRootDaemonSandboxOverRealDocker(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs a root daemon")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if exec.Command("docker", "image", "inspect", "alpine:3.20").Run() != nil {
		t.Skip("alpine:3.20 not present locally")
	}
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "seed.txt"), []byte("seed\n"), 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, _, code, err := NewContainerSandbox("alpine:3.20").Run(ctx, RunOptions{WorkRoot: work,
		Cmd: []string{"sh", "-c", `id -u; echo more >> /work/seed.txt && mkdir -p /work/out && echo built > /work/out/bin && chmod 0 /work/out`}})
	if err != nil || code != 0 {
		t.Fatalf("run: %d %v\n%s", code, err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), fmt.Sprint(ContainerUID)) {
		t.Fatalf("the check ran as %q, want uid %d", strings.TrimSpace(out), ContainerUID)
	}
	assertOwnedByDaemon(t, work)
	if b, err := os.ReadFile(filepath.Join(work, "out", "bin")); err != nil || string(b) != "built\n" {
		t.Fatalf("out/bin = %q, %v", b, err)
	}
}

// TestRootDaemonCheckReadsOwnerOnlyRepoFilesOverRealDocker: as a root daemon,
// a check for a project owned by an ordinary user runs as that user, so a
// 0600 file of theirs in the read-only /repo mount is readable, and the work
// root is the daemon's again afterwards. Root, Docker and alpine:3.20 present,
// or skipped.
func TestRootDaemonCheckReadsOwnerOnlyRepoFilesOverRealDocker(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs a root daemon")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if exec.Command("docker", "image", "inspect", "alpine:3.20").Run() != nil {
		t.Skip("alpine:3.20 not present locally")
	}
	proj := t.TempDir()
	secret := filepath.Join(proj, "private.env.example")
	os.WriteFile(secret, []byte("owner-only\n"), 0o600)
	for _, p := range []string{proj, secret} {
		if err := os.Chown(p, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, stderr, code, err := NewContainerSandbox("alpine:3.20").Run(ctx, RunOptions{WorkRoot: work, RepoRoot: proj,
		Cmd: []string{"sh", "-c", `id -u; cat /repo/private.env.example; echo built > /work/out`}})
	if err != nil || code != 0 {
		t.Fatalf("run: %d %v\n%s\n%s", code, err, out, stderr)
	}
	if got := strings.Fields(out); len(got) < 2 || got[0] != "1000" || got[1] != "owner-only" {
		t.Fatalf("the check printed %q, want uid 1000 reading the 0600 file", out)
	}
	assertOwnedByDaemon(t, work)
}

func assertOwnedByDaemon(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		st := info.Sys().(*syscall.Stat_t)
		if int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
			t.Errorf("%s is owned by %d:%d after the run, want the daemon's %d:%d", path, st.Uid, st.Gid, os.Getuid(), os.Getgid())
		}
		return nil
	})
}
