package executor

import "testing"

// TestCheckModelCompatBlocksTheKnownBadCombination pins the ICP walkthrough
// finding: hermes + gpt-4.1-mini failed deterministically, every run, with no
// preflight to catch it before a paid call. Once a combination is in the
// known-incompatible table it must be refused, not merely warned about.
func TestCheckModelCompatBlocksTheKnownBadCombination(t *testing.T) {
	got := CheckModelCompat("hermes", "openai/gpt-4.1-mini")
	if !got.Blocked {
		t.Fatal("known-incompatible combination was not blocked")
	}
	if got.Message == "" {
		t.Fatal("a blocked combination must say why")
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
