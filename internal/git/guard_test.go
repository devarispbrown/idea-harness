package git

import (
	"os/exec"
	"strings"
	"testing"
)

// gitCmd builds an *exec.Cmd for a git invocation whose exit code the caller
// intends to ignore (a merge/rebase/cherry-pick conflict exits non-zero on
// purpose, to leave the in-progress marker behind).
func gitCmd(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}

// initRepoOnBranch is initRepo with a chosen initial branch name, so a test is
// never at the mercy of the local git installation's configured default
// (init.defaultBranch varies by machine and git version).
func initRepoOnBranch(t *testing.T, branch string) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", branch)
	gitOut(t, dir, "config", "user.name", "test")
	gitOut(t, dir, "config", "user.email", "t@example.com")
	writeFile(t, dir, "a.txt", "base")
	gitOut(t, dir, "add", "a.txt")
	gitOut(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func TestPreCommitGuardAllowsAnOrdinaryFeatureBranch(t *testing.T) {
	repo := initRepoOnBranch(t, "idea/add-health")
	reason, err := PreCommitGuardReason(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none", reason)
	}
}

func TestPreCommitGuardAllowsTheDefaultBranch(t *testing.T) {
	// PreCommitGuardReason no longer judges the default branch at all (review
	// H2 split): that is DefaultBranchGuardReason's job, run only after any
	// checkout.
	repo := initRepoOnBranch(t, "main")
	reason, err := PreCommitGuardReason(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none: PreCommitGuardReason must not judge the default branch", reason)
	}
}

func TestDefaultBranchGuardRefusesTheDefaultBranch(t *testing.T) {
	for _, name := range []string{"main", "master", "trunk", "develop"} {
		t.Run(name, func(t *testing.T) {
			repo := initRepoOnBranch(t, name)
			reason, err := DefaultBranchGuardReason(t.Context(), repo, false)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "" {
				t.Fatal("want a refusal on the default branch, got none")
			}
			if !strings.Contains(reason, name) {
				t.Errorf("reason %q does not name the branch", reason)
			}
			if !strings.Contains(reason, "allow_default_branch") {
				t.Errorf("reason %q does not name the override", reason)
			}
		})
	}
}

func TestDefaultBranchGuardAllowsTheDefaultBranchWhenOverridden(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	reason, err := DefaultBranchGuardReason(t.Context(), repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none with allow_default_branch set", reason)
	}
}

func TestDefaultBranchGuardHonoursOriginHEADOverMainMaster(t *testing.T) {
	repo := initRepoOnBranch(t, "trunk")
	// Simulate a clone's remote-tracking default without a real remote: only
	// the ref needs to exist, never a network fetch.
	gitOut(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")

	reason, err := DefaultBranchGuardReason(t.Context(), repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("want trunk refused as the declared default, got none")
	}

	gitOut(t, repo, "checkout", "-q", "-b", "main")
	reason, err = DefaultBranchGuardReason(t.Context(), repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none: origin/HEAD says trunk is default, not main", reason)
	}
}

// TestDefaultBranchGuardHonoursInitDefaultBranchConfig is the low-priority
// item: when origin/HEAD is missing, git's own init.defaultBranch config (if
// set) names the default branch, rather than only ever guessing
// main/master/trunk/develop.
func TestDefaultBranchGuardHonoursInitDefaultBranchConfig(t *testing.T) {
	repo := initRepoOnBranch(t, "development")
	gitOut(t, repo, "config", "init.defaultBranch", "development")

	reason, err := DefaultBranchGuardReason(t.Context(), repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("want development refused: init.defaultBranch names it, got no refusal")
	}

	// A branch that is neither origin/HEAD's target nor init.defaultBranch's
	// value, and not one of the bare guesses either, must NOT be refused.
	gitOut(t, repo, "checkout", "-q", "-b", "feature")
	reason, err = DefaultBranchGuardReason(t.Context(), repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none: feature is not the configured default", reason)
	}
}

func TestPreCommitGuardRefusesADetachedHEAD(t *testing.T) {
	repo := initRepoOnBranch(t, "work")
	sha := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "checkout", "-q", "--detach", sha)

	reason, err := PreCommitGuardReason(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "detached") {
		t.Errorf("reason = %q, want it to mention detached HEAD", reason)
	}
}

func TestPreCommitGuardRefusesAMergeInProgress(t *testing.T) {
	repo := initRepoOnBranch(t, "work")
	gitOut(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "a.txt", "feature-change")
	gitOut(t, repo, "commit", "-q", "-am", "feature change")
	gitOut(t, repo, "checkout", "-q", "work")
	writeFile(t, repo, "a.txt", "work-change")
	gitOut(t, repo, "commit", "-q", "-am", "work change")

	mergeCmd := gitCmd(t, repo, "merge", "feature")
	_ = mergeCmd.Run() // conflict expected; ignore the (nonzero) exit

	reason, err := PreCommitGuardReason(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "merge") {
		t.Errorf("reason = %q, want it to mention the merge in progress", reason)
	}
}

func TestPreCommitGuardRefusesARebaseInProgress(t *testing.T) {
	repo := initRepoOnBranch(t, "work")
	gitOut(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "a.txt", "feature-change")
	gitOut(t, repo, "commit", "-q", "-am", "feature change")
	gitOut(t, repo, "checkout", "-q", "work")
	writeFile(t, repo, "a.txt", "work-change")
	gitOut(t, repo, "commit", "-q", "-am", "work change")

	rebaseCmd := gitCmd(t, repo, "rebase", "feature")
	_ = rebaseCmd.Run() // conflict expected; ignore the (nonzero) exit

	reason, err := PreCommitGuardReason(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "rebase") {
		t.Errorf("reason = %q, want it to mention the rebase in progress", reason)
	}
}

func TestPreCommitGuardRefusesACherryPickInProgress(t *testing.T) {
	repo := initRepoOnBranch(t, "work")
	gitOut(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "a.txt", "feature-change")
	gitOut(t, repo, "commit", "-q", "-am", "feature change")
	pickSHA := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "checkout", "-q", "work")
	writeFile(t, repo, "a.txt", "work-change")
	gitOut(t, repo, "commit", "-q", "-am", "work change")

	pickCmd := gitCmd(t, repo, "cherry-pick", pickSHA)
	_ = pickCmd.Run() // conflict expected; ignore the (nonzero) exit

	reason, err := PreCommitGuardReason(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "cherry-pick") {
		t.Errorf("reason = %q, want it to mention the cherry-pick in progress", reason)
	}
}

func TestPreCommitGuardAllowsANonRepo(t *testing.T) {
	reason, err := PreCommitGuardReason(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none: Commit's own isRepo check already no-ops a non-repository", reason)
	}
}

func TestDefaultBranchGuardAllowsANonRepo(t *testing.T) {
	reason, err := DefaultBranchGuardReason(t.Context(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want none: Commit's own isRepo check already no-ops a non-repository", reason)
	}
}
