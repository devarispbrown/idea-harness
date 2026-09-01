package model

import "strings"

// Pricing is the USD cost per 1,000,000 tokens for a model family.
type Pricing struct {
	InputUSD  float64
	OutputUSD float64
}

// modelPricing maps canonical model names to per-1M-token prices. Prices are
// approximate list rates (review M4 cost pricing); they make max_cost_usd
// enforceable and are the single place to update when rates change.
var modelPricing = map[string]Pricing{
	"gpt-4o":             {2.50, 10.00},
	"gpt-4o-mini":        {0.15, 0.60},
	"gpt-4.1":            {2.00, 8.00},
	"gpt-4.1-mini":       {0.40, 1.60},
	"gpt-5":              {1.25, 10.00},
	"gpt-5-mini":         {0.25, 2.00},
	"o3-mini":            {1.10, 4.40},
	"claude-opus-4-1":    {15.00, 75.00},
	"claude-sonnet-4-5":  {3.00, 15.00},
	"claude-3-7-sonnet":  {3.00, 15.00},
	"claude-3-5-haiku":   {0.80, 4.00},
	"gemini-2.5-pro":     {1.25, 10.00},
	"gemini-2.5-flash":   {0.30, 2.50},
	"gemini-2.0-flash":   {0.10, 0.40},
	"gemini-1.5-pro":     {1.25, 5.00},
	"deepseek-chat":      {0.27, 1.10},
	"deepseek-reasoner":  {0.55, 2.19},
	"meta-llama/llama-4": {0.12, 0.60},
}

// defaultPricing applies to models not in the table.
var defaultPricing = Pricing{0.50, 2.00}

// normalizeModel maps a versioned model identifier to its canonical family by
// longest-prefix match (e.g. "gpt-4o-2024-08-06" -> "gpt-4o",
// "claude-sonnet-4-5-20250929" -> "claude-sonnet-4-5"). Unknown models are
// returned unchanged and fall through to the default rate.
func normalizeModel(model string) string {
	best := ""
	for k := range modelPricing {
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return best
	}
	return model
}

// CostUSD returns the cost of input/out tokens (counts) for a model name, using
// the canonical pricing table and longest-prefix normalization.
func CostUSD(model string, inputTokens, outputTokens int64) float64 {
	p, ok := modelPricing[normalizeModel(model)]
	if !ok {
		p = defaultPricing
	}
	return (float64(inputTokens)/1e6)*p.InputUSD + (float64(outputTokens)/1e6)*p.OutputUSD
}
