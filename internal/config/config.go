// Package config loads and validates the portable project configuration
// (.idea-harness/project.yaml and harness.yaml) against embedded JSON Schemas
// (§28, §30). It also enforces that secrets never live in the portable tree.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Dir is the portable config directory name inside a project.
const Dir = ".idea-harness"

// ProjectConfig is project.yaml (portable, committable).
type ProjectConfig struct {
	Name string `json:"name" yaml:"name"`
}

// NetworkPolicy is the v0.1 network mode (§46).
type NetworkPolicy string

const (
	NetworkDisabled NetworkPolicy = "disabled"
	NetworkEnabled  NetworkPolicy = "enabled"
)

func (n NetworkPolicy) Valid() bool {
	return n == NetworkDisabled || n == NetworkEnabled
}

// ReviewConfig is the review budget (§11, §16).
type ReviewConfig struct {
	MaxCycles int     `json:"max_cycles" yaml:"max_cycles"`
	MaxTokens int64   `json:"max_tokens" yaml:"max_tokens"`
	MaxCostUSD float64 `json:"max_cost_usd" yaml:"max_cost_usd"`
}

// ProviderConfig describes one model provider (§40). API keys are resolved
// from the environment variable named by APIKeyEnv — never stored inline.
// Type and BaseURL are optional: when empty they are derived from the provider
// key's preset (see providers.go).
type ProviderConfig struct {
	Type      ProviderType `json:"type" yaml:"type"`
	APIKeyEnv string       `json:"api_key_env" yaml:"api_key_env"`
	BaseURL   string       `json:"base_url" yaml:"base_url"`
	Models    []string     `json:"models" yaml:"models"`
}

// ResearchConfig controls the research web tool (§46). URLs are seed sources
// fetched (public-address-only) during the research stage when network is
// enabled.
type ResearchConfig struct {
	URLs []string `json:"urls" yaml:"urls"`
}

// ExecutorKind selects the coding executor the execute pipeline uses (§48–§52).
type ExecutorKind string

const (
	ExecutorInternal  ExecutorKind = "internal"
	ExecutorContainer ExecutorKind = "container"
	ExecutorOMP       ExecutorKind = "omp"
	ExecutorHermes    ExecutorKind = "hermes"
)

func (k ExecutorKind) Valid() bool {
	switch k {
	case ExecutorInternal, ExecutorContainer, ExecutorOMP, ExecutorHermes:
		return true
	}
	return false
}

// ExecutorConfig selects and configures the execute-pipeline executor. Image is
// the container image for the container kind; Model is the coding model for the
// OMP kind. Neither is read by the internal kind.
type ExecutorConfig struct {
	Kind  ExecutorKind `json:"kind" yaml:"kind"`
	Image string       `json:"image,omitempty" yaml:"image,omitempty"`
	Model string       `json:"model,omitempty" yaml:"model,omitempty"`
}

// HarnessConfig is harness.yaml (portable, committable).
type HarnessConfig struct {
	Network      NetworkPolicy             `json:"network" yaml:"network"`
	Review       ReviewConfig              `json:"review" yaml:"review"`
	Research     ResearchConfig            `json:"research" yaml:"research"`
	Providers    map[string]ProviderConfig `json:"providers" yaml:"providers"`
	DefaultModel string                    `json:"default_model" yaml:"default_model"`
	Executor     ExecutorConfig            `json:"executor" yaml:"executor"`
	// Models is the per-role model map: role name → "provider/model". Roles
	// without an entry fall back to default_model (§40). The coding "implementer"
	// role is a valid key here, letting OMP/Hermes run different models.
	Models map[string]string `json:"models" yaml:"models"`
}

// Config is the fully-loaded portable configuration for one project.
type Config struct {
	Project ProjectConfig
	Harness HarnessConfig
}

// Default returns the default configuration. The network is disabled by
// default so research never silently exfiltrates local content (§15). The
// executor defaults to the deterministic internal executor.
func Default() Config {
	return Config{
		Harness: HarnessConfig{
			Network:   NetworkDisabled,
			Review:    ReviewConfig{MaxCycles: 3},
			Providers: map[string]ProviderConfig{},
			Executor:  ExecutorConfig{Kind: ExecutorInternal},
		},
	}
}

// Load reads and validates project.yaml and harness.yaml from dir. Missing
// files fall back to Default(); present files are strictly validated against
// the raw document (so unknown fields and inline secrets are rejected, not
// silently dropped).
func Load(dir string) (Config, error) {
	cfg := Default()

	pBytes, pDoc, err := readDoc(filepath.Join(dir, "project.yaml"))
	if err != nil {
		return cfg, err
	}
	if pBytes != nil {
		if err := yaml.Unmarshal(pBytes, &cfg.Project); err != nil {
			return cfg, fmt.Errorf("config: parse project.yaml: %w", err)
		}
		if err := validateAgainst("project.yaml", projectSchema, pDoc); err != nil {
			return cfg, err
		}
		if err := rejectSecrets("", pDoc); err != nil {
			return cfg, err
		}
	}

	hBytes, hDoc, err := readDoc(filepath.Join(dir, "harness.yaml"))
	if err != nil {
		return cfg, err
	}
	if hBytes != nil {
		if err := yaml.Unmarshal(hBytes, &cfg.Harness); err != nil {
			return cfg, fmt.Errorf("config: parse harness.yaml: %w", err)
		}
		if err := validateAgainst("harness.yaml", harnessSchema, hDoc); err != nil {
			return cfg, err
		}
		if err := rejectSecrets("", hDoc); err != nil {
			return cfg, err
		}
	}

	cfg.applyDefaults()
	if !cfg.Harness.Network.Valid() {
		return cfg, fmt.Errorf("config: harness.network must be one of disabled|enabled, got %q", cfg.Harness.Network)
	}
	if !cfg.Harness.Executor.Kind.Valid() {
		return cfg, fmt.Errorf("config: harness.executor.kind must be one of internal|container|omp, got %q", cfg.Harness.Executor.Kind)
	}
	if cfg.Harness.Review.MaxCycles < 0 {
		return cfg, fmt.Errorf("config: harness.review.max_cycles must be >= 0")
	}
	return cfg, nil
}

// readDoc reads a YAML file and returns its raw bytes and its decoded document
// (as a JSON-compatible map). A missing file yields (nil, nil, nil).
func readDoc(path string) ([]byte, map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return data, doc, nil
}

func (c *Config) applyDefaults() {
	if c.Harness.Network == "" {
		c.Harness.Network = NetworkDisabled
	}
	if c.Harness.Review.MaxCycles == 0 && c.Harness.Review.MaxTokens == 0 && c.Harness.Review.MaxCostUSD == 0 {
		c.Harness.Review.MaxCycles = 3
	}
	if c.Harness.Executor.Kind == "" {
		c.Harness.Executor.Kind = ExecutorInternal
	}
}
