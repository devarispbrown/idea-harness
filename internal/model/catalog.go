package model

// DefaultModels is a curated catalog of common models per provider, used to
// seed the UI/CLI pickers. It is advisory: any model name is accepted.
var DefaultModels = map[string][]string{
	"openai":     {"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "o3-mini"},
	"anthropic":  {"claude-sonnet-4-5", "claude-3-7-sonnet-latest", "claude-3-5-haiku-latest", "claude-opus-4-1"},
	"gemini":     {"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash", "gemini-1.5-pro"},
	"deepseek":   {"deepseek-chat", "deepseek-reasoner"},
	"openrouter": {"openai/gpt-4o", "anthropic/claude-3.7-sonnet", "google/gemini-2.5-flash", "meta-llama/llama-4-maverick"},
	"ollama":     {"llama3.1", "llama3.2", "qwen2.5", "mistral", "deepseek-r1"},
}

// DefaultModelFor returns a sensible default model name for a provider, or
// "model" if the provider is unknown.
func DefaultModelFor(provider string) string {
	if models, ok := DefaultModels[provider]; ok && len(models) > 0 {
		return models[0]
	}
	return "model"
}
