// Package project initializes the portable project configuration
// (.idea-harness/) and manages the global/per-project runtime layout (§30).
package project

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/roles"
)

const (
	configDir   = config.Dir // ".idea-harness"
	projectYAML = "project.yaml"
	harnessYAML = "harness.yaml"
)

var subdirs = []string{"roles", "standards", "policies", "schemas", "artifacts"}

// InitConfig creates the portable .idea-harness/ tree in rootPath. It never
// initializes Git, creates a remote, or pushes (§24).
func InitConfig(rootPath string) error {
	dir := filepath.Join(rootPath, configDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("project: mkdir %s: %w", dir, err)
	}
	for _, s := range subdirs {
		if err := os.MkdirAll(filepath.Join(dir, s), 0o755); err != nil {
			return fmt.Errorf("project: mkdir %s: %w", s, err)
		}
	}

	// project.yaml
	name := filepath.Base(filepath.Clean(rootPath))
	pCfg := config.ProjectConfig{Name: name}
	if err := writeYAML(filepath.Join(dir, projectYAML), pCfg); err != nil {
		return err
	}

	// harness.yaml (safe defaults: network disabled, internal executor)
	hCfg := config.Default().Harness
	hCfg.Network = config.NetworkDisabled
	hCfg.Review = config.ReviewConfig{MaxCycles: 3}
	hCfg.Providers = map[string]config.ProviderConfig{}
	if err := writeYAML(filepath.Join(dir, harnessYAML), hCfg); err != nil {
		return err
	}

	// Instantiate bundled role contracts into roles/.
	for _, name := range roles.Names() {
		rc, err := roles.Load(name)
		if err != nil {
			return err
		}
		if err := writeYAML(filepath.Join(dir, "roles", name+".yaml"), rc); err != nil {
			return err
		}
	}
	return nil
}

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("project: marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("project: write %s: %w", path, err)
	}
	return nil
}
