package fsdiff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSnapshotAlwaysExcludesDefaultIgnoredDirs is a regression test for
// review defect #2: an agent that downloads a toolchain into e.g. .tools/
// must never have those files enter a snapshot (and from there a diff, the
// secret sweep, and the diff endpoint), even with no options and no
// .gitignore at all.
func TestSnapshotAlwaysExcludesDefaultIgnoredDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n")
	for i := 0; i < 50; i++ {
		writeFile(t, root, fmt.Sprintf(".tools/go/pkg/file%d.txt", i), "binary-ish content")
	}
	writeFile(t, root, "node_modules/left-pad/index.js", "module.exports = 1")

	m, err := Snapshot(root)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(m) != 1 || m[0].Path != "main.go" {
		t.Fatalf("Snapshot() = %+v, want only main.go", m)
	}
}

// TestSnapshotWorkspaceIgnoreGit is a regression test for review defect #2's
// .gitignore requirement: a path the WORKSPACE's own .gitignore excludes
// must not appear in a snapshot of a (gitignore-less) work-root copy, when
// the workspace is a real git repository.
func TestSnapshotWorkspaceIgnoreGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	workspace := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	writeFile(t, workspace, ".gitignore", "generated_sdk/\n*.log\n")
	writeFile(t, workspace, "main.go", "package main\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	// The work root is a plain copy (no .git of its own), the way the real
	// seeded work root is -- yet the workspace's .gitignore must still apply.
	workRoot := t.TempDir()
	writeFile(t, workRoot, "main.go", "package main\n")
	writeFile(t, workRoot, "generated_sdk/client.go", "// downloaded, not authored")
	writeFile(t, workRoot, "debug.log", "noise")

	m, err := Snapshot(workRoot, WithWorkspaceIgnore(workspace))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(m) != 1 || m[0].Path != "main.go" {
		t.Fatalf("Snapshot() = %+v, want only main.go (generated_sdk/ and *.log are gitignored)", m)
	}
}

// TestSnapshotWorkspaceIgnoreGitIgnoredWorkspace is a regression test for the
// smoke/agent scratch-dir bug: when the workspace sits inside a gitignored
// directory of its own parent repo (e.g. the harness's .smoke/), git
// check-ignore reports every file the workspace holds as ignored, which
// silently emptied the after-snapshot and therefore the diff. A workspace
// that is itself ignored has no meaningful project .gitignore, so the git
// path must be skipped and its files must survive the snapshot.
func TestSnapshotWorkspaceIgnoreGitIgnoredWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	writeFile(t, repo, ".gitignore", "/.smoke/\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	// The workspace is a scratch dir inside the repo's gitignored .smoke/.
	workspace := filepath.Join(repo, ".smoke", "123", "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	workRoot := t.TempDir()
	writeFile(t, workRoot, "hello.txt", "hello\n")

	m, err := Snapshot(workRoot, WithWorkspaceIgnore(workspace))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(m) != 1 || m[0].Path != "hello.txt" {
		t.Fatalf("Snapshot() = %+v, want hello.txt kept (workspace is gitignored, not a real project)", m)
	}
}

// TestSnapshotWorkspaceIgnoreFallback exercises the non-git fallback matcher
// (WithWorkspaceIgnore against a workspace that is not a git repository).
func TestSnapshotWorkspaceIgnoreFallback(t *testing.T) {
	workspace := t.TempDir()
	writeFile(t, workspace, ".gitignore", "*.tmp\n/build/\n")

	workRoot := t.TempDir()
	writeFile(t, workRoot, "main.go", "package main\n")
	writeFile(t, workRoot, "scratch.tmp", "noise")
	writeFile(t, workRoot, "build/out.bin", "binary")

	m, err := Snapshot(workRoot, WithWorkspaceIgnore(workspace))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(m) != 1 || m[0].Path != "main.go" {
		t.Fatalf("Snapshot() = %+v, want only main.go", m)
	}
}

// TestSnapshotSanityCapFileCount is a regression test for the "diff endpoint
// didn't respond in 100s" half of review defect #2: once a work root holds
// more files than the configured cap, Snapshot fails clearly instead of
// letting the caller collect, sweep, and render an unbounded tree.
func TestSnapshotSanityCapFileCount(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFile(t, root, fmt.Sprintf("f%d.txt", i), "x")
	}

	_, err := Snapshot(root, WithSanityCap(10, 0))
	if err == nil {
		t.Fatal("Snapshot() with a 10-file cap over 20 files = nil error, want SnapshotTooLargeError")
	}
	var tooLarge *SnapshotTooLargeError
	if !asSnapshotTooLarge(err, &tooLarge) {
		t.Fatalf("Snapshot() error = %v (%T), want *SnapshotTooLargeError", err, err)
	}
	if tooLarge.FileCount != 20 {
		t.Errorf("FileCount = %d, want 20", tooLarge.FileCount)
	}
	if len(tooLarge.Examples) == 0 {
		t.Error("Examples is empty, want at least one example path")
	}
	t.Logf("error message: %s", err.Error())

	// Under the cap, it still works.
	if _, err := Snapshot(root, WithSanityCap(100, 0)); err != nil {
		t.Fatalf("Snapshot() under the cap: %v", err)
	}
}

// TestSnapshotSanityCapTotalBytes exercises the byte-based bound.
func TestSnapshotSanityCapTotalBytes(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, 1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Snapshot(root, WithSanityCap(0, 512))
	if err == nil {
		t.Fatal("Snapshot() over the byte cap = nil error, want SnapshotTooLargeError")
	}
	var tooLarge *SnapshotTooLargeError
	if !asSnapshotTooLarge(err, &tooLarge) {
		t.Fatalf("Snapshot() error = %v (%T), want *SnapshotTooLargeError", err, err)
	}
	if tooLarge.TotalBytes != 1024 {
		t.Errorf("TotalBytes = %d, want 1024", tooLarge.TotalBytes)
	}
}

// TestSnapshotWorkspaceIgnoreKeepsTrackedFile is the regression test for C1: a
// file that is TRACKED in the workspace but matches a .gitignore pattern
// (force-added, or added before the pattern existed) must NOT be reported as
// ignored by the after-run snapshot. seedWorkRoot's `git ls-files -co
// --exclude-standard` always includes tracked files (git never excludes a
// tracked path via --exclude-standard), so if the after-snapshot's ignore
// check disagreed and excluded it, fsdiff.Diff would read the asymmetry as a
// deletion of a file the agent never touched -- silently dropping (and, with
// git_commit on, committing the removal of) the user's tracked file.
func TestSnapshotWorkspaceIgnoreKeepsTrackedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	workspace := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	// Tracked BEFORE the ignore pattern existed (the common real-world shape:
	// config/local.yaml was committed, then *.log and config/*.yaml were
	// added to .gitignore later without ever untracking it).
	writeFile(t, workspace, "config/local.yaml", "x: 1\n")
	writeFile(t, workspace, "main.go", "package main\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init before gitignore")
	writeFile(t, workspace, ".gitignore", "config/*.yaml\n*.log\n")
	run("add", ".gitignore")
	run("commit", "-q", "-m", "add gitignore")

	// The work root mirrors what seedWorkRoot actually copies: `git ls-files
	// -co --exclude-standard` includes config/local.yaml because it is
	// tracked, regardless of the new pattern.
	workRoot := t.TempDir()
	writeFile(t, workRoot, "main.go", "package main\n")
	writeFile(t, workRoot, "config/local.yaml", "x: 1\n") // untouched by the agent
	writeFile(t, workRoot, "debug.log", "noise")           // untracked and ignored: still excluded

	m, err := Snapshot(workRoot, WithWorkspaceIgnore(workspace))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	paths := map[string]bool{}
	for _, f := range m {
		paths[f.Path] = true
	}
	if !paths["config/local.yaml"] {
		t.Error("a tracked file matching .gitignore was excluded from the snapshot -- this reads as a phantom delete (C1)")
	}
	if !paths["main.go"] {
		t.Error("main.go missing from snapshot")
	}
	if paths["debug.log"] {
		t.Error("an untracked, genuinely ignored file (debug.log) was NOT excluded")
	}
}

func asSnapshotTooLarge(err error, target **SnapshotTooLargeError) bool {
	if e, ok := err.(*SnapshotTooLargeError); ok {
		*target = e
		return true
	}
	return false
}
