package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
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
//     committed) and catchUpPaths (see below): those would otherwise ride
//     along onto the new branch uninvited, which is exactly the failure this
//     guards against.
//   - catchUpPaths names files this same initiative's OWN earlier applied
//     change sets touched but never committed (review defect D: a real run
//     had git_commit off for its first several applies, then turned it and
//     branch_per_initiative on together -- the workspace was left with files
//     from those earlier applies still uncommitted, and every later attempt
//     refused forever with "uncommitted changes outside this change set"
//     naming the harness's OWN prior writes). A dirty path that matches one
//     of these is committed onto the initiative's branch (right after the
//     branch switch below, so the catch-up commit lands there and not on
//     whatever branch was checked out before) as its own commit, separate
//     from the caller's own change-set commit; a dirty path that matches
//     neither keepPaths nor catchUpPaths still refuses, naming only the
//     paths that are genuinely unexplained.
//
// Returns (branch, "", "", nil) once workTree is checked out on branch. A
// soft refusal -- the tree has a dirty path outside keepPaths and
// catchUpPaths -- is ("", reason, "", nil): workTree is left exactly as
// found, and the caller should skip the commit rather than proceed on
// whatever branch was already checked out. A non-empty warning (with branch
// still set, and no error) means the branch WAS created and checked out, but
// not from the resolved default branch's commit -- see the HEAD-vs-default
// comment below (review H6); the caller should still commit, but surface the
// warning (e.g. as the change set's git_commit_warning). A non-nil error is a
// genuine git failure.
func EnsureInitiativeBranch(ctx context.Context, workTree, existingBranch, titleSeed string, keepPaths, catchUpPaths []string) (branch, reason, warning string, err error) {
	if !isRepo(workTree) {
		return "", "", "", nil
	}
	// The dirty check applies to BOTH paths: switching to an already-created
	// initiative branch and creating a brand-new one. A switch that silently
	// carried someone else's mid-edit onto the initiative's branch would break
	// the promise docs/configuration.md makes (review H2) -- reusing a branch
	// is not exempt just because the branch itself already exists.
	//
	// dirtyPathsOutside itself never reports a path under .idea-harness/ (the
	// harness's own control-plane config + scratch state) as dirty at all --
	// see its own doc comment -- so that directory needs no handling here.
	dirty, err := dirtyPathsOutside(ctx, workTree, keepPaths)
	if err != nil {
		return "", "", "", err
	}
	var catchUp []string
	if len(dirty) > 0 {
		catchUp, dirty = splitCatchUp(dirty, catchUpPaths)
		if len(dirty) > 0 {
			return "", "the workspace has uncommitted changes outside this change set: " + strings.Join(dirty, ", "), "", nil
		}
	}
	commitCatchUp := func() error {
		if len(catchUp) == 0 {
			return nil
		}
		msg := "idea-harness: catch up " + strconv.Itoa(len(catchUp)) +
			" file(s) from earlier applied change sets committed late"
		if _, err := Commit(ctx, workTree, catchUp, msg); err != nil {
			return fmt.Errorf("git: catch-up commit: %w", err)
		}
		return nil
	}
	if existingBranch != "" {
		if out, err := runGit(ctx, workTree, sanitizedEnv(), "checkout", "-q", existingBranch); err != nil {
			return "", "", "", fmt.Errorf("git: checkout %s: %w: %s", existingBranch, err, strings.TrimSpace(out))
		}
		if err := commitCatchUp(); err != nil {
			return "", "", "", err
		}
		return existingBranch, "", "", nil
	}
	name, err := freeBranchName(ctx, workTree, "idea/"+Slugify(titleSeed, 40))
	if err != nil {
		return "", "", "", err
	}
	// A brand-new initiative branch is created from the repository's default
	// branch, never from whatever HEAD happens to be checked out (review H2):
	// HEAD may be another initiative's own branch, and branching from it would
	// silently stack this initiative's commits on top of that unrelated one.
	//
	// defaultBranchRef now resolves only a LOCAL ref (review H6). Even so, the
	// tree is switched to it ONLY when HEAD is already at that exact commit:
	// if local main is ahead of (or otherwise different from) the resolved
	// default -- unpushed commits are the normal case for a local tool -- a
	// plain `checkout -b <name> <baseRef>` would move the working tree onto
	// the default branch's commit, silently reverting every file the change
	// set doesn't touch to that older version. So when HEAD differs, this
	// branches from current HEAD instead (never switching the tree away from
	// what the user already has checked out) and returns a warning rather
	// than a hard refusal: the change set still applies and commits, but the
	// caller should tell the user their new initiative branch did not start
	// from the repository's declared default.
	//
	// When no default branch can be resolved at all (e.g. a fresh repo with
	// only one commit and no candidate name), branching from current HEAD is
	// simply the pre-existing, harmless behavior -- no warning needed, since
	// there is nothing to have diverged from.
	args := []string{"checkout", "-q", "-b", name}
	baseName, baseRef, refErr := defaultBranchRef(ctx, workTree)
	if refErr != nil {
		return "", "", "", refErr
	}
	if baseRef != "" {
		headSHA, out, err := revParse(ctx, workTree, "HEAD")
		if err != nil {
			return "", "", "", fmt.Errorf("git: rev-parse HEAD: %w: %s", err, strings.TrimSpace(out))
		}
		baseSHA, out, err := revParse(ctx, workTree, baseRef)
		if err != nil {
			return "", "", "", fmt.Errorf("git: rev-parse %s: %w: %s", baseRef, err, strings.TrimSpace(out))
		}
		if headSHA == baseSHA {
			args = append(args, baseRef)
		} else {
			warning = fmt.Sprintf(
				"branched %q from the current commit instead of the default branch %q: "+
					"HEAD is not at %[2]s's commit (it may be ahead, e.g. unpushed local commits), "+
					"and switching to it would have silently reverted files this change set doesn't touch",
				name, baseName)
		}
	}
	if out, err := runGit(ctx, workTree, sanitizedEnv(), args...); err != nil {
		return "", "", "", fmt.Errorf("git: checkout -b %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	if err := commitCatchUp(); err != nil {
		return "", "", "", err
	}
	return name, "", warning, nil
}

// splitCatchUp partitions dirty (already known to be outside keepPaths) into
// catchUp -- paths also named in catchUpPaths, safe to commit onto the
// initiative's branch on the caller's behalf -- and the remainder, which is
// still genuinely unexplained and must still refuse the branch switch.
func splitCatchUp(dirty, catchUpPaths []string) (catchUp, unexplained []string) {
	if len(catchUpPaths) == 0 {
		return nil, dirty
	}
	allowed := make(map[string]bool, len(catchUpPaths))
	for _, p := range catchUpPaths {
		allowed[filepath.ToSlash(p)] = true
	}
	for _, p := range dirty {
		if allowed[p] {
			catchUp = append(catchUp, p)
		} else {
			unexplained = append(unexplained, p)
		}
	}
	return catchUp, unexplained
}

// revParse resolves ref to its commit SHA, trimmed. The raw output (usable
// in an error message) is also returned.
func revParse(ctx context.Context, workTree, ref string) (sha, out string, err error) {
	out, err = runGit(ctx, workTree, sanitizedEnv(), "rev-parse", ref)
	return strings.TrimSpace(out), out, err
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
//
// A path under .idea-harness/, the harness's own control-plane directory,
// never counts as dirty here regardless of keep (review defect D): `idea
// init` leaves it untracked until a human chooses to commit it (harness.yaml,
// project.yaml, roles/ -- config meant to be committed, just not yet), and
// its own scratch space (.idea-harness/tmp/, see fsdiff's atomic write) is
// never meant to be committed at all. Neither should ever block landing a
// change set on the initiative's branch -- a founder-journey run hit exactly
// this, refused forever by its own freshly created, still-untracked
// .idea-harness/.
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
			if p == "" || keepSet[p] || seen[p] || isHarnessControlPlanePath(p) {
				continue
			}
			seen[p] = true
			dirty = append(dirty, p)
		}
	}
	return dirty, nil
}

// isHarnessControlPlanePath reports whether p (workspace-relative,
// slash-separated) is .idea-harness itself or something inside it. Duplicated
// as a literal rather than imported from internal/fsdiff's controlPlaneDirs:
// internal/git does not otherwise depend on internal/fsdiff, and this is one
// stable literal, not worth a package dependency to share (same tradeoff
// normalizeRequirementCode documents in internal/projection).
func isHarnessControlPlanePath(p string) bool {
	return p == ".idea-harness" || strings.HasPrefix(p, ".idea-harness/")
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
