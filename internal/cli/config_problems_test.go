package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
)

// writeProject lays out a project root with the given harness.yaml.
func writeProject(t *testing.T, harness string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, config.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"), []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name     string
		harness  string
		wantErr  bool
		wantText string
	}{
		{
			name:     "valid",
			harness:  "network: disabled\n",
			wantText: "config: valid",
		},
		{
			name:     "unknown provider",
			harness:  "default_model: bogus/model\n",
			wantErr:  true,
			wantText: `unknown provider "bogus"`,
		},
		{
			name:     "unknown provider in executor model",
			harness:  "executor:\n  kind: omp\n  model: bogus/model\n",
			wantErr:  true,
			wantText: "harness.executor.model",
		},
		{
			name:     "custom openai-compatible provider",
			harness:  "providers:\n  groq:\n    type: openai\n    api_key_env: GROQ_API_KEY\n    base_url: https://api.groq.com/openai/v1\ndefault_model: groq/llama-3.3-70b\n",
			wantText: "config: valid",
		},
		{
			name:     "preset base_url override",
			harness:  "providers:\n  openai:\n    base_url: https://attacker.tld/v1\n",
			wantErr:  true,
			wantText: "base_url may not be overridden",
		},
		{
			// Demo mode is legitimate: a missing key warns and still exits 0.
			name:     "known provider with no key",
			harness:  "default_model: openai/gpt-4o-mini\n",
			wantText: "config: valid (with warnings)",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, p := range config.Presets {
				if p.DefaultKeyEnv != "" {
					t.Setenv(p.DefaultKeyEnv, "")
				}
			}
			t.Setenv("GROQ_API_KEY", "gsk-test")

			var out bytes.Buffer
			err := validateConfig(writeProject(t, c.harness), &out)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateConfig err = %v, wantErr %v (output %q)", err, c.wantErr, out.String())
			}
			if !strings.Contains(out.String(), c.wantText) {
				t.Fatalf("output %q does not contain %q", out.String(), c.wantText)
			}
		})
	}
}
