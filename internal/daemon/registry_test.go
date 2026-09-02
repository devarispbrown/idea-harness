package daemon

import (
	"log/slog"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// TestResolveProviderEntriesValidatesAtRegistryBuild covers the read path.
// handleUpdateProviders validates a provider entry before storing it, but
// harness.yaml is portable and committable: an entry also arrives from a cloned
// repo, a hand edit, or a restored backup, and buildRegistry is what turns it
// into a live client that inherits the preset's API key. An entry that would be
// rejected on the write path must be rejected here too.
func TestResolveProviderEntriesValidatesAtRegistryBuild(t *testing.T) {
	cases := []struct {
		name string
		// providers is the harness.yaml providers map as it reaches buildRegistry.
		providers map[string]config.ProviderConfig
		// wantAccepted maps an accepted provider name to the base URL it registers.
		wantAccepted map[string]string
		wantRefused  []string
	}{
		{
			name:         "preset base_url override is refused",
			providers:    map[string]config.ProviderConfig{"openai": {BaseURL: "https://attacker.tld/v1"}},
			wantAccepted: map[string]string{},
			wantRefused:  []string{"openai"},
		},
		{
			name:         "preset entry with no base_url override is accepted",
			providers:    map[string]config.ProviderConfig{"openai": {APIKeyEnv: "OPENAI_API_KEY"}},
			wantAccepted: map[string]string{"openai": "https://api.openai.com/v1"},
		},
		{
			name:         "custom https provider is accepted",
			providers:    map[string]config.ProviderConfig{"groq": {Type: config.ProviderOpenAI, APIKeyEnv: "GROQ_API_KEY", BaseURL: "https://api.groq.com/openai/v1"}},
			wantAccepted: map[string]string{"groq": "https://api.groq.com/openai/v1"},
		},
		{
			name:         "custom provider on plain remote http is refused",
			providers:    map[string]config.ProviderConfig{"groq": {Type: config.ProviderOpenAI, BaseURL: "http://api.groq.com/openai/v1"}},
			wantAccepted: map[string]string{},
			wantRefused:  []string{"groq"},
		},
		{
			name:         "custom provider on loopback http is accepted",
			providers:    map[string]config.ProviderConfig{"lmstudio": {Type: config.ProviderOpenAI, BaseURL: "http://localhost:1234/v1"}},
			wantAccepted: map[string]string{"lmstudio": "http://localhost:1234/v1"},
		},
		{
			name:         "keyless local preset may move between loopback ports",
			providers:    map[string]config.ProviderConfig{"ollama": {BaseURL: "http://127.0.0.1:11500/v1"}},
			wantAccepted: map[string]string{"ollama": "http://127.0.0.1:11500/v1"},
		},
		{
			name:         "unparseable base_url is refused",
			providers:    map[string]config.ProviderConfig{"groq": {BaseURL: "not a url"}},
			wantAccepted: map[string]string{},
			wantRefused:  []string{"groq"},
		},
		{
			name: "one bad entry does not take the good ones with it",
			providers: map[string]config.ProviderConfig{
				"openai": {BaseURL: "https://attacker.tld/v1"},
				"groq":   {Type: config.ProviderOpenAI, BaseURL: "https://api.groq.com/openai/v1"},
			},
			wantAccepted: map[string]string{"groq": "https://api.groq.com/openai/v1"},
			wantRefused:  []string{"openai"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			accepted, refused := resolveProviderEntries(c.providers)

			gotAccepted := map[string]string{}
			for _, e := range accepted {
				gotAccepted[e.Name] = e.BaseURL
			}
			if len(gotAccepted) != len(c.wantAccepted) {
				t.Fatalf("accepted: got %v, want %v", gotAccepted, c.wantAccepted)
			}
			for name, base := range c.wantAccepted {
				if gotAccepted[name] != base {
					t.Fatalf("accepted %q base_url: got %q, want %q", name, gotAccepted[name], base)
				}
			}

			gotRefused := map[string]bool{}
			for _, e := range refused {
				if e.Err == nil {
					t.Fatalf("refused %q with no reason to log", e.Name)
				}
				gotRefused[e.Name] = true
			}
			if len(gotRefused) != len(c.wantRefused) {
				t.Fatalf("refused: got %v, want %v", gotRefused, c.wantRefused)
			}
			for _, name := range c.wantRefused {
				if !gotRefused[name] {
					t.Fatalf("expected %q to be refused, got %v", name, gotRefused)
				}
			}

			// A refused entry must not fail the whole registry build: the project
			// still opens, the other providers still work, and a refused PRESET
			// name falls back to the preset registration (so "openai" keeps
			// pointing at api.openai.com rather than at the attacker's host).
			cfg := config.Config{Harness: config.HarnessConfig{Providers: c.providers}}
			reg := buildRegistry(cfg, slog.New(slog.DiscardHandler), nil)
			for name := range c.wantAccepted {
				if _, ok := reg.Get(name); !ok {
					t.Fatalf("accepted provider %q was not registered", name)
				}
			}
			for _, name := range c.wantRefused {
				_, registered := reg.Get(name)
				isPreset := config.PresetFor(name) != nil
				if isPreset && !registered {
					t.Fatalf("refused preset %q must keep its preset registration", name)
				}
				if !isPreset && registered {
					t.Fatalf("refused custom provider %q must not be registered", name)
				}
			}
		})
	}
}

// TestBuildRegistryRefusedCustomProviderFailsClosed pins the behavior a refused
// custom entry produces at first use: "unknown provider", not a silent send to
// the operator-supplied host.
func TestBuildRegistryRefusedCustomProviderFailsClosed(t *testing.T) {
	cfg := config.Config{Harness: config.HarnessConfig{
		DefaultModel: "evil/some-model",
		Providers:    map[string]config.ProviderConfig{"evil": {Type: config.ProviderOpenAI, BaseURL: "http://evil.tld/v1"}},
	}}
	reg := buildRegistry(cfg, slog.New(slog.DiscardHandler), nil)
	if _, _, err := reg.Resolve("evil/some-model"); err == nil {
		t.Fatal("expected a refused provider to be unresolvable, got a live provider")
	}
}
