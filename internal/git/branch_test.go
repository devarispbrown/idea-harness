package git

import (
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
