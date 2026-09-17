package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gomagpie/clean"
	"gomagpie/config"
	"gomagpie/crawl"
	"gomagpie/extract"
	"gomagpie/fetch"
	"gomagpie/scrape"
	"gomagpie/store"
	"gomagpie/vertical"
)

// ProviderHelp is the single home for the --provider value list, shared by
// every command so help text can't drift per-command.
const ProviderHelp = "anthropic|openai|ollama|openrouter|codex|opencode-go|opencode-zen"

// newExtractor builds the provider adapter with run-logging attached.
// One home for the provider switch so scrape and extract can't drift apart.
// Unknown providers error loudly — a typo must never silently bill Anthropic.
func newExtractor(provider, key, model string, sch *extract.Schema, db *store.DB, runID string) (extract.Extractor, error) {
	// Normalize once: adapters log this id into llm_calls, so raw flag
	// casing must never fork the accounting.
	provider = strings.ToLower(provider)
	log := func(purpose string, u extract.TokenUsage) {
		if err := db.LogLLMCall(runID, store.LLMCall{
			Provider: provider, Model: model,
			PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			USDEstimate: u.USDEstimate, Purpose: purpose,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: log llm call: %v\n", err)
		}
	}
	switch provider {
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
		a.Provider = provider
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
		return newZenExtractor(provider, key, model, sch, log, runID)
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

// newZenExtractor builds the OpenCode Go/Zen adapter: base URL per billing
// (flat plan vs pay-as-you-go credits), adapter per model prefix, session +
// UA headers on every call.
func newZenExtractor(provider, key, model string, sch *extract.Schema, log func(string, extract.TokenUsage), runID string) (extract.Extractor, error) {
	base := "https://opencode.ai/zen/v1"
	if provider == "opencode-go" {
		base = "https://opencode.ai/zen/go/v1"
	}
	headers := map[string]string{"User-Agent": extract.MagpieUA}
	if extract.ZenUsesMessages(model) {
		a := extract.NewAnthropic(base, key, model, sch)
		a.Provider, a.SessionID, a.ExtraHeaders, a.Log = provider, runID, headers, log
		return a, nil
	}
	a := extract.NewOpenAI(base, key, model, sch)
	a.Provider, a.SessionID, a.ExtraHeaders, a.Log = provider, runID, headers, log
	return a, nil
}

// openCmdDB opens the cache DB from resolved config. Called after all
// flag validation so usage errors fail before any I/O or file creation.
// Caller defers closeDB(db). One home so new commands stop copying the
// open + nolint-close pair (it already drifted 4×).
func openCmdDB(cfg config.Config) (*store.DB, error) {
	return store.Open(cfg.CacheDB)
}

// closeDB closes the command DB; end-of-command close errors are unactionable.
func closeDB(db *store.DB) {
	_ = db.Close() //nolint:errcheck // end of command; close error unactionable
}

// checkBrowser rejects unknown TLS fingerprints pre-I/O (exit 2). One
// home so scrape, crawl, batch can't drift; scrape.Run re-validates for
// the MCP path, which never touches the CLI.
func checkBrowser(cmd, name string) error {
	if !fetch.ValidBrowser(name) {
		return fail(2, "%s: browser %q must be chrome|firefox|random", cmd, name)
	}
	return nil
}

// scrapeDeps builds the pipeline Deps over db with the CLI extractor
// wiring. Shared so batch's goroutine fan-out uses the exact same
// constructors as single scrapes (thread-safety contract: see Deps).
func scrapeDeps(db *store.DB, cfg config.Config) scrape.Deps {
	return scrape.Deps{
		DB: db,
		ExtractorFor: func(p, key, m string, s *extract.Schema, runID string) (extract.Extractor, error) {
			return newExtractor(p, key, m, s, db, runID)
		},
		APIKeyFor: cfg.APIKey,
	}
}

// scrapeExit maps shared pipeline errors to exit codes, mirroring the
// runScrape switch: missing key → 7, cost ceiling → 6, quality → 8,
// vertical mismatch → 2, non-public address → 2. One home so new
// commands cannot drift.
func scrapeExit(err error, rawURL, provider string) error {
	switch {
	case errors.Is(err, scrape.ErrMissingKey):
		return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
	case errors.Is(err, crawl.ErrCostCeiling):
		return fail(6, "cost ceiling exceeded: %v", err)
	case errors.Is(err, clean.ErrQuality):
		return fail(8, "%s", qualityMessage(err, rawURL))
	case errors.Is(err, vertical.ErrURLMismatch):
		return fail(2, "%s", err.Error())
	case errors.Is(err, fetch.ErrPrivateAddress):
		return fail(2, "%s", err.Error())
	}
	return err
}

// autoProviders filters scrape.AutoProviderOrder to usable providers:
// keyed entries need a configured key, keyless (ollama/codex) always
// qualify. The canonical order lives in scrape (Summarize needs it too);
// this is the CLI view over the same list.
func autoProviders(cfg config.Config) []string {
	var out []string
	for _, p := range scrape.AutoProviderOrder {
		if needsAPIKey(p) && cfg.APIKey(p) == "" {
			continue
		}
		out = append(out, p)
	}
	return out
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
