package model

import "testing"

// TestMaxOutputForPinsVerifiedCeilings is the capability-accuracy pin
// (round-6 B1): every entry here was checked against the provider's own
// current model documentation on 2026-09-21 (see modelMaxOutput's doc
// comment for the exact pages). A change to any of these numbers -- an edit
// to modelMaxOutput, or a regression in the matching logic it shares with
// modelPricing -- must fail this test, not surface three audits later as a
// truncated review against a real API key.
//
// Each case is also exercised through the id forms a real caller can send:
// the bare id, an OpenRouter-style vendor-prefixed id, a dated/versioned
// snapshot id, and (for a version with dots) the dash-spelled form -- proving
// modelCandidates/maxOutputRow actually resolve them the same as CostUSD's
// normalizeModel does, not just the literal map keys.
func TestMaxOutputForPinsVerifiedCeilings(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		// Anthropic (platform.claude.com/docs/en/about-claude/models).
		{"claude-sonnet-5", 128000},
		{"claude-opus-5", 128000},
		{"claude-fable-5-1", 128000},
		{"claude-opus-4-8", 128000},
		{"claude-opus-4-7", 128000},
		{"claude-opus-4-6", 128000},
		{"claude-sonnet-4-6", 128000},
		{"claude-haiku-4-5", 64000},
		{"claude-haiku-4-5-20251001", 64000}, // dated id, same row via release-suffix matching
		{"claude-sonnet-4-5", 64000},
		{"claude-sonnet-4-5-20250929", 64000}, // dated id
		{"anthropic/claude-sonnet-5", 128000},  // OpenRouter-style vendor prefix
		{"claude-opus-4.8", 128000},            // dot-for-dash spelling

		// OpenAI (developers.openai.com/api/docs/models).
		{"gpt-5.6-luna", 128000},
		{"gpt-5.6-sol", 128000},
		{"gpt-5.6-terra", 128000},
		{"gpt-5.5", 128000},
		{"gpt-5.4-mini", 128000},
		{"gpt-4.1", 32768},
		{"gpt-4.1-mini", 32768},
		{"gpt-4o", 16384},
		{"gpt-4o-mini", 16384},
		{"gpt-4o-2024-08-06", 16384}, // dated id
		{"o3-mini", 100000},
		{"o3-mini-high", 100000}, // release-suffix variant
		{"openai/gpt-5.6-luna", 128000},

		// Gemini (ai.google.dev/gemini-api/docs/models).
		{"gemini-3.6-flash", 65536},
		{"gemini-3.7-flash", 65536},
		{"gemini-3.8-flash", 65536},
		{"gemini-3.5-flash", 65536},
		{"gemini-3.1-pro-preview", 65536}, // normalizes to the gemini-3.1-pro row
		{"google/gemini-3.6-flash", 65536},
	}
	for _, c := range cases {
		got, ok := MaxOutputFor(c.model)
		if !ok {
			t.Errorf("MaxOutputFor(%q): not found, want %d", c.model, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("MaxOutputFor(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

// TestUnknownModelHasNoPinnedCeiling: a model this catalog has not verified
// must report ok=false, not a fabricated number -- MaxOutputFor's caller
// (each provider's Complete) relies on this to fall back to its own baseline
// rather than trust an extrapolated guess.
func TestUnknownModelHasNoPinnedCeiling(t *testing.T) {
	for _, m := range []string{"", "totally-unknown-model-xyz", "gpt-9-nonexistent", "claude-nonexistent-9"} {
		if got, ok := MaxOutputFor(m); ok {
			t.Errorf("MaxOutputFor(%q) = (%d, true), want ok=false for an unverified model", m, got)
		}
	}
}

// TestEveryCurrentCatalogModelResolvesItsRealCeiling walks model.DefaultModels
// for the three native HTTP providers (deepseek/openrouter/ollama share
// OpenAIProvider's generic, unverified-per-model baseline and are not
// expected to have individual entries) and constructs the REAL provider for
// each one, exactly as workflow.runModel does, to assert the effective
// ceiling -- Capabilities().MaxOutput overridden by MaxOutputFor when known --
// matches what this catalog actually verified. This is the reachability
// guard for round-6 B1: it fails if a catalog model is added/renamed without
// its ceiling being looked up, not just if modelMaxOutput itself is edited
// wrong.
func TestEveryCurrentCatalogModelResolvesItsRealCeiling(t *testing.T) {
	want := map[string]map[string]int{
		"anthropic": {
			"claude-sonnet-5":            128000,
			"claude-opus-5":              128000,
			"claude-fable-5-1":           128000,
			"claude-haiku-4-5-20251001":  64000,
			"claude-opus-4-8":            128000,
			"claude-sonnet-4-5-20250929": 64000,
			"claude-sonnet-4-5":          64000,
		},
		"openai": {
			"gpt-5.6-luna":  128000,
			"gpt-5.6-sol":   128000,
			"gpt-5.6-terra": 128000,
			"gpt-5.5":       128000,
			"gpt-5.4-mini":  128000,
			"gpt-4.1":       32768,
			"gpt-4.1-mini":  32768,
			"gpt-4o":        16384,
			"gpt-4o-mini":   16384,
			"o3-mini":       100000,
		},
		"gemini": {
			"gemini-3.6-flash":       65536,
			"gemini-3.7-flash":       65536,
			"gemini-3.8-flash":       65536,
			"gemini-3.5-flash":       65536,
			"gemini-3.1-pro-preview": 65536,
		},
	}
	providers := map[string]Provider{
		"anthropic": NewAnthropic("k", "https://example.invalid"),
		"openai":    NewOpenAI("k", "https://example.invalid"),
		"gemini":    NewGemini("k", "https://example.invalid"),
	}
	for provName, models := range want {
		prov := providers[provName]
		for _, m := range DefaultModels[provName] {
			expected, known := models[m]
			if !known {
				t.Fatalf("test table is missing an expected ceiling for %s/%s -- "+
					"add one (look it up first) so a catalog addition cannot silently "+
					"go unverified", provName, m)
			}
			baseline := prov.Capabilities().MaxOutput
			ceiling := baseline
			if v, ok := MaxOutputFor(m); ok {
				ceiling = v
			}
			if ceiling != expected {
				t.Errorf("%s/%s effective ceiling = %d, want %d (baseline %d)", provName, m, ceiling, expected, baseline)
			}
		}
	}
}
