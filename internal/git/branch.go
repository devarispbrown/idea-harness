package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Slugify lowercases s, collapses every run of characters outside [a-z0-9]
// into a single '-', trims a leading or trailing '-', and truncates to maxLen
// without leaving a trailing '-'. It is deterministic and pure ASCII, so the
// result is safe as a git ref component and portable across every git host.
// An input with nothing left after collapsing (all punctuation, all
// non-ASCII, empty) falls back to "initiative" rather than the empty string.
func Slugify(s string, maxLen int) string {
	var b strings.Builder
	lastDash := true // start "after a dash" so no leading '-' is ever written
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if maxLen > 0 && len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
	}
	if out == "" {
		out = "initiative"
	}
	return out
}

// EnsureInitiativeBranch switches workTree onto an initiative's dedicated
// branch for git.branch_per_initiative (review C4, adopted #2), creating it
// from the current HEAD the first time.
//
//   - existingBranch, when non-empty, is the branch a prior apply of this same
//     initiative already created (domain.Initiative.GitBranch); it is checked
//     out directly, so every change set for one initiative lands on the same
//     branch and the name is computed only once.
//   - Otherwise a fresh branch named "idea/<slug of titleSeed>" is created
//     (suffixed -2, -3, … on a name collision) -- but only if the tree has no
//     uncommitted changes outside keepPaths (the change set about to be
//     committed): those would otherwise ride along onto the new branch
//     uninvited, which is exactly the failure this guards against.
//
// Returns (branch, "", nil) once workTree is checked out on branch. A soft
// refusal -- the tree is dirty outside keepPaths -- is ("", reason, nil):
// workTree is left exactly as found, and the caller should skip the commit
// rather than proceed on whatever branch was already checked out. A non-nil
// error is a genuine git failure.
func EnsureInitiativeBranch(ctx context.Context, workTree, existingBranch, titleSeed string, keepPaths []string) (branch, reason string, err error) {
	if !isRepo(workTree) {
		return "", "", nil
	}
	if existingBranch != "" {
		if out, err := runGit(ctx, workTree, sanitizedEnv(), "checkout", "-q", existingBranch); err != nil {
			return "", "", fmt.Errorf("git: checkout %s: %w: %s", existingBranch, err, strings.TrimSpace(out))
		}
		return existingBranch, "", nil
	}
	dirty, err := dirtyPathsOutside(ctx, workTree, keepPaths)
	if err != nil {
		return "", "", err
	}
	if len(dirty) > 0 {
		return "", "the workspace has uncommitted changes outside this change set: " + strings.Join(dirty, ", "), nil
	}
	name, err := freeBranchName(ctx, workTree, "idea/"+Slugify(titleSeed, 40))
	if err != nil {
		return "", "", err
	}
	if out, err := runGit(ctx, workTree, sanitizedEnv(), "checkout", "-q", "-b", name); err != nil {
		return "", "", fmt.Errorf("git: checkout -b %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	return name, "", nil
}

// dirtyPathsOutside returns the workspace-relative paths `git status
// --porcelain` reports as dirty (staged, unstaged, or untracked) and are not
// in keep. `git status --porcelain` prefixes every line with a two-character
// status code and a space; the path starts at column 4 for an ordinary entry,
// which is all EnsureInitiativeBranch's caller ever produces (a rename's
// "old -> new" form is a git-side heuristic on unstaged files, not something
// fsdiff's own add/modify/delete change list produces, so it is intentionally
// not special-cased here).
func dirtyPathsOutside(ctx context.Context, workTree string, keep []string) ([]string, error) {
	out, err := runGit(ctx, workTree, sanitizedEnv(), "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git: status --porcelain: %w: %s", err, strings.TrimSpace(out))
	}
	keepSet := make(map[string]bool, len(keep))
	for _, p := range keep {
		keepSet[filepath.ToSlash(p)] = true
	}
	var dirty []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		p := filepath.ToSlash(strings.TrimSpace(line[3:]))
		if p == "" || keepSet[p] {
			continue
		}
		dirty = append(dirty, p)
	}
	return dirty, nil
}

// freeBranchName returns base, or base suffixed "-2", "-3", … the first name
// with no existing local branch ref, so two initiatives that slugify to the
// same name never collide.
func freeBranchName(ctx context.Context, workTree, base string) (string, error) {
	name := base
	for i := 2; i <= 1000; i++ {
		out, err := runGit(ctx, workTree, sanitizedEnv(), "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
		if err != nil {
			if isExitCode(err, 1) {
				return name, nil
			}
			return "", fmt.Errorf("git: rev-parse --verify refs/heads/%s: %w: %s", name, err, strings.TrimSpace(out))
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return "", fmt.Errorf("git: could not find a free branch name based on %q", base)
}
