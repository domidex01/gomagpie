package cli

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
	// headers parallels bodies: one cloned header map per call.
	headers []http.Header
}

func newFakeProvider(t *testing.T, script ...string) (*httptest.Server, *fakeProvider) {
	t.Helper()
	fp := &fakeProvider{t: t, script: script}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			http.Error(w, rerr.Error(), http.StatusBadRequest)
			return
		}
		fp.mu.Lock()
		defer fp.mu.Unlock()
		fp.calls++
		fp.bodies = append(fp.bodies, string(body))
		fp.headers = append(fp.headers, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		idx := min(fp.calls-1, len(fp.script)-1)
		if _, werr := io.WriteString(w, fp.script[idx]); werr != nil {
			fp.t.Errorf("write: %v", werr)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, fp
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeProvider) lastHeader(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers) == 0 {
		return ""
	}
	return f.headers[len(f.headers)-1].Get(key)
}

func openAIEnvelope(raw string) string {
	b, merr := json.Marshal(raw)
	if merr != nil {
		panic(merr) // marshaling a string cannot fail
	}
	return `{"choices":[{"message":{"content":` + string(b) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`
}

// openAIEnvelopeCost adds provider-computed usage.cost (the OpenRouter shape).
func openAIEnvelopeCost(raw string, cost float64) string {
	b, merr := json.Marshal(raw)
	if merr != nil {
		panic(merr) // marshaling a string cannot fail
	}
	cb, merr := json.Marshal(cost) // canonical float rendering
	if merr != nil {
		panic(merr) // marshaling a float cannot fail
	}
	return `{"choices":[{"message":{"content":` + string(b) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"cost":` + string(cb) + `}}`
}

func anthropicEnvelope(raw string) string {
	b, merr := json.Marshal(raw)
	if merr != nil {
		panic(merr) // marshaling a string cannot fail
	}
	return `{"content":[{"type":"text","text":` + string(b) + `}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10}}`
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustOpenDB(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return db
}

func mustCount(t *testing.T, db *store.DB, run string) int {
	t.Helper()
	n, err := db.LLMCallCount(run)
	if err != nil {
		t.Fatal(err)
	}
	return n
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
	abs := mustAbs(t, "../testdata/clean/article.html")
	out := filepath.Join(t.TempDir(), "out.json")
	err := runScrape(t.Context(), "file://"+abs, scrapeOptions{Format: "json", Out: out, Render: "static"})
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	raw := mustRead(t, out)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if _, ok := doc["markdown"]; !ok {
		t.Error("missing markdown field")
	}
	db := mustOpenDB(t, dbPath)
	n := mustCount(t, db, "")
	if n != 0 {
		t.Errorf("llm_calls = %d, want 0", n)
	}
}

func TestScrapeEndToEndFileURL(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	srv, fp := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "test-key")
	abs := mustAbs(t, "../testdata/clean/article.html")
	schema := mustAbs(t, "../testdata/extract/price.yaml")
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
	raw := mustRead(t, out)
	var doc struct {
		Extracted map[string]any `json:"extracted"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if doc.Extracted["price"] != 12.99 {
		t.Errorf("price = %v, want 12.99", doc.Extracted["price"])
	}
	db := mustOpenDB(t, dbPath)
	n := mustCount(t, db, "")
	if n < 1 {
		t.Errorf("llm_calls = %d, want ≥1", n)
	}
}

func TestScrapeRenderStaticSPAShell(t *testing.T) {
	testEnv(t, "cache.db")
	abs := mustAbs(t, "../testdata/clean/spa-shell.html")
	out := filepath.Join(t.TempDir(), "out.json")
	// Static render must never touch the browser (no Chrome here).
	if err := runScrape(t.Context(), "file://"+abs, scrapeOptions{Format: "json", Out: out, Render: "static"}); err != nil {
		t.Fatalf("scrape spa-shell static: %v", err)
	}
}

func TestScrapeBadFormat(t *testing.T) {
	testEnv(t, "cache.db")
	abs := mustAbs(t, "../testdata/clean/article.html")
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
	abs := mustAbs(t, "../testdata/clean/article.html")
	schema := mustAbs(t, "../testdata/extract/price.yaml")
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
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "")
	t.Setenv("GOMAGPIE_ANTHROPIC_API_KEY", "")
	t.Setenv("GOMAGPIE_API_KEY", "")
	abs := mustAbs(t, "../testdata/clean/article.html")
	schema := mustAbs(t, "../testdata/extract/price.yaml")
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
	html := mustRead(t, "../testdata/clean/article.html")
	// Simulate stdin via temp file (no fetch involved).
	in := filepath.Join(t.TempDir(), "in.html")
	if err := os.WriteFile(in, html, 0o644); err != nil {
		t.Fatal(err)
	}
	schema := mustAbs(t, "../testdata/extract/price.yaml")
	out := filepath.Join(t.TempDir(), "out.json")
	err := runExtract(t.Context(), extractOptions{
		Schema: schema, ContentType: "html", Provider: "openai",
		Model: "gpt-4o-mini", Out: out, File: in,
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	raw := mustRead(t, out)
	var doc struct {
		Extracted map[string]any `json:"extracted"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if doc.Extracted["price"] != 12.99 {
		t.Errorf("price = %v", doc.Extracted["price"])
	}
	db := mustOpenDB(t, dbPath)
	n := mustCount(t, db, "")
	if n < 1 {
		t.Errorf("llm_calls = %d, want ≥1", n)
	}
}
