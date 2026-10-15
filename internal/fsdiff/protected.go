package fsdiff

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// autoExecutingPaths are workspace paths whose content runs without anyone
// invoking it: on a CI runner when the change is pushed, in a git hook on the
// developer's next commit, or in the shell or editor when the directory is
// opened. A change set touching one is not denied -- an initiative may
// legitimately be about CI -- but it is never approved silently either: Execute
// raises a blocking `protected_path` finding that a human has to sign off
// (docs/security-model.md).
//
// Unlike controlPlaneDirs these are NOT excluded from a change set. The
// control plane governs the harness itself and nothing legitimate needs an
// agent to edit it; these are part of the user's project and sometimes the very
// thing the task is about.
//
// Deliberately NOT on the list: Makefile, package.json (npm lifecycle
// scripts), setup.py, build.gradle, Dockerfile and other build definitions.
// They run code too, but only when someone builds, and they are edited by a
// large share of ordinary tasks -- a finding on every one of those would teach
// a reviewer to dismiss the finding, which is how a gate stops being one.
//
// Each entry is matched case-insensitively against the slash-separated path
// relative to the workspace root, the same way isControlPlane is (a
// case-insensitive filesystem treats .GitHub/Workflows as .github/workflows).
// A trailing "/" marks a directory prefix; anything else is an exact file.
var autoExecutingPaths = []struct{ path, what string }{
	// CI: executed on the provider's runners, usually with repository
	// secrets in scope.
	{".github/workflows/", "GitHub Actions workflow"},
	{".github/actions/", "GitHub Actions local action (runs inside workflows)"},
	{".gitlab-ci.yml", "GitLab CI pipeline"},
	{".circleci/", "CircleCI pipeline"},
	{"Jenkinsfile", "Jenkins pipeline"},
	{".travis.yml", "Travis CI pipeline"},
	{"azure-pipelines.yml", "Azure Pipelines definition"},
	{"bitbucket-pipelines.yml", "Bitbucket Pipelines definition"},
	{".buildkite/", "Buildkite pipeline"},
	{".drone.yml", "Drone CI pipeline"},
	// Git hooks installed from the working tree (.git/hooks itself is
	// control plane and never reaches a change set at all).
	{".husky/", "Husky git hook"},
	{".githooks/", "git hook (core.hooksPath)"},
	{".pre-commit-config.yaml", "pre-commit hook configuration"},
	{"lefthook.yml", "Lefthook git hook configuration"},
	{".lefthook.yml", "Lefthook git hook configuration"},
	// Run by the developer's tooling when the directory is entered/opened.
	{".envrc", "direnv script (runs on cd)"},
	{".devcontainer/", "dev container definition (runs lifecycle commands)"},
	{".vscode/tasks.json", "VS Code tasks (can run on folder open)"},
}

// AutoExecutingPath reports whether a workspace-relative path is one whose
// content runs automatically, and what it is.
func AutoExecutingPath(rel string) (what string, ok bool) {
	p := strings.ToLower(path.Clean(filepath.ToSlash(rel)))
	for _, e := range autoExecutingPaths {
		want := strings.ToLower(e.path)
		if strings.HasSuffix(want, "/") {
			if strings.HasPrefix(p, want) {
				return e.what, true
			}
			continue
		}
		if p == want {
			return e.what, true
		}
	}
	return "", false
}

// Escapes reports every change whose path does not stay inside root, one line
// per offending path, or nil when all are contained. It is the apply-time
// half of the `path_escape` system deny: run against the real workspace
// before anything is written, so a refusal is a finding on the change set
// rather than a Prepare error halfway through an apply.
//
// A change set's paths come from the orchestrator's own snapshot of the work
// root, which never follows symlinks, so a path cannot be MADE to escape at
// Execute. What can change is the workspace it is applied to: a directory on
// the path may by then be a symlink pointing outside root, or into .git/ or
// .idea-harness/ -- which the lexical isControlPlane check cannot see. A patch
// built some other way could also name an absolute or ".." path outright.
// Prepare/Commit still resolve every path through security.ResolveWithin and
// fail closed; this exists so the refusal carries a deny code and says why.
func Escapes(root string, changes []domain.FileChange) []string {
	rootResolved, rootErr := filepath.Abs(root)
	if rootErr == nil {
		rootResolved, rootErr = filepath.EvalSymlinks(rootResolved)
	}
	var out []string
	for _, c := range changes {
		if reason := escapeReason(root, rootResolved, rootErr, c.Path); reason != "" {
			out = append(out, fmt.Sprintf("%s: %s", c.Path, reason))
		}
	}
	return out
}

func escapeReason(root, rootResolved string, rootErr error, rel string) string {
	slash := filepath.ToSlash(rel)
	switch {
	case rel == "":
		return "empty path"
	case filepath.IsAbs(rel) || strings.HasPrefix(slash, "/") || filepath.VolumeName(rel) != "":
		return "absolute path"
	}
	clean := path.Clean(slash)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean != slash {
		return "not a clean workspace-relative path"
	}
	dest, err := security.ResolveWithin(root, rel)
	if errors.Is(err, security.ErrPathEscape) {
		return "resolves outside the workspace (a symlink on the path points out of it)"
	}
	if err != nil {
		return fmt.Sprintf("cannot be resolved inside the workspace: %v", err)
	}
	if rootErr == nil {
		if r, err := filepath.Rel(rootResolved, dest); err == nil && isControlPlane(r) {
			return fmt.Sprintf("resolves into the harness control plane (%s) through a symlink", filepath.ToSlash(r))
		}
	}
	return ""
}
