package scrape

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gomagpie/extract"
	"gomagpie/store"
)

// AutoProviderOrder is the documented `--provider auto` priority: keyed
// providers first in listed order, keyless local/CLI providers last.
// Providers needing a key with none configured are skipped.
var AutoProviderOrder = []string{
	"anthropic", "openai", "openrouter", "opencode-zen", "opencode-go", "ollama", "codex",
}

// PromptOptions configures one schema-less prompt call. Provider "auto"
// tries AutoProviderOrder; Purpose is the llm_calls attribution
// ("summarize" | "extract") — the adapter itself never logs, the caller
// owns attribution (see extract.Prompter).
type PromptOptions struct {
	Provider string
	Model    string
	MaxCost  float64
	System   string
	User     string
	Purpose  string
}

// PromptResult is one prompt call: text plus the winning provider and its usage.
type PromptResult struct {
	Text     string
	Provider string
	Usage    extract.TokenUsage
}

// Prompt runs one schema-less LLM call through the ExtractorFor seam,
// type-asserting the product to extract.Prompter (loud error on failure —
// no new seam). Explicit providers fail fast with verbatim errors;
// "auto" falls through to the next candidate and aggregates total failure
// into one error listing every attempt.
func Prompt(ctx context.Context, d Deps, runID string, o PromptOptions) (PromptResult, error) {
	if d.DB == nil {
		return PromptResult{}, fmt.Errorf("scrape: prompt: nil DB")
	}
	providers := []string{o.Provider}
	if o.Provider == "auto" {
		providers = autoCandidates(d)
		if len(providers) == 0 {
			return PromptResult{}, fmt.Errorf("scrape: prompt: --provider auto: no provider has a key (tried %s)", strings.Join(AutoProviderOrder, ", "))
		}
	}
	var attempted []string
	var lastErr error
	for _, p := range providers {
		key := ""
		if d.APIKeyFor != nil {
			key = d.APIKeyFor(p)
		}
		if key == "" && needsAPIKey(p) {
			merr := fmt.Errorf("scrape: provider %s: %w", p, ErrMissingKey)
			if o.Provider != "auto" {
				return PromptResult{}, merr
			}
			continue // auto pre-filters, but a nil APIKeyFor still guards here
		}
		if err := checkCostCeiling(d.DB, runID, p, o.Model, o.User, o.MaxCost); err != nil {
			return PromptResult{}, err
		}
		attempted = append(attempted, p)
		ex, err := d.ExtractorFor(p, key, o.Model, nil, runID)
		if err != nil {
			lastErr = err
			if o.Provider != "auto" {
				return PromptResult{}, err
			}
			continue
		}
		pr, ok := ex.(extract.Prompter)
		if !ok {
			lastErr = fmt.Errorf("scrape: prompt: provider %s does not support prompt mode", p)
			if o.Provider != "auto" {
				return PromptResult{}, lastErr
			}
			continue
		}
		text, usage, err := pr.PromptText(ctx, o.System, o.User)
		if err != nil {
			// Model errors propagate verbatim on explicit provider; auto
			// falls through to the next candidate.
			lastErr = err
			if o.Provider != "auto" {
				return PromptResult{}, err
			}
			continue
		}
		if lerr := d.DB.LogLLMCall(runID, store.LLMCall{
			Provider: p, Model: o.Model,
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			USDEstimate: usage.USDEstimate, Purpose: o.Purpose,
		}); lerr != nil {
			fmt.Fprintf(os.Stderr, "warning: log llm call: %v\n", lerr)
		}
		return PromptResult{Text: text, Provider: p, Usage: usage}, nil
	}
	if len(attempted) == 0 {
		return PromptResult{}, fmt.Errorf("scrape: provider %s: %w", o.Provider, ErrMissingKey)
	}
	return PromptResult{}, fmt.Errorf("scrape: prompt: all providers failed (%s): %v", strings.Join(attempted, ", "), lastErr)
}

// autoCandidates returns the AutoProviderOrder entries usable without a
// missing key: keyed providers need APIKeyFor to yield a key, keyless
// (ollama/codex/"") always qualify.
func autoCandidates(d Deps) []string {
	var out []string
	for _, p := range AutoProviderOrder {
		if needsAPIKey(p) {
			if d.APIKeyFor == nil || d.APIKeyFor(p) == "" {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
