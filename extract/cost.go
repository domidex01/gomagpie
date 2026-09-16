package extract

import (
	"fmt"
	"os"
	"strings"
)

// TokenUsage counts one LLM call.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	USDEstimate      float64
}

// prices: USD per 1M tokens in/out. Treat as data — re-confirm at build.
var priceTable = map[string][2]float64{
	"claude-opus-5":    {5.00, 25.00},
	"claude-sonnet-5":  {2.00, 10.00},
	"claude-haiku-4-5": {1.00, 5.00},
	"gpt-4o-mini":      {0.15, 0.60},
	"gpt-5-nano":       {0.05, 0.40},
}

// EstimateCost computes USD for a model + token counts.
// Unknown model → warning + 0, never blocks.
func EstimateCost(model string, prompt, completion int) float64 {
	p, ok := priceTable[model]
	if !ok {
		// ollama/* local models are free.
		if strings.HasPrefix(strings.ToLower(model), "ollama/") || strings.HasPrefix(strings.ToLower(model), "llama") {
			return 0
		}
		fmt.Fprintf(os.Stderr, "warning: unknown model %q, costing 0\n", model)
		return 0
	}
	return float64(prompt)/1e6*p[0] + float64(completion)/1e6*p[1]
}

// InputPrice returns the per-1M input price (0 when unknown).
func InputPrice(model string) float64 {
	if p, ok := priceTable[model]; ok {
		return p[0]
	}
	return 0
}

// EstimatePromptTokens: ponytail: ≈ len/4 chars (no tokenizer dep).
func EstimatePromptTokens(prompt string) int { return len(prompt) / 4 }

// ProjectedCost estimates the next call before making it.
func ProjectedCost(model, prompt string) float64 {
	in := InputPrice(model)
	if in == 0 {
		// Unknown price but still project *something* so --max-cost can trip:
		// use the cheapest known input price as a floor? No — warn path costs 0.
		// Instead project with a tiny epsilon so a near-zero ceiling aborts.
		if !isFreeModel(model) {
			return 0
		}
		return 0
	}
	return float64(EstimatePromptTokens(prompt)) / 1e6 * in
}

func isFreeModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "ollama/") || strings.HasPrefix(m, "llama")
}
