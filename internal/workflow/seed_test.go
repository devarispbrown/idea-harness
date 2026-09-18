package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedSkipsWhatTheAgentMustNotHave(t *testing.T) {
	ws := t.TempDir() // not a git repository: the directory walk
	writeFile(t, filepath.Join(ws, "main.go"), "package main\n")
	writeFile(t, filepath.Join(ws, ".env.local"), "X=1\n")
	writeFile(t, filepath.Join(ws, "big.bin"), strings.Repeat("x", seedMaxFileBytes+1))
	writeFile(t, filepath.Join(ws, "node_modules", "dep", "index.js"), "x\n")
	writeFile(t, filepath.Join(ws, ".idea-harness", "harness.yaml"), "x\n")
	if err := os.Symlink("/etc/hosts", filepath.Join(ws, "hosts")); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	skipped, err := seedWorkRoot(t.Context(), ws, work, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "main.go")); err != nil {
		t.Errorf("source was not copied: %v", err)
	}
	for _, absent := range []string{".env.local", "big.bin", "hosts", "node_modules", ".idea-harness"} {
		if _, err := os.Lstat(filepath.Join(work, absent)); err == nil {
			t.Errorf("%s was copied", absent)
		}
	}
	got := seedSkipSummary(skipped)
	for _, want := range []string{"big.bin", "hosts"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is not reported as skipped:\n%s", want, got)
		}
	}
}
