package executor

import "testing"

// TestCheckModelCompatNoLongerBlocksGPT41Mini pins the actual fix for the ICP
// walkthrough finding (hermes + gpt-4.1-mini failed deterministically): the
// root cause was hermescfg.go never declaring model.context_length, so
// Hermes's own auto-detection guessed under its 64,000-token floor for a
// model that actually has roughly a 1M-token window. Once hermesConfigYAML
// declares it explicitly (see hermescfg.go / model.ContextWindowFor), the
// combination is not genuinely incompatible -- refusing it here would have
// been treating the symptom. It is not verified either, so it still gets the
// same "untested, run --smoke" advisory as any other unverified model.
func TestCheckModelCompatNoLongerBlocksGPT41Mini(t *testing.T) {
	got := CheckModelCompat("hermes", "openai/gpt-4.1-mini")
	if got.Blocked {
		t.Fatal("gpt-4.1-mini is blocked, but the root cause (missing context_length) is now fixed in hermescfg.go")
	}
	if got.Message == "" || !contains(got.Message, "--smoke") {
		t.Errorf("message = %q, want an untested/--smoke advisory (not verified, not blocked)", got.Message)
	}
}

// TestCheckModelCompatBlocksAKnownBadRow proves the blocking mechanism itself
// still works, independent of whatever knownIncompatible currently holds
// (empty as of this fix -- see its own doc comment): a row placed there is
// refused, not merely warned about. The table exists for the next combination
// that turns out to be genuinely, unfixably incompatible.
func TestCheckModelCompatBlocksAKnownBadRow(t *testing.T) {
	saved := knownIncompatible
	knownIncompatible = []modelIncompatibility{{kind: "hermes", spec: "openai/test-only-bad-model", reason: "test fixture"}}
	t.Cleanup(func() { knownIncompatible = saved })

	got := CheckModelCompat("hermes", "openai/test-only-bad-model")
	if !got.Blocked {
		t.Fatal("a row in knownIncompatible was not blocked")
	}
	if got.Message == "" || !contains(got.Message, "test fixture") {
		t.Errorf("message = %q, want it to include the row's reason", got.Message)
	}
}

// TestCheckModelCompatAcceptsVerifiedCombinations is the other direction: the
// combinations docs/agents.md names as smoke-tested must not be flagged at
// all -- neither blocked nor warned about.
func TestCheckModelCompatAcceptsVerifiedCombinations(t *testing.T) {
	for _, spec := range []string{
		"openai/gpt-5.6-luna",
		"anthropic/claude-sonnet-5",
		"gemini/gemini-3.6-flash",
		"deepseek/deepseek-v4-pro",
		"openrouter/anthropic/claude-sonnet-5",
		"ollama/qwen3.8",
		"ollama/qwen3.8:latest",
	} {
		got := CheckModelCompat("hermes", spec)
		if got.Blocked || got.Message != "" {
			t.Errorf("verified combination hermes+%s was flagged: %+v", spec, got)
		}
	}
}

// TestCheckModelCompatWarnsOnAnUntestedCombination: a model that is neither
// known-bad nor verified must produce an advisory pointing at --smoke, not a
// silent pass and not an outright refusal.
func TestCheckModelCompatWarnsOnAnUntestedCombination(t *testing.T) {
	got := CheckModelCompat("hermes", "openai/some-new-model-nobody-has-tried")
	if got.Blocked {
		t.Fatal("an untested combination must not be refused outright")
	}
	if got.Message == "" {
		t.Fatal("an untested combination should still say so")
	}
	if want := "--smoke"; !contains(got.Message, want) {
		t.Errorf("message = %q, want it to mention %q", got.Message, want)
	}
}

// TestCheckModelCompatIgnoresOtherKinds: the container/internal executors
// (and OMP, which has no verified-model table yet) must never be flagged --
// this table only knows about hermes today.
func TestCheckModelCompatIgnoresUnknownKinds(t *testing.T) {
	got := CheckModelCompat("internal", "openai/gpt-4.1-mini")
	if got.Blocked || got.Message != "" {
		t.Errorf("a kind with no compat table was flagged: %+v", got)
	}
}

// TestCheckModelCompatIgnoresAnEmptyModel: nothing configured yet is not
// "untested" in the sense that deserves a nudge to run --smoke.
func TestCheckModelCompatIgnoresAnEmptyModel(t *testing.T) {
	got := CheckModelCompat("hermes", "")
	if got.Blocked || got.Message != "" {
		t.Errorf("an empty model spec was flagged: %+v", got)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
