// Package roles loads versioned Role Contracts (§9). Contracts are YAML and
// embedded in the binary; they define competencies, required behaviors,
// artifact expectations, rubrics, required checks, and tool requirements —
// not merely "act like X" prompts.
package roles

import (
	"embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed contracts/*.yaml
var contractsFS embed.FS

// RoleContract is a versioned staff-level role definition (§9).
type RoleContract struct {
	Name         string   `yaml:"name"`
	Version      string   `yaml:"version"`
	Competencies []string `yaml:"competencies"`
	Behaviors    []string `yaml:"behaviors"`
	Artifacts    []string `yaml:"artifacts"`
	Rubrics      []string `yaml:"rubrics"`
	Checks       []string `yaml:"checks"`
	Tools        []string `yaml:"tools"`
	Model        string   `yaml:"model"`
}

// Names returns the available role names, sorted.
func Names() []string {
	entries, err := contractsFS.ReadDir("contracts")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, trimYAML(e.Name()))
	}
	sort.Strings(out)
	return out
}

// Load returns one role contract by name.
func Load(name string) (RoleContract, error) {
	data, err := contractsFS.ReadFile("contracts/" + name + ".yaml")
	if err != nil {
		return RoleContract{}, fmt.Errorf("roles: unknown role %q", name)
	}
	var rc RoleContract
	if err := yaml.Unmarshal(data, &rc); err != nil {
		return RoleContract{}, fmt.Errorf("roles: parse %s: %w", name, err)
	}
	if rc.Name == "" || rc.Version == "" {
		return RoleContract{}, fmt.Errorf("roles: %s missing name/version", name)
	}
	return rc, nil
}

// Must returns a role contract, panicking on error (for internal wiring).
func Must(name string) RoleContract {
	rc, err := Load(name)
	if err != nil {
		panic(err)
	}
	return rc
}

func trimYAML(s string) string {
	if len(s) > 5 && s[len(s)-5:] == ".yaml" {
		return s[:len(s)-5]
	}
	return s
}
