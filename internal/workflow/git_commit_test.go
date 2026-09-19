package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/git"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// writeWorkflowFakeGit writes a fake git binary that records each invocation to
// recordPath (baked in, since git.Commit runs git with a sanitized env that
// drops caller env vars) and answers the subcommands Commit invokes, plus the
// read-only introspection CommitGuardReason makes first: an ordinary feature
// branch with no in-progress merge/rebase/cherry-pick and no remote-tracking
// default, so the guard never refuses in these commit-path fixtures (its own
// behaviour is covered end-to-end against a real git binary in
// internal/git/guard_test.go).
func writeWorkflowFakeGit(t *testing.T, recordPath string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf 'called\n' >> '%s'
while [ "$#" -gt 0 ] && [ "$1" = "-c" ]; do shift 2; done
sub="${1:-}"
case "$sub" in
  add) exit 0 ;;
  diff) exit 1 ;;
  commit) exit 0 ;;
  symbolic-ref)
    # refs/remotes/origin/HEAD: no remote-tracking default configured.
    if [ "$4" = "refs/remotes/origin/HEAD" ]; then exit 1; fi
    echo "feature-branch" ;;
  rev-parse)
    # --git-path MARKER: report a path that can never exist, so no
    # merge/rebase/cherry-pick is ever "in progress" here.
    if [ "$2" = "--git-path" ]; then echo "does-not-exist/$3"; exit 0; fi
    echo "abc123fake" ;;
  *) exit 1 ;;
esac
`, recordPath)
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return path
}

// newGitCommitFixture builds an engine whose workspace looks like a git repo
// (a bare .git entry), so git.Commit proceeds when git_commit is enabled.
func newGitCommitFixture(t *testing.T, gitCommit bool) (*Engine, *store.DB, string, string) {
	t.Helper()
	fp := model.NewFakeProvider()
	cfg := config.HarnessConfig{
		Network:   config.NetworkDisabled,
		Review:    config.ReviewConfig{MaxCycles: 3},
		GitCommit: gitCommit,
	}
	eng, db := newTestEngine(t, fp, cfg)
	eng.SetWorkRoot(t.TempDir())
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	eng.SetWorkspaceRoot(workspace)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID, workspace
}

// applyApproved runs the full execute → approve → apply path and returns the
// persisted change set.
func applyApproved(t *testing.T, eng *Engine, db *store.DB, iniID string) *domain.ChangeSet {
	t.Helper()
	return applyApprovedTitled(t, eng, db, iniID, "Implement core")
}

// applyApprovedTitled is applyApproved with an explicit task title, so a
// second call on the same initiative produces a different diff instead of a
// no-op re-application of the internal executor's identical placeholder.
func applyApprovedTitled(t *testing.T, eng *Engine, db *store.DB, iniID, title string) *domain.ChangeSet {
	t.Helper()
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: title, Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestApplyGitCommitDisabledDoesNothing(t *testing.T) {
	eng, db, iniID, _ := newGitCommitFixture(t, false)

	record := filepath.Join(t.TempDir(), "called")
	fake := writeWorkflowFakeGit(t, record)
	prev := git.BinPath
	git.BinPath = fake
	t.Cleanup(func() { git.BinPath = prev })

	got := applyApproved(t, eng, db, iniID)
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	if got.GitCommitSHA != "" {
		t.Fatalf("GitCommitSHA = %q, want empty when git_commit is disabled", got.GitCommitSHA)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("git binary was invoked with git_commit disabled (record %q exists)", record)
	}
}

func TestApplyGitCommitEnabledRecordsSHA(t *testing.T) {
	eng, db, iniID, workspace := newGitCommitFixture(t, true)

	record := filepath.Join(t.TempDir(), "called")
	fake := writeWorkflowFakeGit(t, record)
	prev := git.BinPath
	git.BinPath = fake
	t.Cleanup(func() { git.BinPath = prev })

	got := applyApproved(t, eng, db, iniID)
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	if got.GitCommitSHA != "abc123fake" {
		t.Fatalf("GitCommitSHA = %q, want abc123fake", got.GitCommitSHA)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("git binary should have been invoked with git_commit enabled: %v", err)
	}
	// The apply must still write the file into the workspace (commit is
	// orthogonal to the byte-for-byte apply).
	if _, err := os.Stat(filepath.Join(workspace, "hello.txt")); err != nil {
		t.Fatalf("hello.txt should exist in the workspace after apply: %v", err)
	}
}

// realGitCommitFixture is newGitCommitFixture's counterpart against an actual
// git repository (checked out on branch) rather than a bare `.git` directory,
// so CommitGuardReason's real introspection (current branch, in-progress
// state) has something real to read. Used only by the guard tests below; the
// SHA-recording tests above stay on the fake git binary they already had.
func realGitCommitFixture(t *testing.T, branch string, gitCfg config.GitConfig) (*Engine, *store.DB, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	fp := model.NewFakeProvider()
	cfg := config.HarnessConfig{
		Network:   config.NetworkDisabled,
		Review:    config.ReviewConfig{MaxCycles: 3},
		GitCommit: true,
		Git:       gitCfg,
	}
	eng, db := newTestEngine(t, fp, cfg)
	eng.SetWorkRoot(t.TempDir())
	workspace := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", branch)
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(workspace, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "seed.txt")
	run("commit", "-q", "-m", "seed")
	eng.SetWorkspaceRoot(workspace)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID, workspace
}

// TestApplyGitCommitWarnsOnDefaultBranch is review C4 (adopted #2): the
// commit-on-apply path must refuse to commit directly onto the repository's
// default branch, but the apply itself must still succeed -- only the commit
// is skipped, with the reason recorded as a warning on the change set rather
// than a failure.
func TestApplyGitCommitWarnsOnDefaultBranch(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "main", config.GitConfig{})
	got := applyApproved(t, eng, db, iniID)

	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("status = %q, want applied (a refused commit must not half-apply)", got.Status)
	}
	if got.GitCommitSHA != "" {
		t.Errorf("GitCommitSHA = %q, want empty: apply must not commit onto the default branch", got.GitCommitSHA)
	}
	if !strings.Contains(got.GitCommitWarning, "main") || !strings.Contains(got.GitCommitWarning, "default branch") {
		t.Errorf("GitCommitWarning = %q, want it to name the default branch", got.GitCommitWarning)
	}
	if _, err := os.Stat(filepath.Join(workspace, "hello.txt")); err != nil {
		t.Fatalf("hello.txt should exist in the workspace after apply: %v", err)
	}
}

// TestApplyGitCommitAllowedOnDefaultBranchWhenConfigured is the override:
// git.allow_default_branch lets the same apply commit.
func TestApplyGitCommitAllowedOnDefaultBranchWhenConfigured(t *testing.T) {
	eng, db, iniID, _ := realGitCommitFixture(t, "main", config.GitConfig{AllowDefaultBranch: true})
	got := applyApproved(t, eng, db, iniID)

	if got.GitCommitSHA == "" {
		t.Error("GitCommitSHA is empty, want a commit: git.allow_default_branch was set")
	}
	if got.GitCommitWarning != "" {
		t.Errorf("GitCommitWarning = %q, want empty", got.GitCommitWarning)
	}
}
// gitOutTest runs a git command in dir and returns its trimmed combined output.
func gitOutTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestApplyGitCommitWarnsOnDetachedHEAD covers the other guard: a commit made
// while HEAD is detached is an orphan the next checkout loses.
func TestApplyGitCommitWarnsOnDetachedHEAD(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "work", config.GitConfig{})
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workspace
	sha, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	detach := exec.Command("git", "checkout", "-q", "--detach", strings.TrimSpace(string(sha)))
	detach.Dir = workspace
	if out, err := detach.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	got := applyApproved(t, eng, db, iniID)
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	if got.GitCommitSHA != "" {
		t.Errorf("GitCommitSHA = %q, want empty: apply must not commit on a detached HEAD", got.GitCommitSHA)
	}
	if !strings.Contains(got.GitCommitWarning, "detached") {
		t.Errorf("GitCommitWarning = %q, want it to mention detached HEAD", got.GitCommitWarning)
	}
}
