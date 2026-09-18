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
//
// The rows added on 2026-09-17 are OpenRouter's published per-token prices for
// the same models that day (GET openrouter.ai/api/v1/models), which track the
// providers' own list prices. Before them, every current model fell through to
// defaultPricing: claude-sonnet-5 was budgeted at a quarter of its real rate
// and claude-fable-5-1 at a twentieth, so max_cost_usd let a run spend far
// past its ceiling. TestEveryCatalogModelIsPriced keeps the catalog and this
// table in step.
var modelPricing = map[string]Pricing{
	"gpt-5.6-luna":       {0.20, 1.20},
	"gpt-5.6-sol":        {2.00, 10.00},
	"gpt-5.6-terra":      {2.00, 12.00},
	"gpt-5.5":            {5.00, 30.00},
	"gpt-5.4-mini":       {0.75, 4.50},
	"gpt-4o":             {2.50, 10.00},
	"gpt-4o-mini":        {0.15, 0.60},
	"gpt-4.1":            {2.00, 8.00},
	"gpt-4.1-mini":       {0.40, 1.60},
	"gpt-5":              {1.25, 10.00},
	"gpt-5-mini":         {0.25, 2.00},
	"o3-mini":            {1.10, 4.40},
	"claude-sonnet-5":    {2.00, 10.00},
	"claude-opus-5":      {5.00, 25.00},
	"claude-fable-5-1":   {10.00, 50.00},
	"claude-opus-4-8":    {5.00, 25.00},
	"claude-haiku-4-5":   {1.00, 5.00},
	"claude-opus-4-1":    {15.00, 75.00},
	"claude-sonnet-4-5":  {3.00, 15.00},
	"claude-3-7-sonnet":  {3.00, 15.00},
	"claude-3-5-haiku":   {0.80, 4.00},
	"gemini-3.8-flash":   {0.75, 3.75},
	"gemini-3.7-flash":   {0.75, 3.75},
	"gemini-3.6-flash":   {0.75, 3.75},
	"gemini-3.5-flash":   {1.50, 9.00},
	"gemini-3.1-pro":     {2.00, 12.00},
	"gemini-2.5-pro":     {1.25, 10.00},
	"gemini-2.5-flash":   {0.30, 2.50},
	"gemini-2.0-flash":   {0.10, 0.40},
	"gemini-1.5-pro":     {1.25, 5.00},
	"deepseek-v4-pro":    {1.60, 3.20},
	"deepseek-flash":     {0.09, 0.18},
	"deepseek-chat":      {0.27, 1.10},
	"deepseek-reasoner":  {0.55, 2.19},
	"qwen3.8-max":        {2.00, 6.00},
	"kimi-k3":            {2.10, 10.95},
	"glm-5.3":            {1.40, 4.40},
	"meta-llama/llama-4": {0.12, 0.60},
}

// defaultPricing applies to models not in the table.
var defaultPricing = Pricing{0.50, 2.00}

// normalizeModel maps a versioned model identifier to its canonical family by
// longest-prefix match (e.g. "gpt-4o-2024-08-06" -> "gpt-4o",
// "claude-sonnet-4-5-20250929" -> "claude-sonnet-4-5"). Unknown models are
// returned unchanged and fall through to the default rate.
//
// An OpenRouter response names the model with its vendor
// ("anthropic/claude-sonnet-5", "deepseek/deepseek-v4-pro-0813") and spells
// Claude versions with dots ("claude-opus-4.8"). Both forms are tried after
// the id as given, so the same model costs the same through either route.
func normalizeModel(model string) string {
	candidates := []string{model}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		bare := model[i+1:]
		candidates = append(candidates, bare, strings.ReplaceAll(bare, ".", "-"))
	}
	for _, c := range candidates {
		if best := longestPrefix(c); best != "" {
			return best
		}
	}
	return model
}

func longestPrefix(model string) string {
	best := ""
	for k := range modelPricing {
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	return best
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
