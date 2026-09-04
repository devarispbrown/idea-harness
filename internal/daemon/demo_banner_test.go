package daemon

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// TestIsDemoTracksWhatActuallyResolves pins audit 5's C2, daemon half.
//
// isDemo scanned for provider key environment variables, which is a different
// question from the one the registry answers. With OPENAI_API_KEY exported and
// a bare `default_model: gpt-4o`, every model call resolved to the built-in
// demo provider while the banner was suppressed — so `idea new` and `idea
// status` showed no demo warning and the user believed gpt-4o wrote their PRD.
// `model=gpt-4o actual=demo` appeared only in the daemon log.
func TestIsDemoTracksWhatActuallyResolves(t *testing.T) {
	keys := map[string]string{"OPENAI_API_KEY": "sk-test", "ANTHROPIC_API_KEY": "sk-ant-test"}

	for _, c := range []struct {
		name   string
		cfg    config.HarnessConfig
		dotEnv map[string]string
		want   bool
	}{
		{
			name:   "a bare default_model with keys exported still resolves to demo",
			cfg:    config.HarnessConfig{DefaultModel: "gpt-4o"},
			dotEnv: keys,
			want:   true,
		},
		{
			name:   "no default_model and no provider entry, keys exported",
			cfg:    config.HarnessConfig{},
			dotEnv: keys,
			want:   true,
		},
		{
			name:   "a qualified default_model is not demo",
			cfg:    config.HarnessConfig{DefaultModel: "openai/gpt-4o-mini"},
			dotEnv: keys,
			want:   false,
		},
		{
			name: "a configured provider entry is not demo",
			cfg: config.HarnessConfig{Providers: map[string]config.ProviderConfig{
				"groq": {Type: "openai", APIKeyEnv: "GROQ_API_KEY", BaseURL: "https://api.groq.com/openai/v1"},
			}},
			want: false,
		},
		{
			// A configured provider whose key is missing is misconfigured, not
			// demo: the call fails 401 rather than going synthetic, and
			// keyProblems reports that separately.
			name:   "a qualified default_model with no key is misconfigured, not demo",
			cfg:    config.HarnessConfig{DefaultModel: "openai/gpt-4o-mini"},
			dotEnv: map[string]string{},
			want:   false,
		},
		{
			name: "nothing configured at all",
			cfg:  config.HarnessConfig{},
			want: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := isDemo(config.Config{Harness: c.cfg}, c.dotEnv)
			if got != c.want {
				t.Errorf("isDemo = %v, want %v — the banner and the registry disagree, "+
					"which is how synthetic output gets reported as real", got, c.want)
			}
		})
	}
}

// TestIsDemoAgreesWithTheRegistry is the invariant behind the table: the banner
// must say demo exactly when defaultProviderName picks the demo provider,
// because that is the choice buildRegistry actually makes.
func TestIsDemoAgreesWithTheRegistry(t *testing.T) {
	for _, cfg := range []config.HarnessConfig{
		{},
		{DefaultModel: "gpt-4o"},
		{DefaultModel: "openai/gpt-4o-mini"},
		{Providers: map[string]config.ProviderConfig{"groq": {Type: "openai"}}},
	} {
		full := config.Config{Harness: cfg}
		if isDemo(full, nil) != (defaultProviderName(full) == demoProviderName) {
			t.Errorf("isDemo disagrees with defaultProviderName for %+v", cfg)
		}
	}
}
