package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/git"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/store"
)

// writeWorkflowFakeGit writes a fake git binary that records each invocation to
// recordPath (baked in, since git.Commit runs git with a sanitized env that
// drops caller env vars) and answers the subcommands Commit invokes.
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
  rev-parse) echo "abc123fake" ;;
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
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
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
