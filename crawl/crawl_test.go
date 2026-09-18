package crawl

// Internal test package: retry-timing (fetchWithRetry) and hashURL need
// unexported access. Everything else uses only exported behavior.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"magpie/extract"
	"magpie/fetch"
	"magpie/selector"
	"magpie/store"
)

// selectorHash mirrors production's schemaHash for cache assertions.
func selectorHash(sch *extract.Schema) string { return selector.SchemaHash(sch) }

// --- fakes ---

type llmCall struct {
	purpose string
	url     string
}

type fakeExtractor struct {
	mu     sync.Mutex
	calls  []llmCall
	script map[string]map[string]any
	err    error
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return extract.ExtractResult{}, err
	}
	if f.err != nil {
		return extract.ExtractResult{}, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	purpose := in.Purpose
	if purpose == "" {
		purpose = "extract"
	}
	f.calls = append(f.calls, llmCall{purpose: purpose, url: in.PromptExtra})
	rec := f.script[in.PromptExtra]
	if rec == nil {
		rec = f.script["default"]
	}
	raw, merr := json.Marshal(rec)
	if merr != nil {
		return extract.ExtractResult{}, merr
	}
	return extract.ExtractResult{Record: rec, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) count(purpose string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.purpose == purpose {
			n++
		}
	}
	return n
}

func (f *fakeExtractor) total() int { return f.count("synth") + f.count("extract") }

const crawlTestSchema = `$schema: "https://json-schema.org/draft/2020-12/schema"
type: object
additionalProperties: false
required: [price, title]
properties:
  price: {type: number, x-magpie: {coerce: "eur_decimal"}}
  title: {type: string, x-magpie: {trim: true}}
  ean: {type: string}
`

var crawlTruth = map[string]any{"price": 12.99, "title": "Widget", "ean": "4001234567890"}

func mustTestSchema(t *testing.T) *extract.Schema {
	t.Helper()
	sch, err := extract.ParseSchema([]byte(crawlTestSchema))
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func openCrawlDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return db
}

// --- origin builders ---

type origin struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func (o *origin) count(path string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits[path]
}

// newSiteOrigin serves pages at each path, robotsBody at /robots.txt
// (status 200), and 404 elsewhere.
func newSiteOrigin(t *testing.T, pages map[string]string, robotsBody string) *origin {
	t.Helper()
	o := &origin{hits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.hits[r.URL.Path]++
		o.mu.Unlock()
		if r.URL.Path == "/robots.txt" {
			if robotsBody == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(robotsBody)) //nolint:errcheck // httptest local; short write unactionable
			return
		}
		if html, ok := pages[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(html)) //nolint:errcheck // httptest local; short write unactionable
			return
		}
		http.NotFound(w, r)
	})
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)
	return o
}

func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// --- robots ---

func TestRobots_Gate(t *testing.T) {
	allowAll := "User-agent: *\nDisallow:\n"
	o := newSiteOrigin(t, map[string]string{"/": "<html></html>", "/public": "<html></html>"}, allowAll)
	c := NewChecker()
	if ok, err := c.Allowed(context.Background(), o.srv.URL+"/public"); err != nil || !ok {
		t.Errorf("allow-all = %v,%v want true", ok, err)
	}

	deny := "User-agent: *\nDisallow: /private\n"
	o2 := newSiteOrigin(t, map[string]string{"/public": "<html></html>"}, deny)
	c2 := NewChecker()
	if ok, err := c2.Allowed(context.Background(), o2.srv.URL+"/private/x"); err != nil || ok {
		t.Errorf("Disallow /private = %v,%v want false,nil", ok, err)
	}
	if ok, err := c2.Allowed(context.Background(), o2.srv.URL+"/public"); err != nil || !ok {
		t.Errorf("allow /public = %v,%v want true", ok, err)
	}

	// THE header-vs-token regression: a `User-agent: magpie` group must deny
	// even though the real UA header is `magpie/1.0 (+...)`.
	o3 := newSiteOrigin(t, map[string]string{}, "User-agent: magpie\nDisallow: /x\n")
	c3 := NewChecker()
	if ok, err := c3.Allowed(context.Background(), o3.srv.URL+"/x"); err != nil || ok {
		t.Errorf("User-agent: magpie group = %v,%v want false,nil (bare-token bug)", ok, err)
	}
}

func TestRobots_StatusMatrix(t *testing.T) {
	// 503 → deny + sentinel.
	s503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte("<html></html>")) //nolint:errcheck // httptest local; short write unactionable
	}))
	defer s503.Close()
	c := NewChecker()
	ok, err := c.Allowed(context.Background(), s503.URL+"/")
	if ok || !errors.Is(err, ErrRobotsUnreachable) {
		t.Errorf("503 robots = %v,%v want false + ErrRobotsUnreachable", ok, err)
	}

	// 404 → allow.
	o404 := newSiteOrigin(t, map[string]string{"/": "<html></html>"}, "")
	c404 := NewChecker()
	if ok, err := c404.Allowed(context.Background(), o404.srv.URL+"/"); err != nil || !ok {
		t.Errorf("404 robots = %v,%v want true", ok, err)
	}

	// Closed port → deny + sentinel.
	cClosed := NewChecker()
	ok, err = cClosed.Allowed(context.Background(), "http://"+closedPort(t)+"/")
	if ok || !errors.Is(err, ErrRobotsUnreachable) {
		t.Errorf("closed-port = %v,%v want false + sentinel", ok, err)
	}
}

func TestRobots_CrawlDelayCapitalD(t *testing.T) {
	// Sites write "Crawl-Delay" capitalised; the handler gets the raw key.
	o := newSiteOrigin(t, map[string]string{}, "User-agent: *\nDisallow:\nCrawl-delay: 2\n")
	c := NewChecker()
	if d := c.CrawlDelay(context.Background(), o.srv.URL+"/"); d != 2*time.Second {
		t.Errorf("crawl-delay = %v, want 2s", d)
	}
	lim := NewHostLimiters(10, 3)
	u := strings.TrimPrefix(o.srv.URL, "http://")
	host := u[:strings.Index(u, ":")]
	lim.SetFloor(host, 2*time.Second)
	if got := lim.Limit(host); got > 0.5 {
		t.Errorf("limiter after floor = %v, want <= 0.5/s", got)
	}
	// SetFloor never raises.
	lim.SetFloor(host, time.Millisecond)
	if got := lim.Limit(host); got > 0.5 {
		t.Errorf("limiter after tiny floor = %v, want still <= 0.5/s", got)
	}
}

func TestRobots_Sitemaps(t *testing.T) {
	o := newSiteOrigin(t, map[string]string{}, "User-agent: *\nDisallow:\nSitemap: /sitemap.xml\n")
	c := NewChecker()
	sm := c.Sitemaps(context.Background(), o.srv.URL+"/")
	if len(sm) != 1 || !strings.HasSuffix(sm[0], "/sitemap.xml") {
		t.Errorf("sitemaps = %v, want [/sitemap.xml]", sm)
	}
}

// --- canonicalize ---

func TestCanonical_Vectors(t *testing.T) {
	vectors := map[string]string{
		"HTTP://EX.com:80/a?utm_x=1&b=2": "http://ex.com/a?b=2",
		"https://ex.com:443/a":           "https://ex.com/a",
		"http://ex.com/a?z=1&b=2":        "http://ex.com/a?b=2&z=1",
		"http://ex.com/a?gclid=1&b=2":    "http://ex.com/a?b=2",
		"http://ex.com/a?fbclid=1":       "http://ex.com/a",
		"http://ex.com/a?msclkid=1":      "http://ex.com/a",
		"http://ex.com/a#frag":           "http://ex.com/a",
		"http://ex.com/a?utm_source=x":   "http://ex.com/a",
		"http://ex.com":                  "http://ex.com/",
	}
	for in, want := range vectors {
		if got, err := Canonicalize(in); err != nil || got != want {
			t.Errorf("Canonicalize(%q) = %q,%v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "://bad", "http://"} {
		if _, err := Canonicalize(bad); err == nil {
			t.Errorf("Canonicalize(%q) = nil error, want loud failure", bad)
		}
	}
}

// --- dedup + links ---

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestDedup_DBIsTruth(t *testing.T) {
	db := openCrawlDB(t)
	f := NewFilter()
	fr := NewFrontier(db, f, "r1")
	urls := []string{"http://ex.com/1", "http://ex.com/2", "http://ex.com/3"}
	if n, err := fr.Add(urls, 0); err != nil || n != 3 {
		t.Fatalf("Add = %d,%v want 3", n, err)
	}
	if n, err := fr.Add(urls, 0); err != nil || n != 0 {
		t.Fatalf("re-Add = %d,%v want 0", n, err)
	}
	// Bloom false-positive path: pre-seed the filter with a hash that is
	// NOT in the DB → Add must still enqueue (DB is truth).
	fresh := "http://ex.com/fresh"
	c, err := Canonicalize(fresh)
	if err != nil {
		t.Fatal(err)
	}
	f.Add(shaHex(c))
	if n, err := fr.Add([]string{fresh}, 0); err != nil || n != 1 {
		t.Fatalf("FP-path Add = %d,%v want 1", n, err)
	}
}

func TestLinks_SameHostAndDepth(t *testing.T) {
	db := openCrawlDB(t)
	html := `<html><body>
<a href="/a">a</a><a href="/b">b</a>
<a href="http://other.com/x">ext</a>
</body></html>`
	fr2 := NewFrontier(db, NewFilter(), "r1")
	// Scope arg (Phase D): SameHost-only scope preserves the old exact-host
	// behavior this test locked in.
	if n, err := fr2.ExtractLinks([]byte(html), "http://ex.com/", 0, 0, Scope{SameHost: true}); err != nil || n != 0 {
		t.Fatalf("depth-gated = %d,%v want 0", n, err)
	}
	if n, err := fr2.ExtractLinks([]byte(html), "http://ex.com/", 0, 1, Scope{SameHost: true}); err != nil || n != 2 {
		t.Fatalf("same-host = %d,%v want 2", n, err)
	}
	claimed, err := fr2.Claim(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claimed {
		if c.Depth != 1 {
			t.Errorf("link depth = %d, want 1", c.Depth)
		}
	}
}

// --- retry ---

func okResp() *fetch.FetchResponse {
	return &fetch.FetchResponse{StatusCode: 200, HTML: []byte("<html></html>")}
}

func errResp(status int) *fetch.FetchResponse {
	return &fetch.FetchResponse{StatusCode: status, HTML: []byte("e")}
}

func countingDo(calls *int, script []struct {
	resp *fetch.FetchResponse
	err  error
}) func() (*fetch.FetchResponse, error) {
	return func() (*fetch.FetchResponse, error) {
		idx := *calls
		*calls++
		if idx >= len(script) {
			idx = len(script) - 1
		}
		return script[idx].resp, script[idx].err
	}
}

func TestClassify_PermanentIsOneCall(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 410} {
		calls := 0
		_, err := fetchWithRetry(context.Background(),
			countingDo(&calls, []struct {
				resp *fetch.FetchResponse
				err  error
			}{{errResp(status), nil}}),
			3, time.Millisecond, time.Millisecond)
		if err == nil {
			t.Errorf("status %d: nil error", status)
		}
		if calls != 1 {
			t.Errorf("status %d: calls = %d, want exactly 1", status, calls)
		}
	}
}

func TestRetry_TransientRetries(t *testing.T) {
	calls := 0
	resp, err := fetchWithRetry(context.Background(),
		countingDo(&calls, []struct {
			resp *fetch.FetchResponse
			err  error
		}{{errResp(500), nil}, {errResp(500), nil}, {okResp(), nil}}),
		3, time.Millisecond, time.Millisecond)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("transient = %v,%v", resp, err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want exactly 3 (MaxTries)", calls)
	}
}

func TestRetry_UnparseableRetryAfterFallsBack(t *testing.T) {
	r := errResp(429)
	r.Headers = http.Header{"Retry-After": []string{"soon"}}
	calls := 0
	_, err := fetchWithRetry(context.Background(),
		countingDo(&calls, []struct {
			resp *fetch.FetchResponse
			err  error
		}{{r, nil}, {okResp(), nil}}),
		3, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("unparseable Retry-After: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestRetry_RetryAfterWaits(t *testing.T) {
	r := errResp(429)
	r.Headers = http.Header{"Retry-After": []string{"1"}}
	calls := 0
	start := time.Now()
	_, err := fetchWithRetry(context.Background(),
		countingDo(&calls, []struct {
			resp *fetch.FetchResponse
			err  error
		}{{r, nil}, {okResp(), nil}}),
		3, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("Retry-After(1): %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Errorf("elapsed = %v, want ≥ ~1s Retry-After wait", elapsed)
	}
}

func TestRetry_CanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := fetchWithRetry(ctx,
		countingDo(&calls, []struct {
			resp *fetch.FetchResponse
			err  error
		}{{okResp(), nil}}),
		3, time.Millisecond, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls > 1 {
		t.Errorf("calls = %d, want ≤1", calls)
	}
}

// --- full runs ---

func itemPage(linkPaths ...string) string {
	var sb strings.Builder
	sb.WriteString(`<html><head><title>Widget - Buy</title></head><body><nav>`)
	for _, p := range linkPaths {
		fmt.Fprintf(&sb, `<a href="%s">go</a>`, p)
	}
	// Long visible prose keeps ScoreJSRequired at 0 (static path only —
	// the default suite must never touch a real browser).
	sb.WriteString(`</nav><h1 id="productTitle">Widget</h1><span id="price">12.99</span><span class="ean">4001234567890</span>`)
	sb.WriteString(`<p>The Widget is a fine product with many excellent qualities worth describing at length for discerning buyers everywhere today.</p>`)
	sb.WriteString(`<p>Second paragraph of honest descriptive prose ensures the visible-text heuristic stays well above the two-hundred character mark.</p>`)
	sb.WriteString(`</body></html>`)
	return sb.String()
}

func sevenPages() map[string]string {
	links := []string{"/0", "/1", "/2", "/3", "/4", "/5", "/6"}
	pages := map[string]string{}
	for _, p := range links {
		pages[p] = itemPage(links...)
	}
	return pages
}

func TestTermination_SevenPagesDone(t *testing.T) {
	o := newSiteOrigin(t, sevenPages(), "")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	out := filepath.Join(t.TempDir(), "r.jsonl")
	done := make(chan Result, 1)
	go func() {
		res, err := Run(context.Background(), Options{
			SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
			MaxPages: 100, MaxDepth: 10, SameHost: true,
			FetchWorkers: 4, Rate: 1000, Format: "jsonl", Out: out,
			DB: db, Extractor: fx,
		})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if res.PagesOK != 7 {
			t.Errorf("pages_ok = %d, want 7", res.PagesOK)
		}
		if p, i, d, e, err := db.CrawlStats(res.RunID); err != nil || p != 0 || i != 0 || d != 7 || e != 0 {
			t.Errorf("stats = %d/%d/%d/%d,%v want 0/0/7/0", p, i, d, e, err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("crawl hung; pump+outstanding termination broken")
	}
}

func TestResume_DoneNeverRefetched(t *testing.T) {
	// Uninterrupted reference on its own origin+DB.
	oRef := newSiteOrigin(t, sevenPages(), "")
	dbRef := openCrawlDB(t)
	fxRef := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	refRes, err := Run(context.Background(), Options{
		SeedURL: oRef.srv.URL + "/0", Schema: mustTestSchema(t),
		MaxPages: 100, MaxDepth: 10, SameHost: true,
		FetchWorkers: 4, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "ref.jsonl"),
		DB: dbRef, Extractor: fxRef,
	})
	if err != nil {
		t.Fatalf("reference run: %v", err)
	}
	if refRes.PagesOK != 7 {
		t.Fatalf("reference pages_ok = %d, want 7", refRes.PagesOK)
	}

	// Interrupted run on a separate origin+DB: claim 3, then strand 2 more
	// (the SIGKILL path: inflight rows never marked).
	o := newSiteOrigin(t, sevenPages(), "")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	partRes, err := Run(context.Background(), Options{
		SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
		MaxPages: 3, MaxDepth: 10, SameHost: true,
		FetchWorkers: 2, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "part.jsonl"),
		DB: db, Extractor: fx,
	})
	if err != nil {
		t.Fatalf("partial run: %v", err)
	}
	if partRes.PagesOK != 3 {
		t.Fatalf("partial pages_ok = %d, want 3", partRes.PagesOK)
	}
	stranded, err := db.Claim(partRes.RunID, 2)
	if err != nil || len(stranded) != 2 {
		t.Fatalf("strand claim = %d,%v want 2", len(stranded), err)
	}

	resRes, err := Run(context.Background(), Options{
		SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
		MaxPages: 100, MaxDepth: 10, SameHost: true,
		FetchWorkers: 4, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "res.jsonl"),
		DB: db, Extractor: fx, Resume: true, ResumeID: partRes.RunID,
	})
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if resRes.RunID != partRes.RunID {
		t.Errorf("resume run id changed: %q vs %q", resRes.RunID, partRes.RunID)
	}
	p, i, d, e, err := db.CrawlStats(partRes.RunID)
	if err != nil || p != 0 || i != 0 || d != 7 || e != 0 {
		t.Errorf("resumed stats = %d/%d/%d/%d,%v want 0/0/7/0", p, i, d, e, err)
	}
	if d != refRes.PagesOK {
		t.Errorf("resumed pages_ok = %d, uninterrupted = %d", d, refRes.PagesOK)
	}
	// Done pages never re-fetched: every page fetched exactly once.
	for _, path := range []string{"/0", "/1", "/2", "/3", "/4", "/5", "/6"} {
		if n := o.count(path); n != 1 {
			t.Errorf("origin hits %s = %d, want exactly 1", path, n)
		}
	}
}

func TestCrawl_SecondRunZeroLLM(t *testing.T) {
	o := newSiteOrigin(t, sevenPages(), "")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	run := func(out string) Result {
		t.Helper()
		res, err := Run(context.Background(), Options{
			SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
			MaxPages: 100, MaxDepth: 10, SameHost: true,
			FetchWorkers: 4, Rate: 1000, Format: "jsonl", Out: out,
			DB: db, Extractor: fx,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return res
	}
	res1 := run(filepath.Join(t.TempDir(), "r1.jsonl"))
	if res1.Records != 7 {
		t.Fatalf("run1 records = %d, want 7", res1.Records)
	}
	before := fx.total()
	res2 := run(filepath.Join(t.TempDir(), "r2.jsonl"))
	if res2.Records != 7 {
		t.Fatalf("run2 records = %d, want 7 (still served)", res2.Records)
	}
	if delta := fx.total() - before; delta != 0 {
		t.Errorf("second-run LLM calls = %d, want 0 (steady state cached)", delta)
	}
}

func TestCrawl_RobotsBlocked(t *testing.T) {
	o := newSiteOrigin(t, sevenPages(), "User-agent: *\nDisallow: /\n")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	_, err := Run(context.Background(), Options{
		SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
		MaxPages: 10, SameHost: true, Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		DB: db, Extractor: fx,
	})
	if !errors.Is(err, ErrRobotsBlocked) {
		t.Fatalf("deny-all err = %v, want ErrRobotsBlocked", err)
	}
	if n := o.count("/0"); n != 0 {
		t.Errorf("page fetches = %d, want 0", n)
	}
}

func TestHooks_ProgressAndOnRecord(t *testing.T) {
	links := []string{"/0", "/1", "/2", "/3", "/4"}
	pages := map[string]string{}
	for _, p := range links {
		pages[p] = itemPage(links...)
	}
	o := newSiteOrigin(t, pages, "")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	var mu sync.Mutex
	var progressCalls int
	var records []map[string]any
	res, err := Run(context.Background(), Options{
		SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
		MaxPages: 5, MaxDepth: 10, SameHost: true,
		FetchWorkers: 4, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		DB: db, Extractor: fx,
		Progress: func(done int) {
			mu.Lock()
			defer mu.Unlock()
			progressCalls++
		},
		OnRecord: func(r map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			records = append(records, r)
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if progressCalls != res.PagesOK+res.PagesErr {
		t.Errorf("progress calls = %d, want done+errored = %d", progressCalls, res.PagesOK+res.PagesErr)
	}
	if len(records) != res.PagesOK {
		t.Errorf("onRecord records = %d, want pages_ok = %d", len(records), res.PagesOK)
	}
}

func TestHooks_NilSafe(t *testing.T) {
	links := []string{"/0", "/1"}
	mkPages := func() map[string]string {
		pages := map[string]string{}
		for _, p := range links {
			pages[p] = itemPage(links...)
		}
		return pages
	}
	run := func(t *testing.T, hooks bool) Result {
		o := newSiteOrigin(t, mkPages(), "")
		db := openCrawlDB(t)
		fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
		opts := Options{
			SeedURL: o.srv.URL + "/0", Schema: mustTestSchema(t),
			MaxPages: 10, MaxDepth: 10, SameHost: true,
			FetchWorkers: 4, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
			DB: db, Extractor: fx,
		}
		if hooks {
			opts.Progress = func(int) {}
			opts.OnRecord = func(map[string]any) {}
		}
		res, err := Run(context.Background(), opts)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return res
	}
	withHooks := run(t, true)
	withoutHooks := run(t, false)
	if withHooks.PagesOK != withoutHooks.PagesOK || withHooks.PagesErr != withoutHooks.PagesErr {
		t.Errorf("hooked %+v != nil-hook %+v (hooks must not change results)", withHooks, withoutHooks)
	}
}

func TestCrawl_QualityCountedNotCached(t *testing.T) {
	denyBody, err := os.ReadFile("../testdata/quality/challenge-akamai.html")
	if err != nil {
		t.Fatal(err)
	}
	goodHTML := `<html><head><title>Good</title></head><body><h1>Good page</h1><p>` +
		strings.Repeat("honest crawlable prose ", 30) +
		`</p><a href="/deny">deny</a></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/deny":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(denyBody) //nolint:errcheck // httptest local; short write unactionable
		default:
			_, _ = w.Write([]byte(goodHTML)) //nolint:errcheck // httptest local; short write unactionable
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	out := filepath.Join(t.TempDir(), "r.jsonl")
	res, err := Run(context.Background(), Options{
		SeedURL: srv.URL + "/", Schema: mustTestSchema(t),
		MaxPages: 10, MaxDepth: 1, SameHost: true,
		FetchWorkers: 2, Rate: 1000, Format: "jsonl", Out: out,
		DB: db, Extractor: fx,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PagesErr < 1 {
		t.Errorf("pages_err = %d, want ≥1", res.PagesErr)
	}
	if res.Records != 1 {
		t.Errorf("records = %d, want 1 (good page only)", res.Records)
	}
	raw, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(raw), "/deny") {
		t.Errorf("writer output contains blocked URL:\n%s", raw)
	}
	u, uerr := url.Parse(srv.URL)
	if uerr != nil {
		t.Fatal(uerr)
	}
	if _, ok, gerr := db.GetSelectors(strings.ToLower(u.Host), selectorHash(mustTestSchema(t))); gerr == nil && ok {
		t.Error("selector cache written despite quality failure path")
	}
}

// --- Phase D additions: scope-filtered frontier, NoSitemap, warn-and-
// proceed sitemap seed expansion. Integration runs ride the same bare
// NewStaticFetcher constructor as the pre-existing suite above; the new
// SECURITY assertions live in fetch/ssrf_test.go with explicit options.
// ---

// scopeHTML links every scope-relevant shape: in-prefix, out-of-prefix,
// binary asset, asset smuggled via query, subdomain, foreign host, utm'd.
func scopeHTML() []byte {
	return []byte(`<html><body>
<a href="/docs/a">in</a>
<a href="/api/b">out-prefix</a>
<a href="/x.pdf">pdf</a>
<a href="/f.pdf?dl=1">pdf-query</a>
<a href="http://sub.ex.com/docs/c">sub</a>
<a href="http://other.com/docs/d">foreign</a>
<a href="/docs/e?x=1&utm_source=t">canonical</a>
</body></html>`)
}

func TestLinks_ScopeFilter(t *testing.T) {
	db := openCrawlDB(t)
	fr := NewFrontier(db, NewFilter(), "r1")
	scope, err := CompileScope(true, true, "/docs", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := fr.ExtractLinks(scopeHTML(), "http://ex.com/docs/", 0, 3, scope)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("enqueued = %d, want 3 (docs/a, sub/docs/c, docs/e)", n)
	}
	claimed, err := fr.Claim(10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range claimed {
		got[c.URL] = c.Depth
		if c.Depth != 1 {
			t.Errorf("link %s depth = %d, want 1", c.URL, c.Depth)
		}
	}
	for _, want := range []string{"http://ex.com/docs/a", "http://sub.ex.com/docs/c", "http://ex.com/docs/e?x=1"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing in-scope link %s (got %v)", want, got)
		}
	}
	for _, banned := range []string{"http://ex.com/api/b", "http://ex.com/x.pdf", "http://ex.com/f.pdf?dl=1", "http://other.com/docs/d"} {
		if _, ok := got[banned]; ok {
			t.Errorf("out-of-scope link %s enqueued", banned)
		}
	}
}

// captureStderr swaps os.Stderr for one call (warn-and-proceed asserts).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = we
	fn()
	if err := we.Close(); err != nil {
		t.Errorf("close stderr pipe: %v", err)
	}
	os.Stderr = old
	out, err := io.ReadAll(re)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	return string(out)
}

// sitemapSite builds an origin whose sitemap body can reference the
// runtime-random server URL: baseURL is captured by the handlers and
// assigned before any request is served.
type sitemapSite struct {
	*origin
	baseURL string
}

func newSitemapSite(t *testing.T, robots string) *sitemapSite {
	t.Helper()
	s := &sitemapSite{origin: &origin{hits: map[string]int{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte(robots)) //nolint:errcheck // httptest local
		case "/sitemap.xml":
			_, _ = w.Write([]byte(urlsetOf( //nolint:errcheck // httptest local; short write unactionable
				s.baseURL+"/docs/a",
				s.baseURL+"/api/b",
			))) //nolint:errcheck // httptest local
		case "/docs/a", "/api/b", "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(itemPage())) //nolint:errcheck // httptest local
		default:
			http.NotFound(w, r)
		}
	})
	s.srv = httptest.NewServer(mux)
	s.baseURL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

func TestCrawl_NoSitemapZeroFetch(t *testing.T) {
	s := newSitemapSite(t, "User-agent: *\nDisallow:\nSitemap: /sitemap.xml\n")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	res, err := Run(context.Background(), Options{
		SeedURL: s.srv.URL + "/", Schema: mustTestSchema(t),
		MaxPages: 5, MaxDepth: 1, SameHost: true,
		FetchWorkers: 2, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		DB: db, Extractor: fx, NoSitemap: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PagesOK < 1 {
		t.Errorf("pages_ok = %d, want ≥1 (crawl itself must still work)", res.PagesOK)
	}
	if n := s.count("/sitemap.xml"); n != 0 {
		t.Errorf("sitemap fetches = %d, want 0 with NoSitemap", n)
	}
}

func TestCrawl_SeedExpansionScoped(t *testing.T) {
	s := newSitemapSite(t, "User-agent: *\nDisallow:\nSitemap: /sitemap.xml\n")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	res, err := Run(context.Background(), Options{
		SeedURL: s.srv.URL + "/", Schema: mustTestSchema(t),
		MaxPages: 5, MaxDepth: 0, SameHost: true,
		FetchWorkers: 2, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
		DB: db, Extractor: fx, PathPrefix: "/docs",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Seed "/" (operator's choice) is fetched; /docs/a came from the
	// expansion and is in-prefix; /api/b is out-of-prefix → never enqueued.
	if n := s.count("/docs/a"); n < 1 {
		t.Errorf("/docs/a fetches = %d, want ≥1 (in-prefix expansion enqueued)", n)
	}
	if n := s.count("/api/b"); n != 0 {
		t.Errorf("/api/b fetches = %d, want 0 (out-of-prefix expansion dropped)", n)
	}
	if res.PagesErr != 0 {
		t.Errorf("pages_err = %d, want 0", res.PagesErr)
	}
}

func TestCrawl_SeedExpansionWarnProceed(t *testing.T) {
	// robots.txt advertises /sitemap.xml which 404s: expansion errors, the
	// crawl proceeds seed-only with a stderr warning — never aborts.
	o := newSiteOrigin(t, map[string]string{
		"/":         itemPage(),
		"/onlypage": itemPage(),
	}, "User-agent: *\nDisallow:\nSitemap: /sitemap.xml\n")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}}
	var res Result
	var rerr error
	stderr := captureStderr(t, func() {
		res, rerr = Run(context.Background(), Options{
			SeedURL: o.srv.URL + "/onlypage", Schema: mustTestSchema(t),
			MaxPages: 5, MaxDepth: 0, SameHost: true,
			FetchWorkers: 2, Rate: 1000, Format: "jsonl", Out: filepath.Join(t.TempDir(), "r.jsonl"),
			DB: db, Extractor: fx,
		})
	})
	if rerr != nil {
		t.Fatalf("Run: %v (expansion errors must warn, not abort)", rerr)
	}
	if res.PagesOK != 1 {
		t.Errorf("pages_ok = %d, want 1 (seed-only)", res.PagesOK)
	}
	if !strings.Contains(stderr, "continuing seed-only") {
		t.Errorf("stderr %q missing 'continuing seed-only' warning", stderr)
	}
}
