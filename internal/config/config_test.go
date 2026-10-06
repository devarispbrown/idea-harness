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

// TestReviewRigorDefaultsToAuto is review B1's default.
func TestReviewRigorDefaultsToAuto(t *testing.T) {
	if Default().Harness.Review.Rigor != "auto" {
		t.Fatalf("Default() rigor = %q, want auto", Default().Harness.Review.Rigor)
	}
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.Review.Rigor != "auto" {
		t.Fatalf("Load() of an empty dir rigor = %q, want auto", c.Harness.Review.Rigor)
	}
}

// TestReviewRigorAcceptsEachLevel is review B1: quick|standard|thorough|auto.
func TestReviewRigorAcceptsEachLevel(t *testing.T) {
	for _, level := range []string{"auto", "quick", "standard", "thorough"} {
		dir := t.TempDir()
		writeFile(t, dir, "harness.yaml", "review:\n  rigor: "+level+"\n")
		c, err := Load(dir)
		if err != nil {
			t.Fatalf("rigor %q: %v", level, err)
		}
		if string(c.Harness.Review.Rigor) != level {
			t.Fatalf("rigor %q round-tripped as %q", level, c.Harness.Review.Rigor)
		}
	}
}

// TestReviewRigorRejectsUnknownValue is review B1's validation.
func TestReviewRigorRejectsUnknownValue(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "review:\n  rigor: extreme\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected an unknown review.rigor value to be rejected")
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

func TestLoadCheckImage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "project.yaml", "name: acme\n")
	writeFile(t, dir, "harness.yaml", "network: disabled\ncheck:\n  image: golang:1.25\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.Check.Image != "golang:1.25" {
		t.Fatalf("expected check.image golang:1.25, got %q", c.Harness.Check.Image)
	}
}

// TestExecuteMaxFilesDefaultsBelowTheKnownBadDiff is review C: the ICP re-run
// that restructured an 80-line service into cmd/+internal/ for a one-endpoint
// task produced a 15-file diff, which then hit the reviewer's own output
// ceiling. The built-in default must sit strictly below that known-bad
// count, unset in harness.yaml.
func TestExecuteMaxFilesDefaultsBelowTheKnownBadDiff(t *testing.T) {
	got := Default().Harness.Execute.EffectiveMaxFiles()
	if got <= 0 || got >= 15 {
		t.Fatalf("EffectiveMaxFiles() = %d, want a positive default strictly below 15", got)
	}
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got2 := c.Harness.Execute.EffectiveMaxFiles(); got2 != got {
		t.Fatalf("Load() of an empty dir EffectiveMaxFiles() = %d, want %d (Default()'s)", got2, got)
	}
}

// TestExecuteMaxFilesIsReachableFromConfig is review C's binding condition:
// "the limit must be reachable from config, and the message must name a knob
// that actually works" -- the review.max_tokens defect this explicitly
// echoes named a setting that did nothing. Setting execute.max_files in
// harness.yaml must change what EffectiveMaxFiles() reports, round-tripped
// through Load, not just through the struct literal.
func TestExecuteMaxFilesIsReachableFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "execute:\n  max_files: 25\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness.Execute.MaxFiles != 25 {
		t.Fatalf("expected execute.max_files 25, got %d", c.Harness.Execute.MaxFiles)
	}
	if got := c.Harness.Execute.EffectiveMaxFiles(); got != 25 {
		t.Fatalf("EffectiveMaxFiles() = %d, want 25", got)
	}
}

// TestExecuteMaxFilesRejectsNegative mirrors review.max_cycles' own
// validation: a nonsensical negative limit is refused at Load, not silently
// coerced.
func TestExecuteMaxFilesRejectsNegative(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "execute:\n  max_files: -1\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected a negative execute.max_files to be rejected")
	}
}
