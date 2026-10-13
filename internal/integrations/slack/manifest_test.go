package slack

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// The shipped manifest is what a user pastes into Slack. It must keep Socket
// Mode and interactivity on (the integration has no Request URL to fall back
// to) and request exactly the scopes the code uses: chat:write for
// chat.postMessage, commands for /idea. A scope added "just in case" is a
// scope every install grants for nothing.
func TestSlackManifestMatchesWhatTheCodeUses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "integrations", "slack-manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Features struct {
			SlashCommands []struct {
				Command string `yaml:"command"`
			} `yaml:"slash_commands"`
		} `yaml:"features"`
		OAuth struct {
			Scopes struct {
				Bot []string `yaml:"bot"`
			} `yaml:"scopes"`
		} `yaml:"oauth_config"`
		Settings struct {
			Interactivity struct {
				Enabled bool `yaml:"is_enabled"`
			} `yaml:"interactivity"`
			SocketMode bool `yaml:"socket_mode_enabled"`
		} `yaml:"settings"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Settings.SocketMode || !m.Settings.Interactivity.Enabled {
		t.Fatal("manifest must enable socket mode and interactivity")
	}
	scopes := slices.Clone(m.OAuth.Scopes.Bot)
	slices.Sort(scopes)
	if !slices.Equal(scopes, []string{"chat:write", "commands"}) {
		t.Fatalf("bot scopes = %v; want exactly chat:write, commands", scopes)
	}
	if len(m.Features.SlashCommands) != 1 || m.Features.SlashCommands[0].Command != "/idea" {
		t.Fatalf("slash commands = %+v", m.Features.SlashCommands)
	}
}
