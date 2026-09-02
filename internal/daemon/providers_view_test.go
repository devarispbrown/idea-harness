package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// The registry refuses a provider entry whose base_url is invalid (a preset
// falls back to its built-in endpoint, a custom name is not registered at all).
// The view has to agree with that, or an operator reads "openai →
// https://attacker.tld/v1, ready" while their requests are going to
// api.openai.com — or worse, believes a refused endpoint is live.
func TestProvidersViewReportsTheEffectiveEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		entry       config.ProviderConfig
		wantBaseURL string // "" means: resolves nowhere
		wantProblem bool
		wantReady   bool
	}{
		{
			name:        "preset override falls back to the built-in endpoint",
			provider:    "openai",
			entry:       config.ProviderConfig{BaseURL: "https://attacker.tld/v1"},
			wantBaseURL: "https://api.openai.com/v1",
			wantProblem: true,
			wantReady:   false,
		},
		{
			name:        "refused custom provider resolves nowhere",
			provider:    "sketchy",
			entry:       config.ProviderConfig{Type: "openai", BaseURL: "http://plaintext.example/v1"},
			wantBaseURL: "",
			wantProblem: true,
			wantReady:   false,
		},
		{
			name:        "valid custom provider is reported as configured",
			provider:    "groq",
			entry:       config.ProviderConfig{Type: "openai", BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY"},
			wantBaseURL: "https://api.groq.com/openai/v1",
			wantProblem: false,
			wantReady:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Harness: config.HarnessConfig{
				Providers: map[string]config.ProviderConfig{tc.provider: tc.entry},
			}}
			dotEnv := map[string]string{"GROQ_API_KEY": "k", "OPENAI_API_KEY": "k"}

			view := buildProvidersView(context.Background(), cfg, dotEnv, nil)

			var got *providerSummary
			for i := range view.Providers {
				if view.Providers[i].Name == tc.provider {
					got = &view.Providers[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("provider %q missing from the view", tc.provider)
			}
			if got.BaseURL != tc.wantBaseURL {
				t.Errorf("base_url = %q, want %q", got.BaseURL, tc.wantBaseURL)
			}
			if (got.Problem != "") != tc.wantProblem {
				t.Errorf("problem = %q, want problem: %v", got.Problem, tc.wantProblem)
			}
			if got.Ready != tc.wantReady {
				t.Errorf("ready = %v, want %v", got.Ready, tc.wantReady)
			}
			if tc.wantProblem && strings.Contains(got.BaseURL, "attacker.tld") {
				t.Errorf("the view still advertises the refused endpoint: %q", got.BaseURL)
			}
		})
	}
}
