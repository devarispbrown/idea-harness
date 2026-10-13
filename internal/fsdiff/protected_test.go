package fsdiff

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestAutoExecutingPath(t *testing.T) {
	for _, p := range []string{
		".github/workflows/ci.yml",
		".GitHub/Workflows/release.yaml", // case-insensitive filesystems
		".github/actions/setup/action.yml",
		".gitlab-ci.yml",
		".circleci/config.yml",
		"Jenkinsfile",
		".travis.yml",
		"azure-pipelines.yml",
		"bitbucket-pipelines.yml",
		".buildkite/pipeline.yml",
		".drone.yml",
		".husky/pre-commit",
		".githooks/pre-push",
		".pre-commit-config.yaml",
		"lefthook.yml",
		".envrc",
		".devcontainer/devcontainer.json",
		".vscode/tasks.json",
		"./.github/workflows/ci.yml",
	} {
		if what, ok := AutoExecutingPath(p); !ok || what == "" {
			t.Errorf("%s: not recognised as auto-executing", p)
		}
	}
	for _, p := range []string{
		"Makefile",
		"package.json",
		"Dockerfile",
		".github/CODEOWNERS",
		".github/dependabot.yml",
		"docs/.github/workflows-guide.md",
		"src/.envrc.example",
		"ci/.gitlab-ci.yml", // only the root file is read by GitLab
		"Jenkinsfile.md",
		".vscode/settings.json",
		"github/workflows/ci.yml",
	} {
		if what, ok := AutoExecutingPath(p); ok {
			t.Errorf("%s: wrongly flagged as %s", p, what)
		}
	}
}

func TestEscapesLexical(t *testing.T) {
	root := t.TempDir()
	got := Escapes(root, []domain.FileChange{
		{Path: "src/ok.go"},
		{Path: "../outside.txt"},
		{Path: "/etc/passwd"},
		{Path: "src/../../x"},
		{Path: "a//b"},
		{Path: ""},
	})
	if len(got) != 5 {
		t.Fatalf("want 5 escapes (all but src/ok.go), got %d: %q", len(got), got)
	}
	for _, line := range got {
		if strings.HasPrefix(line, "src/ok.go") {
			t.Errorf("a contained path was reported: %s", line)
		}
	}
}

// TestEscapesThroughSymlink: the change set's path is lexically fine, but by
// apply time the workspace has a symlink on that path pointing outside it, or
// into .git/ -- the case only the real filesystem can reveal.
func TestEscapesThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "vendor")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".git", "hooks"), filepath.Join(root, "scripts")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "target"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(root, "config.js")); err != nil {
		t.Fatal(err)
	}

	got := Escapes(root, []domain.FileChange{
		{Path: "vendor/lib.go"},
		{Path: "scripts/pre-commit"},
		{Path: "config.js"},
		{Path: "src/fine.go"},
	})
	joined := strings.Join(got, "\n")
	if len(got) != 3 {
		t.Fatalf("want 3 escapes, got %d:\n%s", len(got), joined)
	}
	if !strings.Contains(joined, "vendor/lib.go: resolves outside the workspace") {
		t.Errorf("symlinked directory out of the workspace not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "scripts/pre-commit: resolves into the harness control plane (.git/hooks/pre-commit)") {
		t.Errorf("symlink into .git/ not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "config.js: resolves outside the workspace") {
		t.Errorf("a file that is itself a symlink out of the workspace not reported:\n%s", joined)
	}
}
