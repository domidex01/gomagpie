package scrape_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gomagpie/crawl"
	"gomagpie/extract"
	"gomagpie/scrape"
	"gomagpie/selector"
	"gomagpie/store"
)

type fakeExtractor struct {
	mu     sync.Mutex
	calls  int
	script map[string]any
	err    error
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return extract.ExtractResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return extract.ExtractResult{}, f.err
	}
	raw, merr := json.Marshal(f.script)
	if merr != nil {
		return extract.ExtractResult{}, merr
	}
	return extract.ExtractResult{Record: f.script, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

const scrapeHTML = `<html><head><title>Widget</title></head><body><h1>Widget</h1>` +
	`<p>Plenty of honest descriptive prose keeps the static renderer in charge without any browser escalation at all.</p>` +
	`<p>A second paragraph of harmless filler text pushes visible length safely past the two-hundred character threshold.</p>` +
	`</body></html>`

func scrapeOrigin(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body)) //nolint:errcheck // httptest local; short write unactionable
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func openScrapeDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return db
}

func testSchema(t *testing.T) *extract.Schema {
	t.Helper()
	sch, err := extract.ParseSchema([]byte(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	return sch
}

func fakeDeps(db *store.DB, fx *fakeExtractor, key string) scrape.Deps {
	return scrape.Deps{
		DB: db,
		ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
			return fx, nil
		},
		APIKeyFor: func(string) string { return key },
	}
}

func TestRun_MarkdownOnly(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openScrapeDB(t)
	res, err := scrape.Run(context.Background(), fakeDeps(db, fx, ""), scrapeOrigin(t, scrapeHTML), scrape.Options{Render: "static"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Markdown == "" {
		t.Error("markdown-only Run returned empty markdown")
	}
	if res.Record != nil {
		t.Errorf("markdown-only Run record = %v, want nil", res.Record)
	}
	if got := fx.total(); got != 0 {
		t.Errorf("markdown-only Run made %d extractor calls, want 0", got)
	}
}

func TestRun_Schema(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openScrapeDB(t)
	res, err := scrape.Run(context.Background(), fakeDeps(db, fx, "test-key"), scrapeOrigin(t, scrapeHTML), scrape.Options{
		Schema: testSchema(t), Render: "static", Provider: "fake", UseCache: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Record["title"] != "Widget" {
		t.Errorf("record = %v, want title Widget", res.Record)
	}
	if got := fx.total(); got != 1 {
		t.Errorf("schema Run made %d extractor calls, want 1", got)
	}
}

func TestRun_MissingKey(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openScrapeDB(t)
	_, err := scrape.Run(context.Background(), fakeDeps(db, fx, ""), scrapeOrigin(t, scrapeHTML), scrape.Options{
		Schema: testSchema(t), Render: "static", Provider: "openai",
	})
	if !errors.Is(err, scrape.ErrMissingKey) {
		t.Errorf("err = %v, want ErrMissingKey", err)
	}
}

func TestRun_CostCeiling(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openScrapeDB(t)
	_, err := scrape.Run(context.Background(), fakeDeps(db, fx, "test-key"), scrapeOrigin(t, scrapeHTML), scrape.Options{
		Schema: testSchema(t), Render: "static", Provider: "openai", MaxCost: 1e-9,
	})
	if !errors.Is(err, crawl.ErrCostCeiling) {
		t.Errorf("err = %v, want ErrCostCeiling", err)
	}
}

func TestRun_CacheHitZeroCalls(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openScrapeDB(t)
	sch := testSchema(t)
	origin := scrapeOrigin(t, scrapeHTML)
	u, uerr := url.Parse(origin)
	if uerr != nil {
		t.Fatalf("parse origin: %v", uerr)
	}
	domain := strings.ToLower(u.Host)
	doc, merr := json.Marshal(selector.SelectorDoc{
		SchemaHash: selector.SchemaHash(sch), Domain: domain,
		Fields: map[string]selector.FieldSelector{"title": {Type: "css", Expr: "title"}},
	})
	if merr != nil {
		t.Fatalf("marshal doc: %v", merr)
	}
	if err := db.PutSelectors(domain, selector.SchemaHash(sch), string(doc), 3); err != nil {
		t.Fatalf("PutSelectors: %v", err)
	}
	res, err := scrape.Run(context.Background(), fakeDeps(db, fx, "test-key"), origin, scrape.Options{
		Schema: sch, Render: "static", Provider: "fake", UseCache: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FromCache {
		t.Error("seeded cache Run FromCache = false, want true")
	}
	if got := fx.total(); got != 0 {
		t.Errorf("cache-hit Run made %d extractor calls, want 0", got)
	}
	// UseCache: false bypasses the seeded doc.
	fx2 := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	if _, err := scrape.Run(context.Background(), fakeDeps(db, fx2, "test-key"), origin, scrape.Options{
		Schema: sch, Render: "static", Provider: "fake", UseCache: false,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fx2.total(); got != 1 {
		t.Errorf("no-cache Run made %d extractor calls, want 1", got)
	}
}
