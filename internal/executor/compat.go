package executor

import (
	"fmt"
	"testing"
)

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
//
// Empty today. hermes + openai/gpt-4.1-mini is what motivated this table, but
// investigating it found a root cause this codebase could actually fix
// (hermescfg.go now declares model.context_length explicitly -- see its own
// doc comment and model.ContextWindowFor), not a real model limitation:
// gpt-4.1-mini has roughly a 1M-token context window, well above Hermes's own
// 64,000-token floor, and only failed because nothing told Hermes that and its
// own auto-detection guessed wrong. The table -- and this function -- stay for
// the next combination that turns out to be genuinely, unfixably incompatible
// rather than the same shape of bug.
var knownIncompatible = []modelIncompatibility{}

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

// TestOnlySetKnownIncompatible appends a fixture row to the known-incompatible
// table and returns a func that restores it -- for a test outside this
// package (internal/cli's, internal/daemon's and internal/workflow's own
// executor-setup tests) that needs to prove the refusal mechanism still
// works, without the test depending on knownIncompatible actually holding a
// real row (it does not, as of the fix that made gpt-4.1-mini's context
// window explicit instead of refusing it -- see knownIncompatible's own doc
// comment).
//
// This mutates package-level state with no synchronization, so calling it
// from anything other than a test would be a real, exploitable defect: any
// importer of this package could silently poison every other caller's
// CheckModelCompat result. It cannot simply move into a `_test.go` file the
// way an internal-only test seam normally would (LOW, release-gate report,
// "move it behind a test-only file") -- Go never links one package's
// `_test.go` files into another package's build, and the three callers named
// above are in different packages, so a `_test.go` version of this function
// would compile clean for `go test ./internal/executor` and then fail to
// resolve for every other package that needs it. testing.Testing() (stable
// since Go 1.21, and this module already requires 1.25) is the enforcement a
// file rename can't provide here: it reports whether the CURRENT BINARY was
// built by `go test`, not merely whether this call happens to be reachable
// from test code, so a production `idea`/`idea-harness` build panics instead
// of quietly accepting the mutation.
func TestOnlySetKnownIncompatible(kind, spec, reason string) (restore func()) {
	if !testing.Testing() {
		panic("executor: TestOnlySetKnownIncompatible must only be called from a test binary")
	}
	saved := knownIncompatible
	knownIncompatible = append(append([]modelIncompatibility{}, saved...), modelIncompatibility{kind: kind, spec: spec, reason: reason})
	return func() { knownIncompatible = saved }
}
