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
	script := fmt.Sprintf(`#!/usr/bin/env bash
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
