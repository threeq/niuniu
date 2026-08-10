package service

// ModelPricing is USD per 1M tokens for each token category.
// Values mirror LiteLLM's model_prices_and_context_window.json shape, manually
// curated for Claude family. Update when Anthropic publishes new pricing.
type ModelPricing struct {
	InputPerMTok         float64
	OutputPerMTok        float64
	CacheReadPerMTok     float64
	CacheCreationPerMTok float64
}

// modelPricingTable maps full model names (as written in JSONL `message.model`)
// to pricing. For unknown models we fall back to zero cost (still count tokens).
//
// Source: Anthropic's pricing page + LiteLLM's model_prices_and_context_window.json.
// Snapshot date: 2026-05-02. Update with each Anthropic price change.
var modelPricingTable = map[string]ModelPricing{
	// Claude 4 / 4.5 / 4.6 / 4.7 family
	"claude-opus-4-7":            {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.50, CacheCreationPerMTok: 18.75},
	"claude-opus-4-6":            {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.50, CacheCreationPerMTok: 18.75},
	"claude-opus-4-5":            {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.50, CacheCreationPerMTok: 18.75},
	"claude-opus-4-1-20250805":   {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.50, CacheCreationPerMTok: 18.75},
	"claude-opus-4-20250514":     {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.50, CacheCreationPerMTok: 18.75},
	"claude-sonnet-4-7":          {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-sonnet-4-6":          {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-sonnet-4-5-20250929": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-sonnet-4-20250514":   {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-haiku-4-5-20251001":  {InputPerMTok: 1, OutputPerMTok: 5, CacheReadPerMTok: 0.10, CacheCreationPerMTok: 1.25},
	// Claude 3.x family
	"claude-3-7-sonnet-20250219": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-3-5-sonnet-20241022": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-3-5-sonnet-20240620": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
	"claude-3-5-haiku-20241022":  {InputPerMTok: 1, OutputPerMTok: 5, CacheReadPerMTok: 0.10, CacheCreationPerMTok: 1.25},
	"claude-3-opus-20240229":     {InputPerMTok: 15, OutputPerMTok: 75, CacheReadPerMTok: 1.50, CacheCreationPerMTok: 18.75},
	"claude-3-haiku-20240307":    {InputPerMTok: 0.25, OutputPerMTok: 1.25, CacheReadPerMTok: 0.03, CacheCreationPerMTok: 0.30},
	"claude-3-sonnet-20240229":   {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75},
}

// computeCostUSD returns the USD cost for a given model + token usage.
// Returns 0 (no error) for unknown models — token counts still aggregate.
func computeCostUSD(model string, input, output, cacheRead, cacheCreation int64) float64 {
	p, ok := modelPricingTable[model]
	if !ok {
		return 0
	}
	const million = 1_000_000.0
	return (float64(input)*p.InputPerMTok +
		float64(output)*p.OutputPerMTok +
		float64(cacheRead)*p.CacheReadPerMTok +
		float64(cacheCreation)*p.CacheCreationPerMTok) / million
}
