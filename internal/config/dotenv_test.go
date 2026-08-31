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
