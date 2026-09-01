package model

import "testing"

func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"gpt-4o-2024-08-06":           "gpt-4o",
		"gpt-4o-mini-2024-07-18":      "gpt-4o-mini",
		"claude-sonnet-4-5-20250929":  "claude-sonnet-4-5",
		"gemini-2.5-flash-001":        "gemini-2.5-flash",
		"gpt-5.2":                     "gpt-5",
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
