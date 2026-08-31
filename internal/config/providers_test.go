package config

import "testing"

func TestResolveProviderPresets(t *testing.T) {
	cases := []struct {
		key     string
		wantTyp ProviderType
		wantURL string
		wantEnv string
	}{
		{"openai", ProviderOpenAI, "https://api.openai.com/v1", "OPENAI_API_KEY"},
		{"anthropic", ProviderAnthropic, "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY"},
		{"gemini", ProviderGemini, "https://generativelanguage.googleapis.com/v1beta", "GOOGLE_API_KEY"},
		{"openrouter", ProviderOpenAI, "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
		{"ollama", ProviderOpenAI, "http://localhost:11434/v1", ""},
	}
	for _, c := range cases {
		typ, url, env := ResolveProvider(c.key, ProviderConfig{})
		if typ != c.wantTyp || url != c.wantURL || env != c.wantEnv {
			t.Errorf("%s: got (%s, %s, %s), want (%s, %s, %s)", c.key, typ, url, env, c.wantTyp, c.wantURL, c.wantEnv)
		}
	}
}

func TestResolveProviderOverrides(t *testing.T) {
	typ, url, env := ResolveProvider("custom", ProviderConfig{
		Type:      ProviderGemini,
		BaseURL:   "http://localhost:9999",
		APIKeyEnv: "MY_KEY",
	})
	if typ != ProviderGemini || url != "http://localhost:9999" || env != "MY_KEY" {
		t.Fatalf("overrides not honored: %s %s %s", typ, url, env)
	}
	// Custom key with no type defaults to openai-compatible.
	typ, _, _ = ResolveProvider("custom", ProviderConfig{})
	if typ != ProviderOpenAI {
		t.Fatalf("custom provider should default to openai type, got %s", typ)
	}
}

func TestParseModelSpec(t *testing.T) {
	p, m := ParseModelSpec("openai/gpt-4o-mini")
	if p != "openai" || m != "gpt-4o-mini" {
		t.Fatalf("got (%s, %s)", p, m)
	}
	// OpenRouter model ids contain slashes; split on the first slash only.
	p, m = ParseModelSpec("openrouter/anthropic/claude-3.7-sonnet")
	if p != "openrouter" || m != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("got (%s, %s)", p, m)
	}
	p, m = ParseModelSpec("gpt-4o")
	if p != "" || m != "gpt-4o" {
		t.Fatalf("bare model: got (%s, %s)", p, m)
	}
}
