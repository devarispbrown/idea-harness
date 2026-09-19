package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GHBinPath is the path to the GitHub CLI binary CreatePR and PRChecks
// invoke. A package-level variable so tests can substitute a fake, mirroring
// BinPath for git itself. Defaults to "gh", resolved via PATH.
var GHBinPath = "gh"

// ErrGHNotFound is returned by CreatePR and PRChecks when the GitHub CLI
// cannot be located, so a caller can give a clear, specific message rather
// than an opaque "exec: not found".
var ErrGHNotFound = errors.New("git: the GitHub CLI (`gh`) is not installed or not on PATH")

// PRRequest is what CreatePR needs to open a pull request.
type PRRequest struct {
	// Branch is the head branch, already checked out (git.branch_per_initiative
	// creates it) and pushed by CreatePR itself.
	Branch string
	// Base is the target branch; empty lets `gh` fall back to the
	// repository's default branch.
	Base  string
	Title string
	Body  string
}

// CreatePR pushes req.Branch to `origin` (with `-u`, so it is safe to call
// again against an already-pushed branch) and runs `gh pr create`, returning
// the new pull request's URL. `gh` prints exactly the URL on success; a
// caller with an existing PR for this branch should not call CreatePR again
// -- Engine.CreatePR is the idempotent entry point that checks
// domain.Initiative.PRURL first.
func CreatePR(ctx context.Context, workTree string, req PRRequest) (string, error) {
	if _, err := exec.LookPath(GHBinPath); err != nil {
		return "", ErrGHNotFound
	}
	if out, err := runGit(ctx, workTree, sanitizedEnv(), "push", "-u", "origin", req.Branch); err != nil {
		return "", fmt.Errorf("git: push %s: %w: %s", req.Branch, err, strings.TrimSpace(out))
	}
	args := []string{"pr", "create", "--head", req.Branch,
		"--title", sanitizeTitle(req.Title), "--body", req.Body}
	if req.Base != "" {
		args = append(args, "--base", req.Base)
	}
	out, err := runGH(ctx, workTree, args...)
	if err != nil {
		return "", fmt.Errorf("gh: pr create: %w: %s", err, strings.TrimSpace(out))
	}
	url := lastNonEmptyLine(out)
	if url == "" {
		return "", fmt.Errorf("gh: pr create produced no URL:\n%s", out)
	}
	return url, nil
}

// CheckStatus is one CI check's state, from `gh pr checks --json`.
type CheckStatus struct {
	Name  string `json:"name"`
	State string `json:"state"` // e.g. SUCCESS, FAILURE, PENDING, SKIPPED
	Link  string `json:"link,omitempty"`
}

// PRChecksResult summarizes `gh pr checks` for one pull request.
type PRChecksResult struct {
	// Overall is "success" (every check succeeded), "failure" (at least one
	// failed), "pending" (none failed, at least one still running), or "none"
	// (no checks reported at all -- a brand-new PR, or a repository with no
	// CI configured).
	Overall string        `json:"overall"`
	Checks  []CheckStatus `json:"checks"`
}

// PRChecks reports the latest CI status for ref (a branch name or PR number)
// via `gh pr checks --json`. `gh` exits non-zero when any check failed or is
// still pending -- that is itself the answer, not a call failure, so only an
// unparsable result (or gh/PR genuinely missing) is reported as an error.
func PRChecks(ctx context.Context, workTree, ref string) (PRChecksResult, error) {
	if _, err := exec.LookPath(GHBinPath); err != nil {
		return PRChecksResult{}, ErrGHNotFound
	}
	out, runErr := runGH(ctx, workTree, "pr", "checks", ref, "--json", "name,state,link")
	var checks []CheckStatus
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &checks); jsonErr != nil {
		if runErr != nil {
			return PRChecksResult{}, fmt.Errorf("gh: pr checks: %w: %s", runErr, strings.TrimSpace(out))
		}
		return PRChecksResult{}, fmt.Errorf("gh: pr checks: parse: %w", jsonErr)
	}
	return summarizeChecks(checks), nil
}

func summarizeChecks(checks []CheckStatus) PRChecksResult {
	res := PRChecksResult{Checks: checks, Overall: "none"}
	if len(checks) == 0 {
		return res
	}
	res.Overall = "success"
	for _, c := range checks {
		switch strings.ToUpper(c.State) {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED":
			res.Overall = "failure"
		case "PENDING", "QUEUED", "IN_PROGRESS", "WAITING", "REQUESTED", "STALE":
			if res.Overall == "success" {
				res.Overall = "pending"
			}
		}
	}
	return res
}

// runGH executes the `gh` binary with a sanitized environment and returns its
// combined output.
func runGH(ctx context.Context, workTree string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, GHBinPath, args...)
	cmd.Dir = workTree
	cmd.Env = sanitizedGHEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sanitizedGHEnv is sanitizedEnv's allowlist (PATH/HOME/LANG,
// GIT_TERMINAL_PROMPT=0) plus GH_TOKEN/GITHUB_TOKEN passed through when set.
// Those two are `gh`'s own explicit authentication mechanism -- not a
// provider secret riding along by accident, which is the leak sanitizedEnv
// exists to prevent -- so passing them through is not the same hazard.
func sanitizedGHEnv() []string {
	env := sanitizedEnv()
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// sanitizeTitle collapses newlines so a model-authored initiative title can
// never smuggle extra `gh pr create` argument-looking content into a single
// `--title` value. Mirrors workflow's sanitizeCommitTitle for the same reason,
// kept separate to avoid an import cycle (workflow already imports git).
func sanitizeTitle(title string) string {
	title = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(title)
	return strings.TrimSpace(title)
}

// lastNonEmptyLine returns the last non-blank line of s, trimmed. `gh pr
// create`'s only guaranteed stdout on success is the PR URL as its final line.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
