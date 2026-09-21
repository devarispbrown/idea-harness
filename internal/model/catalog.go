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
// Removed 2026-09-19 (ICP walkthrough finding: "the bundled model catalog
// lists deprecated models (e.g. gemini-2.5-flash) that 404 at call time"):
// gemini-2.5-flash and gemini-2.5-pro, which an earlier revision of this
// catalog kept, deprioritized, "for accounts that can still reach them". A
// model this catalog already knows returns "no longer available to new
// users" for every new account has no business staying in a list whose whole
// purpose is steering new setups away from dead ids -- being last in the list
// still means a project's config or the Settings picker can offer it. If your
// account can still reach one of them, set it explicitly with
// `idea model set gemini/gemini-2.5-flash`; a name absent from this list is a
// warning, never a rejection (see the package doc above), and a 404 against
// any model, catalog member or not, now names the model in the error (see
// model.httpError).
//
//   - gemini: gemini-3.8-flash returned 503 "high demand" and
//     gemini-3.1-pro-preview returned 429 (quota) on 2026-09-17, so neither
//     leads, but both exist and are not known-dead the way gemini-2.5-* is.
//   - ollama: these are library names the operator must pull first. The
//     catalog cannot verify them against a server it does not run.
var DefaultModels = map[string][]string{
	"openai":     {"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.5", "gpt-5.4-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4o", "gpt-4o-mini", "o3-mini"},
	"anthropic":  {"claude-sonnet-5", "claude-opus-5", "claude-fable-5-1", "claude-haiku-4-5-20251001", "claude-opus-4-8", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
	"gemini":     {"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash", "gemini-3.5-flash", "gemini-3.1-pro-preview"},
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

// ModelContextWindows overrides contextWindowFallback with a verified figure
// for a specific "provider/model" spec, checked against that provider's own
// model documentation (not a listing, not a guess). Add an entry here only
// once you have actually looked it up.
//
// This exists for the coding-agent executor (internal/executor/hermescfg.go):
// Hermes Agent refuses to start below 64,000 tokens of context, and every
// provider here is wired into Hermes as an explicit custom_provider with no
// context declared, so without this Hermes has to guess via its own
// model-family auto-detection -- which is exactly how hermes + openai/gpt-4.1
// -mini failed deterministically (ICP walkthrough finding): gpt-4.1-mini
// actually has a ~1M-token window, real and verified below, but Hermes's own
// guess for it landed under the 64k floor anyway. Declaring the real number
// explicitly removes the guess instead of refusing the combination.
//
// Checked 2026-09-19 against developers.openai.com/api/docs/models/<id>.
var ModelContextWindows = map[string]int{
	"openai/gpt-4.1":      1047576,
	"openai/gpt-4.1-mini": 1047576,
	"openai/gpt-4o":       128000,
	"openai/gpt-4o-mini":  128000,
	"openai/o3-mini":      200000,
}

// contextWindowFallback is the conservative default for a "provider/model"
// not listed in ModelContextWindows: each provider's own
// Capabilities.ContextLimit (internal/model/{openai,anthropic,gemini}.go),
// the figures this codebase already states elsewhere -- not a fresh guess,
// and every one of them comfortably clears Hermes's 64,000-token floor.
//
// ollama has no entry, deliberately: a local model's real context window
// ranges from ~2k to over 1M depending entirely on what the operator pulled
// and how they configured it, so a blanket default here could just as easily
// understate it (harmless) as overstate it (Hermes then sends a prompt the
// local server truncates or rejects -- a new, different failure). Ollama's
// own /v1/models-style endpoint can report the real number, which is why
// Hermes's own docs single it out as the one case its auto-detection is
// actually reliable for (see the FAQ) -- so it is left to that.
var contextWindowFallback = map[string]int{
	"openai":     128000,
	"anthropic":  200000,
	"gemini":     1000000,
	"deepseek":   128000,
	"openrouter": 128000,
}

// ContextWindowFor returns the context window, in tokens, to declare for a
// provider/model pair, or 0 when nothing should be declared explicitly (an
// unrecognized provider, or ollama -- see contextWindowFallback).
func ContextWindowFor(provider, model string) int {
	if v, ok := ModelContextWindows[provider+"/"+model]; ok {
		return v
	}
	if v, ok := contextWindowFallback[provider]; ok {
		return v
	}
	return 0
}

// Per-model output-ceiling accuracy (round-6 B1, the change-set adversarial
// review truncation defect) is owned by pricing.go's modelMaxOutput /
// MaxOutputFor, not this file -- see pricing.go.
