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

// ProviderHelp is the single home for the --provider value list, shared by
// every command so help text can't drift per-command.
const ProviderHelp = "anthropic|openai|ollama|openrouter|codex|opencode-go|opencode-zen"

// newExtractor builds the provider adapter with run-logging attached.
// One home for the provider switch so scrape and extract can't drift apart.
// Unknown providers error loudly — a typo must never silently bill Anthropic.
func newExtractor(provider, key, model string, sch *extract.Schema, db *store.DB, runID string) (extract.Extractor, error) {
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
		return a, nil
	case "anthropic":
		a := extract.NewAnthropic("", key, model, sch)
		a.Log = log
		return a, nil
	case "openrouter":
		a := extract.NewOpenAI("https://openrouter.ai/api/v1", key, model, sch)
		a.Provider = "openrouter"
		a.Log = log
		a.ExtraHeaders = map[string]string{
			"HTTP-Referer": "https://github.com/you/gomagpie",
			"X-Title":      "magpie",
		}
		// Hard schema routing: without require_parameters OpenRouter may
		// route to an upstream that treats the schema as a hint.
		a.BodyExtra = map[string]any{"provider": map[string]any{"require_parameters": true}}
		return a, nil
	case "opencode-go", "opencode-zen":
		id := strings.ToLower(provider)
		base := "https://opencode.ai/zen/v1"
		if id == "opencode-go" {
			base = "https://opencode.ai/zen/go/v1"
		}
		headers := map[string]string{"User-Agent": extract.MagpieUA}
		if extract.ZenUsesMessages(model) {
			a := extract.NewAnthropic(base, key, model, sch)
			a.Provider = id
			a.SessionID = runID
			a.ExtraHeaders = headers
			a.Log = log
			return a, nil
		}
		a := extract.NewOpenAI(base, key, model, sch)
		a.Provider = id
		a.SessionID = runID
		a.ExtraHeaders = headers
		a.Log = log
		return a, nil
	case "codex":
		a := extract.NewCodexExec(model, sch, log)
		if err := a.Preflight(); err != nil {
			return nil, err
		}
		return a, nil
	default:
		return nil, fail(2, "unknown provider %q (want %s)", provider, ProviderHelp)
	}
}

// checkCostCeiling fails closed: an unknown running total aborts before any
// LLM spend. promptText is the full prompt that ProjectedCost prices.
// Flat-rate providers (codex, opencode-go) bill the subscription, not the
// call, so the ceiling exempts them immediately instead of projecting a
// bogus per-token floor.
func checkCostCeiling(db *store.DB, runID, provider, model, promptText string, maxCost float64) error {
	if maxCost <= 0 {
		return nil
	}
	if extract.IsFlatRateProvider(provider) {
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

// needsAPIKey reports whether a provider needs an API key: ollama is local,
// codex shells out to the user's own logged-in CLI.
func needsAPIKey(p string) bool {
	switch strings.ToLower(p) {
	case "ollama", "codex":
		return false
	}
	return true
}

func uuidNew() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("%x-%d", b, os.Getpid())
	}
	// ponytail: timestamp+pid fallback on RNG failure; collision needs same-ns fork + broken RNG.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}
