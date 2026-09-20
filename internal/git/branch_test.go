package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Per-user API keys with rate limiting", "per-user-api-keys-with-rate-limiting"},
		{"  leading and trailing spaces  ", "leading-and-trailing-spaces"},
		{"Weird!!__Punctuation???", "weird-punctuation"},
		{"", "initiative"},
		{"曖昧", "initiative"},
	} {
		if got := Slugify(c.in, 40); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyTruncatesWithoutATrailingDash(t *testing.T) {
	got := Slugify("one two three four five six seven eight", 12)
	if len(got) > 12 {
		t.Fatalf("Slugify result %q exceeds maxLen 12", got)
	}
	if strings.HasSuffix(got, "-") {
		t.Fatalf("Slugify result %q ends with a dash", got)
	}
}

func TestEnsureInitiativeBranchCreatesAndReuses(t *testing.T) {
	repo := initRepoOnBranch(t, "main")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if branch != "idea/add-rate-limiting" {
		t.Fatalf("branch = %q, want idea/add-rate-limiting", branch)
	}
	if got := gitOut(t, repo, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("checked out branch = %q, want %q", got, branch)
	}

	// A prior apply's branch is reused verbatim, from any starting branch.
	gitOut(t, repo, "checkout", "-q", "main")
	again, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, branch, "Add rate limiting", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if again != branch {
		t.Fatalf("branch = %q, want the same existing branch %q", again, branch)
	}
	if got := gitOut(t, repo, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("checked out branch = %q, want %q", got, branch)
	}
}

func TestEnsureInitiativeBranchAvoidsACollidingName(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	gitOut(t, repo, "branch", "idea/add-rate-limiting")

	branch, _, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "idea/add-rate-limiting-2" {
		t.Fatalf("branch = %q, want idea/add-rate-limiting-2 (the plain name was taken)", branch)
	}
}

func TestEnsureInitiativeBranchRefusesUnrelatedDirtyFiles(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	writeFile(t, repo, "unrelated.txt", "an edit the agent never touched")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want none: the tree has an unrelated dirty file", branch)
	}
	if !strings.Contains(reason, "unrelated.txt") {
		t.Errorf("reason %q does not name the dirty file", reason)
	}
	// Refusing must not touch the workspace: still on the original branch.
	if got := gitOut(t, repo, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("HEAD moved to %q despite the refusal", got)
	}
}

func TestEnsureInitiativeBranchAllowsTheChangeSetsOwnDirtyFiles(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	writeFile(t, repo, "server.js", "app.get('/health', ok)")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: server.js is the change set's own file", reason)
	}
	if branch == "" {
		t.Fatal("want a branch, got none")
	}
}

// TestEnsureInitiativeBranchNeverBlockedByIdeaHarnessDir reproduces the ICP
// re-run's own defect D: `idea init` leaves .idea-harness/ (harness.yaml,
// project.yaml, roles/*.yaml) untracked, and a founder-journey run turned on
// git.branch_per_initiative before ever committing it -- the branch switch
// refused forever, naming files that are the harness's own control-plane
// config, not anything the change set's reviewer or a PR would ever need to
// see moved between branches.
func TestEnsureInitiativeBranchNeverBlockedByIdeaHarnessDir(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	if err := os.MkdirAll(filepath.Join(repo, ".idea-harness", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, filepath.Join(".idea-harness", "harness.yaml"), "git_commit: true\n")
	writeFile(t, repo, filepath.Join(".idea-harness", "roles", "architect.yaml"), "name: architect\n")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: an untracked .idea-harness/ must never block the branch switch", reason)
	}
	if branch == "" {
		t.Fatal("want a branch, got none")
	}
}

// TestEnsureInitiativeBranchCatchesUpEarlierAppliedFiles reproduces the other
// half of defect D: files from this SAME initiative's earlier applied change
// sets, left uncommitted because git_commit was off at the time, must be
// caught up onto the initiative's branch rather than refusing it forever.
func TestEnsureInitiativeBranchCatchesUpEarlierAppliedFiles(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	writeFile(t, repo, "auth.go", "package main // from an earlier applied change set")
	writeFile(t, repo, "server.js", "app.get('/health', ok)") // this change set's own file

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting",
		[]string{"server.js"}, []string{"auth.go"})
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: auth.go is named in catchUpPaths", reason)
	}
	if branch == "" {
		t.Fatal("want a branch, got none")
	}
	// auth.go must have been committed (caught up), on the new branch, as its
	// own commit -- separate from server.js, which the caller still commits
	// itself afterward and is deliberately left uncommitted here.
	log := gitOut(t, repo, "log", "--name-only", "--pretty=format:%s")
	if !strings.Contains(log, "catch up") || !strings.Contains(log, "auth.go") {
		t.Fatalf("git log = %q, want a catch-up commit naming auth.go", log)
	}
	status := gitOut(t, repo, "status", "--porcelain")
	if strings.Contains(status, "auth.go") {
		t.Fatalf("status = %q, auth.go should have been committed by the catch-up", status)
	}
	if !strings.Contains(status, "server.js") {
		t.Fatalf("status = %q, server.js (this change set's own file) should still be uncommitted", status)
	}
}

// TestEnsureInitiativeBranchStillRefusesFilesNotInCatchUp pins the other side
// of the catch-up fix: a dirty path that is NOT one of this initiative's own
// earlier applied files (a genuine, unrelated in-progress edit) must still
// refuse the branch switch, and the refusal must name only that file -- never
// the ones catchUpPaths already explains.
func TestEnsureInitiativeBranchStillRefusesFilesNotInCatchUp(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	writeFile(t, repo, "auth.go", "package main // from an earlier applied change set")
	writeFile(t, repo, "unrelated.txt", "someone's mid-edit")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting",
		[]string{"server.js"}, []string{"auth.go"})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want none: unrelated.txt is not explained by keepPaths or catchUpPaths", branch)
	}
	if !strings.Contains(reason, "unrelated.txt") {
		t.Errorf("reason %q does not name the genuinely unexplained file", reason)
	}
	if strings.Contains(reason, "auth.go") {
		t.Errorf("reason %q should not name auth.go: catchUpPaths already explains it", reason)
	}
	// Refusing must leave the tree untouched: no catch-up commit either.
	if got := gitOut(t, repo, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("HEAD moved to %q despite the refusal", got)
	}
	status := gitOut(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "auth.go") {
		t.Fatalf("status = %q, auth.go should still be uncommitted: the whole switch was refused", status)
	}
}

// TestEnsureInitiativeBranchRefusesUnrelatedDirtyFilesInNestedDir is review
// H1: the old `git status --porcelain` (default untracked-files=normal)
// collapses a brand-new directory to a single "?? dir/" entry, so a change
// set that adds src/new/a.go (in keepPaths) saw "src/" itself reported dirty
// -- a path that can never match a keepPaths entry naming the file inside it
// -- and the branch was refused forever. --untracked-files=all must list the
// unrelated nested file individually instead.
func TestEnsureInitiativeBranchRefusesUnrelatedDirtyFilesInNestedDir(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	if err := os.MkdirAll(filepath.Join(repo, "src", "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, filepath.Join("src", "new", "unrelated.go"), "package new")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want none: src/new/unrelated.go is not in scope", branch)
	}
	if !strings.Contains(reason, "src/new/unrelated.go") {
		t.Errorf("reason %q does not name the nested dirty file (want the file, not just the collapsed directory)", reason)
	}
}

// TestEnsureInitiativeBranchAllowsANestedFileInScope is H1's positive case:
// a change set that legitimately adds a file inside a brand-new nested
// directory must not be refused just because that directory is new.
func TestEnsureInitiativeBranchAllowsANestedFileInScope(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	if err := os.MkdirAll(filepath.Join(repo, "src", "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, filepath.Join("src", "new", "a.go"), "package new")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"src/new/a.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: src/new/a.go is the change set's own file", reason)
	}
	if branch == "" {
		t.Fatal("want a branch, got none")
	}
}

// TestEnsureInitiativeBranchRefusesUnrelatedDirtyFileWithSpaces is H1's other
// half: a path containing a space must be recognized correctly (matched
// against keepPaths, and named in the refusal), not left C-quoted or
// otherwise mangled the way default (non -z) `git status --porcelain` output
// would require special unescaping to parse.
func TestEnsureInitiativeBranchRefusesUnrelatedDirtyFileWithSpaces(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	writeFile(t, repo, "my new file.txt", "an edit the agent never touched")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want none: %q has a space and is not in scope", branch, "my new file.txt")
	}
	if !strings.Contains(reason, "my new file.txt") {
		t.Errorf("reason %q does not name the dirty file with a space in its name", reason)
	}
}

// TestEnsureInitiativeBranchAllowsAPathWithSpacesInScope confirms a keepPaths
// entry with a space actually matches (not just that a foreign one is
// reported), so the -z parser round-trips a space-containing path exactly.
func TestEnsureInitiativeBranchAllowsAPathWithSpacesInScope(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	writeFile(t, repo, "my new file.txt", "the change set's own content")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"my new file.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: %q is the change set's own file", reason, "my new file.txt")
	}
	if branch == "" {
		t.Fatal("want a branch, got none")
	}
}

// TestEnsureInitiativeBranchSwitchAppliesDirtyCheckToo is review H2: reusing
// an already-created initiative branch (the existingBranch path) must be
// just as refused by an unrelated dirty file as creating a brand-new branch
// is -- a switch must never carry someone else's edits onto the initiative's
// branch either (docs/configuration.md).
func TestEnsureInitiativeBranchSwitchAppliesDirtyCheckToo(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	gitOut(t, repo, "branch", "idea/add-rate-limiting")
	writeFile(t, repo, "unrelated.txt", "a mid-edit the agent never touched")

	branch, reason, _, err := EnsureInitiativeBranch(t.Context(), repo, "idea/add-rate-limiting", "Add rate limiting", []string{"server.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want none: the switch should have refused on the unrelated dirty file", branch)
	}
	if !strings.Contains(reason, "unrelated.txt") {
		t.Errorf("reason %q does not name the dirty file", reason)
	}
	// Refusing must not touch the workspace: still on the original branch.
	if got := gitOut(t, repo, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("HEAD moved to %q despite the refusal", got)
	}
}

// TestEnsureInitiativeBranchFromUnrelatedHEADWarnsRatherThanSwitch is H6's
// supersession of review H2's original fix here. H2 had this ALWAYS branch
// from the repository's default rather than current HEAD, reasoning that
// HEAD might be another initiative's own branch and stacking this
// initiative's commit on top of it would be wrong. H6 found the sharper
// case: doing that unconditionally is exactly what silently reverted a
// user's own unpushed commits on the default branch to an older ref's
// state (see TestEnsureInitiativeBranchWhenHEADIsAheadOfLocalDefaultWarnsAndKeepsHEAD).
// The two can't both always win -- H6's decision is that the working tree is
// NEVER moved away from wherever it already is; when that isn't the
// resolved default branch's own commit, the new branch is created from
// current HEAD instead, and a warning says so (surfaced as the change set's
// git_commit_warning), rather than silently doing either thing.
func TestEnsureInitiativeBranchFromUnrelatedHEADWarnsRatherThanSwitch(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	// A prior, unrelated initiative's branch, checked out and one commit
	// ahead of main.
	gitOut(t, repo, "checkout", "-q", "-b", "idea/other-initiative")
	writeFile(t, repo, "other.txt", "other initiative's own file")
	gitOut(t, repo, "add", "other.txt")
	gitOut(t, repo, "commit", "-q", "-m", "other initiative's commit")
	otherHead := gitOut(t, repo, "rev-parse", "HEAD")
	mainHead := gitOut(t, repo, "rev-parse", "main")
	if otherHead == mainHead {
		t.Fatal("setup: idea/other-initiative should be ahead of main")
	}

	branch, reason, warning, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: this must not be a soft refusal", reason)
	}
	if warning == "" {
		t.Fatal("want a warning: HEAD (idea/other-initiative) is not at the default branch's commit")
	}
	// H6: the tree is never moved away from HEAD's own commit. The new
	// branch is at otherHead, not mainHead.
	if got := gitOut(t, repo, "rev-parse", branch); got != otherHead {
		t.Fatalf("new branch %s is at %s, want current HEAD %s (must not switch the tree to the default branch)", branch, got, otherHead)
	}
}

// TestEnsureInitiativeBranchPrefersLocalDefaultOverStaleOriginHEAD is review
// H6's own bug: HEAD is checked out ON the default branch itself, which has
// unpushed commits -- origin/HEAD's remote-tracking ref is behind it. The
// old code branched from `refs/remotes/origin/HEAD` directly, which
// silently reverted every file outside the change set to origin's older
// version the moment the new branch's paths didn't conflict. The fix
// resolves the LOCAL `refs/heads/<default>` instead, which here is exactly
// HEAD's own (newer) commit, so no warning is needed and nothing is
// reverted.
func TestEnsureInitiativeBranchPrefersLocalDefaultOverStaleOriginHEAD(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	// Simulate a remote whose origin/HEAD -> origin/main is behind local
	// main by one commit (the normal state for a local tool with unpushed
	// commits), without needing an actual remote server.
	staleHead := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "update-ref", "refs/remotes/origin/main", staleHead)
	gitOut(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	writeFile(t, repo, "local-only.txt", "an unpushed local commit")
	gitOut(t, repo, "add", "local-only.txt")
	gitOut(t, repo, "commit", "-q", "-m", "unpushed local commit")
	localHead := gitOut(t, repo, "rev-parse", "HEAD")
	if localHead == staleHead {
		t.Fatal("setup: local main should be ahead of the simulated origin/main")
	}

	branch, reason, warning, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none: this must not be a soft refusal", reason)
	}
	if branch == "" {
		t.Fatal("want a branch, got none")
	}
	if warning != "" {
		t.Fatalf("warning = %q, want none: HEAD IS the resolved (local) default's commit", warning)
	}
	// The critical assertion (review H6): the tree must NOT have been moved
	// to the stale origin/HEAD commit. local-only.txt is not reverted.
	if got := gitOut(t, repo, "rev-parse", branch); got != localHead {
		t.Fatalf("new branch %s is at %s, want local HEAD %s (must not silently revert to the stale origin/HEAD commit)", branch, got, localHead)
	}
	if _, err := os.Stat(filepath.Join(repo, "local-only.txt")); err != nil {
		t.Fatalf("local-only.txt must still exist in the working tree: %v", err)
	}
}
