package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetGlobals() {
	cfgFile, cacheDB, apiKey = "", "", ""
	maxCost = 0
}

func captureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wo, we
	fn()
	if err := wo.Close(); err != nil {
		t.Errorf("close stdout pipe: %v", err)
	}
	if err := we.Close(); err != nil {
		t.Errorf("close stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = oldOut, oldErr
	out, err := io.ReadAll(ro)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	errOut, err := io.ReadAll(re)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	return string(out), string(errOut)
}

func crawlPageHTML(links ...string) string {
	var sb strings.Builder
	sb.WriteString(`<html><head><title>Widget - Buy</title></head><body><h1>Widget</h1><span class="price">12.99</span><nav>`)
	for _, l := range links {
		fmt.Fprintf(&sb, `<a href="%s">x</a>`, l)
	}
	sb.WriteString(`</nav><p>Plenty of honest descriptive prose keeps the static renderer in charge without any browser escalation at all.</p>`)
	sb.WriteString(`<p>A second paragraph of harmless filler text pushes visible length safely past the two-hundred character threshold.</p>`)
	sb.WriteString(`</body></html>`)
	return sb.String()
}

// writeFileSite creates index.html + pages linked from it; returns file:// URL.
func writeFileSite(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	names := []string{}
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf("p%d.html", i))
	}
	var nav strings.Builder
	for _, nm := range names {
		fmt.Fprintf(&nav, `<a href="%s">x</a>`, nm)
	}
	prose := `<p>Plenty of honest descriptive prose keeps the static renderer in charge without any browser escalation at all.</p>`
	for _, nm := range names {
		html := `<html><head><title>Widget - Buy</title></head><body><h1>Widget</h1><span class="price">12.99</span>` + prose + `</body></html>`
		if err := os.WriteFile(filepath.Join(dir, nm), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index := `<html><head><title>Widget - Buy</title></head><body><h1>Widget</h1><span class="price">12.99</span><nav>` + nav.String() + `</nav>` + prose + `</body></html>`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return "file://" + abs
}

func newTestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func fakeLLM(t *testing.T, doc string) *fakeProvider {
	t.Helper()
	srv, fp := newFakeProvider(t, openAIEnvelope(doc))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "test-key")
	return fp
}

func priceSchema(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../testdata/extract/price.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestCrawl_FileSiteExit0(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	fp := fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 3)
	out := filepath.Join(t.TempDir(), "r.jsonl")
	err := runCrawl(t.Context(), seed, crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: out,
		MaxPages: 10, MaxDepth: 3, Concurrency: 4, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	raw := mustRead(t, out)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 {
		t.Fatalf("jsonl lines = %d, want 4 (index + 3 pages)", len(lines))
	}
	schMustValidate(t, priceSchema(t), lines)
	db := mustOpenDB(t, dbPath)
	if n := mustCount(t, db, ""); n < 1 {
		t.Errorf("llm_calls = %d, want ≥1", n)
	}
	_ = fp
}

func schMustValidate(t *testing.T, schemaPath string, lines []string) {
	t.Helper()
	// Schema-valid per record: extracted.name == Widget, price == 12.99.
	for _, ln := range lines {
		var doc struct {
			Extracted map[string]any `json:"extracted"`
		}
		if err := json.Unmarshal([]byte(ln), &doc); err != nil {
			t.Fatalf("line not JSON: %v", err)
		}
		if doc.Extracted["name"] != "Widget" || doc.Extracted["price"] != 12.99 {
			t.Errorf("record = %v, want name=Widget price=12.99", doc.Extracted)
		}
	}
}

func TestCrawl_SecondRunZeroLLMCalls(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 3)
	mkOpts := func(out string) crawlCLIOptions {
		return crawlCLIOptions{
			Schema: priceSchema(t), Format: "jsonl", Out: out,
			MaxPages: 10, MaxDepth: 3, Concurrency: 4, SameHost: true, Rate: 1000,
			Provider: "openai", Model: "gpt-4o-mini",
		}
	}
	if err := runCrawl(t.Context(), seed, mkOpts(filepath.Join(t.TempDir(), "r1.jsonl"))); err != nil {
		t.Fatalf("run1: %v", err)
	}
	dbPath := os.Getenv("GOMAGPIE_CACHE_DB")
	db := mustOpenDB(t, dbPath)
	n1, err := db.LLMCallCount("")
	if err != nil {
		t.Fatal(err)
	}
	if err := runCrawl(t.Context(), seed, mkOpts(filepath.Join(t.TempDir(), "r2.jsonl"))); err != nil {
		t.Fatalf("run2: %v", err)
	}
	n2, err := db.LLMCallCount("")
	if err != nil {
		t.Fatal(err)
	}
	if n2 != n1 {
		t.Errorf("llm_calls delta = %d, want 0 (steady state cached)", n2-n1)
	}
}

func TestCrawl_ZeroRecordsExit3(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	out := filepath.Join(t.TempDir(), "r.jsonl")
	err := runCrawl(t.Context(), "file:///nonexistent-gomagpie.html", crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: out,
		MaxPages: 5, Concurrency: 2, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	})
	if codeOf(err) != 3 {
		t.Fatalf("exit = %d, want 3 (err=%v)", codeOf(err), err)
	}
}

func TestCrawl_PartialExit4(t *testing.T) {
	testEnv(t, "cache.db")
	fp := fakeLLM(t, `{"name":"Widget","price":12.99}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/good", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(crawlPageHTML("/bad"))) //nolint:errcheck // httptest local; short write unactionable
	})
	mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	srv := newTestServer(t, mux)
	out := filepath.Join(t.TempDir(), "r.jsonl")
	err := runCrawl(t.Context(), srv+"/good", crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: out,
		MaxPages: 5, MaxDepth: 1, Concurrency: 2, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	})
	if codeOf(err) != 4 {
		t.Fatalf("exit = %d, want 4 (err=%v)", codeOf(err), err)
	}
	raw := mustRead(t, out)
	if len(strings.TrimSpace(string(raw))) == 0 {
		t.Error("no records despite one good page")
	}
	_ = fp
}

func TestCrawl_RobotsExit5(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	newOrigin := func(robots func(w http.ResponseWriter, r *http.Request)) (string, *int) {
		mux := http.NewServeMux()
		mux.HandleFunc("/robots.txt", robots)
		hits := 0
		mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
			hits++
			_, _ = w.Write([]byte(crawlPageHTML())) //nolint:errcheck // httptest local; short write unactionable
		})
		srv := newTestServer(t, mux)
		return srv + "/page", &hits
	}
	deny := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("User-agent: *\nDisallow: /\n")) } //nolint:errcheck // httptest local
	unavail := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "x", http.StatusServiceUnavailable)
	}

	for name, robots := range map[string]func(w http.ResponseWriter, r *http.Request){"deny": deny, "unreachable": unavail} {
		seed, hits := newOrigin(robots)
		err := runCrawl(t.Context(), seed, crawlCLIOptions{
			Schema: priceSchema(t), Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
			MaxPages: 5, Concurrency: 2, SameHost: true, Rate: 1000,
			Provider: "openai", Model: "gpt-4o-mini",
		})
		if codeOf(err) != 5 {
			t.Errorf("%s: exit = %d, want 5 (err=%v)", name, codeOf(err), err)
		}
		if *hits != 0 {
			t.Errorf("%s: page fetches = %d, want 0", name, *hits)
		}
	}
}

func TestCrawl_IgnoreRobotsWarns(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n")) //nolint:errcheck // httptest local
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(crawlPageHTML())) //nolint:errcheck // httptest local
	})
	srv := newTestServer(t, mux)
	_, stderr := captureOutput(t, func() {
		if cerr := runCrawl(t.Context(), srv+"/page", crawlCLIOptions{
			Schema: priceSchema(t), Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
			MaxPages: 2, MaxDepth: 0, Concurrency: 2, SameHost: true, Rate: 1000,
			Provider: "openai", Model: "gpt-4o-mini", IgnoreRobots: true,
		}); cerr != nil {
			t.Errorf("ignore-robots crawl: %v", cerr)
		}
	})
	if !strings.Contains(stderr, "WARNING: --ignore-robots") {
		t.Errorf("stderr missing ignore-robots warning; got %q", stderr)
	}
}

func TestCrawl_MaxCostExit6(t *testing.T) {
	testEnv(t, "cache.db")
	fp := fakeLLM(t, `{"name":"Widget","price":12.99}`)
	t.Setenv("GOMAGPIE_MAX_COST", "0.000001")
	seed := writeFileSite(t, 1)
	err := runCrawl(t.Context(), seed, crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		MaxPages: 5, Concurrency: 2, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	})
	if codeOf(err) != 6 {
		t.Fatalf("exit = %d, want 6 (err=%v)", codeOf(err), err)
	}
	if fp.callCount() != 0 {
		t.Errorf("provider calls = %d, want 0", fp.callCount())
	}
}

func TestCacheCmd_InspectClear(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 3)
	if err := runCrawl(t.Context(), seed, crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		MaxPages: 5, MaxDepth: 1, Concurrency: 2, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("crawl: %v", err)
	}
	resetGlobals()
	root := rootCmd()
	root.SetArgs([]string{"cache", "inspect", "--domain", "file"})
	stdout, _ := captureOutput(t, func() {
		if err := root.Execute(); err != nil {
			t.Errorf("inspect: %v", err)
		}
	})
	if !strings.Contains(stdout, "price") || !strings.Contains(stdout, "null_rate") {
		t.Errorf("inspect output missing selectors/rates; got %q", stdout)
	}
	resetGlobals()
	root2 := rootCmd()
	root2.SetArgs([]string{"cache", "clear", "--domain", "file"})
	captureOutput(t, func() {
		if err := root2.Execute(); err != nil {
			t.Errorf("clear: %v", err)
		}
	})
	resetGlobals()
	root3 := rootCmd()
	root3.SetArgs([]string{"cache", "inspect", "--domain", "file"})
	_, stderr := captureOutput(t, func() {
		if err := root3.Execute(); err != nil {
			t.Errorf("re-inspect: %v", err)
		}
	})
	if !strings.Contains(stderr, "no matching selectors") {
		t.Errorf("re-inspect stderr = %q, want cache miss note", stderr)
	}
}

func TestCacheCmd_HealTooFewSamples(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(crawlPageHTML())) //nolint:errcheck // httptest local
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv, "http://")
	resetGlobals()
	root := rootCmd()
	root.SetArgs([]string{"cache", "heal", "--domain", host, "--schema", priceSchema(t), "--seed-url", srv + "/only", "--provider", "openai", "--model", "gpt-4o-mini"})
	_, stderr := captureOutput(t, func() {
		if err := root.Execute(); err == nil {
			t.Error("heal with 1 sample = nil error, want loud failure")
		}
	})
	if !strings.Contains(stderr, "at least 2") {
		t.Errorf("heal stderr = %q, want sample-count complaint", stderr)
	}
}

func TestScrape_FromCache(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	fp := fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 2)
	if err := runCrawl(t.Context(), seed, crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		MaxPages: 5, MaxDepth: 1, Concurrency: 2, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("warm crawl: %v", err)
	}
	before := fp.callCount()
	page := strings.TrimPrefix(seed, "file://")
	out := filepath.Join(t.TempDir(), "s.json")
	if err := runScrape(t.Context(), "file://"+page, scrapeOptions{
		Schema: priceSchema(t), Format: "json", Out: out, Render: "static",
		Provider: "openai", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("scrape: %v", err)
	}
	raw := mustRead(t, out)
	if !strings.Contains(string(raw), `"from_cache": true`) {
		t.Errorf("scrape output missing from_cache=true; got %s", raw)
	}
	if delta := fp.callCount() - before; delta != 0 {
		t.Errorf("scrape LLM calls = %d, want 0", delta)
	}
	db := mustOpenDB(t, dbPath)
	_ = db
}

func TestScrape_NoCacheForcesLLM(t *testing.T) {
	testEnv(t, "cache.db")
	fp := fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 2)
	if err := runCrawl(t.Context(), seed, crawlCLIOptions{
		Schema: priceSchema(t), Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		MaxPages: 5, MaxDepth: 1, Concurrency: 2, SameHost: true, Rate: 1000,
		Provider: "openai", Model: "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("warm crawl: %v", err)
	}
	before := fp.callCount()
	page := strings.TrimPrefix(seed, "file://")
	out := filepath.Join(t.TempDir(), "s.json")
	if err := runScrape(t.Context(), "file://"+page, scrapeOptions{
		Schema: priceSchema(t), Format: "json", Out: out, Render: "static",
		Provider: "openai", Model: "gpt-4o-mini", NoCache: true,
	}); err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if delta := fp.callCount() - before; delta < 1 {
		t.Errorf("scrape LLM calls = %d, want ≥1 with --no-cache", delta)
	}
}

func TestCrawl_FormatsCSVSQLiteJSON(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	seed := writeFileSite(t, 1) // index + p0 = 2 pages
	dbPath := os.Getenv("GOMAGPIE_CACHE_DB")
	mkOpts := func(format, out string) crawlCLIOptions {
		return crawlCLIOptions{
			Schema: priceSchema(t), Format: format, Out: out,
			MaxPages: 5, MaxDepth: 1, Concurrency: 2, SameHost: true, Rate: 1000,
			Provider: "openai", Model: "gpt-4o-mini",
		}
	}
	csvOut := filepath.Join(t.TempDir(), "r.csv")
	if err := runCrawl(t.Context(), seed, mkOpts("csv", csvOut)); err != nil {
		t.Fatalf("csv crawl: %v", err)
	}
	raw := strings.Split(strings.TrimSpace(string(mustRead(t, csvOut))), "\n")
	if raw[0] != "name,price" {
		t.Errorf("csv header = %q, want required-then-sorted name,price", raw[0])
	}
	if len(raw) != 3 {
		t.Errorf("csv lines = %d, want 3 (header + 2 records)", len(raw))
	}
	jsonOut := filepath.Join(t.TempDir(), "r.json")
	if err := runCrawl(t.Context(), seed, mkOpts("json", jsonOut)); err != nil {
		t.Fatalf("json crawl: %v", err)
	}
	var arr []any
	if err := json.Unmarshal(mustRead(t, jsonOut), &arr); err != nil || len(arr) != 2 {
		t.Errorf("json array len = %d, want 2 (err=%v)", len(arr), err)
	}
	if err := runCrawl(t.Context(), seed, mkOpts("sqlite", "")); err != nil {
		t.Fatalf("sqlite crawl: %v", err)
	}
	db := mustOpenDB(t, dbPath)
	n, err := db.TableCount("records")
	if err != nil || n != 2 {
		t.Errorf("records rows = %d,%v want 2", n, err)
	}
}

func TestCacheCmd_HealSuccess(t *testing.T) {
	testEnv(t, "cache.db")
	fakeLLM(t, `{"name":"Widget","price":12.99}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		links := ""
		if r.URL.Path == "/seed" {
			links = `<a href="/second">x</a>`
		}
		_, _ = w.Write([]byte(`<html><body><h1>Widget</h1><span class="price">12.99</span>` + links + `</body></html>`)) //nolint:errcheck // httptest local
	})
	srv := newTestServer(t, mux)
	host := strings.TrimPrefix(srv, "http://")
	resetGlobals()
	root := rootCmd()
	root.SetArgs([]string{"cache", "heal", "--domain", host, "--schema", priceSchema(t), "--seed-url", srv + "/seed", "--provider", "openai", "--model", "gpt-4o-mini"})
	_, stderr := captureOutput(t, func() {
		if err := root.Execute(); err != nil {
			t.Errorf("heal: %v", err)
		}
	})
	if !strings.Contains(stderr, "healed "+host) {
		t.Errorf("heal stderr = %q, want healed note", stderr)
	}
}
