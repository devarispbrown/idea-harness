package executor

import (
	"net/url"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// Every provider this project presets must produce a usable Hermes config.
//
// The alternative -- letting Hermes resolve the provider by name -- is what the
// executor did, and its registry disagrees with ours: `openai` normalises to
// OPENROUTER and `ollama` to "custom". An OpenAI key would have gone unused
// while OpenRouter was billed, silently.
func TestEveryPresetProviderRendersAHermesConfig(t *testing.T) {
	if len(config.Presets) == 0 {
		t.Fatal("no presets — this check proves nothing")
	}
	for _, p := range config.Presets {
		cfg, err := hermesConfigYAML(p.Key, p.BaseURL, "some-model", p.DefaultKeyEnv)
		if err != nil {
			t.Errorf("%s: %v", p.Key, err)
			continue
		}
		slug := hermesProviderSlug(p.Key)
		for _, want := range []string{
			"custom_providers:",
			"name: " + slug,
			"base_url: " + p.BaseURL,
			"provider: " + slug,
			"default: some-model",
		} {
			if !strings.Contains(cfg, want) {
				t.Errorf("%s: config missing %q:\n%s", p.Key, want, cfg)
			}
		}
		// Hermes warns when custom_providers has no model section and then does
		// not know which provider to use — it told us so itself.
		if !strings.Contains(cfg, "model:") {
			t.Errorf("%s: no model section, which Hermes warns about", p.Key)
		}
	}
}

func TestHermesConfigRefusesAnUnusableProvider(t *testing.T) {
	if _, err := hermesConfigYAML("", "https://x/v1", "m", ""); err == nil {
		t.Error("an empty provider name was accepted")
	}
	if _, err := hermesConfigYAML("openai", "", "m", ""); err == nil {
		t.Error("a provider with no base URL was accepted, so the agent would have no endpoint")
	}
}

// Ollama's preset base URL is loopback, and inside a container `localhost` is
// the container — so the agent would dial its own empty port instead of the
// operator's Ollama.
func TestLoopbackEndpointsAreRewrittenForTheContainer(t *testing.T) {
	for _, tc := range []struct {
		in         string
		wantHost   string
		wantGatewy bool
	}{
		{"http://localhost:11434/v1", "host.docker.internal", true},
		{"http://127.0.0.1:11434/v1", "host.docker.internal", true},
		{"https://api.openai.com/v1", "api.openai.com", false},
		{"https://openrouter.ai/api/v1", "openrouter.ai", false},
		// Everything config accepts as loopback must be routed as loopback,
		// or the agent dials its own container.
		{"http://localhost/v1", "host.docker.internal", true},
		{"http://LOCALHOST:1234/v1", "host.docker.internal:1234", true},
		{"http://127.0.0.2:8080/v1", "host.docker.internal:8080", true},
		{"http://[::1]:11434/v1", "host.docker.internal:11434", true},
		{"http://user@localhost:11434/v1", "host.docker.internal:11434", true},
		// A loopback URL in a query string is not a loopback endpoint.
		{"https://proxy.example/x?u=http://localhost:1", "proxy.example", false},
	} {
		got, gateway := containerBaseURL(tc.in)
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("%s -> unparseable %q", tc.in, got)
		}
		if u.Host != tc.wantHost && u.Hostname() != tc.wantHost {
			t.Errorf("%s -> %s, want host %s", tc.in, got, tc.wantHost)
		}
		if !gateway && got != tc.in {
			t.Errorf("%s was rewritten to %s although it is not loopback", tc.in, got)
		}
		if gateway != tc.wantGatewy {
			t.Errorf("%s: host-gateway=%v, want %v", tc.in, gateway, tc.wantGatewy)
		}
	}
	// The preset itself must be the loopback one this exists for.
	p := config.PresetFor("ollama")
	if p == nil {
		t.Fatal("no ollama preset")
	}
	if _, gateway := containerBaseURL(p.BaseURL); !gateway {
		t.Errorf("ollama's preset %q is not treated as loopback, so the container cannot reach it", p.BaseURL)
	}
}

// The provider prefix is pinned by the generated config, so the model passed to
// Hermes must be bare — an endpoint handed `openai/gpt-x` rejects it.
func TestBareModelStripsTheProviderPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"openai/gpt-5.6-luna":                "gpt-5.6-luna",
		"anthropic/claude-sonnet-5":          "claude-sonnet-5",
		"ollama/qwen3.8:latest":              "qwen3.8:latest",
		"ollama/llama3.1":                    "llama3.1",
		"gpt-4o":                             "gpt-4o",
		"openrouter/anthropic/claude-opus-5": "anthropic/claude-opus-5",
	} {
		if got := bareModel(in); got != want {
			t.Errorf("bareModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A keyed provider must name its key env in the generated config, and a keyless
// one must not. Without key_env, Hermes sends "no-key-required", so Anthropic,
// Gemini and DeepSeek were rejected with keys that were valid on the host.
func TestHermesConfigNamesTheKeyEnv(t *testing.T) {
	for _, p := range config.Presets {
		cfg, err := hermesConfigYAML(p.Key, p.BaseURL, "m", p.DefaultKeyEnv)
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(cfg, "key_env: ")
		if p.NeedsKey && !strings.Contains(cfg, "key_env: "+p.DefaultKeyEnv) {
			t.Errorf("%s needs a key but the config does not name %s:\n%s", p.Key, p.DefaultKeyEnv, cfg)
		}
		if !p.NeedsKey && has {
			t.Errorf("%s needs no key but the config names one:\n%s", p.Key, cfg)
		}
	}
}

func TestGeminiUsesItsOpenAICompatibleEndpoint(t *testing.T) {
	p := config.PresetFor("gemini")
	if p == nil {
		t.Fatal("no gemini preset")
	}
	if got := agentBaseURL(AgentProvider{Type: p.Type}, p.BaseURL); !strings.HasSuffix(got, "/openai") {
		t.Errorf("gemini agent endpoint %q is the native API, which does not serve /chat/completions", got)
	}
	if got := agentBaseURL(AgentProvider{Type: p.Type}, p.BaseURL+"/openai"); strings.Count(got, "/openai") != 1 {
		t.Errorf("an already-compatible URL was rewritten again: %q", got)
	}
	for _, other := range []string{"openai", "anthropic", "deepseek", "openrouter", "ollama"} {
		o := config.PresetFor(other)
		if got := agentBaseURL(AgentProvider{Type: o.Type}, o.BaseURL); got != o.BaseURL {
			t.Errorf("%s endpoint changed: %q -> %q", other, o.BaseURL, got)
		}
	}
}

// base_url and model come from harness.yaml. Formatted by hand, a ` #` turned
// the rest of a URL into a comment, and a newline could add keys of its own.
func TestHermesConfigSurvivesHostileValues(t *testing.T) {
	cfg, err := hermesConfigYAML("custom", "https://x.example/v1 #frag", "m: odd\nkey_env: STOLEN", "")
	if err != nil {
		t.Fatal(err)
	}
	var got hermesConfig
	if err := yaml.Unmarshal([]byte(cfg), &got); err != nil {
		t.Fatalf("the generated config does not parse: %v\n%s", err, cfg)
	}
	if got.CustomProviders[0].BaseURL != "https://x.example/v1 #frag" {
		t.Errorf("base_url was altered: %q", got.CustomProviders[0].BaseURL)
	}
	if got.Model.Default != "m: odd\nkey_env: STOLEN" {
		t.Errorf("model was altered: %q", got.Model.Default)
	}
	if got.CustomProviders[0].KeyEnv != "" {
		t.Errorf("a value injected a key_env: %q", got.CustomProviders[0].KeyEnv)
	}
}
