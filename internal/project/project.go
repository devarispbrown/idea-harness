// Package project initializes the portable project configuration
// (.idea-harness/) and manages the global/per-project runtime layout (§30).
package project

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/roles"
)

const (
	configDir   = config.Dir // ".idea-harness"
	projectYAML = "project.yaml"
	harnessYAML = "harness.yaml"
)

// gitignoreContent is written to .idea-harness/.gitignore by InitConfig
// (review defect D). It draws the line between what belongs in the user's
// history and what doesn't: project.yaml, harness.yaml, and roles/*.yaml are
// portable config, meant to be committed like any other project file --
// nothing here ignores them. tmp/ is the harness's own scratch space
// (fsdiff's atomic-write staging area, internal/fsdiff.writeAtomic), never
// meant to be committed at all, and standards/ policies/ schemas/ artifacts/
// are empty placeholders until a user populates them, so there is nothing
// there yet to make a decision about.
//
// Without this, a fresh .idea-harness/ is entirely untracked, and a
// founder-journey run turning on git.branch_per_initiative before ever
// running `git add .idea-harness` had EVERY apply refuse with "uncommitted
// changes outside this change set" naming the harness's own config -- fixed
// independently in internal/git (dirtyPathsOutside never reports a
// .idea-harness/ path as dirty at all), but a .gitignore here is still the
// right hygiene regardless: without it, a plain `git add -A` sweeps tmp/'s
// scratch files into the user's own commit.
const gitignoreContent = "# idea-harness's own scratch space -- never part of your project's history.\n" +
	"# project.yaml, harness.yaml, and roles/ above are meant to be committed.\n" +
	"/tmp/\n"

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

	// .gitignore (review defect D): drawn between committable config
	// (project.yaml, harness.yaml, roles/) and the harness's own local
	// scratch state (tmp/) -- see gitignoreContent's doc comment.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignoreContent), 0o644); err != nil {
		return fmt.Errorf("project: write %s: %w", filepath.Join(dir, ".gitignore"), err)
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
