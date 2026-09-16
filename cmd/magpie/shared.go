package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"gomagpie/extract"
	"gomagpie/fetch"
	"gomagpie/store"
)

// newExtractor builds the provider adapter with run-logging attached.
// One home for the provider switch so scrape and extract can't drift apart.
func newExtractor(provider, key, model string, sch *extract.Schema, db *store.DB, runID string) extract.Extractor {
	log := func(purpose string, u extract.TokenUsage) {
		if err := db.LogLLMCall(runID, store.LLMCall{
			Provider: provider, Model: model,
			PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			USDEstimate: u.USDEstimate, Purpose: purpose,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: log llm call: %v\n", err)
		}
	}
	switch strings.ToLower(provider) {
	case "openai", "ollama":
		a := extract.NewOpenAI("", key, model, sch)
		a.Log = log
		return a
	default:
		a := extract.NewAnthropic("", key, model, sch)
		a.Log = log
		return a
	}
}

// checkCostCeiling fails closed: an unknown running total aborts before any
// LLM spend. promptText is the full prompt that ProjectedCost prices.
func checkCostCeiling(db *store.DB, runID, model, promptText string, maxCost float64) error {
	if maxCost <= 0 {
		return nil
	}
	running, err := db.RunCost(runID)
	if err != nil {
		return err // fail closed: never spend against an unknown total
	}
	proj := extract.ProjectedCost(model, promptText)
	// When price is unknown (0), any positive ceiling with real content aborts:
	// estimate prompt tokens × a reference floor so a near-zero ceiling trips.
	if proj == 0 {
		if toks := extract.EstimatePromptTokens(promptText); toks > 0 {
			proj = float64(toks) / 1e6 * 2.00 // reference input price floor
		}
	}
	if running+proj > maxCost {
		return fail(6, "cost ceiling exceeded: running %.6f + projected %.6f > max %.6f", running, proj, maxCost)
	}
	return nil
}

// fetchBrowser runs one browser fetch with a log line. Single home for the
// rod lifecycle so the render=browser and auto-escalation paths match.
func fetchBrowser(ctx context.Context, rawURL, msg string) (*fetch.FetchResponse, bool, error) {
	rod := fetch.NewRodFetcher()
	defer func() { _ = rod.Close() }() //nolint:errcheck // browser teardown; failure unactionable
	fmt.Fprintln(os.Stderr, msg)
	resp, err := rod.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
	if err != nil {
		return nil, true, err
	}
	return resp, true, nil
}

type markdownOut struct {
	URL            string          `json:"url"`
	FinalURL       string          `json:"final_url"`
	Title          string          `json:"title"`
	Markdown       string          `json:"markdown"`
	StructuredData json.RawMessage `json:"structured_data"`
}

type usageOut struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	USDEstimate      float64 `json:"usd_estimate"`
	Attempts         int     `json:"attempts"`
}

type extractedOut struct {
	URL       string         `json:"url"`
	FinalURL  string         `json:"final_url"`
	Title     string         `json:"title"`
	Extracted map[string]any `json:"extracted"`
	Usage     usageOut       `json:"usage"`
	FromCache bool           `json:"from_cache"`
}

func marshalOut(v any, what string) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%s: marshal output: %w", what, err)
	}
	return string(b), nil
}

func writeOut(path, s string) error {
	if path == "" {
		fmt.Println(s)
		return nil
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return fmt.Errorf("write out: %w", err)
	}
	return nil
}

func isFreeProvider(p string) bool {
	return strings.ToLower(p) == "ollama"
}

func uuidNew() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("%x-%d", b, os.Getpid())
	}
	// ponytail: timestamp+pid fallback on RNG failure; collision needs same-ns fork + broken RNG.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}
