package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/git"
)

// fakeOriginRemote makes workspace's "origin" a second, otherwise-untouched
// local bare repository, so `git push` in CreatePR succeeds with no network.
func fakeOriginRemote(t *testing.T, workspace string) {
	t.Helper()
	remote := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--bare")
	cmd.Dir = remote
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	gitOutTest(t, workspace, "remote", "add", "origin", remote)
}

// writeWorkflowFakeGH mirrors internal/git's writeFakeGH for workflow-level
// tests: a fake `gh` that answers `pr create` and `pr checks` offline.
func writeWorkflowFakeGH(t *testing.T, url, checksJSON string, checksExit int) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
sub="$1"
case "$sub" in
  pr)
    case "$2" in
      create) echo '%s' ;;
      checks) echo '%s'; exit %d ;;
      *) exit 1 ;;
    esac
    ;;
  *) exit 1 ;;
esac
`, url, checksJSON, checksExit)
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return path
}

func TestCreatePRPushesAndRecordsURL(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	applyApproved(t, eng, db, iniID)
	fakeOriginRemote(t, workspace)

	prevGH := git.GHBinPath
	git.GHBinPath = writeWorkflowFakeGH(t, "https://github.com/example/repo/pull/7", `[]`, 0)
	t.Cleanup(func() { git.GHBinPath = prevGH })

	url, err := eng.CreatePR(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/example/repo/pull/7" {
		t.Fatalf("url = %q, want the fake gh's URL", url)
	}
	ini, err := db.GetInitiative(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.PRURL != url {
		t.Errorf("initiative.PRURL = %q, want %q", ini.PRURL, url)
	}

	// Idempotent: a repeat call returns the same URL without calling gh again
	// (a second `pr create` for an already-open PR is an error in real gh).
	marker := filepath.Join(t.TempDir(), "no-such-gh")
	git.GHBinPath = marker
	url2, err := eng.CreatePR(context.Background(), iniID)
	if err != nil {
		t.Fatalf("second CreatePR should not call gh at all: %v", err)
	}
	if url2 != url {
		t.Errorf("second CreatePR url = %q, want %q", url2, url)
	}
}

func TestCreatePRRequiresADedicatedBranch(t *testing.T) {
	eng, db, iniID, _ := realGitCommitFixture(t, "main", config.GitConfig{}) // branch_per_initiative off
	applyApproved(t, eng, db, iniID)

	_, err := eng.CreatePR(context.Background(), iniID)
	if err == nil {
		t.Fatal("want an error: the initiative has no dedicated branch")
	}
	if err != ErrNoInitiativeBranch {
		t.Errorf("err = %v, want ErrNoInitiativeBranch", err)
	}
}

// TestCreatePRNamesTheSpecificGitCommitWarning is review defect D's precision
// requirement: when branch_per_initiative IS on but the branch was never
// created because a specific guard refused the commit (here, the default
// branch guard), `idea pr`'s error must say exactly that instead of repeating
// "enable git.branch_per_initiative and apply a change set first" -- advice
// the user already followed.
func TestCreatePRNamesTheSpecificGitCommitWarning(t *testing.T) {
	eng, db, iniID, _ := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	// AllowDefaultBranch is left false, but BranchPerInitiative should have
	// created and switched to idea/<slug> before the default-branch guard
	// ever runs (see execute.go's ordering comment) -- so to reach a
	// GitCommitWarning with GitBranch still empty, disable branch creation by
	// making dirtyPathsOutside refuse it instead: a genuinely unrelated dirty
	// file outside the change set's own paths.
	workspace := eng.workspaceRoot
	if err := os.WriteFile(filepath.Join(workspace, "unrelated.txt"), []byte("mid-edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyApproved(t, eng, db, iniID)

	_, err := eng.CreatePR(context.Background(), iniID)
	if err == nil {
		t.Fatal("want an error: the initiative has no dedicated branch")
	}
	if !errors.Is(err, ErrNoInitiativeBranch) {
		t.Errorf("err = %v, want it to still satisfy errors.Is(err, ErrNoInitiativeBranch)", err)
	}
	if !strings.Contains(err.Error(), "unrelated.txt") {
		t.Errorf("err = %v, want it to name the specific reason (unrelated.txt) instead of only the generic advice", err)
	}
}

// TestCreatePRDoesNotTellYouToEnableAnAlreadyEnabledSetting is a real
// founder-journey defect: git.branch_per_initiative was already on, and
// `idea pr` was run before any change set had been applied (so there is no
// GitCommitWarning yet either -- TestCreatePRNamesTheSpecificGitCommitWarning
// covers that case). The error still said "enable git.branch_per_initiative
// and apply a change set first", telling the user to do something they had
// already done. The missing precondition is just "apply a change set first".
func TestCreatePRDoesNotTellYouToEnableAnAlreadyEnabledSetting(t *testing.T) {
	eng, _, iniID, _ := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	// Deliberately no applyApproved(t, eng, db, iniID): nothing has been
	// applied yet, so ini.GitBranch is still empty and there is no
	// GitCommitWarning to name either.

	_, err := eng.CreatePR(context.Background(), iniID)
	if err == nil {
		t.Fatal("want an error: the initiative has no dedicated branch")
	}
	if !errors.Is(err, ErrNoInitiativeBranch) {
		t.Errorf("err = %v, want it to still satisfy errors.Is(err, ErrNoInitiativeBranch)", err)
	}
	if strings.Contains(err.Error(), "enable git.branch_per_initiative") {
		t.Errorf("err = %v, should not tell the user to enable a setting that is already on", err)
	}
	if !strings.Contains(err.Error(), "already enabled") || !strings.Contains(err.Error(), "apply a change set") {
		t.Errorf("err = %v, want it to name the actual missing precondition (apply a change set)", err)
	}
}

func TestCreatePRReportsMissingGH(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	applyApproved(t, eng, db, iniID)
	fakeOriginRemote(t, workspace)

	prevGH := git.GHBinPath
	git.GHBinPath = filepath.Join(t.TempDir(), "no-such-gh-binary")
	t.Cleanup(func() { git.GHBinPath = prevGH })

	_, err := eng.CreatePR(context.Background(), iniID)
	if err == nil {
		t.Fatal("want an error when gh is missing")
	}
	if !strings.Contains(err.Error(), "gh") {
		t.Errorf("error %v does not mention gh", err)
	}
}

func TestPRChecksRequiresAnExistingPR(t *testing.T) {
	eng, db, iniID, _ := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	cs := applyApproved(t, eng, db, iniID)

	_, err := eng.PRChecks(context.Background(), cs.ID)
	if err != ErrNoPR {
		t.Errorf("err = %v, want ErrNoPR", err)
	}
}

func TestPRChecksReportsCIStatus(t *testing.T) {
	eng, db, iniID, workspace := realGitCommitFixture(t, "main", config.GitConfig{BranchPerInitiative: true})
	cs := applyApproved(t, eng, db, iniID)
	fakeOriginRemote(t, workspace)

	prevGH := git.GHBinPath
	git.GHBinPath = writeWorkflowFakeGH(t, "https://github.com/example/repo/pull/7",
		`[{"name":"build","state":"SUCCESS"}]`, 0)
	t.Cleanup(func() { git.GHBinPath = prevGH })

	if _, err := eng.CreatePR(context.Background(), iniID); err != nil {
		t.Fatal(err)
	}

	res, err := eng.PRChecks(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Overall != "success" {
		t.Errorf("overall = %q, want success", res.Overall)
	}
}
