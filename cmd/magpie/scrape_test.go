package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gomagpie/store"
)

type fakeProvider struct {
	t      *testing.T
	mu     sync.Mutex
	script []string
	calls  int
	bodies []string
}

func newFakeProvider(t *testing.T, script ...string) (*httptest.Server, *fakeProvider) {
	t.Helper()
	fp := &fakeProvider{t: t, script: script}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fp.mu.Lock()
		defer fp.mu.Unlock()
		fp.calls++
		fp.bodies = append(fp.bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		idx := min(fp.calls-1, len(fp.script)-1)
		_, _ = io.WriteString(w, fp.script[idx])
	}))
	t.Cleanup(srv.Close)
	return srv, fp
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func openAIEnvelope(raw string) string {
	b, _ := json.Marshal(raw)
	return `{"choices":[{"message":{"content":` + string(b) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`
}

func testEnv(t *testing.T, dbName string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("APPDATA", "")
	db := filepath.Join(dir, dbName)
	t.Setenv("GOMAGPIE_CACHE_DB", db)
	return db
}

func codeOf(err error) int {
	if err == nil {
		return 0
	}
	if ce, ok := err.(*cmdError); ok {
		return ce.code
	}
	return 1
}

func TestScrapeNoSchema(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	abs, _ := filepath.Abs("../../testdata/clean/article.html")
	out := filepath.Join(t.TempDir(), "out.json")
	err := runScrape(t.Context(), "file://"+abs, scrapeOptions{Format: "json", Out: out, Render: "static"})
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	raw, _ := os.ReadFile(out)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if _, ok := doc["markdown"]; !ok {
		t.Error("missing markdown field")
	}
	db, _ := store.Open(dbPath)
	defer db.Close()
	n, _ := db.LLMCallCount("")
	if n != 0 {
		t.Errorf("llm_calls = %d, want 0", n)
	}
}

func TestScrapeEndToEndFileURL(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	srv, fp := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "test-key")
	abs, _ := filepath.Abs("../../testdata/clean/article.html")
	schema, _ := filepath.Abs("../../testdata/extract/price.yaml")
	out := filepath.Join(t.TempDir(), "out.json")
	err := runScrape(t.Context(), "file://"+abs, scrapeOptions{
		Schema: schema, Format: "json", Out: out, Render: "static",
		Provider: "openai", Model: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if fp.callCount() < 1 {
		t.Fatalf("provider calls = 0, want ≥1")
	}
	raw, _ := os.ReadFile(out)
	var doc struct {
		Extracted map[string]any `json:"extracted"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	schRaw, _ := os.ReadFile(schema)
	_ = schRaw
	if doc.Extracted["price"] != 12.99 {
		t.Errorf("price = %v, want 12.99", doc.Extracted["price"])
	}
	db, _ := store.Open(dbPath)
	defer db.Close()
	n, _ := db.LLMCallCount("")
	if n < 1 {
		t.Errorf("llm_calls = %d, want ≥1", n)
	}
}

func TestScrapeRenderStaticSPAShell(t *testing.T) {
	testEnv(t, "cache.db")
	abs, _ := filepath.Abs("../../testdata/clean/spa-shell.html")
	out := filepath.Join(t.TempDir(), "out.json")
	// Static render must never touch the browser (no Chrome here).
	if err := runScrape(t.Context(), "file://"+abs, scrapeOptions{Format: "json", Out: out, Render: "static"}); err != nil {
		t.Fatalf("scrape spa-shell static: %v", err)
	}
}

func TestScrapeBadFormat(t *testing.T) {
	testEnv(t, "cache.db")
	abs, _ := filepath.Abs("../../testdata/clean/article.html")
	err := runScrape(t.Context(), "file://"+abs, scrapeOptions{Format: "csv", Render: "static"})
	if codeOf(err) == 0 {
		t.Fatal("expected non-zero exit for csv")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %v, want 'not supported'", err)
	}
}

func TestMaxCostAbortsBeforeCall(t *testing.T) {
	testEnv(t, "cache.db")
	srv, fp := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "test-key")
	t.Setenv("GOMAGPIE_MAX_COST", "0.000001")
	abs, _ := filepath.Abs("../../testdata/clean/article.html")
	schema, _ := filepath.Abs("../../testdata/extract/price.yaml")
	err := runScrape(t.Context(), "file://"+abs, scrapeOptions{
		Schema: schema, Render: "static", Provider: "openai", Model: "gpt-4o-mini",
	})
	if codeOf(err) != 6 {
		t.Fatalf("exit = %d, want 6 (err=%v)", codeOf(err), err)
	}
	if fp.callCount() != 0 {
		t.Errorf("provider calls = %d, want 0", fp.callCount())
	}
}

func TestMissingKeyExit7(t *testing.T) {
	testEnv(t, "cache.db")
	os.Unsetenv("GOMAGPIE_OPENAI_API_KEY")
	os.Unsetenv("GOMAGPIE_ANTHROPIC_API_KEY")
	os.Unsetenv("GOMAGPIE_API_KEY")
	abs, _ := filepath.Abs("../../testdata/clean/article.html")
	schema, _ := filepath.Abs("../../testdata/extract/price.yaml")
	err := runScrape(t.Context(), "file://"+abs, scrapeOptions{
		Schema: schema, Render: "static", Provider: "openai", Model: "gpt-4o-mini",
	})
	if codeOf(err) != 7 {
		t.Fatalf("exit = %d, want 7 (err=%v)", codeOf(err), err)
	}
	if !strings.Contains(err.Error(), "magpie config set-key") {
		t.Errorf("error = %v, want set-key hint", err)
	}
}

func TestExtractCmdStdinHTML(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	srv, _ := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "test-key")
	html, _ := os.ReadFile("../../testdata/clean/article.html")
	// Simulate stdin via temp file (no fetch involved).
	in := filepath.Join(t.TempDir(), "in.html")
	os.WriteFile(in, html, 0o644)
	schema, _ := filepath.Abs("../../testdata/extract/price.yaml")
	out := filepath.Join(t.TempDir(), "out.json")
	err := runExtract(t.Context(), extractOptions{
		Schema: schema, ContentType: "html", Provider: "openai",
		Model: "gpt-4o-mini", Out: out, File: in,
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	raw, _ := os.ReadFile(out)
	var doc struct {
		Extracted map[string]any `json:"extracted"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if doc.Extracted["price"] != 12.99 {
		t.Errorf("price = %v", doc.Extracted["price"])
	}
	db, _ := store.Open(dbPath)
	defer db.Close()
	n, _ := db.LLMCallCount("")
	if n < 1 {
		t.Errorf("llm_calls = %d, want ≥1", n)
	}
}
