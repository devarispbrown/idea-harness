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

// ErrNoRemote is returned by CreatePR when the repository has no git remote
// configured at all (ICP re-run gap: `idea pr` on such a repository dumped a
// raw git error -- "'origin' does not appear to be a git repository" -- with
// no indication of what was actually wrong or what to do about it).
// pushRemote's own "origin" fallback names a remote that must actually exist
// for that raw error to be avoidable; this is checked before ever shelling
// out to `git push`, rather than pattern-matching git's own (unversioned,
// locale-dependent) stderr text after the fact.
var ErrNoRemote = errors.New("git: this repository has no remote configured; add one (e.g. `git remote add origin <url>`) and push the branch, then run `idea pr` again")

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

// CreatePR pushes req.Branch to its configured upstream remote (or `origin`
// when none is configured -- review M7) with `-u`, so it is safe to call
// again against an already-pushed branch, and runs `gh pr create`, returning
// the new pull request's URL. `gh` prints exactly the URL on success; a
// caller with an existing PR for this branch should not call CreatePR again
// -- Engine.CreatePR is the idempotent entry point that checks
// domain.Initiative.PRURL first.
func CreatePR(ctx context.Context, workTree string, req PRRequest) (string, error) {
	if _, err := exec.LookPath(GHBinPath); err != nil {
		return "", ErrGHNotFound
	}
	remote, err := pushRemote(ctx, workTree, req.Branch)
	if err != nil {
		return "", err
	}
	if out, err := runGit(ctx, workTree, sanitizedPushEnv(), "push", "-u", remote, req.Branch); err != nil {
		// pushRemote already refuses when "origin" itself does not exist, but
		// a branch can carry a `branch.<name>.remote` config naming a remote
		// that was since removed -- still a "no remote configured" situation
		// from the user's point of view, just discovered one step later, at
		// push time, by git itself (ICP re-run gap: the raw text this used to
		// surface verbatim named neither the cause nor a fix).
		if looksLikeMissingRemote(out) {
			return "", ErrNoRemote
		}
		return "", fmt.Errorf("git: push %s %s: %w: %s", remote, req.Branch, err, strings.TrimSpace(out))
	}
	// gh's --head wants "owner:branch" when the branch lives on a fork --
	// a remote whose owner differs from the BASE repository's -- rather than
	// a plain branch name, which gh would otherwise resolve against the base
	// repository and fail to find (review M7, M6). This only works out the
	// box for github.com remotes; a non-GitHub host (or a remote URL gh/git
	// cannot parse an owner from) falls back to the plain branch name, which
	// is correct whenever the push remote and the base repository are the
	// same host+owner, and otherwise is a known limitation documented in
	// docs/cli.md.
	//
	// The base repository is "upstream" when that remote exists, else
	// "origin" (review M6): `gh repo fork --clone` sets origin to the FORK
	// and upstream to the PARENT, with no per-branch remote config at all,
	// so pushRemote() above correctly returns "origin" (the fork actually
	// does need the push) -- but comparing that against origin's own owner
	// as "the base" always found them equal (both are the fork) and never
	// prefixed --head, so gh resolved the bare branch name against upstream
	// (its own default base-repo choice in a fork clone) and found nothing.
	head := req.Branch
	baseRemote := "origin"
	if remoteExists(ctx, workTree, "upstream") {
		baseRemote = "upstream"
	}
	if forkOwner, oerr := remoteOwner(ctx, workTree, remote); oerr == nil && forkOwner != "" {
		baseOwner, _ := remoteOwner(ctx, workTree, baseRemote)
		if baseOwner == "" || !strings.EqualFold(baseOwner, forkOwner) {
			head = forkOwner + ":" + req.Branch
		}
	}
	args := []string{"pr", "create", "--head", head,
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

// pushRemote returns the remote CreatePR should push branch to: the branch's
// configured upstream remote (`branch.<name>.remote`) when set, else "origin"
// (review M7). Checking the branch's own local config, not merely origin,
// means a branch someone already set up to track a fork keeps pushing there
// on every subsequent `idea pr` call.
//
// The "origin" fallback is verified to actually exist (ICP re-run gap)
// before it is ever handed to `git push`: a repository with no remote at all
// has no `branch.<name>.remote` config either, so the old code returned
// "origin" unconditionally and let `git push -u origin <branch>` fail with
// git's own raw, unclassified error text.
func pushRemote(ctx context.Context, workTree, branch string) (string, error) {
	out, err := runGit(ctx, workTree, sanitizedEnv(), "config", "--get", "branch."+branch+".remote")
	if err == nil {
		if remote := strings.TrimSpace(out); remote != "" {
			return remote, nil
		}
	} else if !isExitCode(err, 1) {
		return "", fmt.Errorf("git: config branch.%s.remote: %w: %s", branch, err, strings.TrimSpace(out))
	}
	if !remoteExists(ctx, workTree, "origin") {
		return "", ErrNoRemote
	}
	return "origin", nil
}

// looksLikeMissingRemote reports whether git's push output describes a
// remote that does not exist, rather than any other push failure (rejected,
// no network, auth). Matched against git's own English-language wording,
// which is not a stable API -- this is a best-effort classification for a
// friendlier message, not a correctness boundary; an unmatched case still
// falls through to the raw (if uglier) git error rather than being
// misreported as something it is not.
func looksLikeMissingRemote(out string) bool {
	out = strings.ToLower(out)
	return strings.Contains(out, "does not appear to be a git repository") ||
		strings.Contains(out, "no configured push destination") ||
		strings.Contains(out, "repository not found")
}

// remoteExists reports whether a remote named name is configured (review
// M6: used to detect the `gh repo fork --clone` layout, where "upstream" is
// the parent repository and "origin" is the fork).
func remoteExists(ctx context.Context, workTree, name string) bool {
	_, err := runGit(ctx, workTree, sanitizedEnv(), "remote", "get-url", name)
	return err == nil
}

// remoteOwner extracts the "owner" segment from a GitHub remote URL (any of
// git@github.com:owner/repo.git, https://github.com/owner/repo(.git), or
// ssh://git@github.com/owner/repo.git). Returns "" for a non-GitHub remote or
// one remoteOwner cannot parse, rather than an error: an unresolvable owner
// just means CreatePR falls back to a plain branch name for --head.
func remoteOwner(ctx context.Context, workTree, remote string) (string, error) {
	out, err := runGit(ctx, workTree, sanitizedEnv(), "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("git: remote get-url %s: %w: %s", remote, err, strings.TrimSpace(out))
	}
	return parseGitHubOwner(strings.TrimSpace(out)), nil
}

// parseGitHubOwner parses the owner segment out of a github.com remote URL,
// or "" if url does not look like one.
func parseGitHubOwner(url string) string {
	url = strings.TrimSuffix(url, ".git")
	i := strings.Index(url, "github.com")
	if i < 0 {
		return ""
	}
	rest := url[i+len("github.com"):]
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimPrefix(rest, "/")
	rest = strings.TrimPrefix(rest, "/") // ssh://git@github.com//owner/repo, belt and suspenders
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0]
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

// authPassthroughVars are the caller's own authentication-related environment
// variables that `git push` and `gh` need to work non-interactively (review
// M7): SSH_AUTH_SOCK and GIT_SSH_COMMAND for an SSH-based push,
// XDG_CONFIG_HOME for gh's own config/credential file location, GH_HOST for
// which host GH_TOKEN authenticates against, and GH_TOKEN itself. None of
// these is a provider secret riding along by accident, which is the leak
// sanitizedEnv exists to prevent -- they are how the operator's own git/gh
// already authenticate outside the daemon, so passing them through is not
// the same hazard.
var authPassthroughVars = []string{"SSH_AUTH_SOCK", "GIT_SSH_COMMAND", "XDG_CONFIG_HOME", "GH_HOST", "GH_TOKEN"}

// withAuthPassthrough extends base with authPassthroughVars, each only when
// actually set in the daemon's own environment.
func withAuthPassthrough(base []string) []string {
	env := base
	for _, k := range authPassthroughVars {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// sanitizedPushEnv is sanitizedEnv plus authPassthroughVars, for the one git
// invocation (push) that needs to authenticate against a remote (review M7).
// Every other git call in this package keeps the plain sanitizedEnv, since
// none of them talk to a remote.
func sanitizedPushEnv() []string {
	return withAuthPassthrough(sanitizedEnv())
}

// sanitizedGHEnv is sanitizedEnv's allowlist (PATH/HOME/LANG,
// GIT_TERMINAL_PROMPT=0) plus authPassthroughVars and GITHUB_TOKEN (gh's
// older/alternate token variable name) passed through when set.
func sanitizedGHEnv() []string {
	env := withAuthPassthrough(sanitizedEnv())
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		env = append(env, "GITHUB_TOKEN="+v)
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
