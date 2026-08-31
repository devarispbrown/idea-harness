package config

import "strings"

// ProviderType is the wire protocol a provider speaks.
type ProviderType string

const (
	ProviderOpenAI    ProviderType = "openai"    // OpenAI-compatible chat completions
	ProviderAnthropic ProviderType = "anthropic" // Anthropic messages API
	ProviderGemini    ProviderType = "gemini"    // Google generative-language API
)

func (t ProviderType) Valid() bool {
	switch t {
	case ProviderOpenAI, ProviderAnthropic, ProviderGemini:
		return true
	}
	return false
}

// Preset is a well-known provider preset used for config resolution and UI.
type Preset struct {
	Key           string
	Label         string
	Type          ProviderType
	BaseURL       string
	DefaultKeyEnv string
	NeedsKey      bool
}

// Presets are the built-in providers (§10, §41). OpenRouter and Ollama speak
// the OpenAI-compatible wire format with different endpoints; Gemini and
// Anthropic use native APIs.
var Presets = []Preset{
	{Key: "openai", Label: "OpenAI", Type: ProviderOpenAI, BaseURL: "https://api.openai.com/v1", DefaultKeyEnv: "OPENAI_API_KEY", NeedsKey: true},
	{Key: "anthropic", Label: "Anthropic", Type: ProviderAnthropic, BaseURL: "https://api.anthropic.com/v1", DefaultKeyEnv: "ANTHROPIC_API_KEY", NeedsKey: true},
	{Key: "gemini", Label: "Google Gemini", Type: ProviderGemini, BaseURL: "https://generativelanguage.googleapis.com/v1beta", DefaultKeyEnv: "GOOGLE_API_KEY", NeedsKey: true},
	{Key: "deepseek", Label: "DeepSeek", Type: ProviderOpenAI, BaseURL: "https://api.deepseek.com/v1", DefaultKeyEnv: "DEEPSEEK_API_KEY", NeedsKey: true},
	{Key: "openrouter", Label: "OpenRouter", Type: ProviderOpenAI, BaseURL: "https://openrouter.ai/api/v1", DefaultKeyEnv: "OPENROUTER_API_KEY", NeedsKey: true},
	{Key: "ollama", Label: "Ollama (local)", Type: ProviderOpenAI, BaseURL: "http://localhost:11434/v1", DefaultKeyEnv: "", NeedsKey: false},
}

// PresetFor returns the preset matching a provider key, or nil for a custom key.
func PresetFor(key string) *Preset {
	for i := range Presets {
		if Presets[i].Key == key {
			return &Presets[i]
		}
	}
	return nil
}

// ResolveProvider derives the effective type, base URL, and key-env name for a
// provider entry, falling back to the preset for known keys (§40).
func ResolveProvider(key string, pc ProviderConfig) (ProviderType, string, string) {
	typ := pc.Type
	base := pc.BaseURL
	keyEnv := pc.APIKeyEnv

	if p := PresetFor(key); p != nil {
		if typ == "" {
			typ = p.Type
		}
		if base == "" {
			base = p.BaseURL
		}
		if keyEnv == "" {
			keyEnv = p.DefaultKeyEnv
		}
	}
	if typ == "" {
		typ = ProviderOpenAI // custom providers default to OpenAI-compatible
	}
	return typ, base, keyEnv
}

// ParseModelSpec splits a "provider/model" default-model string into its
// provider and model parts. A model with no "/" returns ("", model); the
// caller applies the default provider. Splitting is on the FIRST slash so
// OpenRouter model ids like "anthropic/claude-3.7-sonnet" remain intact.
func ParseModelSpec(spec string) (provider, model string) {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, "/"); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return "", spec
}
