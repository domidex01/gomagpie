package cli

import (
	"context"
	"encoding/json"
	"errors"

	"gomagpie/clean"
	"gomagpie/config"
	"gomagpie/crawl"
	"gomagpie/extract"
	"gomagpie/scrape"
	"gomagpie/store"
	"gomagpie/vertical"

	"github.com/spf13/cobra"
)

func newScrapeCmd() *cobra.Command {
	var schema, render, provider, model, out, format string
	var noCache bool
	var pageFormat, headerProfile, cookies string
	var include, exclude []string
	var onlyMainContent bool
	var verticalName string
	cmd := &cobra.Command{
		Use:   "scrape <url>",
		Short: "Fetch → clean → extract a single URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScrape(cmd.Context(), args[0], scrapeOptions{
				Schema: schema, Render: render, Provider: provider, Model: model,
				Out: out, Format: format, NoCache: noCache,
				PageFormat: pageFormat, Include: include, Exclude: exclude,
				OnlyMainContent: onlyMainContent, HeaderProfile: headerProfile, Cookies: cookies,
				Vertical: verticalName,
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
	cmd.Flags().StringVar(&pageFormat, "page-format", "", "page output format: markdown|llm|text|json")
	cmd.Flags().StringSliceVar(&include, "include", nil, "comma-separated CSS selectors: scrape only matching subtrees")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "comma-separated CSS selectors: drop matching nodes")
	cmd.Flags().BoolVar(&onlyMainContent, "only-main-content", false, "main-content only (trafilatura already does this)")
	cmd.Flags().StringVar(&headerProfile, "header-profile", "", "request header bundle: default|chrome|firefox")
	cmd.Flags().StringVar(&cookies, "cookies", "", "raw Cookie header value, e.g. \"a=b; c=d\"")
	cmd.Flags().StringVar(&verticalName, "vertical", "", "zero-LLM typed extractor: auto or a name (default off; `magpie vertical --list` in Phase C)")
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
	// PageFormat is flag-only: it must never enter config.Config.Format,
	// which crawl validates as jsonl|json|csv|sqlite.
	PageFormat      string
	Include         []string
	Exclude         []string
	OnlyMainContent bool
	HeaderProfile   string
	Cookies         string
	// Vertical is flag-only (auto|name, default off): validated pre-I/O.
	Vertical string
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
	if o.PageFormat != "" {
		switch o.PageFormat {
		case "markdown", "llm", "text", "json":
		default:
			return fail(2, "page-format %q must be markdown|llm|text|json", o.PageFormat)
		}
	}
	// Unknown vertical names fail pre-I/O: no fetch, no DB touched beyond open.
	if o.Vertical != "" && o.Vertical != "auto" {
		if _, ok := vertical.Lookup(o.Vertical); !ok {
			return fail(2, "vertical %q unknown (see `magpie vertical --list`)", o.Vertical)
		}
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
		PageFormat: o.PageFormat,
		Scope:      clean.Scope{Include: o.Include, Exclude: o.Exclude, OnlyMainContent: o.OnlyMainContent},
		Profile:    o.HeaderProfile, Cookies: o.Cookies,
		Vertical: o.Vertical,
	})
	if err != nil {
		switch {
		case errors.Is(err, scrape.ErrMissingKey):
			return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
		case errors.Is(err, crawl.ErrCostCeiling):
			return fail(6, "cost ceiling exceeded: %v", err)
		case errors.Is(err, clean.ErrQuality):
			return fail(8, "%s", qualityMessage(err, rawURL))
		case errors.Is(err, vertical.ErrURLMismatch):
			return fail(2, "%s", err.Error())
		}
		return err
	}
	if res.Vertical != "" {
		vdoc, merr := verticalDoc(res)
		if merr != nil {
			return merr
		}
		return writeOut(cfg.Out, vdoc)
	}
	if sch == nil {
		if o.PageFormat == "json" {
			return writeOut(cfg.Out, res.Rendered)
		}
		if o.PageFormat == "llm" || o.PageFormat == "text" {
			return writeOut(cfg.Out, res.Rendered)
		}
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

// qualityMessage renders the typed issue when available, falling back to
// the raw URL when the wrap chain carries no QualityError.
func qualityMessage(err error, rawURL string) string {
	if issue := clean.QualityIssue(err); issue != clean.IssueNone {
		return "quality blocked (" + string(issue) + ") for " + rawURL
	}
	return "quality blocked for " + rawURL
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

// verticalDoc renders a zero-LLM vertical hit: the typed record plus the
// extractor name, no LLM usage block (nothing was billed).
func verticalDoc(r scrape.Result) (string, error) {
	return marshalOut(struct {
		URL      string         `json:"url"`
		FinalURL string         `json:"final_url"`
		Title    string         `json:"title"`
		Vertical string         `json:"vertical"`
		Record   map[string]any `json:"record"`
	}{r.URL, r.FinalURL, r.Title, r.Vertical, r.Record}, "scrape")
}

func orEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}
