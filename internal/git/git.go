// Package git creates opt-in commits of an applied change set. It is
// deliberately minimal and hardened: a commit stages ONLY the caller-supplied
// paths (never `git add -A`), runs with repository hooks disabled, and executes
// in a sanitized environment so a git hook, credential helper, or editor can
// never observe the daemon's privileged environment or provider API keys (§M7).
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BinPath is the path to the git binary used by Commit. It is a package-level
// variable so tests can substitute a fake git (mirroring the executor's
// dockerPath override). Defaults to "git", resolved via PATH.
var BinPath = "git"

// Commit stages the given workspace-relative paths (adds, modifications, and
// deletions) and creates one commit whose subject is message. It returns the
// new commit's SHA.
//
// Behavior:
//   - If workTree is not a git repository (workTree/.git missing), it returns
//     ("", nil) — a non-repo is a silent no-op, not an error.
//   - If there is nothing to commit (the staged diff is empty), it returns
//     ("", nil).
//   - Only the given paths are staged; the commit never uses `git add -A`.
//   - Repository hooks are disabled (core.hooksPath points at an empty temp
//     dir), so `git commit` cannot execute arbitrary hook code under the
//     daemon's environment.
//   - The git process runs with a sanitized environment (PATH/HOME/LANG plus
//     GIT_TERMINAL_PROMPT=0), so inherited GIT_* variables and provider API
//     keys are dropped.
//
// Any real git failure (missing binary, identity not configured, lock held,
// etc.) is returned as an error; the caller treats it as non-fatal.
func Commit(ctx context.Context, workTree string, paths []string, message string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	if !isRepo(workTree) {
		return "", nil
	}

	// An empty temp dir as core.hooksPath means git can find no hook files, so
	// no pre-commit/commit-msg/prepare-commit-msg hook ever runs.
	hooksDir, err := os.MkdirTemp("", "idea-harness-git-hooks-")
	if err != nil {
		return "", fmt.Errorf("git: create hooks dir: %w", err)
	}
	defer os.RemoveAll(hooksDir)

	env := sanitizedEnv()

	// Stage exactly the given paths. `git add -- <path>` also stages deletions
	// of tracked files. `git add -A` is intentionally never used.
	addArgs := append([]string{"-c", "core.hooksPath=" + hooksDir, "add", "--"}, paths...)
	if out, err := runGit(ctx, workTree, env, addArgs...); err != nil {
		return "", fmt.Errorf("git: add: %w: %s", err, strings.TrimSpace(out))
	}

	// Nothing staged in scope (empty diff) → nothing to commit. Scoped to
	// paths with `-- <paths>` (not the whole index), so pre-staged content
	// outside the change set can never make this look like there is
	// something of OURS to commit when there is not (matching --only below).
	// `git diff --cached --quiet` exits 0 with no staged change in scope and 1
	// when there is one (both with and without an existing HEAD); any other
	// exit code is a real error.
	diffArgs := append([]string{"diff", "--cached", "--quiet", "--"}, paths...)
	diffCmd := exec.CommandContext(ctx, BinPath, diffArgs...)
	diffCmd.Dir = workTree
	diffCmd.Env = env
	if err := diffCmd.Run(); err == nil {
		return "", nil
	} else {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return "", fmt.Errorf("git: diff --cached: %w", err)
		}
	}

	// --only restricts the commit to exactly these pathspecs regardless of
	// what else the index holds (review H2): `git add -- <paths>` above stages
	// them onto whatever index already existed, so without --only a commit
	// would also sweep in anything the USER had pre-staged before the daemon
	// ever ran (a merge conflict resolution in progress, a partially staged
	// unrelated change, ...) into the agent's own commit.
	commitArgs := append([]string{"-c", "core.hooksPath=" + hooksDir, "commit", "--only", "-m", message, "--"}, paths...)
	if out, err := runGit(ctx, workTree, env, commitArgs...); err != nil {
		return "", fmt.Errorf("git: commit: %w: %s", err, strings.TrimSpace(out))
	}

	out, err := runGit(ctx, workTree, env, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git: rev-parse: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// isRepo reports whether workTree looks like a git repository: a .git entry
// (directory for a normal clone, file for a worktree/submodule) is present.
func isRepo(workTree string) bool {
	_, err := os.Stat(filepath.Join(workTree, ".git"))
	return err == nil
}

// runGit executes the git binary with the sanitized environment and returns its
// combined output.
func runGit(ctx context.Context, workTree string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, BinPath, args...)
	cmd.Dir = workTree
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sanitizedEnv builds a minimal, hardened environment for the git process. Only
// PATH (locate the binary and any git sub-processes), HOME (identity config),
// and LANG (stable messages) are inherited; every GIT_* variable and provider
// API-key variable from the daemon environment is dropped by this whitelist.
// GIT_TERMINAL_PROMPT=0 is then set so git can never block on a credential or
// terminal prompt.
func sanitizedEnv() []string {
	env := make([]string, 0, 4)
	for _, k := range []string{"PATH", "HOME", "LANG"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}
