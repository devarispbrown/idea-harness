package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeGH writes a fake `gh` binary that records its argv to argsPath and
// answers `pr create` and `pr checks` deterministically, entirely offline.
func writeFakeGH(t *testing.T, argsPath string, checksJSON string, checksExit int) string {
	t.Helper()
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' "$@" >> '%s'
echo '---' >> '%s'
sub="$1"
case "$sub" in
  pr)
    case "$2" in
      create) echo "https://github.com/example/repo/pull/42" ;;
      checks) echo '%s'; exit %d ;;
      *) exit 1 ;;
    esac
    ;;
  *) exit 1 ;;
esac
`, argsPath, argsPath, checksJSON, checksExit)
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return path
}

// fakeRemoteRepo makes repo's "origin" a second, otherwise-untouched local
// git repository, so `git push -u origin <branch>` succeeds without any
// network -- exactly what CreatePR needs to exercise for real.
func fakeRemoteRepo(t *testing.T, repo string) {
	t.Helper()
	remote := t.TempDir()
	cmd := gitCmd(t, remote, "init", "-q", "--bare")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	gitOut(t, repo, "remote", "add", "origin", remote)
}

func TestCreatePRPushesAndReturnsURL(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	fakeRemoteRepo(t, repo)
	gitOut(t, repo, "checkout", "-q", "-b", "idea/add-rate-limiting")

	argsPath := filepath.Join(t.TempDir(), "args")
	prevGH := GHBinPath
	GHBinPath = writeFakeGH(t, argsPath, `[]`, 0)
	t.Cleanup(func() { GHBinPath = prevGH })

	url, err := CreatePR(t.Context(), repo, PRRequest{
		Branch: "idea/add-rate-limiting",
		Title:  "Per-user API keys",
		Body:   "## Summary\n\nAdds rate limiting.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/example/repo/pull/42" {
		t.Fatalf("url = %q, want the fake gh's URL", url)
	}

	// The branch really was pushed to the fake remote, not merely claimed to be.
	if got := gitOut(t, repo, "ls-remote", "--heads", "origin", "idea/add-rate-limiting"); got == "" {
		t.Error("branch was not actually pushed to origin")
	}

	argsRaw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsRaw)
	if !strings.Contains(args, "--head\nidea/add-rate-limiting") {
		t.Errorf("gh args do not scope --head to the branch:\n%s", args)
	}
	if !strings.Contains(args, "Per-user API keys") {
		t.Errorf("gh args do not carry the title:\n%s", args)
	}
}

func TestCreatePRReportsMissingGH(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	prevGH := GHBinPath
	GHBinPath = filepath.Join(t.TempDir(), "no-such-gh-binary")
	t.Cleanup(func() { GHBinPath = prevGH })

	_, err := CreatePR(t.Context(), repo, PRRequest{Branch: "main", Title: "x"})
	if err == nil {
		t.Fatal("want an error when gh is missing")
	}
	if !strings.Contains(err.Error(), "gh") {
		t.Errorf("error %v does not mention gh", err)
	}
}

func TestPRChecksSummarizesSuccess(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	prevGH := GHBinPath
	GHBinPath = writeFakeGH(t, filepath.Join(t.TempDir(), "args"),
		`[{"name":"build","state":"SUCCESS"},{"name":"test","state":"SUCCESS"}]`, 0)
	t.Cleanup(func() { GHBinPath = prevGH })

	res, err := PRChecks(t.Context(), repo, "idea/add-rate-limiting")
	if err != nil {
		t.Fatal(err)
	}
	if res.Overall != "success" {
		t.Errorf("overall = %q, want success", res.Overall)
	}
	if len(res.Checks) != 2 {
		t.Fatalf("checks = %v, want 2", res.Checks)
	}
}

func TestPRChecksSummarizesFailureOverPending(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	prevGH := GHBinPath
	// gh exits non-zero when a check failed or is pending -- that is itself
	// the answer, encoded in the JSON, not a call failure.
	GHBinPath = writeFakeGH(t, filepath.Join(t.TempDir(), "args"),
		`[{"name":"build","state":"SUCCESS"},{"name":"test","state":"FAILURE"},{"name":"lint","state":"PENDING"}]`, 8)
	t.Cleanup(func() { GHBinPath = prevGH })

	res, err := PRChecks(t.Context(), repo, "idea/add-rate-limiting")
	if err != nil {
		t.Fatal(err)
	}
	if res.Overall != "failure" {
		t.Errorf("overall = %q, want failure (a failure outranks a pending check)", res.Overall)
	}
}

func TestPRChecksSummarizesPending(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	prevGH := GHBinPath
	GHBinPath = writeFakeGH(t, filepath.Join(t.TempDir(), "args"),
		`[{"name":"build","state":"SUCCESS"},{"name":"test","state":"PENDING"}]`, 8)
	t.Cleanup(func() { GHBinPath = prevGH })

	res, err := PRChecks(t.Context(), repo, "idea/add-rate-limiting")
	if err != nil {
		t.Fatal(err)
	}
	if res.Overall != "pending" {
		t.Errorf("overall = %q, want pending", res.Overall)
	}
}

func TestPRChecksNoneWhenEmpty(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	prevGH := GHBinPath
	GHBinPath = writeFakeGH(t, filepath.Join(t.TempDir(), "args"), `[]`, 0)
	t.Cleanup(func() { GHBinPath = prevGH })

	res, err := PRChecks(t.Context(), repo, "idea/add-rate-limiting")
	if err != nil {
		t.Fatal(err)
	}
	if res.Overall != "none" {
		t.Errorf("overall = %q, want none", res.Overall)
	}
}

func TestPRChecksReportsMissingGH(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	prevGH := GHBinPath
	GHBinPath = filepath.Join(t.TempDir(), "no-such-gh-binary")
	t.Cleanup(func() { GHBinPath = prevGH })

	_, err := PRChecks(t.Context(), repo, "idea/add-rate-limiting")
	if err == nil {
		t.Fatal("want an error when gh is missing")
	}
}
