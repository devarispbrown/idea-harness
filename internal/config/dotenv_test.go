package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	content := `# comment
DEEPSEEK_API_KEY=sk-deepseek-123
export OPENAI_API_KEY="sk-openai-456"
GEMINI_API_KEY='sk-gemini-789'

EMPTY=
`
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadDotEnv(dir)
	if got["DEEPSEEK_API_KEY"] != "sk-deepseek-123" {
		t.Fatalf("plain key: %q", got["DEEPSEEK_API_KEY"])
	}
	if got["OPENAI_API_KEY"] != "sk-openai-456" {
		t.Fatalf("quoted export key: %q", got["OPENAI_API_KEY"])
	}
	if got["GEMINI_API_KEY"] != "sk-gemini-789" {
		t.Fatalf("single-quoted key: %q", got["GEMINI_API_KEY"])
	}
	if _, ok := got["EMPTY"]; ok {
		t.Fatal("empty value should be skipped")
	}
}

func TestLoadDotEnvMissing(t *testing.T) {
	got := LoadDotEnv(t.TempDir())
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestSetDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	// Create a fresh file and add a key.
	if err := SetDotEnv(dir, "ANTHROPIC_API_KEY", "sk-ant-1"); err != nil {
		t.Fatal(err)
	}
	if got := LoadDotEnv(dir)["ANTHROPIC_API_KEY"]; got != "sk-ant-1" {
		t.Fatalf("new key = %q", got)
	}

	// Replace an existing (export-quoted) key in place.
	if err := os.WriteFile(path, []byte("# comment\nexport ANTHROPIC_API_KEY=\"old\"\nKEEP=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetDotEnv(dir, "ANTHROPIC_API_KEY", "sk-ant-2"); err != nil {
		t.Fatal(err)
	}
	got := LoadDotEnv(dir)
	if got["ANTHROPIC_API_KEY"] != "sk-ant-2" {
		t.Fatalf("replaced key = %q", got["ANTHROPIC_API_KEY"])
	}
	if got["KEEP"] != "yes" {
		t.Fatalf("unrelated line lost: %v", got)
	}
	// The comment and the KEEP line survive, and the file is still 0600.
	b, _ := os.ReadFile(path)
	if !contains(string(b), "# comment") || !contains(string(b), "KEEP=yes") {
		t.Fatalf("unexpected file content:\n%s", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
