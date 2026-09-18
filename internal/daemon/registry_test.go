package daemon

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/model"
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

// TestBuildRegistryRefusesAKeyedProviderWithNoKey is F1's choke-point test.
//
// Before this, a keyed provider with no key still got a real HTTP client
// registered (key=""), so the first call reached the provider and its raw 401
// JSON body was the only thing anyone saw -- in the daemon log at best, and in
// the UI as an unreadable blob at worst. Complete must now fail locally,
// naming the variable to set and the fix, before any network call.
func TestBuildRegistryRefusesAKeyedProviderWithNoKey(t *testing.T) {
	// Force-cleared: a developer's own shell commonly has OPENAI_API_KEY
	// exported for unrelated tools, and that ambient value would otherwise
	// leak into buildRegistry's lookupEnv (which checks os.Getenv first) and
	// make this test pass for the wrong reason -- or, worse, silently make a
	// real network call.
	t.Setenv("OPENAI_API_KEY", "")
	cfg := config.Config{Harness: config.HarnessConfig{DefaultModel: "openai/gpt-4o-mini"}}
	reg := buildRegistry(cfg, slog.New(slog.DiscardHandler), nil) // nil dotEnv: no key anywhere either

	_, err := reg.Complete(t.Context(), "openai/gpt-4o-mini", model.Request{})
	if err == nil {
		t.Fatal("Complete succeeded with no OPENAI_API_KEY set anywhere")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error does not name the missing variable: %v", err)
	}
	if !strings.Contains(err.Error(), "idea daemon stop") {
		t.Errorf("error does not name the fix: %v", err)
	}
	// The raw provider error shape ("status 401", a JSON body) must not leak
	// through -- that is exactly the defect this closes.
	if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "{") {
		t.Errorf("error looks like a raw provider response, not the pre-flight message: %v", err)
	}
}

// TestBuildRegistryDemoModeIsUnaffectedByTheKeyCheck: the deterministic demo
// provider is not "keyed" in the sense F1 cares about, and must keep working
// with no key configured anywhere -- which is also the daemon's own default
// state, so a regression here would break every fresh `idea init`.
func TestBuildRegistryDemoModeIsUnaffectedByTheKeyCheck(t *testing.T) {
	cfg := config.Config{} // no default_model, no providers: pure demo mode
	reg := buildRegistry(cfg, slog.New(slog.DiscardHandler), nil)
	resp, err := reg.Complete(t.Context(), demoProviderName+"/anything", model.Request{
		JSONSchemaName: "idea_brief",
		Messages:       []model.Message{{Role: "user", Content: "an idea"}},
	})
	if err != nil {
		t.Fatalf("demo provider failed: %v", err)
	}
	if resp.Text == "" && resp.RawJSON == "" {
		t.Error("demo provider returned an empty response")
	}
}
