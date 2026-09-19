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

// TestCreatePRUsesBranchUpstreamRemoteWhenConfigured is review M7: a branch
// already configured to track a remote other than origin (e.g. a
// contributor's own fork) must be pushed there, not to origin.
func TestCreatePRUsesBranchUpstreamRemoteWhenConfigured(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	fakeRemoteRepo(t, repo) // origin -- must NOT receive the push
	fork := t.TempDir()
	if out, err := gitCmd(t, fork, "init", "-q", "--bare").CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	gitOut(t, repo, "remote", "add", "fork", fork)
	gitOut(t, repo, "checkout", "-q", "-b", "idea/add-rate-limiting")
	gitOut(t, repo, "config", "branch.idea/add-rate-limiting.remote", "fork")

	argsPath := filepath.Join(t.TempDir(), "args")
	prevGH := GHBinPath
	GHBinPath = writeFakeGH(t, argsPath, `[]`, 0)
	t.Cleanup(func() { GHBinPath = prevGH })

	if _, err := CreatePR(t.Context(), repo, PRRequest{Branch: "idea/add-rate-limiting", Title: "x"}); err != nil {
		t.Fatal(err)
	}

	if got := gitOut(t, repo, "ls-remote", "--heads", "fork", "idea/add-rate-limiting"); got == "" {
		t.Error("branch was not pushed to the configured upstream remote (fork)")
	}
	if got := gitOut(t, repo, "ls-remote", "--heads", "origin", "idea/add-rate-limiting"); got != "" {
		t.Error("branch was pushed to origin instead of the configured upstream remote (fork)")
	}
}

// TestCreatePRPassesThroughAuthEnv is review M7: SSH_AUTH_SOCK,
// GIT_SSH_COMMAND, XDG_CONFIG_HOME, GH_HOST and GH_TOKEN must reach both
// `git push` and `gh`, while an ordinary provider secret in the daemon's
// environment must not.
func TestCreatePRPassesThroughAuthEnv(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	gitOut(t, repo, "checkout", "-q", "-b", "idea/add-rate-limiting")

	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-agent.sock")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /fake/key")
	t.Setenv("XDG_CONFIG_HOME", "/fake/xdg")
	t.Setenv("GH_HOST", "github.example.com")
	t.Setenv("GH_TOKEN", "gh-fake-token")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-should-not-leak")

	pushEnvPath := filepath.Join(t.TempDir(), "push-env")
	prevBin := BinPath
	BinPath = writeFakeGitRecordingPushEnv(t, pushEnvPath)
	t.Cleanup(func() { BinPath = prevBin })

	ghEnvPath := filepath.Join(t.TempDir(), "gh-env")
	prevGH := GHBinPath
	GHBinPath = writeFakeGHRecordingEnv(t, ghEnvPath)
	t.Cleanup(func() { GHBinPath = prevGH })

	if _, err := CreatePR(t.Context(), repo, PRRequest{Branch: "idea/add-rate-limiting", Title: "x"}); err != nil {
		t.Fatal(err)
	}

	for _, envPath := range []string{pushEnvPath, ghEnvPath} {
		raw, err := os.ReadFile(envPath)
		if err != nil {
			t.Fatalf("read %s: %v", envPath, err)
		}
		env := string(raw)
		for _, want := range []string{"SSH_AUTH_SOCK=/tmp/fake-agent.sock", "GIT_SSH_COMMAND=ssh -i /fake/key", "XDG_CONFIG_HOME=/fake/xdg", "GH_HOST=github.example.com", "GH_TOKEN=gh-fake-token"} {
			if !strings.Contains(env, want) {
				t.Errorf("%s: missing %q:\n%s", envPath, want, env)
			}
		}
		if strings.Contains(env, "sk-test-should-not-leak") {
			t.Errorf("%s: leaked the provider secret:\n%s", envPath, env)
		}
	}
}

// writeFakeGitRecordingPushEnv writes a fake git binary that dumps its
// environment to envPath on `push` and no-ops otherwise.
func writeFakeGitRecordingPushEnv(t *testing.T, envPath string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/usr/bin/env bash
if [ "$1" = "push" ]; then
  env | sort > '%s'
  exit 0
fi
exit 1
`, envPath)
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return path
}

// writeFakeGHRecordingEnv writes a fake gh binary that dumps its environment
// to envPath and answers `pr create` deterministically.
func writeFakeGHRecordingEnv(t *testing.T, envPath string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/usr/bin/env bash
env | sort > '%s'
if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo "https://github.com/example/repo/pull/42"
  exit 0
fi
exit 1
`, envPath)
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return path
}

// TestCreatePRUsesOwnerColonBranchForAFork is review M7: when the push
// remote is a github.com fork with a different owner than origin, gh's
// --head must be given the "owner:branch" form.
func TestCreatePRUsesOwnerColonBranchForAFork(t *testing.T) {
	repo := initRepoOnBranch(t, "main")
	// origin is never pushed to in this test (the branch's upstream remote is
	// "fork"), so its URL only needs to be readable by `git remote get-url`
	// for owner parsing -- it need not be a reachable remote.
	gitOut(t, repo, "remote", "add", "origin", "git@github.com:upstream-owner/repo.git")
	fork := t.TempDir()
	if out, err := gitCmd(t, fork, "init", "-q", "--bare").CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	gitOut(t, repo, "remote", "add", "fork", fork)
	// fork's URL is a local bare-repo path so the push above succeeds for
	// real; swap it for a github.com URL only for the owner-parsing calls by
	// pointing pushURL at the local path but overriding what remoteOwner
	// sees is not possible without a second remote name, so instead: push
	// really happens against the bare repo (any name resolves fine for
	// `git push`), and a *second*, github.com-shaped URL is layered on for
	// owner parsing via `git remote set-url --push` is unnecessary here --
	// `git remote get-url` (fetch URL, no --push) is what remoteOwner reads,
	// so set the fetch URL to the GitHub shape and push URL to the real bare
	// repo.
	gitOut(t, repo, "remote", "set-url", "fork", "git@github.com:fork-owner/repo.git")
	gitOut(t, repo, "remote", "set-url", "--push", "fork", fork)
	gitOut(t, repo, "checkout", "-q", "-b", "idea/add-rate-limiting")
	gitOut(t, repo, "config", "branch.idea/add-rate-limiting.remote", "fork")

	argsPath := filepath.Join(t.TempDir(), "args")
	prevGH := GHBinPath
	GHBinPath = writeFakeGH(t, argsPath, `[]`, 0)
	t.Cleanup(func() { GHBinPath = prevGH })

	if _, err := CreatePR(t.Context(), repo, PRRequest{Branch: "idea/add-rate-limiting", Title: "x"}); err != nil {
		t.Fatal(err)
	}

	argsRaw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argsRaw), "--head\nfork-owner:idea/add-rate-limiting") {
		t.Errorf("gh args do not use the owner:branch form for the fork:\n%s", string(argsRaw))
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
