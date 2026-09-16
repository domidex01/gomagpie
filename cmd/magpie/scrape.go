package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gomagpie/clean"
	"gomagpie/config"
	"gomagpie/extract"
	"gomagpie/fetch"
	"gomagpie/store"

	"github.com/spf13/cobra"
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
	defer func() { _ = db.Close() }() //nolint:errcheck // end of command; close error unactionable

	runID := uuidNew()
	if err := db.BeginRun(runID, "scrape"); err != nil {
		return err
	}
	finish := func(ok, er int, status string) {
		if err := db.FinishRun(runID, ok, er, status); err != nil {
			fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", err)
		}
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
		mdoc, merr := markdownDoc(cleaned, page)
		if merr != nil {
			return merr
		}
		return writeOut(cfg.Out, mdoc)
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
	ex := newExtractor(provider, key, model, sch, db, runID)

	// --max-cost pre-check: running total + projected next-call cost.
	promptText := "Extract structured data.\n" + string(cleaned.StructuredData) + "\n" + cleaned.Markdown
	if err := checkCostCeiling(db, runID, model, promptText, cfg.MaxCost); err != nil {
		finish(0, 0, "error")
		return err
	}

	res, err := ex.Extract(ctx, extract.ExtractInput{
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData, Schema: sch,
	})
	if err != nil {
		finish(0, 1, "error")
		return err
	}
	finish(1, 0, "finished")
	edoc, merr := extractedDoc(cleaned, page, res)
	if merr != nil {
		return merr
	}
	return writeOut(cfg.Out, edoc)
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
		return fetchBrowser(ctx, rawURL, "fetch: using browser renderer")
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
	if embedded || !fetch.NeedsBrowser(score) {
		return resp, false, nil
	}
	return fetchBrowser(ctx, rawURL, "fetch: escalating to browser renderer")
}

func markdownDoc(c clean.CleanedPage, page *fetch.FetchResponse) (string, error) {
	return marshalOut(markdownOut{
		URL: page.URL, FinalURL: c.FinalURL, Title: c.Title,
		Markdown: c.Markdown, StructuredData: orEmpty(c.StructuredData),
	}, "scrape")
}

func extractedDoc(c clean.CleanedPage, page *fetch.FetchResponse, res extract.ExtractResult) (string, error) {
	return marshalOut(extractedOut{
		URL: page.URL, FinalURL: c.FinalURL, Title: c.Title,
		Extracted: res.Record,
		Usage: usageOut{
			Provider: res.Provider, Model: res.Model,
			PromptTokens: res.Usage.PromptTokens, CompletionTokens: res.Usage.CompletionTokens,
			USDEstimate: res.Usage.USDEstimate, Attempts: res.Attempts,
		},
	}, "scrape")
}

func orEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}
