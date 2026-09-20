package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
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
	return realGitCommitFixtureWithGitCommit(t, branch, true, gitCfg)
}

// realGitCommitFixtureWithGitCommit is realGitCommitFixture with an explicit
// GitCommit setting, so a test can pin the "branch_per_initiative alone
// implies committing, git_commit need not also be set" fix (review defect D)
// against the real guard/branch machinery, not just the fake-git SHA-only
// path newGitCommitFixture covers.
func realGitCommitFixtureWithGitCommit(t *testing.T, branch string, gitCommit bool, gitCfg config.GitConfig) (*Engine, *store.DB, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	fp := model.NewFakeProvider()
	cfg := config.HarnessConfig{
		Network:   config.NetworkDisabled,
		Review:    config.ReviewConfig{MaxCycles: 3},
		GitCommit: gitCommit,
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

// TestApplyGitCommitBranchPerInitiativeCreatesAndReuses is review C4 piece 2:
// the first apply of an initiative creates and checks out idea/<slug>, later
// applies reuse it, and the initiative's own branch is what gets committed to
// -- even though the default-branch guard would otherwise refuse a commit
// while `main` is checked out.
func TestApplyGitCommitBranchPerInitiativeCreatesAndReuses(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})

	first := applyApproved(t, eng, db, iniID)
	if first.GitCommitWarning != "" {
		t.Fatalf("GitCommitWarning = %q, want none: the initiative's own branch, not main, should have been committed to", first.GitCommitWarning)
	}
	if first.GitCommitSHA == "" {
		t.Fatal("GitCommitSHA is empty, want a commit on the initiative's branch")
	}
	ini, err := db.GetInitiative(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.GitBranch == "" || !strings.HasPrefix(ini.GitBranch, "idea/") {
		t.Fatalf("initiative.GitBranch = %q, want an idea/<slug> branch", ini.GitBranch)
	}
	if got := gitOutTest(t, workspace, "symbolic-ref", "--short", "HEAD"); got != ini.GitBranch {
		t.Fatalf("checked out branch = %q, want %q", got, ini.GitBranch)
	}

	// A second task (a distinct title, so the internal executor's placeholder
	// actually differs and there is something new to commit) on the same
	// initiative reuses the same branch.
	second := applyApprovedTitled(t, eng, db, iniID, "add /metrics")
	if second.GitCommitSHA == "" {
		t.Fatal("second apply did not commit")
	}
	ini2, err := db.GetInitiative(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini2.GitBranch != ini.GitBranch {
		t.Fatalf("second apply's branch = %q, want the same branch %q", ini2.GitBranch, ini.GitBranch)
	}
}

// TestApplyGitCommitImpliedByBranchPerInitiativeAlone reproduces the ICP
// re-run's own defect D: git.branch_per_initiative did nothing at all unless
// git_commit was ALSO set, even though `idea pr`'s own error told a user to
// "enable git.branch_per_initiative and apply a change set first" -- no
// mention of a second flag. Setting only branch_per_initiative must be
// sufficient to create the initiative's branch and commit onto it.
func TestApplyGitCommitImpliedByBranchPerInitiativeAlone(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixtureWithGitCommit(t, "main", false, config.GitConfig{BranchPerInitiative: true})

	got := applyApproved(t, eng, db, iniID)
	if got.GitCommitSHA == "" {
		t.Fatal("GitCommitSHA is empty, want a commit: branch_per_initiative alone must imply committing")
	}
	ini, err := db.GetInitiative(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.GitBranch == "" || !strings.HasPrefix(ini.GitBranch, "idea/") {
		t.Fatalf("initiative.GitBranch = %q, want an idea/<slug> branch even with git_commit unset", ini.GitBranch)
	}
	if got := gitOutTest(t, workspace, "symbolic-ref", "--short", "HEAD"); got != ini.GitBranch {
		t.Fatalf("checked out branch = %q, want %q", got, ini.GitBranch)
	}
}

// TestApplyGitCommitBranchPerInitiativeCatchesUpEarlierApplies is the other
// half of defect D end to end: an initiative has an earlier applied change
// set that touched a file while git_commit was off (so it landed in the
// workspace, uncommitted, on whatever branch was checked out), then
// branch_per_initiative was turned on for a later apply. The later apply must
// not refuse forever over that earlier change set's own uncommitted file --
// it catches it up onto the new initiative branch instead, as its own commit,
// separate from the later apply's own commit.
//
// The internal executor's placeholder always writes to the same "hello.txt"
// (see InternalExecutor.Collect), so the earlier change set here is built by
// hand with a distinct path ("manual.txt") -- otherwise this change set's own
// keepPaths would already cover the same path and never exercise the
// catch-up branch at all.
func TestApplyGitCommitBranchPerInitiativeCatchesUpEarlierApplies(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixtureWithGitCommit(t, "main", false, config.GitConfig{})
	ctx := context.Background()

	// An earlier task/change set, applied (files written to the real
	// workspace) while git_commit was off, so it was never committed.
	earlierTask := &domain.Task{InitiativeID: iniID, Title: "earlier task", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, earlierTask, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "manual.txt"), []byte("from an earlier applied change set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := eng.rawBlobs.Put([]byte("from an earlier applied change set\n"))
	if err != nil {
		t.Fatal(err)
	}
	patchBytes, err := json.Marshal(fsdiff.Patch{Files: []domain.FileChange{
		{Path: "manual.txt", Digest: digest, ChangeType: domain.FileAdded},
	}})
	if err != nil {
		t.Fatal(err)
	}
	patchDigest, err := eng.rawBlobs.Put(patchBytes)
	if err != nil {
		t.Fatal(err)
	}
	earlierCS := &domain.ChangeSet{InitiativeID: iniID, TaskID: earlierTask.ID, PatchDigest: patchDigest, HumanID: "CHG-manual"}
	if err := db.CreateChangeSet(ctx, earlierCS); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, earlierCS.ID, step.from, step.to, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, earlierCS.ID, "digest", "test"); err != nil {
		t.Fatal(err)
	}
	if status := gitOutTest(t, workspace, "status", "--porcelain"); !strings.Contains(status, "manual.txt") {
		t.Fatalf("status = %q, want manual.txt left uncommitted by the earlier apply", status)
	}

	// Now flip on branch_per_initiative (which, per the fix above, also
	// implies committing) for a new apply, through the normal execute path.
	eng.Reload(config.HarnessConfig{
		Network: config.NetworkDisabled,
		Review:  config.ReviewConfig{MaxCycles: 3},
		Git:     config.GitConfig{BranchPerInitiative: true},
	}, eng.registry, nil, eng.blobs, nil)

	got := applyApprovedTitled(t, eng, db, iniID, "add /metrics")
	if got.GitCommitWarning != "" {
		t.Fatalf("GitCommitWarning = %q, want none: manual.txt should have been caught up, not refused", got.GitCommitWarning)
	}
	if got.GitCommitSHA == "" {
		t.Fatal("GitCommitSHA is empty, want this apply to commit once manual.txt is caught up")
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	status := gitOutTest(t, workspace, "status", "--porcelain")
	if status != "" {
		t.Fatalf("status = %q, want a clean tree: both the catch-up and this apply should have committed", status)
	}
	log := gitOutTest(t, workspace, "log", "--name-only", "--pretty=format:%s", ini.GitBranch)
	if !strings.Contains(log, "catch up") || !strings.Contains(log, "manual.txt") {
		t.Fatalf("git log = %q, want a catch-up commit naming manual.txt", log)
	}
	// The catch-up commit is separate from this apply's own commit: hello.txt
	// (this apply's own file) must not appear in the catch-up commit's own
	// file list.
	catchUpFiles := gitOutTest(t, workspace, "show", "--name-only", "--pretty=format:", "HEAD~1")
	if strings.Contains(catchUpFiles, "hello.txt") {
		t.Fatalf("catch-up commit's files = %q, must not include hello.txt (this apply's own file)", catchUpFiles)
	}
}

// TestApplyGitCommitBranchPerInitiativeRefusesUnrelatedDirtyFiles: an
// unrelated uncommitted edit in the workspace must not ride onto the
// initiative's freshly created branch -- the commit is skipped instead, with
// a warning, and the apply itself still succeeds.
func TestApplyGitCommitBranchPerInitiativeRefusesUnrelatedDirtyFiles(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	if err := os.WriteFile(filepath.Join(workspace, "unrelated.txt"), []byte("mid-edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := applyApproved(t, eng, db, iniID)
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	if got.GitCommitSHA != "" {
		t.Errorf("GitCommitSHA = %q, want empty: the branch switch should have refused", got.GitCommitSHA)
	}
	if !strings.Contains(got.GitCommitWarning, "unrelated.txt") {
		t.Errorf("GitCommitWarning = %q, want it to name the unrelated dirty file", got.GitCommitWarning)
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
