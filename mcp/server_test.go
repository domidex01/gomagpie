package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/domidex01/magpie/extract"
	magpiemcp "github.com/domidex01/magpie/mcp"
	"github.com/domidex01/magpie/scrape"
	"github.com/domidex01/magpie/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

func openMCPDB(t *testing.T) *store.DB {
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

func testMCPServer(t *testing.T, db *store.DB, fx *fakeExtractor) *sdk.Server {
	t.Helper()
	return magpiemcp.NewServer(magpiemcp.Deps{
		DB: db,
		ScrapeDeps: scrape.Deps{
			DB: db,
			ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
				return fx, nil
			},
			APIKeyFor: func(string) string { return "test-key" },
		},
		DefaultProvider: "fake",
		DefaultModel:    "fake",
	})
}

func dialInMemory(t *testing.T, srv *sdk.Server, opts *sdk.ClientOptions) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "v0.0.1"}, opts)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close() //nolint:errcheck // test teardown; failure unactionable
		_ = ss.Close() //nolint:errcheck // test teardown; failure unactionable
	})
	return cs
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, progressToken string) *sdk.CallToolResult {
	t.Helper()
	params := &sdk.CallToolParams{Name: name, Arguments: args}
	if progressToken != "" {
		params.Meta = sdk.Meta{"progressToken": progressToken}
	}
	res, err := cs.CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func decodeOut(t *testing.T, res *sdk.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned error: %v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode out: %v", err)
	}
	return out
}

func pageHTML(title, links string) string {
	return `<html><head><title>` + title + `</title></head><body><h1>` + title + `</h1>` + links +
		`<p>Plenty of honest descriptive prose keeps the static renderer in charge without any browser escalation at all.</p>` +
		`<p>A second paragraph of harmless filler text pushes visible length safely past the two-hundred character threshold.</p>` +
		`</body></html>`
}

// origin3Pages serves /a → {/b, /c}: a 3-page crawlable origin.
func origin3Pages(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/a":
			_, _ = w.Write([]byte(pageHTML("Page A", `<nav><a href="/b">b</a><a href="/c">c</a></nav>`))) //nolint:errcheck // httptest local; short write unactionable
		case "/b":
			_, _ = w.Write([]byte(pageHTML("Page B", ""))) //nolint:errcheck // httptest local; short write unactionable
		case "/c":
			_, _ = w.Write([]byte(pageHTML("Page C", ""))) //nolint:errcheck // httptest local; short write unactionable
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/a"
}

func originHost(t *testing.T, seedURL string) string {
	t.Helper()
	u, err := url.Parse(seedURL)
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	return strings.ToLower(u.Host)
}

var testSchemaMap = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title": map[string]any{"type": "string"},
	},
	"required": []any{"title"},
}

func TestMCP_FourTools(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)
	seed := origin3Pages(t)

	t.Run("scrape_markdown", func(t *testing.T) {
		out := decodeOut(t, callTool(t, cs, "scrape_url", map[string]any{"url": seed}, ""))
		if out["title"] == "" {
			t.Errorf("scrape_url markdown out has no title: %v", out)
		}
		if _, ok := out["extracted"]; ok {
			t.Errorf("markdown-only out has extracted: %v", out)
		}
	})

	t.Run("scrape_schema", func(t *testing.T) {
		out := decodeOut(t, callTool(t, cs, "scrape_url", map[string]any{"url": seed, "schema": testSchemaMap}, ""))
		ex, ok := out["extracted"].(map[string]any)
		if !ok || ex["title"] != "Widget" {
			t.Errorf("scrape_url extracted = %v, want title Widget", out["extracted"])
		}
	})

	t.Run("extract_html", func(t *testing.T) {
		out := decodeOut(t, callTool(t, cs, "extract_structured", map[string]any{
			"content": pageHTML("Widget", ""), "content_type": "html", "schema": testSchemaMap,
		}, ""))
		ex, ok := out["extracted"].(map[string]any)
		if !ok || ex["title"] != "Widget" {
			t.Errorf("extract_structured html = %v, want title Widget", out["extracted"])
		}
	})

	t.Run("extract_markdown", func(t *testing.T) {
		out := decodeOut(t, callTool(t, cs, "extract_structured", map[string]any{
			"content": "# Widget\n\nA fine widget.", "content_type": "markdown", "schema": testSchemaMap,
		}, ""))
		if _, ok := out["extracted"].(map[string]any); !ok {
			t.Errorf("extract_structured markdown has no extracted: %v", out)
		}
	})

	t.Run("selectors", func(t *testing.T) {
		host := originHost(t, seed)
		doc := `{"schema_hash":"abc","domain":"` + host + `","fields":{"title":{"type":"css","expr":"title","null_rate":0.1}},"synthesized_at":"2026-01-01T00:00:00Z","samples_used":3,"engine_version":1}`
		if err := db.PutSelectors(host, "abc", doc, 3); err != nil {
			t.Fatalf("PutSelectors: %v", err)
		}
		out := decodeOut(t, callTool(t, cs, "get_cached_selectors", map[string]any{"domain": host}, ""))
		sels, ok := out["selectors"].([]any)
		if !ok || len(sels) != 1 {
			t.Fatalf("selectors = %v, want 1 entry", out["selectors"])
		}
		sel := sels[0].(map[string]any)
		fields, ok := sel["fields"].(map[string]any)
		if !ok || fields["title"] == nil {
			t.Errorf("selector fields = %v, want title", sel["fields"])
		}
		if nulls, ok := sel["null_rates"].(map[string]any); !ok || nulls["title"] != 0.1 {
			t.Errorf("null_rates = %v, want title 0.1", sel["null_rates"])
		}
	})

	t.Run("crawl", func(t *testing.T) {
		out := decodeOut(t, callTool(t, cs, "crawl_site", map[string]any{"url": seed, "max_pages": 10}, ""))
		for _, k := range []string{"run_id", "pages_crawled", "records", "status"} {
			if out[k] == nil {
				t.Errorf("crawl_site out missing %q: %v", k, out)
			}
		}
		if out["run_id"] == "" {
			t.Error("crawl_site run_id empty")
		}
		if out["pages_crawled"].(float64) != 3 {
			t.Errorf("pages_crawled = %v, want 3", out["pages_crawled"])
		}
	})
}

func TestCrawlSite_RunIDZeroCalls(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)

	res := callTool(t, cs, "crawl_site", map[string]any{"url": origin3Pages(t), "max_pages": 10}, "")
	out := decodeOut(t, res)
	runID, _ := out["run_id"].(string)
	if runID == "" {
		t.Fatal("crawl_site out has no run_id")
	}
	before := fx.total()
	if before == 0 {
		t.Fatal("first crawl made 0 extractor calls — fixture isn't exercising the LLM path")
	}

	res2 := callTool(t, cs, "crawl_site", map[string]any{"run_id": runID}, "")
	out2 := decodeOut(t, res2)
	if out2["run_id"] != runID {
		t.Errorf("status run_id = %v, want %q", out2["run_id"], runID)
	}
	if got := fx.total() - before; got != 0 {
		t.Errorf("status re-invoke made %d extractor calls, want 0", got)
	}
	if out2["status"] != "finished" {
		t.Errorf("status = %v, want finished", out2["status"])
	}
}

func TestCrawlSite_UnknownRunID(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)

	res := callTool(t, cs, "crawl_site", map[string]any{"run_id": "no-such-run-xyz"}, "")
	if !res.IsError {
		t.Fatal("unknown run_id: IsError = false, want tool error")
	}
	raw, _ := json.Marshal(res.Content) //nolint:errcheck // Content is server-built JSON; marshal cannot fail
	if !strings.Contains(string(raw), "no-such-run-xyz") {
		t.Errorf("error content %s does not name the run id", raw)
	}
}

func TestCrawlSite_Progress(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	srv := testMCPServer(t, db, fx)

	var mu sync.Mutex
	notes := 0
	cs := dialInMemory(t, srv, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			mu.Lock()
			defer mu.Unlock()
			notes++
			if req.Params.Message == "" {
				t.Error("progress notification with empty message")
			}
		},
	})

	// Stdout must stay clean: crawl_site writing to os.Stdout would corrupt
	// the stdio transport's JSON-RPC framing in production.
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("pipe: %v", perr)
	}
	old := os.Stdout
	os.Stdout = w

	callTool(t, cs, "crawl_site", map[string]any{"url": origin3Pages(t), "max_pages": 10}, "p1")

	if err := w.Close(); err != nil {
		t.Errorf("close stdout pipe: %v", err)
	}
	os.Stdout = old
	data, rerr := io.ReadAll(r)
	if rerr != nil {
		t.Fatalf("read stdout pipe: %v", rerr)
	}
	if len(data) != 0 {
		t.Errorf("crawl_site wrote %d bytes to stdout, want 0 (Out must be os.DevNull)", len(data))
	}

	mu.Lock()
	defer mu.Unlock()
	if notes < 1 {
		t.Error("0 progress notifications on a 3-page crawl, want >= 1")
	}
}

func TestScrapeURL_PageFormat(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)
	seed := origin3Pages(t)
	out := decodeOut(t, callTool(t, cs, "scrape_url", map[string]any{"url": seed, "page_format": "llm"}, ""))
	content, _ := out["content"].(string)
	if !strings.Contains(content, "## Links") && !strings.Contains(content, "Page A") {
		t.Errorf("page_format llm content missing envelope: %v", out)
	}
	if _, ok := out["markdown"]; !ok {
		t.Errorf("markdown dropped when page_format set (back-compat break): %v", out)
	}
}

func TestScrapeURL_ScopeAndCookies(t *testing.T) {
	var gotCookie string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Shell</title></head><body><nav>nav-marker-text</nav><article><h1>Scoped From MCP</h1><p>Enough honest prose in the MCP-scoped article branch to survive trafilatura extraction cleanly.</p></article></body></html>`)) //nolint:errcheck // httptest local
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)
	// render static: the shell would otherwise escalate to the rod browser,
	// whose fetches carry neither profile headers nor cookies.
	out := decodeOut(t, callTool(t, cs, "scrape_url", map[string]any{
		"url": srv.URL, "render": "static", "include": []any{"article"}, "cookies": "a=b",
	}, ""))
	md, _ := out["markdown"].(string)
	if !strings.Contains(md, "Scoped From MCP") || strings.Contains(md, "nav-marker-text") {
		t.Errorf("include scoping failed via MCP:\n%s", md)
	}
	if gotCookie != "a=b" {
		t.Errorf("origin saw Cookie %q, want a=b", gotCookie)
	}
}

func TestScrapeURL_QualityError(t *testing.T) {
	raw, err := os.ReadFile("../testdata/quality/challenge-akamai.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(raw) //nolint:errcheck // httptest local; short write unactionable
	}))
	t.Cleanup(srv.Close)
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "scrape_url", Arguments: map[string]any{"url": srv.URL},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("403 challenge via MCP: want tool error, got %+v", res.StructuredContent)
	}
}

// TestScrapeURL_BadRender pins MCP/CLI validation parity: the tool path
// never re-implements the option switches, so agents see scrape's exact
// strings ( KD-2: this text is API surface).
func TestScrapeURL_BadRender(t *testing.T) {
	fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
	db := openMCPDB(t)
	cs := dialInMemory(t, testMCPServer(t, db, fx), nil)
	res := callTool(t, cs, "scrape_url", map[string]any{"url": "https://example.com", "render": "nope"}, "")
	if !res.IsError {
		t.Fatal("bad render via MCP: IsError = false, want tool error")
	}
	raw, _ := json.Marshal(res.Content) //nolint:errcheck // Content is server-built; marshal cannot fail
	var contents []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &contents); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(contents) == 0 || contents[0].Text != `mcp: scrape_url: scrape: render "nope" must be auto|static|browser` {
		t.Errorf("tool error = %s, want scrape's exact validation string", raw)
	}
}
