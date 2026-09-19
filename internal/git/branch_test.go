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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil)
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
	again, reason, err := EnsureInitiativeBranch(t.Context(), repo, branch, "Add rate limiting", nil)
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

	branch, _, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil)
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"})
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"})
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"})
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"src/new/a.go"})
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"server.js"})
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", []string{"my new file.txt"})
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

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "idea/add-rate-limiting", "Add rate limiting", []string{"server.js"})
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

// TestEnsureInitiativeBranchCreatesFromDefaultNotCurrentHEAD is review H2: a
// brand-new initiative branch must be created from the repository's default
// branch, not from whatever HEAD happens to be checked out -- which may be
// another initiative's own branch, stacking this initiative's commit on top
// of that unrelated one.
func TestEnsureInitiativeBranchCreatesFromDefaultNotCurrentHEAD(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	// A prior, unrelated initiative's branch, checked out and one commit
	// ahead of main -- the trap: creating the new branch from current HEAD
	// would silently include this commit too.
	gitOut(t, repo, "checkout", "-q", "-b", "idea/other-initiative")
	writeFile(t, repo, "other.txt", "other initiative's own file")
	gitOut(t, repo, "add", "other.txt")
	gitOut(t, repo, "commit", "-q", "-m", "other initiative's commit")
	otherHead := gitOut(t, repo, "rev-parse", "HEAD")
	mainHead := gitOut(t, repo, "rev-parse", "main")
	if otherHead == mainHead {
		t.Fatal("setup: idea/other-initiative should be ahead of main")
	}

	branch, reason, err := EnsureInitiativeBranch(t.Context(), repo, "", "Add rate limiting", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if got := gitOut(t, repo, "rev-parse", branch); got != mainHead {
		t.Fatalf("new branch %s is at %s, want main's HEAD %s (branched from current HEAD instead of the default branch)", branch, got, mainHead)
	}
}
