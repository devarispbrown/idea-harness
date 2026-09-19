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

// CommitGuardReason reports why the opt-in commit-on-apply path must not
// create a commit in workTree right now, or "" when it may proceed (review
// C4, adopted change #2). allowDefaultBranch mirrors the config toggle
// `git.allow_default_branch`.
//
// A refusal here must never half-apply a change set: the caller still writes
// the files and marks the change set applied, and only skips the commit,
// reporting this reason as a warning on the change set instead of failing the
// apply. Hooks stay disabled for whatever commit does land, exactly as before
// (docs/configuration.md).
//
// The guard checks, in order:
//   - a merge or cherry-pick in progress (a commit here would either finish
//     someone else's merge with the agent's message, or abandon it);
//   - a rebase in progress (same hazard, different mechanism);
//   - a detached HEAD (a commit here is an orphan the next checkout loses);
//   - the repository's default branch, unless overridden (an agent commit
//     landing directly on it, with no human commit of their own in between,
//     is a stronger claim than opting into git_commit alone should make).
//
// Not a repository at all is not refused here -- Commit's own isRepo check
// already makes that a silent no-op, and this function has nothing to add.
func CommitGuardReason(ctx context.Context, workTree string, allowDefaultBranch bool) (string, error) {
	if !isRepo(workTree) {
		return "", nil
	}
	if reason, err := mergeOrRebaseInProgress(ctx, workTree); err != nil {
		return "", err
	} else if reason != "" {
		return reason, nil
	}
	branch, detached, err := currentBranch(ctx, workTree)
	if err != nil {
		return "", err
	}
	if detached {
		return "HEAD is detached", nil
	}
	if !allowDefaultBranch {
		isDefault, err := onDefaultBranch(ctx, workTree, branch)
		if err != nil {
			return "", err
		}
		if isDefault {
			return fmt.Sprintf("%q is the repository's default branch (set git.allow_default_branch to override)", branch), nil
		}
	}
	return "", nil
}

// mergeOrRebaseInProgress reports why workTree is mid-operation, using
// `git rev-parse --git-path` for each marker so a linked worktree (its own
// MERGE_HEAD/rebase-merge live under a per-worktree git-dir, not the shared
// one) is checked correctly rather than by guessing ".git/...".
func mergeOrRebaseInProgress(ctx context.Context, workTree string) (string, error) {
	for _, m := range []struct{ path, verb string }{
		{"MERGE_HEAD", "a merge"},
		{"CHERRY_PICK_HEAD", "a cherry-pick"},
	} {
		p, err := gitPath(ctx, workTree, m.path)
		if err != nil {
			return "", err
		}
		if _, statErr := os.Stat(p); statErr == nil {
			return m.verb + " is in progress", nil
		}
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		p, err := gitPath(ctx, workTree, dir)
		if err != nil {
			return "", err
		}
		if fi, statErr := os.Stat(p); statErr == nil && fi.IsDir() {
			return "a rebase is in progress", nil
		}
	}
	return "", nil
}

// gitPath resolves a path relative to workTree's actual git directory
// (`git rev-parse --git-path <rel>`), which is correct for a linked worktree
// or a submodule, unlike joining ".git" onto workTree by hand.
func gitPath(ctx context.Context, workTree, rel string) (string, error) {
	out, err := runGit(ctx, workTree, sanitizedEnv(), "rev-parse", "--git-path", rel)
	if err != nil {
		return "", fmt.Errorf("git: rev-parse --git-path %s: %w: %s", rel, err, strings.TrimSpace(out))
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(workTree, p)
	}
	return p, nil
}

// currentBranch returns HEAD's branch name, or detached=true if HEAD points
// directly at a commit rather than a ref. `symbolic-ref` succeeds for an
// unborn branch (a fresh repo with no commits yet) exactly as it does for an
// ordinary one, since it only reads the ref pointer -- so a brand-new
// repository is correctly reported as "on a branch", not detached.
func currentBranch(ctx context.Context, workTree string) (branch string, detached bool, err error) {
	out, runErr := runGit(ctx, workTree, sanitizedEnv(), "symbolic-ref", "-q", "--short", "HEAD")
	if runErr != nil {
		if isExitCode(runErr, 1) {
			return "", true, nil
		}
		return "", false, fmt.Errorf("git: symbolic-ref: %w: %s", runErr, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), false, nil
}

// onDefaultBranch reports whether branch is the repository's default. A
// remote-tracking `origin/HEAD` (set by a clone, or `git remote set-head`) is
// authoritative when present; otherwise "main" or "master" is assumed to be
// the default, the same convention `gh` and GitHub itself use for a
// repository with no remote configured yet.
func onDefaultBranch(ctx context.Context, workTree, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	out, err := runGit(ctx, workTree, sanitizedEnv(), "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		name := strings.TrimPrefix(strings.TrimSpace(out), "origin/")
		return name == branch, nil
	}
	if !isExitCode(err, 1) {
		return false, fmt.Errorf("git: resolve origin/HEAD: %w: %s", err, strings.TrimSpace(out))
	}
	return branch == "main" || branch == "master", nil
}

// isExitCode reports whether err is an *exec.ExitError with exactly this exit
// code (never true for a missing binary or a killed process).
func isExitCode(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}
