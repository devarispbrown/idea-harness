package executor

import "fmt"

// ModelCompat is a verdict on one executor-kind/model combination: whether it
// is known to fail deterministically, or simply untested (§ docs/agents.md
// "Known executor/model compatibility").
type ModelCompat struct {
	// Blocked means the combination is known to fail every time -- a real
	// call, not a flaky one -- and the caller should refuse rather than spend
	// an image pull or a paid call on it.
	Blocked bool
	// Message explains why, and how to change it. Empty when the combination
	// is verified and nothing needs to be said.
	Message string
}

// modelIncompatibility is one row of the known-bad table: an executor kind
// paired with a "provider/model" spec (or "provider/*" for the whole
// provider) that fails deterministically, and why.
type modelIncompatibility struct {
	kind, spec, reason string
}

// knownIncompatible is intentionally small and data-driven (ICP walkthrough
// finding: "hermes + gpt-4.1-mini failed deterministically every time", with
// no preflight to catch it before a paid call). Add a row here only once a
// combination has actually been observed to fail every time, with a reason --
// this is not a place to guess at every model nobody has tried yet; that is
// what the "untested" warning below is for.
var knownIncompatible = []modelIncompatibility{
	{
		kind: "hermes",
		spec: "openai/gpt-4.1-mini",
		reason: "Hermes Agent requires at least 64,000 tokens of context for its tool-calling loop " +
			"(https://hermes-agent.nousresearch.com/docs/reference/faq) and refuses to start below that floor. " +
			"Every provider here is wired into Hermes as an explicit custom_provider (see hermescfg.go), which " +
			"idea-harness never declares model.context_length for -- so Hermes falls back to its own model-family " +
			"auto-detection to guess the window. For gpt-4.1-mini that guess deterministically lands under the " +
			"floor (the same failure mode as NousResearch/hermes-agent#24000, a custom endpoint falling back to " +
			"32,768 tokens), so the agent never starts. Choose a verified model below, or a model you know has a " +
			"large context window.",
	},
}

// verifiedModels lists the "provider/model" specs docs/agents.md's compatibility
// table names as actually run end-to-end (`--smoke`) against the pinned image.
// Anything else is not known-bad, just untested -- CheckModelCompat warns and
// recommends the same smoke test rather than refusing.
var verifiedModels = map[string][]string{
	"hermes": {
		"openai/gpt-5.6-luna",
		"anthropic/claude-sonnet-5",
		"gemini/gemini-3.6-flash",
		"deepseek/deepseek-v4-pro",
		"openrouter/anthropic/claude-sonnet-5",
		// Ollama's verified model is a local pull, matched by prefix below
		// rather than an exact spec (ollama/qwen3.8:latest, qwen3.8, etc. all
		// name the same pull).
		"ollama/qwen3.8",
	},
}

// CheckModelCompat reports whether kind (hermes|omp) paired with modelSpec
// ("provider/model") is known to fail, or merely untested. modelSpec with no
// provider prefix (a bare model, or "" when nothing is configured yet) is
// always treated as untested -- there is nothing to match in either table.
func CheckModelCompat(kind, modelSpec string) ModelCompat {
	for _, row := range knownIncompatible {
		if row.kind != kind {
			continue
		}
		if row.spec == modelSpec || (len(row.spec) > 2 && row.spec[len(row.spec)-2:] == "/*" && hasProviderPrefix(modelSpec, row.spec)) {
			return ModelCompat{Blocked: true, Message: fmt.Sprintf(
				"%s + %s is a known-incompatible combination: %s", kind, modelSpec, row.reason)}
		}
	}
	verified := verifiedModels[kind]
	if verified == nil || modelSpec == "" {
		return ModelCompat{}
	}
	for _, v := range verified {
		if v == modelSpec || (len(modelSpec) >= len(v) && modelSpec[:len(v)] == v) {
			return ModelCompat{}
		}
	}
	return ModelCompat{Message: fmt.Sprintf(
		"%s + %s is untested (not in docs/agents.md's verified list); run `idea executors setup --kind %s --model %s --smoke --yes` "+
			"to confirm it can produce a real file change before relying on it for a real run.", kind, modelSpec, kind, modelSpec)}
}

// hasProviderPrefix reports whether modelSpec's provider matches a
// "provider/*" wildcard row.
func hasProviderPrefix(modelSpec, wildcardSpec string) bool {
	provider := wildcardSpec[:len(wildcardSpec)-2]
	return len(modelSpec) > len(provider) && modelSpec[:len(provider)+1] == provider+"/"
}
