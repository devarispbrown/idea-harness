package model

import (
	"regexp"
	"strings"
)

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
	"gpt-5.6-luna":          {0.20, 1.20},
	"gpt-5.6-luna-pro":      {0.20, 1.20},
	"gpt-5.6-sol":           {2.00, 10.00},
	"gpt-5.6-sol-pro":       {2.00, 10.00},
	"gpt-5.6-terra":         {2.00, 12.00},
	"gpt-5.6-terra-pro":     {2.00, 12.00},
	"gpt-5.5":               {5.00, 30.00},
	"gpt-5.5-pro":           {30.00, 180.00},
	"gpt-5.4":               {2.50, 15.00},
	"gpt-5.4-pro":           {30.00, 180.00},
	"gpt-5.4-mini":          {0.75, 4.50},
	"gpt-5.4-nano":          {0.20, 1.25},
	"gpt-5.3-codex":         {1.75, 14.00},
	"gpt-5.2":               {1.75, 14.00},
	"gpt-5.2-codex":         {1.75, 14.00},
	"gpt-5.2-pro":           {21.00, 168.00},
	"gpt-5.1":               {1.25, 10.00},
	"gpt-5.1-codex":         {1.25, 10.00},
	"gpt-5.1-codex-max":     {1.25, 10.00},
	"gpt-5.1-codex-mini":    {0.25, 2.00},
	"gpt-5":                 {1.25, 10.00},
	"gpt-5-codex":           {1.25, 10.00},
	"gpt-5-pro":             {15.00, 120.00},
	"gpt-5-mini":            {0.25, 2.00},
	"gpt-5-nano":            {0.05, 0.40},
	"gpt-4.1":               {2.00, 8.00},
	"gpt-4.1-mini":          {0.40, 1.60},
	"gpt-4.1-nano":          {0.10, 0.40},
	"gpt-4o":                {2.50, 10.00},
	"gpt-4o-mini":           {0.15, 0.60},
	"o1":                    {15.00, 60.00},
	"o1-pro":                {150.00, 600.00},
	"o3":                    {2.00, 8.00},
	"o3-pro":                {20.00, 80.00},
	"o3-mini":               {1.10, 4.40},
	"o4-mini":               {1.10, 4.40},
	"claude-sonnet-5":       {2.00, 10.00},
	"claude-opus-5":         {5.00, 25.00},
	"claude-fable-5-1":      {10.00, 50.00},
	"claude-fable-5":        {10.00, 50.00},
	"claude-opus-4-8":       {5.00, 25.00},
	"claude-opus-4-7":       {5.00, 25.00},
	"claude-opus-4-6":       {5.00, 25.00},
	"claude-opus-4-5":       {5.00, 25.00},
	"claude-sonnet-4-6":     {3.00, 15.00},
	"claude-sonnet-4-5":     {3.00, 15.00},
	"claude-haiku-4-5":      {1.00, 5.00},
	"claude-opus-4-1":       {15.00, 75.00},
	"claude-3-7-sonnet":     {3.00, 15.00},
	"claude-3-5-haiku":      {0.80, 4.00},
	"gemini-3.8-flash":      {0.75, 3.75},
	"gemini-3.7-flash":      {0.75, 3.75},
	"gemini-3.6-flash":      {0.75, 3.75},
	"gemini-3.5-flash":      {1.50, 9.00},
	"gemini-3.5-flash-lite": {0.30, 2.50},
	"gemini-3.1-pro":        {2.00, 12.00},
	"gemini-2.5-pro":        {1.25, 10.00},
	"gemini-2.5-flash":      {0.30, 2.50},
	"gemini-2.0-flash":      {0.10, 0.40},
	"gemini-1.5-pro":        {1.25, 5.00},
	"deepseek-v4-pro":       {1.60, 3.20},
	"deepseek-v4-pro-0813":  {1.32, 3.96},
	"deepseek-v4-flash":     {0.09, 0.18},
	"deepseek-flash":        {0.09, 0.18},
	"deepseek-chat":         {0.27, 1.10},
	"deepseek-reasoner":     {0.55, 2.19},
	"qwen3.8-max":           {2.00, 6.00},
	"kimi-k3":               {2.10, 10.95},
	"glm-5.3":               {1.40, 4.40},
	"glm-5.3-flash":         {0.09, 0.30},
	"llama-4-maverick":      {0.19, 0.65},
}

// defaultPricing applies to models not in the table.
var defaultPricing = Pricing{0.50, 2.00}

// normalizeModel maps a model identifier to its row in modelPricing, or
// returns it unchanged (and it is charged defaultPricing).
//
// A row matches the id exactly, or as a prefix followed ONLY by a date or
// release suffix ("gpt-4o-2024-08-06" -> "gpt-4o",
// "claude-sonnet-4-5-20250929" -> "claude-sonnet-4-5",
// "gemini-3.1-pro-preview" -> "gemini-3.1-pro"). A bare longest-prefix match
// priced different models as their family: gpt-5.4-pro ($30/$180) as gpt-5
// ($1.25/$10), eighteen times under, so max_cost_usd let it spend far past
// the ceiling.
//
// The id is also tried without a vendor prefix ("anthropic/claude-sonnet-5",
// as OpenRouter returns it) and with dots in a version spelled as dashes
// ("claude-opus-4.8"), so one model costs the same by any route.
func normalizeModel(model string) string {
	candidates := []string{model}
	bare := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		bare = model[i+1:]
		candidates = append(candidates, bare)
	}
	if dashed := strings.ReplaceAll(bare, ".", "-"); dashed != bare {
		candidates = append(candidates, dashed)
	}
	for _, c := range candidates {
		if best := priceRow(c); best != "" {
			return best
		}
	}
	return model
}

// releaseSuffix is what may follow a price row's key in an id that is still
// the same model: a date, a snapshot number, a channel, or an OpenRouter
// variant tag such as ":free".
var releaseSuffix = regexp.MustCompile(`^(-\d{4}-\d{2}-\d{2}|-\d{8}|-\d{3,4}|-latest|-preview(-[a-z0-9-]+)?|-exp(-[a-z0-9-]+)?|-high|-chat(-latest)?)?(:[a-z0-9-]+)?$`)

func priceRow(model string) string {
	best := ""
	for k := range modelPricing {
		if strings.HasPrefix(model, k) && len(k) > len(best) && releaseSuffix.MatchString(model[len(k):]) {
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
