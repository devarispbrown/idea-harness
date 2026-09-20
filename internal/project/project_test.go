package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitConfigWritesGitignore pins the review defect D fix: `idea init`
// writes .idea-harness/.gitignore drawing the line between committable
// config (project.yaml, harness.yaml, roles/) and the harness's own local
// scratch state (tmp/, used by fsdiff's atomic write) -- without it, a fresh
// .idea-harness/ tree is entirely untracked and a plain `git add -A` sweeps
// scratch files into a user's own commit right along with real config.
func TestInitConfigWritesGitignore(t *testing.T) {
	root := t.TempDir()
	if err := InitConfig(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".idea-harness", ".gitignore"))
	if err != nil {
		t.Fatalf("read .idea-harness/.gitignore: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "/tmp/") {
		t.Errorf(".gitignore = %q, want it to ignore tmp/ (the harness's own scratch space)", content)
	}
	// It must NOT ignore the files that are meant to be committed -- checked
	// against the actual pattern lines only, not the explanatory comments
	// (which legitimately name harness.yaml/project.yaml/roles/ in prose).
	var patternLines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patternLines = append(patternLines, line)
	}
	for _, committable := range []string{"harness.yaml", "project.yaml", "roles"} {
		for _, line := range patternLines {
			if strings.Contains(line, committable) {
				t.Errorf("pattern line %q ignores %s, which is meant to be committed", line, committable)
			}
		}
	}
}

// TestInitConfigCreatesCommittableConfig is a baseline sanity check that
// InitConfig still produces exactly the files/dirs the fix's own reasoning
// depends on: harness.yaml, project.yaml, and roles/ actually exist.
func TestInitConfigCreatesCommittableConfig(t *testing.T) {
	root := t.TempDir()
	if err := InitConfig(root); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(root, ".idea-harness", "harness.yaml"),
		filepath.Join(root, ".idea-harness", "project.yaml"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, ".idea-harness", "roles"))
	if err != nil || len(entries) == 0 {
		t.Errorf("expected .idea-harness/roles/ to be populated, err=%v entries=%d", err, len(entries))
	}
}
