// Package scrape is the shared single-URL flow: fetch → clean →
// extract + selector-cache apply. Both the CLI `scrape` command and the
// MCP `scrape_url` tool call Run; output formatting stays in the CLI.
package scrape

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gomagpie/clean"
	"gomagpie/crawl"
	"gomagpie/extract"
	"gomagpie/fetch"
	"gomagpie/selector"
	"gomagpie/store"
)

// ErrMissingKey marks a schema extraction without credentials (CLI maps to exit 7).
var ErrMissingKey = errors.New("missing API key")

// Deps injects CLI-owned constructors so scrape never imports the CLI
// package (which would cycle once serve lives there).
type Deps struct {
	DB           *store.DB
	ExtractorFor func(provider, key, model string, sch *extract.Schema, runID string) (extract.Extractor, error)
	APIKeyFor    func(provider string) string
}

// Options configures one scrape. A nil Schema means markdown-only (no LLM).
type Options struct {
	Schema     *extract.Schema
	Render     string // auto|static|browser ("" = auto)
	Provider   string
	Model      string
	MaxCost    float64
	UseCache   bool
	PageFormat string // markdown|llm|text|json ("" = markdown)
	Scope      clean.Scope
	Profile    string
	Cookies    string
}

// Result is one scraped page.
type Result struct {
	RunID          string
	URL            string
	FinalURL       string
	Title          string
	Markdown       string          `json:",omitempty"`
	StructuredData json.RawMessage `json:",omitempty"`
	Record         map[string]any  `json:",omitempty"`
	FromCache      bool
	Usage          extract.TokenUsage
	Provider       string
	Model          string
	Rendered       string
}

// Run fetches, cleans, and optionally extracts one URL.
func Run(ctx context.Context, d Deps, rawURL string, o Options) (Result, error) {
	if d.DB == nil {
		return Result{}, fmt.Errorf("scrape: nil DB")
	}
	render := o.Render
	if render == "" {
		render = "auto"
	}
	switch render {
	case "auto", "static", "browser":
	default:
		return Result{}, fmt.Errorf("scrape: render %q must be auto|static|browser", render)
	}

	runID := uuidNew()
	if err := d.DB.BeginRun(runID, "scrape"); err != nil {
		return Result{}, err
	}
	finish := func(ok, er int, status string) {
		if err := d.DB.FinishRun(runID, ok, er, status); err != nil {
			fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", err)
		}
	}

	static, err := fetch.NewStaticFetcher()
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}
	page, err := fetchURL(ctx, static, rawURL, render, o.Profile, o.Cookies)
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}

	cleaned, err := clean.Clean(ctx, clean.RawPage{
		HTML: page.HTML, URL: page.URL, FinalURL: page.FinalURL,
		Scope: o.Scope, StatusCode: page.StatusCode,
	})
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}
	if cleaned.Quality != clean.IssueNone {
		finish(0, 1, "error")
		return Result{}, fmt.Errorf("scrape: quality blocked (%s) for %s: %w", cleaned.Quality, rawURL, clean.ErrQuality)
	}
	base := Result{RunID: runID, URL: page.URL, FinalURL: cleaned.FinalURL, Title: cleaned.Title,
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData}
	rendered, rerr := clean.Render(cleaned, o.PageFormat)
	if rerr != nil {
		finish(0, 1, "error")
		return Result{}, rerr
	}
	base.Rendered = rendered

	// No schema → markdown only, no LLM.
	if o.Schema == nil {
		finish(1, 0, "finished")
		return base, nil
	}

	provider := o.Provider
	model := o.Model
	key := ""
	if d.APIKeyFor != nil {
		key = d.APIKeyFor(provider)
	}
	if key == "" && needsAPIKey(provider) {
		finish(0, 0, "error")
		return Result{}, fmt.Errorf("scrape: provider %s: %w", provider, ErrMissingKey)
	}
	ex, err := d.ExtractorFor(provider, key, model, o.Schema, runID)
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}

	// Selector cache: hit + all required fields non-null → 0 LLM calls.
	if o.UseCache {
		if doc, ok, gerr := d.DB.GetSelectors(domainOfURL(cleaned.FinalURL), selector.SchemaHash(o.Schema)); gerr == nil && ok {
			if rec, nulls := selectorApply(doc, o.Schema, page.HTML, cleaned.StructuredData); len(nulls) == 0 {
				finish(1, 0, "finished")
				base.Record = rec
				base.FromCache = true
				return base, nil
			}
		}
	}

	promptText := "Extract structured data.\n" + string(cleaned.StructuredData) + "\n" + cleaned.Markdown
	if err := checkCostCeiling(d.DB, runID, provider, model, promptText, o.MaxCost); err != nil {
		finish(0, 0, "error")
		return Result{}, err
	}

	res, err := ex.Extract(ctx, extract.ExtractInput{
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData, Schema: o.Schema,
	})
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}
	finish(1, 0, "finished")
	base.Record = res.Record
	base.Usage = res.Usage
	base.Provider = res.Provider
	base.Model = res.Model
	return base, nil
}

func fetchURL(ctx context.Context, static *fetch.StaticFetcher, rawURL, render, profile, cookies string) (*fetch.FetchResponse, error) {
	if render == "browser" {
		return fetchBrowser(ctx, rawURL)
	}
	// A4 pass-through: every status reaches Clean+Classify so blocked pages
	// get typed quality errors instead of "fetch: HTTP %d".
	resp, err := static.Fetch(ctx, fetch.FetchRequest{URL: rawURL, Profile: profile, Cookies: cookies})
	if err != nil {
		return nil, err
	}
	if render == "static" {
		return resp, nil
	}
	score, embedded := fetch.ScoreJSRequired(resp.HTML, resp.Headers)
	if embedded || !fetch.NeedsBrowser(score) {
		return resp, nil
	}
	return fetchBrowser(ctx, rawURL)
}

func fetchBrowser(ctx context.Context, rawURL string) (*fetch.FetchResponse, error) {
	rod := fetch.NewRodFetcher()
	defer func() { _ = rod.Close() }() //nolint:errcheck // browser teardown; failure unactionable
	resp, err := rod.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func domainOfURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "file"
	}
	return strings.ToLower(u.Host)
}

func selectorApply(docJSON string, sch *extract.Schema, html []byte, sidecar json.RawMessage) (map[string]any, []string) {
	var doc selector.SelectorDoc
	if err := json.Unmarshal([]byte(docJSON), &doc); err != nil {
		return nil, []string{"*"}
	}
	return selector.NewApplier(doc, sch).Apply(string(html), sidecar)
}

func needsAPIKey(p string) bool {
	switch strings.ToLower(p) {
	case "ollama", "codex", "":
		return false
	}
	return true
}

// checkCostCeiling reuses crawl.ErrCostCeiling — no second sentinel.
func checkCostCeiling(db *store.DB, runID, provider, model, promptText string, maxCost float64) error {
	if maxCost <= 0 {
		return nil
	}
	if extract.IsFlatRateProvider(provider) {
		return nil
	}
	running, err := db.RunCost(runID)
	if err != nil {
		return err
	}
	proj := extract.ProjectedCost(model, promptText)
	if proj == 0 {
		if toks := extract.EstimatePromptTokens(promptText); toks > 0 {
			proj = float64(toks) / 1e6 * 2.00
		}
	}
	if running+proj > maxCost {
		return fmt.Errorf("scrape: running %.6f + projected %.6f > max %.6f: %w", running, proj, maxCost, crawl.ErrCostCeiling)
	}
	return nil
}

func uuidNew() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("%x-%d", b, os.Getpid())
	}
	// ponytail: timestamp+pid fallback on RNG failure; collision needs same-ns fork + broken RNG.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}
