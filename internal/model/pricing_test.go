package model

import "testing"

func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"gpt-4o-2024-08-06":           "gpt-4o",
		"gpt-4o-mini-2024-07-18":      "gpt-4o-mini",
		"claude-sonnet-4-5-20250929":  "claude-sonnet-4-5",
		"gemini-2.5-flash-001":        "gemini-2.5-flash",
		"gpt-5.2":                     "gpt-5.2",
		"gemini-3.1-pro-preview":      "gemini-3.1-pro",
		"o3-mini-high":                "o3-mini",
		"deepseek-v4-flash:free":      "deepseek-v4-flash",
		"completely-unknown-model-v2": "completely-unknown-model-v2",
	}
	for in, want := range cases {
		if got := normalizeModel(in); got != want {
			t.Errorf("normalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCostUSDMath(t *testing.T) {
	// gpt-4o: $2.50/M input, $10/M output.
	cost := CostUSD("gpt-4o-2024-08-06", 1_000_000, 1_000_000)
	if cost != 12.50 {
		t.Fatalf("CostUSD(gpt-4o, 1M/1M) = %f, want 12.50", cost)
	}
	// Unknown model hits the default rate ($0.50/M in, $2.00/M out).
	if got := CostUSD("unknown-model", 1_000_000, 1_000_000); got != 2.50 {
		t.Fatalf("CostUSD(unknown, 1M/1M) = %f, want 2.50", got)
	}
}

// Every model the catalog offers must have a real price, by an exact or
// release-suffix match -- not by a family prefix. An unpriced model
// falls through to defaultPricing, which for claude-fable-5-1 was a twentieth
// of its rate: max_cost_usd let a run spend twenty times its ceiling.
func TestEveryCatalogModelIsPriced(t *testing.T) {
	for provider, models := range DefaultModels {
		if provider == "ollama" {
			continue // local; costs nothing per token
		}
		for _, m := range models {
			if _, ok := modelPricing[normalizeModel(m)]; !ok {
				t.Errorf("%s/%s has no price and would be budgeted at the default rate", provider, m)
			}
		}
	}
}

// The same model costs the same whether it is called directly or through
// OpenRouter, which prefixes the vendor and spells Claude versions with dots.
func TestOpenRouterIdsPriceLikeTheDirectModel(t *testing.T) {
	for routed, direct := range map[string]string{
		"anthropic/claude-sonnet-5":   "claude-sonnet-5",
		"anthropic/claude-opus-4.8":   "claude-opus-4-8",
		"openai/gpt-5.6-luna":         "gpt-5.6-luna",
		"google/gemini-3.6-flash":     "gemini-3.6-flash",
		"meta-llama/llama-4-maverick": "llama-4-maverick",
		"claude-opus-4.8":             "claude-opus-4-8",
	} {
		if got := normalizeModel(routed); got != direct {
			t.Errorf("normalizeModel(%q) = %q, want %q", routed, got, direct)
		}
	}
}

// Prefix matching must pick the model, not a shorter family that happens to
// prefix it: gpt-5.6-luna is not gpt-5.
func TestNewerModelsAreNotPricedAsTheirPrefix(t *testing.T) {
	if got := normalizeModel("gpt-5.6-luna"); got != "gpt-5.6-luna" {
		t.Errorf("gpt-5.6-luna priced as %q", got)
	}
	if got := CostUSD("claude-fable-5-1", 1_000_000, 1_000_000); got != 60 {
		t.Errorf("claude-fable-5-1 1M/1M = %f, want 60", got)
	}
}

// A family prefix is not the model. Longest-prefix matching priced these as
// their family, up to forty times under.
func TestVariantsAreNotPricedAsTheirFamily(t *testing.T) {
	for id, want := range map[string]float64{
		"gpt-5.4-pro": 30 + 180, // was gpt-5, 1.25+10
		"gpt-5-pro":   15 + 120,
		"gpt-5.5-pro": 30 + 180, // was gpt-5.5
		"o3-pro":      20 + 80,  // was the default, 0.50+2
		"o3":          2 + 8,
		"gpt-5-nano":  0.05 + 0.40, // was gpt-5, over
	} {
		if got := CostUSD(id, 1_000_000, 1_000_000); got < want-0.001 || got > want+0.001 {
			t.Errorf("%s 1M/1M = %.2f, want %.2f", id, got, want)
		}
	}
	// A model with no row is not its family: gpt-5.7-ultra starts with
	// "gpt-5" and must fall to the default, not take gpt-5's price.
	if got := normalizeModel("gpt-5.7-ultra"); got != "gpt-5.7-ultra" {
		t.Errorf("gpt-5.7-ultra priced as %q", got)
	}
	// An unrelated model that merely starts with a row's key is not that model.
	if got := normalizeModel("glm-5.3-flash-experimental-thing"); got == "glm-5.3" {
		t.Error("a different model took glm-5.3's price by prefix")
	}
}
