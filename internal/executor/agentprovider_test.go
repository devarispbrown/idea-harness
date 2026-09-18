package executor

import (
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// A providers: entry is read before the preset, as the model registry reads it.
// Checking the preset first sent the agent to the preset's port and key
// variable while every other model call used the configured ones.
func TestAgentProviderHonoursHarnessOverrides(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"ollama": {BaseURL: "http://localhost:11500/v1"},
		"openai": {APIKeyEnv: "WORK_OPENAI_KEY"},
	}
	p, err := ResolveAgentProvider("ollama/qwen3.8", "", providers)
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != "http://localhost:11500/v1" {
		t.Errorf("ollama base URL = %q, want the configured port 11500", p.BaseURL)
	}
	p, err = ResolveAgentProvider("openai/gpt-5.6-luna", "", providers)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyEnv != "WORK_OPENAI_KEY" {
		t.Errorf("openai key env = %q, want the configured WORK_OPENAI_KEY", p.KeyEnv)
	}
	if p.BaseURL == "" {
		t.Error("a key-only override lost the preset's base URL")
	}
}

// A hand-edited entry the registry would reject must not reach the agent,
// which would receive its key.
func TestAgentProviderRejectsAnInvalidEntry(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"sketchy": {BaseURL: "http://203.0.113.5/v1", APIKeyEnv: "SKETCHY_KEY"},
	}
	if _, err := ResolveAgentProvider("sketchy/m", "", providers); err == nil {
		t.Fatal("plain http to a non-loopback host was handed to the agent")
	}
}

func TestAgentProviderResolvesACustomProvider(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"groq": {Type: config.ProviderOpenAI, BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY"},
	}
	p, err := ResolveAgentProvider("groq/llama-4", "", providers)
	if err != nil {
		t.Fatal(err)
	}
	if p != (AgentProvider{Name: "groq", KeyEnv: "GROQ_API_KEY", BaseURL: "https://api.groq.com/openai/v1"}) {
		t.Errorf("groq resolved to %+v", p)
	}
}

// A bare model id takes the default model's provider. A prefixed one never
// does: `opneai/gpt-x` is a typo, and resolving it to the default provider sent
// a model nobody chose to an endpoint the typo did not name.
func TestAgentProviderFallsBackOnlyForABareModel(t *testing.T) {
	p, err := ResolveAgentProvider("gpt-5.6-luna", "openai/gpt-5.6-luna", nil)
	if err != nil {
		t.Fatalf("a bare model did not take the default's provider: %v", err)
	}
	if p.Name != "openai" {
		t.Errorf("bare model resolved to %q, want openai", p.Name)
	}
	for _, typo := range []string{"opneai/gpt-x", "meta-llama/llama-4"} {
		_, err := ResolveAgentProvider(typo, "openai/gpt-5.6-luna", nil)
		if err == nil {
			t.Errorf("%s resolved through the default model; it must fail closed", typo)
			continue
		}
		if !strings.Contains(err.Error(), typo) {
			t.Errorf("the refusal does not name %s: %v", typo, err)
		}
	}
}

func TestEveryPresetResolvesForTheAgent(t *testing.T) {
	for _, preset := range config.Presets {
		p, err := ResolveAgentProvider(preset.Key+"/m", "", nil)
		if err != nil {
			t.Errorf("%s: %v", preset.Key, err)
			continue
		}
		if p.BaseURL != preset.BaseURL || p.KeyEnv != preset.DefaultKeyEnv {
			t.Errorf("%s resolved to %+v", preset.Key, p)
		}
	}
}
