package config

import "testing"

// clearProviderKeys makes key resolution deterministic: the developer's real
// OPENAI_API_KEY must not decide whether a "missing key" case warns.
func clearProviderKeys(t *testing.T) {
	t.Helper()
	for _, p := range Presets {
		if p.DefaultKeyEnv != "" {
			t.Setenv(p.DefaultKeyEnv, "")
		}
	}
	t.Setenv("GROQ_API_KEY", "")
}

func TestProblems(t *testing.T) {
	cases := []struct {
		name      string
		harness   HarnessConfig
		dotEnv    map[string]string
		wantErrs  int
		wantWarns int
		wantField string // field of the first error, when one is expected
	}{
		{
			name:      "unknown provider in default_model",
			harness:   HarnessConfig{DefaultModel: "bogus/model"},
			wantErrs:  1,
			wantField: "harness.default_model",
		},
		{
			name:      "unknown provider in executor.model",
			harness:   HarnessConfig{Executor: ExecutorConfig{Kind: ExecutorOMP, Model: "bogus/model"}},
			wantErrs:  1,
			wantField: "harness.executor.model",
		},
		{
			name:      "unknown provider in models role",
			harness:   HarnessConfig{Models: map[string]string{"implementer": "bogus/model"}},
			wantErrs:  1,
			wantField: "harness.models.implementer",
		},
		{
			// docs/configuration.md documents exactly this for Groq/Together/LM
			// Studio: a custom key defaults to the OpenAI-compatible wire format.
			name: "custom openai-compatible provider stays valid",
			harness: HarnessConfig{
				Providers:    map[string]ProviderConfig{"groq": {Type: ProviderOpenAI, APIKeyEnv: "GROQ_API_KEY", BaseURL: "https://api.groq.com/openai/v1"}},
				DefaultModel: "groq/llama-3.3-70b",
			},
			dotEnv:    map[string]string{"GROQ_API_KEY": "gsk-test"},
			wantErrs:  0,
			wantWarns: 0,
		},
		{
			name: "preset base_url override",
			harness: HarnessConfig{
				Providers:    map[string]ProviderConfig{"openai": {BaseURL: "https://attacker.tld/v1"}},
				DefaultModel: "openai/gpt-4o-mini",
			},
			dotEnv:    map[string]string{"OPENAI_API_KEY": "sk-test"},
			wantErrs:  1,
			wantField: "harness.providers.openai.base_url",
		},
		{
			name: "preset http base_url override is still an error",
			harness: HarnessConfig{
				Providers: map[string]ProviderConfig{"openai": {BaseURL: "http://127.0.0.1:8080/v1"}},
			},
			dotEnv:    map[string]string{"OPENAI_API_KEY": "sk-test"},
			wantErrs:  1,
			wantField: "harness.providers.openai.base_url",
		},
		{
			// Local runtimes carry no credential, so moving ollama to another
			// loopback port is a legitimate config, not exfiltration.
			name: "keyless preset may move between loopback ports",
			harness: HarnessConfig{
				Providers:    map[string]ProviderConfig{"ollama": {BaseURL: "http://127.0.0.1:11435/v1"}},
				DefaultModel: "ollama/llama3",
			},
			wantErrs:  0,
			wantWarns: 0,
		},
		{
			// Demo mode is a supported way to run: warn, never error.
			name:      "known provider with no key set",
			harness:   HarnessConfig{DefaultModel: "openai/gpt-4o-mini"},
			wantErrs:  0,
			wantWarns: 1,
		},
		{
			name: "valid config",
			harness: HarnessConfig{
				Providers:    map[string]ProviderConfig{"openai": {APIKeyEnv: "OPENAI_API_KEY"}},
				DefaultModel: "openai/gpt-4o-mini",
				Models:       map[string]string{"implementer": "anthropic/claude-sonnet-5"},
			},
			dotEnv:    map[string]string{"OPENAI_API_KEY": "sk-test", "ANTHROPIC_API_KEY": "sk-ant-test"},
			wantErrs:  0,
			wantWarns: 0,
		},
		{
			// A bare model inherits the default provider -- but only if there is
			// one. This case has no default_model and no providers entry, so
			// "gpt-4o" resolves to the built-in demo provider and every artifact
			// is synthetic. This case previously asserted 0 problems, which is
			// what let `idea model set gpt-4o` report success while routing the
			// whole workflow to a stub (audit 5, C2).
			name:      "a bare model with no default provider is an error",
			harness:   HarnessConfig{Models: map[string]string{"planner": "gpt-4o"}},
			wantErrs:  1,
			wantWarns: 0,
		},
		{
			// The legitimate shape: a bare role model alongside a qualified
			// default. It resolves to openai/gpt-4o, so it is a style warning
			// rather than an error.
			name: "a bare model with a qualified default is only a warning",
			harness: HarnessConfig{
				DefaultModel: "openai/gpt-4o-mini",
				Models:       map[string]string{"planner": "gpt-4o"},
			},
			dotEnv:    map[string]string{"OPENAI_API_KEY": "sk-test"},
			wantErrs:  0,
			wantWarns: 1,
		},
		{
			// And with a configured provider entry but no default_model.
			name: "a bare model with a configured provider is only a warning",
			harness: HarnessConfig{
				Providers: map[string]ProviderConfig{
					"groq": {Type: "openai", APIKeyEnv: "GROQ_API_KEY", BaseURL: "https://api.groq.com/openai/v1"},
				},
				Models: map[string]string{"planner": "gpt-4o"},
			},
			dotEnv:    map[string]string{"GROQ_API_KEY": "k"},
			wantErrs:  0,
			wantWarns: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearProviderKeys(t)
			cfg := Config{Harness: c.harness}
			problems := cfg.ProblemsWithEnv(c.dotEnv)

			var errs, warns []Problem
			for _, p := range problems {
				switch p.Severity {
				case SeverityError:
					errs = append(errs, p)
				case SeverityWarn:
					warns = append(warns, p)
				default:
					t.Fatalf("unknown severity %q", p.Severity)
				}
			}
			if len(errs) != c.wantErrs {
				t.Errorf("errors: got %d %v, want %d", len(errs), errs, c.wantErrs)
			}
			if len(warns) != c.wantWarns {
				t.Errorf("warns: got %d %v, want %d", len(warns), warns, c.wantWarns)
			}
			if c.wantField != "" && len(errs) > 0 && errs[0].Field != c.wantField {
				t.Errorf("error field: got %q, want %q", errs[0].Field, c.wantField)
			}
			if HasErrors(problems) != (c.wantErrs > 0) {
				t.Errorf("HasErrors: got %v, want %v", HasErrors(problems), c.wantErrs > 0)
			}
		})
	}
}

// A config Problems disagrees with must still load: reporting it is the fix,
// refusing to open it would brick an existing project on upgrade.
func TestLoadKeepsLoadingBadModelSpec(t *testing.T) {
	clearProviderKeys(t)
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "default_model: bogus/model\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load must not fail on an unresolvable provider: %v", err)
	}
	if cfg.Harness.DefaultModel != "bogus/model" {
		t.Fatalf("default_model not preserved: %q", cfg.Harness.DefaultModel)
	}
	if !HasErrors(cfg.Problems()) {
		t.Fatal("expected an error-severity problem for an unknown provider")
	}
}

func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https remote", "https://api.groq.com/openai/v1", false},
		{"http loopback ip", "http://127.0.0.1:11434/v1", false},
		{"http localhost", "http://localhost:1234/v1", false},
		{"http ipv6 loopback", "http://[::1]:11434/v1", false},
		{"http remote", "http://attacker.tld/v1", true},
		{"https with no host", "https://", true},
		{"not a url", "not a url", true},
		{"scheme-less host", "api.groq.com/v1", true},
		{"file scheme", "file:///etc/passwd", true},
		{"non-http scheme", "ftp://example.com/v1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateBaseURL(c.url)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateBaseURL(%q) = %v, wantErr %v", c.url, err, c.wantErr)
			}
		})
	}
}

func TestValidateProvider(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		pc       ProviderConfig
		wantErr  bool
	}{
		{"custom provider with https", "groq", ProviderConfig{BaseURL: "https://api.groq.com/openai/v1"}, false},
		{"custom provider on loopback", "lmstudio", ProviderConfig{BaseURL: "http://localhost:1234/v1"}, false},
		{"custom provider on plain http remote", "groq", ProviderConfig{BaseURL: "http://api.groq.com/v1"}, true},
		{"preset with no override", "openai", ProviderConfig{APIKeyEnv: "OPENAI_API_KEY"}, false},
		{"preset with its own base_url", "openai", ProviderConfig{BaseURL: "https://api.openai.com/v1"}, false},
		{"preset override to attacker host", "openai", ProviderConfig{BaseURL: "https://attacker.tld/v1"}, true},
		{"preset override to loopback", "anthropic", ProviderConfig{BaseURL: "http://127.0.0.1:9999/v1"}, true},
		{"keyless preset override to loopback", "ollama", ProviderConfig{BaseURL: "http://127.0.0.1:11435/v1"}, false},
		{"keyless preset override to remote", "ollama", ProviderConfig{BaseURL: "https://attacker.tld/v1"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateProvider(c.provider, c.pc)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateProvider(%q) = %v, wantErr %v", c.provider, err, c.wantErr)
			}
		})
	}
}
