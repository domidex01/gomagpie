package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gomagpie/clean"
	"gomagpie/config"
	"gomagpie/extract"
	"gomagpie/fetch"
	"gomagpie/store"
)

func newScrapeCmd() *cobra.Command {
	var schema, render, provider, model, out, format string
	var noCache bool
	cmd := &cobra.Command{
		Use:   "scrape <url>",
		Short: "Fetch → clean → extract a single URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScrape(cmd.Context(), args[0], scrapeOptions{
				Schema: schema, Render: render, Provider: provider, Model: model,
				Out: out, Format: format, NoCache: noCache,
			})
		},
	}
	cmd.Flags().StringVar(&schema, "schema", "", "JSON Schema file (yaml/json)")
	cmd.Flags().StringVar(&render, "render", "", "auto|static|browser")
	cmd.Flags().StringVar(&provider, "provider", "", "anthropic|openai|ollama")
	cmd.Flags().StringVar(&model, "model", "", "model name")
	cmd.Flags().StringVar(&out, "out", "", "output path (default stdout)")
	cmd.Flags().StringVar(&format, "format", "", "json|jsonl (csv|sqlite not supported in Phase 1)")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "bypass selector cache")
	return cmd
}

type scrapeOptions struct {
	Schema   string
	Render   string
	Provider string
	Model    string
	Out      string
	Format   string
	NoCache  bool
}

func runScrape(ctx context.Context, rawURL string, o scrapeOptions) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	applyScrapeFlags(&cfg, o)

	if cfg.Format == "csv" || cfg.Format == "sqlite" {
		return fail(2, "format %q not supported in Phase 1 (use json|jsonl)", cfg.Format)
	}
	if cfg.Format == "" {
		cfg.Format = "json"
	}
	if cfg.Format != "json" && cfg.Format != "jsonl" {
		return fail(2, "format %q must be json|jsonl", cfg.Format)
	}
	if cfg.Render == "" {
		cfg.Render = "auto"
	}
	switch cfg.Render {
	case "auto", "static", "browser":
	default:
		return fail(2, "render %q must be auto|static|browser", cfg.Render)
	}

	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer db.Close()

	runID := uuidNew()
	if err := db.BeginRun(runID, "scrape"); err != nil {
		return err
	}
	finish := func(ok, er int, status string) {
		_ = db.FinishRun(runID, ok, er, status)
	}

	// --- fetch ---
	staticFetcher, err := fetch.NewStaticFetcher()
	if err != nil {
		finish(0, 1, "error")
		return err
	}
	page, usedBrowser, err := fetchURL(ctx, staticFetcher, rawURL, cfg.Render)
	if err != nil {
		finish(0, 1, "error")
		return err
	}
	_ = usedBrowser

	// --- clean ---
	cleaned, err := clean.Clean(ctx, clean.RawPage{HTML: page.HTML, URL: page.URL, FinalURL: page.FinalURL})
	if err != nil {
		finish(0, 1, "error")
		return err
	}

	// No schema → print markdown, no LLM.
	if cfg.Schema == "" {
		finish(1, 0, "finished")
		return writeOut(cfg.Out, markdownDoc(cleaned, page))
	}

	// --- extract ---
	sch, err := extract.LoadSchema(cfg.Schema)
	if err != nil {
		finish(0, 1, "error")
		return err
	}
	provider := cfg.ExtractProvider
	if o.Provider != "" {
		provider = o.Provider
	}
	model := cfg.Model
	if o.Model != "" {
		model = o.Model
	}
	key := cfg.APIKey(provider)
	if key == "" && !isFreeProvider(provider) {
		finish(0, 0, "error")
		return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
	}
	var ex extract.Extractor
	switch strings.ToLower(provider) {
	case "openai", "ollama":
		a := extract.NewOpenAI("", key, model, sch)
		a.Log = func(purpose string, u extract.TokenUsage) {
			_ = db.LogLLMCall(runID, store.LLMCall{Provider: provider, Model: model, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, USDEstimate: u.USDEstimate, Purpose: purpose})
		}
		ex = a
	default:
		a := extract.NewAnthropic("", key, model, sch)
		a.Log = func(purpose string, u extract.TokenUsage) {
			_ = db.LogLLMCall(runID, store.LLMCall{Provider: provider, Model: model, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, USDEstimate: u.USDEstimate, Purpose: purpose})
		}
		ex = a
	}

	// --max-cost pre-check: running total + projected next-call cost.
	if cfg.MaxCost > 0 {
		running, _ := db.RunCost(runID)
		proj := extract.ProjectedCost(model, buildPromptPreview(cleaned, sch))
		// When price is unknown (0), any positive ceiling with real content aborts:
		// estimate prompt tokens × a reference floor so a near-zero ceiling trips.
		if proj == 0 {
			if toks := extract.EstimatePromptTokens(cleaned.Markdown); toks > 0 {
				proj = float64(toks) / 1e6 * 2.00 // reference input price floor
			}
		}
		if running+proj > cfg.MaxCost {
			finish(0, 0, "error")
			return fail(6, "cost ceiling exceeded: running %.6f + projected %.6f > max %.6f", running, proj, cfg.MaxCost)
		}
	}

	res, err := ex.Extract(ctx, extract.ExtractInput{
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData, Schema: sch,
	})
	if err != nil {
		finish(0, 1, "error")
		return err
	}
	finish(1, 0, "finished")
	return writeOut(cfg.Out, extractedDoc(cleaned, page, res))
}

func applyScrapeFlags(cfg *config.Config, o scrapeOptions) {
	f := config.Flags{}
	if o.Provider != "" {
		f.Provider, f.ProviderChanged = o.Provider, true
	}
	if o.Model != "" {
		f.Model, f.ModelChanged = o.Model, true
	}
	if o.Render != "" {
		f.Render, f.RenderChanged = o.Render, true
	}
	if o.Format != "" {
		f.Format, f.FormatChanged = o.Format, true
	}
	if o.Out != "" {
		f.Out, f.OutChanged = o.Out, true
	}
	if o.Schema != "" {
		f.Schema, f.SchemaChanged = o.Schema, true
	}
	if o.NoCache {
		f.NoCache, f.NoCacheChanged = true, true
	}
	cfg.ApplyFlags(f)
}

func fetchURL(ctx context.Context, static *fetch.StaticFetcher, rawURL, render string) (*fetch.FetchResponse, bool, error) {
	if render == "browser" {
		rod := fetch.NewRodFetcher()
		defer rod.Close()
		fmt.Fprintln(os.Stderr, "fetch: using browser renderer")
		resp, err := rod.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil
	}
	resp, err := static.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, false, fmt.Errorf("fetch: HTTP %d for %s", resp.StatusCode, rawURL)
	}
	if render == "static" {
		return resp, false, nil
	}
	score, embedded := fetch.ScoreJSRequired(resp.HTML, resp.Headers)
	if embedded {
		return resp, false, nil
	}
	if !fetch.NeedsBrowser(score) {
		return resp, false, nil
	}
	rod := fetch.NewRodFetcher()
	defer rod.Close()
	fmt.Fprintln(os.Stderr, "fetch: escalating to browser renderer")
	rresp, err := rod.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
	if err != nil {
		return nil, true, err
	}
	return rresp, true, nil
}

func buildPromptPreview(cleaned clean.CleanedPage, sch *extract.Schema) string {
	return "Extract structured data.\n" + string(cleaned.StructuredData) + "\n" + cleaned.Markdown
}

func markdownDoc(c clean.CleanedPage, page *fetch.FetchResponse) string {
	doc := map[string]any{
		"url": page.URL, "final_url": c.FinalURL, "title": c.Title,
		"markdown": c.Markdown, "structured_data": json.RawMessage(orEmpty(c.StructuredData)),
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

func extractedDoc(c clean.CleanedPage, page *fetch.FetchResponse, res extract.ExtractResult) string {
	doc := map[string]any{
		"url": page.URL, "final_url": c.FinalURL, "title": c.Title,
		"extracted": res.Record,
		"usage": map[string]any{
			"provider": res.Provider, "model": res.Model,
			"prompt_tokens": res.Usage.PromptTokens, "completion_tokens": res.Usage.CompletionTokens,
			"usd_estimate": res.Usage.USDEstimate, "attempts": res.Attempts,
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

func orEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
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
	// Avoid a uuid dep: timestamp + pid is unique enough for run ids.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}
