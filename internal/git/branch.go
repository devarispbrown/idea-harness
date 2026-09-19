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
	// The dirty check applies to BOTH paths: switching to an already-created
	// initiative branch and creating a brand-new one. A switch that silently
	// carried someone else's mid-edit onto the initiative's branch would break
	// the promise docs/configuration.md makes (review H2) -- reusing a branch
	// is not exempt just because the branch itself already exists.
	dirty, err := dirtyPathsOutside(ctx, workTree, keepPaths)
	if err != nil {
		return "", "", err
	}
	if len(dirty) > 0 {
		return "", "the workspace has uncommitted changes outside this change set: " + strings.Join(dirty, ", "), nil
	}
	if existingBranch != "" {
		if out, err := runGit(ctx, workTree, sanitizedEnv(), "checkout", "-q", existingBranch); err != nil {
			return "", "", fmt.Errorf("git: checkout %s: %w: %s", existingBranch, err, strings.TrimSpace(out))
		}
		return existingBranch, "", nil
	}
	name, err := freeBranchName(ctx, workTree, "idea/"+Slugify(titleSeed, 40))
	if err != nil {
		return "", "", err
	}
	// A brand-new initiative branch is created from the repository's default
	// branch, never from whatever HEAD happens to be checked out (review H2):
	// HEAD may be another initiative's own branch, and branching from it would
	// silently stack this initiative's commits on top of that unrelated one.
	// When no default branch can be resolved (e.g. a fresh repo with only one
	// commit and no candidate name), falling back to plain `checkout -b`
	// (from current HEAD) keeps the pre-existing, harmless behavior.
	args := []string{"checkout", "-q", "-b", name}
	if _, baseRef, refErr := defaultBranchRef(ctx, workTree); refErr != nil {
		return "", "", refErr
	} else if baseRef != "" {
		args = append(args, baseRef)
	}
	if out, err := runGit(ctx, workTree, sanitizedEnv(), args...); err != nil {
		return "", "", fmt.Errorf("git: checkout -b %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	return name, "", nil
}

// dirtyPathsOutside returns the workspace-relative paths `git status
// --porcelain=v1 -z --untracked-files=all` reports as dirty (staged,
// unstaged, or untracked) and are not in keep.
//
// The plain `git status --porcelain` (no -z, default untracked-files=normal)
// this used to run has two bugs (review H1): a brand-new directory collapses
// to a single "?? dir/" entry instead of listing the files inside it, so a
// change set that adds a nested file like src/new/a.go sees "src/" reported
// dirty and is refused forever; and a path containing a space or another
// shell-special character is quoted C-style, which the byte-offset-3 slice
// below left quoted (and unescaped) instead of matching keep.
//
// `-z` NUL-terminates every record and never quotes a path, and
// `--untracked-files=all` lists every file inside a new directory
// individually instead of just the directory. A rename or copy record
// carries two NUL-terminated fields (the new path, then the original path);
// both are checked against keep, since either one dirties the tree outside
// the change set.
func dirtyPathsOutside(ctx context.Context, workTree string, keep []string) ([]string, error) {
	out, err := runGit(ctx, workTree, sanitizedEnv(), "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("git: status --porcelain=v1 -z: %w: %s", err, strings.TrimSpace(out))
	}
	keepSet := make(map[string]bool, len(keep))
	for _, p := range keep {
		keepSet[filepath.ToSlash(p)] = true
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var dirty []string
	seen := make(map[string]bool)
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		status := entry[:2]
		paths := []string{entry[3:]}
		// A rename or copy (X or Y is 'R'/'C') carries a second field: the
		// original path, immediately following in the NUL stream.
		if (status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C') && i+1 < len(fields) {
			i++
			paths = append(paths, fields[i])
		}
		for _, p := range paths {
			p = filepath.ToSlash(p)
			if p == "" || keepSet[p] || seen[p] {
				continue
			}
			seen[p] = true
			dirty = append(dirty, p)
		}
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
