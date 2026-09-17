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
	"gomagpie/vertical"
)

// ErrMissingKey marks a schema extraction without credentials (CLI maps to exit 7).
var ErrMissingKey = errors.New("missing API key")

// Deps injects CLI-owned constructors so scrape never imports the CLI
// package (which would cycle once serve lives there).
// Deps must be safe for concurrent use: Batch fans one Deps out over N
// goroutines sharing DB and the constructors. Nil-schema (markdown-only)
// runs never call ExtractorFor; *store.DB handles concurrent readers.
type Deps struct {
	DB           *store.DB
	ExtractorFor func(provider, key, model string, sch *extract.Schema, runID string) (extract.Extractor, error)
	APIKeyFor    func(provider string) string
	// Fetcher serves raw-URL fetches and vertical sub-fetches; nil =
	// NewStaticFetcher() (production default; tests inject a fake).
	Fetcher vertical.Fetcher
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
	Browser    string // TLS fingerprint: chrome|firefox|random ("" = stock)
	Cookies    string
	// Vertical selects a zero-LLM typed extractor: "" (default) = off,
	// "auto" = strict auto-dispatch, or an extractor name for explicit
	// selection. Explicit selection with a schema ignores the schema.
	Vertical string
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
	// Vertical names the extractor that produced Record ("" when unused).
	Vertical string `json:",omitempty"`
}

// OptionsError marks a pre-I/O options validation failure (CLI exit 2).
// Callers match it with errors.As; the message text is API surface
// (crosses the MCP boundary to agents) — change only with a declared
// message change.
type OptionsError struct{ msg string }

func (e *OptionsError) Error() string { return e.msg }

// ValidateOptions checks Render, PageFormat, Browser, and Vertical before
// any I/O. One switch site, shared by Run (which calls it first), the CLI
// commands, and batch — adding an option edits this function, not four
// validators. The trust boundary stays in this package: callers map the
// typed error (exit 2) but never re-implement the checks.
func ValidateOptions(o Options) error {
	render := o.Render
	if render == "" {
		render = "auto"
	}
	switch render {
	case "auto", "static", "browser":
	default:
		return &OptionsError{fmt.Sprintf("scrape: render %q must be auto|static|browser", render)}
	}
	switch o.PageFormat {
	case "", "markdown", "llm", "text", "json":
	default:
		return &OptionsError{fmt.Sprintf("scrape: page format %q must be markdown|llm|text|json", o.PageFormat)}
	}
	if !fetch.ValidBrowser(o.Browser) {
		return &OptionsError{fmt.Sprintf("scrape: browser %q must be chrome|firefox|random", o.Browser)}
	}
	if o.Vertical != "" && o.Vertical != "auto" {
		if _, ok := vertical.Lookup(o.Vertical); !ok {
			return &OptionsError{fmt.Sprintf("scrape: vertical %q unknown (see `magpie vertical --list`)", o.Vertical)}
		}
	}
	return nil
}

// Run fetches, cleans, and optionally extracts one URL.
func Run(ctx context.Context, d Deps, rawURL string, o Options) (Result, error) {
	if d.DB == nil {
		return Result{}, fmt.Errorf("scrape: nil DB")
	}
	if err := ValidateOptions(o); err != nil {
		return Result{}, err
	}
	// ValidateOptions guaranteed the name; resolve the extractor for dispatch.
	var explicit *vertical.Extractor
	if o.Vertical != "" && o.Vertical != "auto" {
		ex, _ := vertical.Lookup(o.Vertical)
		explicit = &ex
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

	var vf = d.Fetcher
	if vf == nil {
		static, serr := fetch.NewStaticFetcher()
		if serr != nil {
			finish(0, 1, "error")
			return Result{}, serr
		}
		vf = static
	}
	fetchStart := time.Now()
	render := o.Render
	if render == "" {
		render = "auto" // same default ValidateOptions validated
	}
	page, err := fetchURL(ctx, vf, rawURL, render, o.Profile, o.Cookies, o.Browser)
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}
	// Fetch telemetry rides the run row next to LLM usage; warn-only,
	// never fails the page.
	if lerr := d.DB.LogFetch(runID, int64(len(page.HTML)), time.Since(fetchStart).Milliseconds()); lerr != nil {
		fmt.Fprintf(os.Stderr, "warning: log fetch: %v\n", lerr)
	}

	cleaned, err := clean.Clean(ctx, clean.RawPage{
		HTML: page.HTML, URL: page.URL, FinalURL: page.FinalURL,
		Scope: o.Scope, StatusCode: page.StatusCode,
		ContentType: page.Headers.Get("Content-Type"),
	})
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}
	if cleaned.Quality != clean.IssueNone {
		finish(0, 1, "error")
		return Result{}, &clean.QualityError{Issue: cleaned.Quality, URL: rawURL}
	}
	base := Result{RunID: runID, URL: page.URL, FinalURL: cleaned.FinalURL, Title: cleaned.Title,
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData}
	rendered, rerr := clean.Render(cleaned, o.PageFormat)
	if rerr != nil {
		finish(0, 1, "error")
		return Result{}, rerr
	}
	base.Rendered = rendered

	if explicit != nil {
		// ^ --list ships in Phase C; the message names it anyway so the string never changes.
		if u, err := url.Parse(rawURL); err != nil || !explicit.Match(u) {
			finish(0, 1, "error")
			return Result{}, fmt.Errorf("scrape: vertical %q: %w for %s", o.Vertical, vertical.ErrURLMismatch, rawURL)
		}
		return runVertical(ctx, vf, rawURL, *explicit, base, finish)
	}
	if o.Vertical == "auto" {
		if ex, ok := vertical.MatchURL(rawURL); ok {
			return runVertical(ctx, vf, rawURL, ex, base, finish)
		}
	}

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

// runVertical runs one zero-LLM extractor: default headers only (profiles
// exist for challenge-prone HTML pages, not registry APIs), no selector
// cache interaction (vertical output isn't selector-derived; caching it
// would poison schema-keyed lookups), no LLM. Extractor errors are hard
// errors — never a silent LLM fallback.
func runVertical(ctx context.Context, vf vertical.Fetcher, rawURL string, ex vertical.Extractor, base Result, finish func(ok, er int, status string)) (Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		finish(0, 1, "error")
		return Result{}, fmt.Errorf("scrape: vertical %s: %w", ex.Info.Name, err)
	}
	m, err := ex.Extract(ctx, vf, u)
	if err != nil {
		finish(0, 1, "error")
		return Result{}, err
	}
	finish(1, 0, "finished")
	base.Record = m
	base.Vertical = ex.Info.Name
	return base, nil
}

func fetchURL(ctx context.Context, vf vertical.Fetcher, rawURL, render, profile, cookies, browser string) (*fetch.FetchResponse, error) {
	if render == "browser" {
		return fetchBrowser(ctx, rawURL)
	}
	// A4 pass-through: every status reaches Clean+Classify so blocked pages
	// get typed quality errors instead of "fetch: HTTP %d".
	resp, err := vf.Fetch(ctx, fetch.FetchRequest{URL: rawURL, Profile: profile, Cookies: cookies, Browser: browser})
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
