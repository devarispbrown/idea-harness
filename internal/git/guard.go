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

// PreCommitGuardReason reports why the opt-in commit-on-apply path must not
// touch workTree at all right now -- including the branch switch/creation
// that may happen before the commit itself -- or "" when it may proceed
// (review C4, adopted change #2; review H2 split this out of the combined
// CommitGuardReason so a caller can run it BEFORE any checkout).
//
// A refusal here must never half-apply a change set: the caller still writes
// the files and marks the change set applied, and only skips the commit (and
// any branch switch), reporting this reason as a warning on the change set
// instead of failing the apply.
//
// The guard checks, in order:
//   - a merge or cherry-pick in progress (a checkout/commit here would either
//     finish someone else's merge with the agent's message, abandon it, or
//     fail outright because git refuses to switch branches mid-conflict);
//   - a rebase in progress (same hazard, different mechanism);
//   - a detached HEAD (committing here is an orphan the next checkout loses,
//     and -- since this now runs before any checkout -- a branch-per-initiative
//     switch is refused too rather than silently leaving the detached state
//     for the checkout to (maybe) fix).
//
// Not a repository at all is not refused here -- Commit's own isRepo check
// already makes that a silent no-op, and this function has nothing to add.
func PreCommitGuardReason(ctx context.Context, workTree string) (string, error) {
	if !isRepo(workTree) {
		return "", nil
	}
	if reason, err := mergeOrRebaseInProgress(ctx, workTree); err != nil {
		return "", err
	} else if reason != "" {
		return reason, nil
	}
	_, detached, err := currentBranch(ctx, workTree)
	if err != nil {
		return "", err
	}
	if detached {
		return "HEAD is detached", nil
	}
	return "", nil
}

// DefaultBranchGuardReason reports whether workTree's CURRENT branch (after
// any branch-per-initiative switch has already happened) is the repository's
// default branch, unless allowDefaultBranch overrides it (the
// `git.allow_default_branch` config toggle). This is deliberately the only
// check still run AFTER a checkout (review H2): an agent commit landing
// directly on the default branch, with no human commit of their own in
// between, is a stronger claim than opting into git_commit alone should make,
// and that judgment has to be made against whatever branch is checked out by
// the time a commit is about to happen -- the initiative's own branch, not
// whatever HEAD was before branch-per-initiative switched it.
func DefaultBranchGuardReason(ctx context.Context, workTree string, allowDefaultBranch bool) (string, error) {
	if !isRepo(workTree) || allowDefaultBranch {
		return "", nil
	}
	branch, detached, err := currentBranch(ctx, workTree)
	if err != nil {
		return "", err
	}
	if detached {
		// Should not happen (PreCommitGuardReason already refused a detached
		// HEAD before any checkout), but a defensive report beats a panic on
		// an empty branch name below.
		return "HEAD is detached", nil
	}
	isDefault, err := onDefaultBranch(ctx, workTree, branch)
	if err != nil {
		return "", err
	}
	if isDefault {
		return fmt.Sprintf("%q is the repository's default branch (set git.allow_default_branch to override)", branch), nil
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

// defaultBranchNames are the conventional default-branch names guessed when
// `origin/HEAD` cannot be resolved (review low-priority item). Two things
// this list, and the detection order below, deliberately no longer do:
//
//   - consult `init.defaultBranch`: that git config is normally set GLOBALLY
//     and describes what a future `git init` should name a brand-new
//     repository's branch, not what THIS repository's default branch
//     actually is;
//   - guess "develop": a git-flow repository with no `origin/HEAD`
//     configured (common for a purely local clone) would have its own
//     "develop" branch silently treated as the default and blocked by the
//     default-branch guard, with no way around it short of
//     `git.allow_default_branch`.
//
// The order is now exactly: `origin/HEAD` (resolvedDefaultBranchName), then
// this list. It is the same order the guard (onDefaultBranch) and branch
// creation (defaultBranchRef) both use.
var defaultBranchNames = []string{"main", "master"}

// onDefaultBranch reports whether branch is the repository's default. A
// remote-tracking `origin/HEAD` (set by a clone, or `git remote set-head`) is
// authoritative when present; otherwise branch is checked against
// defaultBranchNames, the same convention `gh` and GitHub itself use for a
// repository with no remote configured yet (review low-priority item:
// `init.defaultBranch` is no longer consulted -- see defaultBranchNames's
// doc comment).
func onDefaultBranch(ctx context.Context, workTree, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	name, err := resolvedDefaultBranchName(ctx, workTree)
	if err != nil {
		return false, err
	}
	if name != "" {
		return name == branch, nil
	}
	for _, cand := range defaultBranchNames {
		if branch == cand {
			return true, nil
		}
	}
	return false, nil
}

// resolvedDefaultBranchName returns the repository's declared default branch
// name: `origin/HEAD`'s target when the remote-tracking ref exists, else ""
// (meaning: fall back to guessing from defaultBranchNames). It no longer
// consults `init.defaultBranch` (review low-priority item; see
// defaultBranchNames's doc comment).
func resolvedDefaultBranchName(ctx context.Context, workTree string) (string, error) {
	out, err := runGit(ctx, workTree, sanitizedEnv(), "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
	}
	if !isExitCode(err, 1) {
		return "", fmt.Errorf("git: resolve origin/HEAD: %w: %s", err, strings.TrimSpace(out))
	}
	return "", nil
}

// defaultBranchRef resolves the name and the LOCAL ref to branch a brand-new
// initiative branch from (review H2, H6): the name comes from `origin/HEAD`
// when present, else the first local branch matching defaultBranchNames.
//
// The ref returned is always a LOCAL `refs/heads/<name>`, never the
// remote-tracking `refs/remotes/origin/HEAD` itself (review H6): branching
// straight from origin/HEAD moves the new branch onto whatever origin last
// advertised, which can silently be BEHIND the local default branch (a
// normal state for a local tool with unpushed commits) -- exactly the
// scenario that let branch-per-initiative revert the user's own local
// commits on the default branch to origin's version. When origin/HEAD names
// a branch with no local copy at all, ref is "" (name is still returned) --
// the caller falls back to branching from current HEAD rather than a name
// that exists only on the remote.
//
// Returns ("", "", nil) when nothing resolves at all (e.g. a fresh repo with
// no commit yet on any conventional name) -- the caller falls back to
// branching from current HEAD.
func defaultBranchRef(ctx context.Context, workTree string) (name, ref string, err error) {
	name, err = resolvedDefaultBranchName(ctx, workTree)
	if err != nil {
		return "", "", err
	}
	if name == "" {
		for _, cand := range defaultBranchNames {
			if localBranchExists(ctx, workTree, cand) {
				return cand, "refs/heads/" + cand, nil
			}
		}
		return "", "", nil
	}
	if localBranchExists(ctx, workTree, name) {
		return name, "refs/heads/" + name, nil
	}
	return name, "", nil
}

// localBranchExists reports whether a local branch named name exists.
func localBranchExists(ctx context.Context, workTree, name string) bool {
	_, err := runGit(ctx, workTree, sanitizedEnv(), "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// isExitCode reports whether err is an *exec.ExitError with exactly this exit
// code (never true for a missing binary or a killed process).
func isExitCode(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}
