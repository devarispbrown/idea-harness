package model

// DefaultModels is a curated catalog of common models per provider. It seeds
// the UI/CLI pickers and, since audit 4, backs config.UnknownModelProblems.
//
// Still advisory in the sense that matters: any model name is accepted, and a
// name absent from this list produces a *warning*, never a rejection, because a
// valid model can postdate the catalog. But it is no longer only cosmetic --
// deleting an entry here makes a correct id start warning, so keep it current.
//
// Order matters: DefaultModelFor returns models[0], and that is the model a
// new user gets when they set only a provider key. Put a model first only after
// a real call to it returns 200; a listing is not enough.
//
// Refreshed 2026-09-17 against each provider's live API. Every id below
// answered a one-token chat completion with 200 that day, except where noted.
// Removed because they returned 404: claude-3-7-sonnet-latest,
// claude-3-5-haiku-latest, claude-opus-4-1, gemini-2.0-flash, gemini-1.5-pro
// and openrouter's anthropic/claude-3.7-sonnet.
//
//   - gemini: gemini-2.5-flash and gemini-2.5-pro still appear in Google's
//     listing, but return 404 "no longer available to new users". They stay
//     for accounts that can still reach them, and must never lead.
//     gemini-3.8-flash returned 503 "high demand" and gemini-3.1-pro-preview
//     returned 429 (quota), so neither leads, but both exist.
//   - ollama: these are library names the operator must pull first. The
//     catalog cannot verify them against a server it does not run.
var DefaultModels = map[string][]string{
	"openai":     {"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.5", "gpt-5.4-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4o", "gpt-4o-mini", "o3-mini"},
	"anthropic":  {"claude-sonnet-5", "claude-opus-5", "claude-fable-5-1", "claude-haiku-4-5-20251001", "claude-opus-4-8", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
	"gemini":     {"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash", "gemini-3.5-flash", "gemini-3.1-pro-preview", "gemini-2.5-flash", "gemini-2.5-pro"},
	"deepseek":   {"deepseek-v4-pro", "deepseek-flash", "deepseek-chat", "deepseek-reasoner"},
	"openrouter": {"anthropic/claude-sonnet-5", "openai/gpt-5.6-luna", "google/gemini-3.6-flash", "deepseek/deepseek-v4-pro-0813", "qwen/qwen3.8-max-0902", "moonshotai/kimi-k3", "z-ai/glm-5.3", "openai/gpt-4o", "meta-llama/llama-4-maverick"},
	"ollama":     {"qwen3.8", "gpt-oss:20b", "gemma4:26b", "llama3.1", "llama3.2", "qwen2.5", "mistral", "deepseek-r1"},
}

// DefaultModelFor returns a sensible default model name for a provider, or
// "model" if the provider is unknown.
func DefaultModelFor(provider string) string {
	if models, ok := DefaultModels[provider]; ok && len(models) > 0 {
		return models[0]
	}
	return "model"
}
