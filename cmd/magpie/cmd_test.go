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

	"gomagpie/config"
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

// ---- LLM providers (codex exec + openrouter + opencode go/zen) ----

// stubCodexCmdScript is a minimal canned `codex`: preflight answers plus
// one valid record via the -o file (mirrors the extract-package stub).
const stubCodexCmdScript = `#!/bin/sh
LOG="$CALL_LOG"
echo "argv: $*" >> "$LOG"
cat >> "$LOG"
if [ "$1" = "--help" ]; then
  echo "Usage: codex exec [...] --output-schema <file> --json [...]"
  exit 0
fi
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":10}}'
echo '{"name":"Widget","price":12.99}' > "$out"
exit 0
`

func stubCodexCmd(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(stubCodexCmdScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALL_LOG", filepath.Join(dir, "calls.log"))
}

func scrapeOne(t *testing.T, provider, model string) error {
	t.Helper()
	seed := writeFileSite(t, 1)
	out := filepath.Join(t.TempDir(), "s.json")
	return runScrape(t.Context(), seed, scrapeOptions{
		Schema: priceSchema(t), Format: "json", Out: out, Render: "static",
		Provider: provider, Model: model,
	})
}

func TestProvider_UnknownErrors(t *testing.T) {
	testEnv(t, "cache.db")
	t.Setenv("GOMAGPIE_WAT_API_KEY", "x") // reach the switch past the key check
	err := scrapeOne(t, "wat", "m")
	if err == nil || !strings.Contains(err.Error(), "wat") {
		t.Fatalf("expected error naming the provider, got %v (silent default bills Anthropic)", err)
	}
	if codeOf(err) != 2 {
		t.Errorf("exit = %d, want 2", codeOf(err))
	}
}

func TestProvider_CodexNeedsNoKey(t *testing.T) {
	testEnv(t, "cache.db")
	stubCodexCmd(t)
	t.Setenv("GOMAGPIE_API_KEY", "")
	t.Setenv("GOMAGPIE_CODEX_API_KEY", "")
	if err := scrapeOne(t, "codex", "gpt-5.2"); err != nil {
		t.Fatalf("codex without key: %v (want keyless preflight+extract)", err)
	}
}

func TestProvider_OllamaNeedsNoKey(t *testing.T) {
	testEnv(t, "cache.db")
	srv, _ := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	if err := scrapeOne(t, "ollama", "llama3.1"); err != nil {
		t.Fatalf("ollama without key: %v", err)
	}
}

func TestProvider_OpenRouterNeedsKey(t *testing.T) {
	testEnv(t, "cache.db")
	t.Setenv("GOMAGPIE_API_KEY", "")
	t.Setenv("GOMAGPIE_OPENROUTER_API_KEY", "")
	if err := scrapeOne(t, "openrouter", "openai/gpt-4o-mini"); codeOf(err) != 7 {
		t.Fatalf("exit = %d, want 7 (err=%v)", codeOf(err), err)
	}
}

func TestProvider_DefaultModels(t *testing.T) {
	for _, tc := range []struct{ provider, want string }{
		{"anthropic", "claude-sonnet-5"},
		{"openai", "gpt-4o-mini"},
		{"ollama", "llama3.1"},
		{"openrouter", "openai/gpt-4o-mini"},
		{"codex", "gpt-5.2"},
		{"opencode-go", "glm-5.3"},
		{"opencode-zen", "claude-sonnet-4-6"},
	} {
		if got := config.DefaultModel(tc.provider); got != tc.want {
			t.Errorf("DefaultModel(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestSetKey_DashMessage(t *testing.T) {
	if err := config.SetKey("opencode-go", "bogus"); err != nil {
		if !strings.Contains(err.Error(), "GOMAGPIE_OPENCODE_GO_API_KEY") {
			t.Errorf("SetKey error = %q, want dash-to-underscore env name", err.Error())
		}
	}
	// Read path uses the same transform (white-box: env name is the contract).
	t.Setenv("GOMAGPIE_OPENCODE_GO_API_KEY", "k")
	if got := (config.Config{}).APIKey("opencode-go"); got != "k" {
		t.Errorf("APIKey(opencode-go) = %q, want env hit (dash→underscore)", got)
	}
}

func TestMaxCost_FlatRateExempt(t *testing.T) {
	// opencode-go leg: flat plan proceeds despite a near-zero ceiling.
	testEnv(t, "cache.db")
	srv, fp := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENCODE_GO_API_KEY", "k")
	t.Setenv("GOMAGPIE_MAX_COST", "0.000001")
	if err := scrapeOne(t, "opencode-go", "glm-5.3"); err != nil {
		t.Fatalf("opencode-go with tiny ceiling: %v (want exemption)", err)
	}
	if fp.callCount() < 1 {
		t.Error("opencode-go provider hits = 0, want ≥1 (exempt path proceeds)")
	}
	// codex leg: subscription spend proceeds too, keyless.
	testEnv(t, "cache.db")
	stubCodexCmd(t)
	t.Setenv("GOMAGPIE_MAX_COST", "0.000001")
	t.Setenv("GOMAGPIE_API_KEY", "")
	if err := scrapeOne(t, "codex", "gpt-5.2"); err != nil {
		t.Fatalf("codex with tiny ceiling: %v (want exemption)", err)
	}
	// metered control: openai aborts pre-call with 0 hits.
	testEnv(t, "cache.db")
	srv2, fp2 := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv2.URL)
	t.Setenv("GOMAGPIE_OPENAI_API_KEY", "k")
	t.Setenv("GOMAGPIE_MAX_COST", "0.000001")
	err := scrapeOne(t, "openai", "gpt-4o-mini")
	if codeOf(err) != 6 {
		t.Fatalf("exit = %d, want 6 (err=%v)", codeOf(err), err)
	}
	if fp2.callCount() != 0 {
		t.Errorf("metered provider calls = %d, want 0 (aborted pre-call)", fp2.callCount())
	}
}

func TestOpenRouter_CostRow(t *testing.T) {
	dbPath := testEnv(t, "cache.db")
	srv, _ := newFakeProvider(t, openAIEnvelopeCost(`{"name":"Widget","price":12.99}`, 0.0042))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENROUTER_API_KEY", "test-key")
	if err := scrapeOne(t, "openrouter", "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatalf("scrape: %v", err)
	}
	db := mustOpenDB(t, dbPath)
	calls, err := db.LLMCalls("")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range calls {
		if c.Provider == "openrouter" {
			found = true
			if c.USDEstimate != 0.0042 {
				t.Errorf("usd_estimate = %v, want 0.0042 from usage.cost", c.USDEstimate)
			}
		}
	}
	if !found {
		t.Errorf("no openrouter row in llm_calls (%d rows)", len(calls))
	}
}

func TestZen_SessionHeaders(t *testing.T) {
	// chat/completions leg on the Go base.
	testEnv(t, "cache.db")
	srv, fp := newFakeProvider(t, openAIEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	t.Setenv("GOMAGPIE_OPENCODE_GO_API_KEY", "k")
	if err := scrapeOne(t, "opencode-go", "glm-5.3"); err != nil {
		t.Fatalf("go chat leg: %v", err)
	}
	if got := fp.lastHeader("x-opencode-session"); got == "" {
		t.Error("go leg missing x-opencode-session header")
	}
	if !strings.Contains(fp.lastHeader("User-Agent"), "magpie") {
		t.Errorf("go leg UA = %q, want magpie", fp.lastHeader("User-Agent"))
	}
	// /messages leg on the Zen base (fresh DB: file-domain cache is per-DB).
	testEnv(t, "cache.db")
	srv2, fp2 := newFakeProvider(t, anthropicEnvelope(`{"name":"Widget","price":12.99}`))
	t.Setenv("GOMAGPIE_BASE_URL", srv2.URL)
	t.Setenv("GOMAGPIE_OPENCODE_ZEN_API_KEY", "k")
	if err := scrapeOne(t, "opencode-zen", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("zen messages leg: %v", err)
	}
	if got := fp2.lastHeader("x-opencode-session"); got == "" {
		t.Error("zen leg missing x-opencode-session header")
	}
	if !strings.Contains(fp2.lastHeader("User-Agent"), "magpie") {
		t.Errorf("zen leg UA = %q, want magpie", fp2.lastHeader("User-Agent"))
	}
}

func TestProviderHelp_ListsAll(t *testing.T) {
	resetGlobals()
	ids := []string{"anthropic", "openai", "ollama", "openrouter", "codex", "opencode-go", "opencode-zen"}
	root := rootCmd()
	usages := map[string]string{}
	for _, c := range root.Commands() {
		if f := c.Flags().Lookup("provider"); f != nil {
			usages[c.Name()] = f.Usage
		}
		for _, sub := range c.Commands() {
			if f := sub.Flags().Lookup("provider"); f != nil {
				usages[c.Name()+" "+sub.Name()] = f.Usage
			}
		}
	}
	for _, cmd := range []string{"scrape", "crawl", "extract", "cache heal"} {
		u, ok := usages[cmd]
		if !ok {
			t.Fatalf("no --provider flag found on %s (have %v)", cmd, usages)
		}
		for _, id := range ids {
			if !strings.Contains(u, id) {
				t.Errorf("%s --provider help missing %q (got %q)", cmd, id, u)
			}
		}
	}
}
