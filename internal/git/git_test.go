package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// initRepo returns a fresh git repository with identity configured.
func initRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q")
	gitOut(t, dir, "config", "user.name", "test")
	gitOut(t, dir, "config", "user.email", "t@example.com")
	return dir
}

// writeFakeGit writes a fake git binary that records its argv and environment to
// the given files (baked in, since the sanitized env drops caller env vars) and
// answers each subcommand Commit invokes. `rev-parse HEAD` returns "abc123fake".
func writeFakeGit(t *testing.T, argsPath, envPath string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" >> '%s'
{ echo '---env---'; env | sort; } >> '%s'
while [ "$#" -gt 0 ] && [ "$1" = "-c" ]; do shift 2; done
sub="${1:-}"
case "$sub" in
  add) exit 0 ;;
  diff) exit 1 ;;
  commit) exit 0 ;;
  rev-parse) echo "abc123fake" ;;
  *) exit 1 ;;
esac
`, argsPath, envPath)
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return path
}

func TestCommitNonRepoNoOp(t *testing.T) {
	sha, err := Commit(t.Context(), t.TempDir(), []string{"a.txt"}, "msg")
	if err != nil {
		t.Fatalf("Commit() error = %v, want nil (non-repo no-op)", err)
	}
	if sha != "" {
		t.Fatalf("Commit() sha = %q, want empty", sha)
	}
}

func TestCommitEmptyPathsNoOp(t *testing.T) {
	sha, err := Commit(t.Context(), t.TempDir(), nil, "msg")
	if err != nil {
		t.Fatalf("Commit() error = %v, want nil", err)
	}
	if sha != "" {
		t.Fatalf("Commit() sha = %q, want empty", sha)
	}
}

func TestCommitScopesPathsAndReturnsSHA(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "base-a")
	writeFile(t, repo, "c.txt", "base-c")
	gitOut(t, repo, "add", "a.txt", "c.txt")
	gitOut(t, repo, "commit", "-q", "-m", "base")

	// a.txt (tracked, modified) and b.txt (new) are in scope; c.txt is modified
	// but NOT in scope and must be left out of the commit.
	writeFile(t, repo, "a.txt", "changed-a")
	writeFile(t, repo, "b.txt", "new-b")
	writeFile(t, repo, "c.txt", "changed-c")

	sha, err := Commit(t.Context(), repo, []string{"a.txt", "b.txt"}, "CHG-CHG-1: scope test")
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if want := gitOut(t, repo, "rev-parse", "HEAD"); sha != want {
		t.Fatalf("Commit() sha = %q, want HEAD %q", sha, want)
	}

	committed := map[string]bool{}
	for _, line := range strings.Split(gitOut(t, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			committed[p] = true
		}
	}
	want := map[string]bool{"a.txt": true, "b.txt": true}
	if len(committed) != len(want) {
		t.Fatalf("committed files = %v, want %v", committed, want)
	}
	for p := range want {
		if !committed[p] {
			t.Fatalf("committed files = %v, missing %q", committed, p)
		}
	}
	if committed["c.txt"] {
		t.Fatalf("c.txt was committed but was not in scope")
	}

	// c.txt's modification must remain in the working tree (not committed), and
	// the committed c.txt must still be the base content.
	data, err := os.ReadFile(filepath.Join(repo, "c.txt"))
	if err != nil {
		t.Fatalf("read c.txt: %v", err)
	}
	if string(data) != "changed-c" {
		t.Fatalf("c.txt = %q, want changed-c (its change was consumed by the commit)", string(data))
	}
	if got := gitOut(t, repo, "show", "HEAD:c.txt"); got != "base-c" {
		t.Fatalf("HEAD:c.txt = %q, want base-c (c.txt must not be committed)", got)
	}
}

// TestCommitIgnoresPreStagedContentOutsideScope is review H2: a file the
// USER already staged (e.g. mid-way through their own unrelated edit) before
// the daemon ever ran must never enter the agent's commit, even though
// `git add -- <paths>` above stages the in-scope paths onto that same
// pre-existing index. Without `commit --only`, a plain `git commit` commits
// the WHOLE index, silently including it.
func TestCommitIgnoresPreStagedContentOutsideScope(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "base-a")
	writeFile(t, repo, "staged-by-user.txt", "base-staged")
	gitOut(t, repo, "add", "a.txt", "staged-by-user.txt")
	gitOut(t, repo, "commit", "-q", "-m", "base")

	writeFile(t, repo, "a.txt", "changed-a")
	// The user's own pre-staged, unrelated edit -- added to the index BEFORE
	// Commit is ever called, mimicking a mid-edit the daemon must not touch.
	writeFile(t, repo, "staged-by-user.txt", "user-was-mid-edit")
	gitOut(t, repo, "add", "staged-by-user.txt")

	sha, err := Commit(t.Context(), repo, []string{"a.txt"}, "CHG-CHG-1: scope test")
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sha == "" {
		t.Fatal("Commit() sha is empty, want a commit (a.txt has a real change)")
	}

	committed := map[string]bool{}
	for _, line := range strings.Split(gitOut(t, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			committed[p] = true
		}
	}
	if committed["staged-by-user.txt"] {
		t.Fatalf("committed files = %v: the user's pre-staged edit outside the change set was committed", committed)
	}
	if !committed["a.txt"] {
		t.Fatalf("committed files = %v, missing a.txt", committed)
	}
	// The pre-staged content must remain staged in the index (untouched),
	// not partially consumed by the scoped commit.
	if got := gitOut(t, repo, "diff", "--cached", "--name-only"); got != "staged-by-user.txt" {
		t.Fatalf("index after commit = %q, want staged-by-user.txt to remain staged", got)
	}
}

func TestCommitEmptyDiffNoOp(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "base")
	gitOut(t, repo, "add", "a.txt")
	gitOut(t, repo, "commit", "-q", "-m", "base")

	// Re-commit an unchanged path: nothing is staged, so Commit must return an
	// empty sha without creating a commit.
	sha, err := Commit(t.Context(), repo, []string{"a.txt"}, "msg")
	if err != nil {
		t.Fatalf("Commit() error = %v, want nil", err)
	}
	if sha != "" {
		t.Fatalf("Commit() sha = %q, want empty (nothing to commit)", sha)
	}
	if n := gitOut(t, repo, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("commit count = %s, want 1 (no new commit)", n)
	}
}

// TestCommitDisablesHooks verifies the effect, not just the flag: a pre-commit
// hook that would both touch a marker and fail must not run.
func TestCommitDisablesHooks(t *testing.T) {
	repo := initRepo(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, repo, "a.txt", "new")
	sha, err := Commit(t.Context(), repo, []string{"a.txt"}, "msg")
	if err != nil {
		t.Fatalf("Commit() error = %v (hook should have been disabled)", err)
	}
	if sha == "" {
		t.Fatal("Commit() sha = empty, want a commit")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-commit hook ran despite core.hooksPath override (marker exists)")
	}
}

// TestCommitDisablesHooksFlagAndSanitizesEnv asserts the concrete argv/environment
// Commit passes to git: `-c core.hooksPath=` (hooks disabled) with `--` (path
// scoping), and an environment that drops GIT_* and provider keys.
func TestCommitDisablesHooksFlagAndSanitizesEnv(t *testing.T) {
	workTree := t.TempDir()
	if err := os.Mkdir(filepath.Join(workTree, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	argsPath := filepath.Join(t.TempDir(), "args")
	envPath := filepath.Join(t.TempDir(), "env")
	fake := writeFakeGit(t, argsPath, envPath)
	prev := BinPath
	BinPath = fake
	t.Cleanup(func() { BinPath = prev })

	// Plant daemon-style variables that must NOT reach the git process.
	t.Setenv("GIT_DIR", "/tmp/evil-git-dir")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-secret")

	sha, err := Commit(t.Context(), workTree, []string{"a.txt"}, "CHG-CHG-1: hi")
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sha != "abc123fake" {
		t.Fatalf("Commit() sha = %q, want abc123fake", sha)
	}

	argsRaw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	args := string(argsRaw)
	if !strings.Contains(args, "core.hooksPath=") {
		t.Errorf("recorded git args missing core.hooksPath (hooks not disabled): %q", args)
	}
	if !strings.Contains(args, "-c") || !strings.Contains(args, "--") {
		t.Errorf("recorded git args missing -c/-- (path scoping): %q", args)
	}

	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read recorded env: %v", err)
	}
	env := string(envRaw)
	for _, forbidden := range []string{"GIT_DIR", "ANTHROPIC_API_KEY", "sk-test-secret"} {
		if strings.Contains(env, forbidden) {
			t.Errorf("git process env leaked %q:\n%s", forbidden, env)
		}
	}
	if !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("git process env missing GIT_TERMINAL_PROMPT=0")
	}
}
