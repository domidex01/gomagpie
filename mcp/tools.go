package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gomagpie/clean"
	"gomagpie/crawl"
	"gomagpie/extract"
	"gomagpie/scrape"
	"gomagpie/selector"
	"gomagpie/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultCrawlSchema is used when crawl_site gets no schema: crawl.Run
// needs a non-nil *extract.Schema, and records flow to the writer
// unvalidated, so a permissive object schema is the honest default.
var defaultCrawlSchema = []byte(`{"type":"object","properties":{"title":{"type":"string"}}}`)

// --- scrape_url ---

// ScrapeIn is the scrape_url input.
type ScrapeIn struct {
	URL      string         `json:"url" jsonschema:"absolute http(s) or file URL to scrape"`
	Schema   map[string]any `json:"schema,omitempty" jsonschema:"JSON Schema object; omit for cleaned markdown only"`
	Render   string         `json:"render,omitempty" jsonschema:"auto, static, or browser"`
	UseCache *bool          `json:"use_cache,omitempty" jsonschema:"apply cached selectors when available"`
}

// ScrapeOut is the scrape_url output.
type ScrapeOut struct {
	URL       string         `json:"url" jsonschema:"requested URL"`
	FinalURL  string         `json:"final_url" jsonschema:"final URL after redirects"`
	Title     string         `json:"title" jsonschema:"page title"`
	Markdown  string         `json:"markdown,omitempty" jsonschema:"cleaned markdown (no schema)"`
	Extracted map[string]any `json:"extracted,omitempty" jsonschema:"extracted record (with schema)"`
	FromCache bool           `json:"from_cache" jsonschema:"served from selector cache with zero LLM calls"`
	Usage     map[string]any `json:"usage,omitempty" jsonschema:"LLM usage when the extractor ran"`
}

func handleScrape(d Deps) func(context.Context, *sdk.CallToolRequest, ScrapeIn) (*sdk.CallToolResult, ScrapeOut, error) {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in ScrapeIn) (*sdk.CallToolResult, ScrapeOut, error) {
		var sch *extract.Schema
		if len(in.Schema) > 0 {
			raw, err := json.Marshal(in.Schema)
			if err != nil {
				return nil, ScrapeOut{}, fmt.Errorf("mcp: scrape_url schema: %w", err)
			}
			sch, err = extract.ParseSchema(raw)
			if err != nil {
				return nil, ScrapeOut{}, fmt.Errorf("mcp: scrape_url schema: %w", err)
			}
		}
		useCache := true
		if in.UseCache != nil {
			useCache = *in.UseCache
		}
		res, err := scrape.Run(ctx, d.ScrapeDeps, in.URL, scrape.Options{
			Schema: sch, Render: in.Render, Provider: d.DefaultProvider,
			Model: d.DefaultModel, MaxCost: d.MaxCost, UseCache: useCache,
		})
		if err != nil {
			return nil, ScrapeOut{}, fmt.Errorf("mcp: scrape_url: %w", err)
		}
		out := ScrapeOut{URL: res.URL, FinalURL: res.FinalURL, Title: res.Title, FromCache: res.FromCache}
		if sch == nil {
			out.Markdown = res.Markdown
		} else {
			out.Extracted = res.Record
			if !res.FromCache {
				out.Usage = map[string]any{
					"provider": res.Provider, "model": res.Model,
					"prompt_tokens":     res.Usage.PromptTokens,
					"completion_tokens": res.Usage.CompletionTokens,
					"usd_estimate":      res.Usage.USDEstimate,
				}
			}
		}
		return nil, out, nil
	}
}

// --- crawl_site ---

// CrawlIn is the crawl_site input. Set only RunID to poll a previous run
// (status path: zero extractor calls).
type CrawlIn struct {
	URL      string         `json:"url,omitempty" jsonschema:"seed URL for a fresh crawl"`
	MaxPages int            `json:"max_pages,omitempty" jsonschema:"max pages to claim and fetch"`
	MaxDepth int            `json:"max_depth,omitempty" jsonschema:"max link depth from seed"`
	SameHost *bool          `json:"same_host,omitempty" jsonschema:"follow only same-host links (default true)"`
	Schema   map[string]any `json:"schema,omitempty" jsonschema:"JSON Schema object for extraction"`
	RunID    string         `json:"run_id,omitempty" jsonschema:"poll a previous run instead of crawling"`
}

// CrawlOut is the crawl_site output (fresh run and status poll share it).
type CrawlOut struct {
	RunID        string         `json:"run_id" jsonschema:"run id (pass back as run_id to poll)"`
	PagesCrawled int            `json:"pages_crawled" jsonschema:"pages done plus errored"`
	Records      int            `json:"records" jsonschema:"records extracted (pages_ok)"`
	Errors       int            `json:"errors,omitempty" jsonschema:"pages errored"`
	Status       string         `json:"status" jsonschema:"run status"`
	Usage        map[string]any `json:"usage,omitempty" jsonschema:"accumulated LLM usage"`
}

func handleCrawl(d Deps) func(context.Context, *sdk.CallToolRequest, CrawlIn) (*sdk.CallToolResult, CrawlOut, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, in CrawlIn) (*sdk.CallToolResult, CrawlOut, error) {
		// Status-only path: stored status, zero extractor involvement.
		if in.RunID != "" {
			info, err := d.DB.GetRun(in.RunID)
			if err != nil {
				return nil, CrawlOut{}, fmt.Errorf("mcp: crawl_site: %w", err)
			}
			_, _, done, errs, serr := d.DB.CrawlStats(in.RunID)
			if serr != nil {
				return nil, CrawlOut{}, fmt.Errorf("mcp: crawl_site: %w", serr)
			}
			return nil, CrawlOut{
				RunID: in.RunID, PagesCrawled: done + errs, Records: done,
				Errors: errs, Status: info.Status, Usage: runUsage(info),
			}, nil
		}

		sch, err := crawlSchema(in.Schema)
		if err != nil {
			return nil, CrawlOut{}, err
		}
		sameHost := true
		if in.SameHost != nil {
			sameHost = *in.SameHost
		}
		key := ""
		if d.ScrapeDeps.APIKeyFor != nil {
			key = d.ScrapeDeps.APIKeyFor(d.DefaultProvider)
		}
		runID := newRunID()
		ex, err := d.ScrapeDeps.ExtractorFor(d.DefaultProvider, key, d.DefaultModel, sch, runID)
		if err != nil {
			return nil, CrawlOut{}, fmt.Errorf("mcp: crawl_site: %w", err)
		}

		// Progress: one NotifyProgress per finished page when the client
		// sent a progress token. Total is omitted — the frontier total is
		// unknowable upfront.
		var progress func(done int)
		if token := req.Params.GetProgressToken(); token != nil {
			progress = func(done int) {
				_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ //nolint:errcheck // progress is best-effort; a failed notify must not fail the crawl
					ProgressToken: token, Progress: float64(done),
					Message: fmt.Sprintf("%d pages", done),
				})
			}
		}
		res, err := crawl.Run(ctx, crawl.Options{
			SeedURL: in.URL, Schema: sch, MaxPages: in.MaxPages, MaxDepth: in.MaxDepth,
			SameHost: sameHost, Format: "jsonl", Out: os.DevNull, RunID: runID,
			Provider: d.DefaultProvider, Model: d.DefaultModel, MaxCost: d.MaxCost,
			DB: d.DB, Extractor: ex, Progress: progress,
		})
		if err != nil {
			return nil, CrawlOut{}, fmt.Errorf("mcp: crawl_site: %w", err)
		}
		info, err := d.DB.GetRun(res.RunID)
		if err != nil {
			return nil, CrawlOut{}, fmt.Errorf("mcp: crawl_site: %w", err)
		}
		return nil, CrawlOut{
			RunID: res.RunID, PagesCrawled: res.PagesOK + res.PagesErr,
			Records: res.Records, Errors: res.PagesErr,
			Status: info.Status, Usage: runUsage(info),
		}, nil
	}
}

func crawlSchema(m map[string]any) (*extract.Schema, error) {
	raw := defaultCrawlSchema
	if len(m) > 0 {
		var err error
		raw, err = json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("mcp: crawl_site schema: %w", err)
		}
	}
	sch, err := extract.ParseSchema(raw)
	if err != nil {
		return nil, fmt.Errorf("mcp: crawl_site schema: %w", err)
	}
	return sch, nil
}

func runUsage(info store.RunInfo) map[string]any {
	return map[string]any{
		"prompt_tokens":     info.PromptTokens,
		"completion_tokens": info.CompletionTokens,
		"usd_estimate":      info.USDEstimate,
	}
}

// --- extract_structured ---

// ExtractIn is the extract_structured input (no fetch).
type ExtractIn struct {
	Content     string         `json:"content" jsonschema:"HTML or markdown source to extract from"`
	ContentType string         `json:"content_type,omitempty" jsonschema:"html or markdown (default html)"`
	Schema      map[string]any `json:"schema" jsonschema:"JSON Schema object (required)"`
}

// ExtractOut is the extract_structured output.
type ExtractOut struct {
	Extracted map[string]any `json:"extracted" jsonschema:"extracted record"`
	Usage     map[string]any `json:"usage,omitempty" jsonschema:"LLM usage"`
}

func handleExtract(d Deps) func(context.Context, *sdk.CallToolRequest, ExtractIn) (*sdk.CallToolResult, ExtractOut, error) {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in ExtractIn) (*sdk.CallToolResult, ExtractOut, error) {
		if len(in.Schema) == 0 {
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured: schema is required")
		}
		raw, err := json.Marshal(in.Schema)
		if err != nil {
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured schema: %w", err)
		}
		sch, err := extract.ParseSchema(raw)
		if err != nil {
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured schema: %w", err)
		}
		ct := in.ContentType
		if ct == "" {
			ct = "html"
		}
		var markdown string
		var sidecar json.RawMessage
		switch ct {
		case "html":
			cleaned, err := clean.Clean(ctx, clean.RawPage{HTML: []byte(in.Content)})
			if err != nil {
				return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured: %w", err)
			}
			markdown, sidecar = cleaned.Markdown, cleaned.StructuredData
		case "markdown":
			markdown = in.Content
		default:
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured: content_type %q must be html|markdown", ct)
		}

		runID := newRunID()
		if err := d.DB.BeginRun(runID, "mcp-extract"); err != nil {
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured: %w", err)
		}
		finish := func(ok int, status string) {
			if err := d.DB.FinishRun(runID, ok, 0, status); err != nil {
				fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", err)
			}
		}
		key := ""
		if d.ScrapeDeps.APIKeyFor != nil {
			key = d.ScrapeDeps.APIKeyFor(d.DefaultProvider)
		}
		ex, err := d.ScrapeDeps.ExtractorFor(d.DefaultProvider, key, d.DefaultModel, sch, runID)
		if err != nil {
			finish(0, "error")
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured: %w", err)
		}
		res, err := ex.Extract(ctx, extract.ExtractInput{
			Markdown: markdown, StructuredData: sidecar, Schema: sch,
		})
		if err != nil {
			finish(0, "error")
			return nil, ExtractOut{}, fmt.Errorf("mcp: extract_structured: %w", err)
		}
		finish(1, "finished")
		return nil, ExtractOut{Extracted: res.Record, Usage: map[string]any{
			"provider": res.Provider, "model": res.Model,
			"prompt_tokens":     res.Usage.PromptTokens,
			"completion_tokens": res.Usage.CompletionTokens,
			"usd_estimate":      res.Usage.USDEstimate,
		}}, nil
	}
}

// --- get_cached_selectors ---

// SelectorsIn is the get_cached_selectors input.
type SelectorsIn struct {
	Domain     string `json:"domain" jsonschema:"domain to list cached selectors for"`
	SchemaHash string `json:"schema_hash,omitempty" jsonschema:"only this schema hash"`
}

// CachedSelector is one cached doc.
type CachedSelector struct {
	SchemaHash    string             `json:"schema_hash" jsonschema:"schema hash"`
	Fields        map[string]any     `json:"fields" jsonschema:"selector fields"`
	SynthesizedAt string             `json:"synthesized_at" jsonschema:"synthesis time"`
	NullRates     map[string]float64 `json:"null_rates,omitempty" jsonschema:"per-field null rates"`
}

// SelectorsOut is the get_cached_selectors output.
type SelectorsOut struct {
	Domain    string           `json:"domain" jsonschema:"queried domain"`
	Selectors []CachedSelector `json:"selectors" jsonschema:"cached selector docs"`
}

func handleSelectors(d Deps) func(context.Context, *sdk.CallToolRequest, SelectorsIn) (*sdk.CallToolResult, SelectorsOut, error) {
	return func(_ context.Context, _ *sdk.CallToolRequest, in SelectorsIn) (*sdk.CallToolResult, SelectorsOut, error) {
		entries, err := d.DB.ListSelectors(in.Domain)
		if err != nil {
			return nil, SelectorsOut{}, fmt.Errorf("mcp: get_cached_selectors: %w", err)
		}
		out := SelectorsOut{Domain: in.Domain}
		for _, e := range entries {
			if in.SchemaHash != "" && e.SchemaHash != in.SchemaHash {
				continue
			}
			var doc selector.SelectorDoc
			if err := json.Unmarshal([]byte(e.FieldsJSON), &doc); err != nil {
				continue
			}
			fields := map[string]any{}
			nulls := map[string]float64{}
			for name, sel := range doc.Fields {
				fields[name] = map[string]any{"type": sel.Type, "expr": sel.Expr}
				nulls[name] = sel.NullRate
			}
			out.Selectors = append(out.Selectors, CachedSelector{
				SchemaHash: e.SchemaHash, Fields: fields,
				SynthesizedAt: e.SynthesizedAt, NullRates: nulls,
			})
		}
		if out.Selectors == nil {
			out.Selectors = []CachedSelector{}
		}
		return nil, out, nil
	}
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("%x-%d", b, os.Getpid())
	}
	// ponytail: timestamp+pid fallback on RNG failure.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}
