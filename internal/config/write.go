package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WriteHarness writes harness.yaml to dir, applying defaults first. It is the
// single write path for provider/model configuration (§47: no secrets written —
// only *_env references are permitted).
func WriteHarness(dir string, h HarnessConfig) error {
	cfg := Config{Harness: h}
	cfg.applyDefaults()
	if !cfg.Harness.Network.Valid() {
		return fmt.Errorf("config: network must be disabled|enabled")
	}

	data, err := yaml.Marshal(cfg.Harness)
	if err != nil {
		return fmt.Errorf("config: marshal harness: %w", err)
	}
	path := filepath.Join(dir, "harness.yaml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}
