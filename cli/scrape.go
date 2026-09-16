package cli

import (
	"context"
	"encoding/json"
	"errors"

	"gomagpie/config"
	"gomagpie/crawl"
	"gomagpie/extract"
	"gomagpie/scrape"
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
	cmd.Flags().StringVar(&provider, "provider", "", ProviderHelp)
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

	provider := cfg.ExtractProvider
	if o.Provider != "" {
		provider = o.Provider
	}
	model := cfg.Model
	if o.Model != "" {
		model = o.Model
	}

	var sch *extract.Schema
	if cfg.Schema != "" {
		sch, err = extract.LoadSchema(cfg.Schema)
		if err != nil {
			return err
		}
	}

	res, err := scrape.Run(ctx, scrape.Deps{
		DB: db,
		ExtractorFor: func(p, key, m string, s *extract.Schema, runID string) (extract.Extractor, error) {
			return newExtractor(p, key, m, s, db, runID)
		},
		APIKeyFor: cfg.APIKey,
	}, rawURL, scrape.Options{
		Schema: sch, Render: cfg.Render, Provider: provider, Model: model,
		MaxCost: cfg.MaxCost, UseCache: !cfg.NoCache,
	})
	if err != nil {
		switch {
		case errors.Is(err, scrape.ErrMissingKey):
			return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
		case errors.Is(err, crawl.ErrCostCeiling):
			return fail(6, "cost ceiling exceeded: %v", err)
		}
		return err
	}
	if sch == nil {
		mdoc, merr := markdownDoc(res)
		if merr != nil {
			return merr
		}
		return writeOut(cfg.Out, mdoc)
	}
	edoc, merr := extractedDoc(res)
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

func markdownDoc(r scrape.Result) (string, error) {
	return marshalOut(markdownOut{
		URL: r.URL, FinalURL: r.FinalURL, Title: r.Title,
		Markdown: r.Markdown, StructuredData: orEmpty(r.StructuredData),
	}, "scrape")
}

func extractedDoc(r scrape.Result) (string, error) {
	out := extractedOut{
		URL: r.URL, FinalURL: r.FinalURL, Title: r.Title,
		Extracted: r.Record, FromCache: r.FromCache,
	}
	if !r.FromCache {
		out.Usage = usageOut{
			Provider: r.Provider, Model: r.Model,
			PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens,
			USDEstimate: r.Usage.USDEstimate,
		}
	}
	return marshalOut(out, "scrape")
}

func orEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}
