package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefault(t *testing.T) {
	c := Default()
	if c.Harness.Network != NetworkDisabled {
		t.Fatalf("network must default to disabled, got %q", c.Harness.Network)
	}
	if c.Harness.Review.MaxCycles != 3 {
		t.Fatalf("max_cycles must default to 3, got %d", c.Harness.Review.MaxCycles)
	}
}

func TestLoadEmptyDir(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.Network != NetworkDisabled {
		t.Fatalf("expected default network, got %q", c.Harness.Network)
	}
}

func TestLoadValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "project.yaml", "name: acme\n")
	writeFile(t, dir, "harness.yaml", "network: enabled\nreview:\n  max_cycles: 4\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Project.Name != "acme" {
		t.Fatalf("expected project name acme, got %q", c.Project.Name)
	}
	if c.Harness.Network != NetworkEnabled {
		t.Fatalf("expected enabled, got %q", c.Harness.Network)
	}
	if c.Harness.Review.MaxCycles != 4 {
		t.Fatalf("expected max_cycles 4, got %d", c.Harness.Review.MaxCycles)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "network: enabled\nbogus_field: true\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected unknown field to be rejected by schema")
	}
}

func TestLoadRejectsInvalidNetwork(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "network: maybe\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected invalid network to be rejected")
	}
}

func TestRejectInlineSecret(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "providers:\n  openai:\n    api_key: sk-secret\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected inline api_key to be rejected")
	}
}

func TestAllowEnvReference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "providers:\n  openai:\n    api_key_env: OPENAI_API_KEY\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.Providers["openai"].APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("expected api_key_env to be preserved, got %q", c.Harness.Providers["openai"].APIKeyEnv)
	}
}
