package crawl

// Sitemap listing tests: robots seeds, exact urlsets, one-level index
// fan-out, gzip sniffing, child/URL caps, and garbage bodies. All bodies
// are served by a seam fake or generated in-test — no .gz or 10k-row
// fixtures are ever committed.

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/motherlodelab/magpie/fetch"
)

// fakeSitemapFetcher is the crawl-side copy of the substring→bytes fake,
// keyed on robots/sitemap bodies with an order log for follow-count asserts.
type fakeSitemapFetcher struct {
	mu     sync.Mutex
	bodies map[string]fakeSitemapResp
	order  []string
}

type fakeSitemapResp struct {
	status int
	body   []byte
	err    error
}

func (f *fakeSitemapFetcher) Fetch(_ context.Context, req fetch.FetchRequest) (*fetch.FetchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, req.URL)
	best, bestLen := "", -1
	for sub := range f.bodies {
		if strings.Contains(req.URL, sub) && len(sub) > bestLen {
			best, bestLen = sub, len(sub)
		}
	}
	if bestLen < 0 {
		return nil, fmt.Errorf("fake fetcher: unexpected URL %s", req.URL)
	}
	r := f.bodies[best]
	if r.err != nil {
		return nil, r.err
	}
	st := r.status
	if st == 0 {
		st = 200
	}
	return &fetch.FetchResponse{URL: req.URL, FinalURL: req.URL, StatusCode: st, HTML: r.body}, nil
}

func (f *fakeSitemapFetcher) follows() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

// gzipBytes compresses in-test (never commit .gz fixtures).
func gzipBytes(t *testing.T, xml string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(xml)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// locSet builds a synthetic urlset with n loc rows.
func locSet(n int) string {
	var sb strings.Builder
	sb.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `<url><loc>https://example.com/p%d</loc></url>`, i)
	}
	return sb.String() + `</urlset>`
}

const testRobots = `User-agent: *
Disallow:

Sitemap: https://example.com/s1.xml
Sitemap: https://example.com/s2.xml # inline comment
`

func TestSitemap_RobotsSeeds(t *testing.T) {
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte(testRobots)},
		"example.com/s1.xml":     {body: []byte(`<urlset><url><loc>https://example.com/a</loc></url></urlset>`)},
		"example.com/s2.xml":     {body: []byte(`<urlset><url><loc>https://example.com/b</loc></url></urlset>`)},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com/")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	if len(urls) != 2 || urls[0] != "https://example.com/a" || urls[1] != "https://example.com/b" {
		t.Errorf("urls = %v, want [a b] (both robots seeds fetched)", urls)
	}
}

func TestSitemap_UrlsetExact(t *testing.T) {
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {status: 404, body: []byte("nope")},
		"example.com/sitemap.xml": {body: []byte(
			`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` +
				`<url><loc>https://example.com/x</loc></url>` +
				`<url><loc>https://example.com/y</loc></url>` +
				`<url><loc>https://example.com/z</loc></url></urlset>`)},
	}}
	urls, _, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	want := map[string]bool{"https://example.com/x": true, "https://example.com/y": true, "https://example.com/z": true}
	if len(urls) != 3 {
		t.Fatalf("urls = %v, want exactly 3", urls)
	}
	for _, u := range urls {
		if !want[u] {
			t.Errorf("unexpected url %q", u)
		}
	}
}

func TestSitemap_IndexOneLevel(t *testing.T) {
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/index.xml\n")},
		"example.com/index.xml": {body: []byte(`<sitemapindex>` +
			`<sitemap><loc>https://example.com/c1.xml</loc></sitemap>` +
			`<sitemap><loc>https://example.com/c2.xml</loc></sitemap></sitemapindex>`)},
		"example.com/c1.xml": {body: []byte(`<urlset><url><loc>https://example.com/1</loc></url></urlset>`)},
		"example.com/c2.xml": {body: []byte(`<urlset><url><loc>https://example.com/2</loc></url></urlset>`)},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	if len(urls) != 2 || urls[0] != "https://example.com/1" || urls[1] != "https://example.com/2" {
		t.Errorf("urls = %v, want union of both children", urls)
	}
}

func TestSitemap_Gzip(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "gz-extension", path: "sitemap.xml.gz"},
		{name: "no-extension", path: "feed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{}}
			seed := "https://example.com/" + tc.path
			f.bodies["example.com/robots.txt"] = fakeSitemapResp{body: []byte("Sitemap: " + seed + "\n")}
			f.bodies["example.com/"+tc.path] = fakeSitemapResp{body: gzipBytes(t, locSet(3))}
			urls, _, err := ListSitemapURLs(context.Background(), f, "https://example.com")
			if err != nil {
				t.Fatalf("ListSitemapURLs: %v", err)
			}
			if len(urls) != 3 {
				t.Errorf("urls = %v, want 3 decoded locs (magic sniff, not extension)", urls)
			}
		})
	}
}

func TestSitemap_ChildCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<sitemapindex>`)
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&sb, `<sitemap><loc>https://example.com/kid%d.xml</loc></sitemap>`, i)
	}
	sb.WriteString(`</sitemapindex>`)
	bodies := map[string]fakeSitemapResp{
		"example.com/robots.txt":    {body: []byte("Sitemap: https://example.com/big-index.xml\n")},
		"example.com/big-index.xml": {body: []byte(sb.String())},
	}
	for i := 0; i < 101; i++ {
		bodies[fmt.Sprintf("example.com/kid%d.xml", i)] = fakeSitemapResp{
			body: []byte(fmt.Sprintf(`<urlset><url><loc>https://example.com/u%d</loc></url></urlset>`, i)),
		}
	}
	f := &fakeSitemapFetcher{bodies: bodies}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true past 100 children")
	}
	followed := 0
	for _, u := range f.follows() {
		if strings.Contains(u, "/kid") {
			followed++
		}
	}
	if followed != 100 {
		t.Errorf("followed %d children, want 100", followed)
	}
	if len(urls) != 100 {
		t.Errorf("len(urls) = %d, want 100 distinct child locs", len(urls))
	}
}

func TestSitemap_URLCap(t *testing.T) {
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/huge.xml\n")},
		"example.com/huge.xml":   {body: []byte(locSet(10001))},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true past 10k URLs")
	}
	if len(urls) != 10000 {
		t.Errorf("len(urls) = %d, want 10000", len(urls))
	}
}

func TestSitemap_Garbage(t *testing.T) {
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/broken.xml\n")},
		"example.com/broken.xml": {body: []byte("<not xml")},
	}}
	_, _, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "broken.xml") {
		t.Fatalf("err = %v, want error naming the body URL", err)
	}
}

// --- Phase D additions: depth recursion, entities, tolerance, partials,
// budget-vs-cancel, content-encoding gzip. TestSitemap_Garbage above is
// unchanged: the empty+firstErr rule preserves its wording. ---

func indexListing(children ...string) string {
	var sb strings.Builder
	sb.WriteString(`<sitemapindex>`)
	for _, c := range children {
		sb.WriteString(`<sitemap><loc>` + c + `</loc></sitemap>`)
	}
	return sb.String() + `</sitemapindex>`
}

func urlsetOf(locs ...string) string {
	var sb strings.Builder
	sb.WriteString(`<urlset>`)
	for _, l := range locs {
		sb.WriteString(`<url><loc>` + l + `</loc></url>`)
	}
	return sb.String() + `</urlset>`
}

func TestSitemap_IndexChainDepth3(t *testing.T) {
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/idx0.xml\n")},
		"example.com/idx0.xml":   {body: []byte(indexListing("https://example.com/idx1.xml"))},
		"example.com/idx1.xml":   {body: []byte(indexListing("https://example.com/u.xml"))},
		"example.com/u.xml":      {body: []byte(urlsetOf("https://example.com/deep"))},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false (chain within depth)")
	}
	if len(urls) != 1 || urls[0] != "https://example.com/deep" {
		t.Errorf("urls = %v, want [deep] (index→index→urlset recursed)", urls)
	}
}

func TestSitemap_DepthFiveChain(t *testing.T) {
	// idx0(d0) → idx1(d1) → idx2(d2) → idx3(d3) → idx4(d4) → urlset(d5):
	// the deepest legal chain finds its locs.
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/idx0.xml\n")},
		"example.com/idx0.xml":   {body: []byte(indexListing("https://example.com/idx1.xml"))},
		"example.com/idx1.xml":   {body: []byte(indexListing("https://example.com/idx2.xml"))},
		"example.com/idx2.xml":   {body: []byte(indexListing("https://example.com/idx3.xml"))},
		"example.com/idx3.xml":   {body: []byte(indexListing("https://example.com/idx4.xml"))},
		"example.com/idx4.xml":   {body: []byte(indexListing("https://example.com/u.xml"))},
		"example.com/u.xml":      {body: []byte(urlsetOf("https://example.com/deep5"))},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false at depth-5 urlset")
	}
	if len(urls) != 1 || urls[0] != "https://example.com/deep5" {
		t.Errorf("urls = %v, want [deep5] (depth-5 recursion)", urls)
	}

	// One level deeper: the d6 urlset is skipped → truncated, no error.
	f2 := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/idx0.xml\n")},
		"example.com/idx0.xml":   {body: []byte(indexListing("https://example.com/idx1.xml"))},
		"example.com/idx1.xml":   {body: []byte(indexListing("https://example.com/idx2.xml"))},
		"example.com/idx2.xml":   {body: []byte(indexListing("https://example.com/idx3.xml"))},
		"example.com/idx3.xml":   {body: []byte(indexListing("https://example.com/idx4.xml"))},
		"example.com/idx4.xml":   {body: []byte(indexListing("https://example.com/idx5.xml"))},
		"example.com/idx5.xml":   {body: []byte(indexListing("https://example.com/u.xml"))},
		"example.com/u.xml":      {body: []byte(urlsetOf("https://example.com/deep6"))},
	}}
	urls, truncated, err = ListSitemapURLs(context.Background(), f2, "https://example.com")
	if err != nil {
		t.Fatalf("depth-6 ListSitemapURLs: %v", err)
	}
	if !truncated {
		t.Error("depth-6: truncated = false, want true past maxSitemapDepth")
	}
	if len(urls) != 0 {
		t.Errorf("depth-6 urls = %v, want none", urls)
	}
}

func TestSitemap_EntityDecoding(t *testing.T) {
	// encoding/xml already entity-decodes <loc> — proven, not assumed.
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/s.xml\n")},
		"example.com/s.xml":      {body: []byte(urlsetOf("https://example.com/p?a=1&amp;b=2"))},
	}}
	urls, _, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if len(urls) != 1 || !strings.Contains(urls[0], "a=1&b=2") {
		t.Errorf("urls = %v, want decoded &amp; → &", urls)
	}
}

func TestSitemap_RobotsTolerance(t *testing.T) {
	// Three sloppy spellings of the same directive; all three seeds must
	// be fetched. (The existing TestSitemap_RobotsSeeds already proves
	// inline comments parse via grobotstxt.)
	variants := map[string]string{
		"uppercase":     "SITEMAP: https://example.com/u.xml\n",
		"leading-space": "   Sitemap: https://example.com/u.xml\n",
		"trailing-hash": "Sitemap: https://example.com/u.xml # rotated nightly\n",
		"tab-separated": "Sitemap:\thttps://example.com/u.xml\n",
		"no-space":      "sitemap:https://example.com/u.xml\n",
	}
	for name, robots := range variants {
		t.Run(name, func(t *testing.T) {
			f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
				"example.com/robots.txt": {body: []byte(robots)},
				"example.com/u.xml":      {body: []byte(urlsetOf("https://example.com/x"))},
			}}
			urls, _, err := ListSitemapURLs(context.Background(), f, "https://example.com")
			if err != nil {
				t.Fatalf("ListSitemapURLs: %v", err)
			}
			fetched := false
			for _, u := range f.follows() {
				if strings.HasSuffix(u, "/u.xml") {
					fetched = true
				}
			}
			if !fetched {
				t.Errorf("seed /u.xml never fetched (follows=%v)", f.follows())
			}
			if len(urls) != 1 || urls[0] != "https://example.com/x" {
				t.Errorf("urls = %v, want [x]", urls)
			}
		})
	}
	// The fallback scanner (belt to grobotstxt's suspenders) directly:
	// case, spacing, comments, no-space all yield the value.
	got := fallbackSitemaps("SITEMAP:https://a.example/1.xml\n  sitemap: https://b.example/2.xml # c\n# just a comment\nUser-agent: *\n")
	if len(got) != 2 || got[0] != "https://a.example/1.xml" || got[1] != "https://b.example/2.xml" {
		t.Errorf("fallbackSitemaps = %v, want both values", got)
	}
}

func TestSitemap_PoisonedChildPartial(t *testing.T) {
	// One good child + one 500 child: siblings survive, truncated, nil err.
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/idx.xml\n")},
		"example.com/idx.xml":    {body: []byte(indexListing("https://example.com/good.xml", "https://example.com/dead.xml"))},
		"example.com/good.xml":   {body: []byte(urlsetOf("https://example.com/g1", "https://example.com/g2"))},
		"example.com/dead.xml":   {status: 500, body: []byte("boom")},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatalf("poisoned child became fatal: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true after child failure")
	}
	if len(urls) != 2 {
		t.Errorf("urls = %v, want the good child's 2 locs", urls)
	}
}

// sleepyFetcher wraps the fake with a targeted per-URL sleep so the 25s
// budget can be observed via a short injected budget (never by sleeping
// 25s in a test).
type sleepyFetcher struct {
	*fakeSitemapFetcher
	on string // sleep only for request URLs containing this substring
	d  time.Duration
}

func (s *sleepyFetcher) Fetch(ctx context.Context, req fetch.FetchRequest) (*fetch.FetchResponse, error) {
	if strings.Contains(req.URL, s.on) {
		time.Sleep(s.d)
	}
	return s.fakeSitemapFetcher.Fetch(ctx, req)
}

func TestSitemap_BudgetVsCancel(t *testing.T) {
	// Parent cancel → error (never a silent partial).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/s.xml\n")},
		"example.com/s.xml":      {body: []byte(urlsetOf("https://example.com/a"))},
	}}
	if urls, truncated, err := ListSitemapURLs(ctx, f, "https://example.com"); err == nil {
		t.Fatalf("canceled parent = (%v, %v, nil), want ctx error", urls, truncated)
	}

	// Budget expiry → partial + truncated + NIL error (the webclaw
	// discover-within semantics: partial beats timeout-drop).
	old := sitemapBudget
	sitemapBudget = 40 * time.Millisecond
	t.Cleanup(func() { sitemapBudget = old })
	sf := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/idx.xml\n")},
		"example.com/idx.xml":    {body: []byte(indexListing("https://example.com/fast.xml", "https://example.com/slow.xml"))},
		"example.com/fast.xml":   {body: []byte(urlsetOf("https://example.com/quick1", "https://example.com/quick2"))},
		"example.com/slow.xml":   {body: []byte(urlsetOf("https://example.com/never"))},
	}}
	sleepy := &sleepyFetcher{fakeSitemapFetcher: sf, on: "slow.xml", d: 200 * time.Millisecond}
	urls, truncated, err := ListSitemapURLs(context.Background(), sleepy, "https://example.com")
	if err != nil {
		t.Fatalf("budget expiry = %v, want partial + nil error", err)
	}
	if !truncated {
		t.Error("budget expiry: truncated = false, want true")
	}
	// The fast child's locs survive; the slow child's fate is timing noise
	// (it may or may not land — the point is partial-beats-error).
	if len(urls) < 2 {
		t.Errorf("budget expiry urls = %v, want at least the fast child's set", urls)
	}
}

func TestSitemap_ContentEncodingGzip(t *testing.T) {
	// Real static fetcher + Content-Encoding: gzip → body arrives
	// pre-decoded (transport path), same URL set as the raw-sniff row in
	// TestSitemap_Gzip. Explicit AllowPrivate (httptest is a private
	// literal): this pins the decode path, not the hatch.
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Sitemap: /s.xml\n")) //nolint:errcheck // httptest local
	})
	mux.HandleFunc("/s.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		if _, err := gz.Write([]byte(urlsetOf("https://example.com/g1", "https://example.com/g2"))); err != nil {
			t.Error(err)
		}
		_ = gz.Close() //nolint:errcheck // test server teardown; client already decoded
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	static, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	urls, _, err := ListSitemapURLs(context.Background(), static, srv.URL)
	if err != nil {
		t.Fatalf("ListSitemapURLs: %v", err)
	}
	if len(urls) != 2 {
		t.Errorf("urls = %v, want 2 (content-encoding decode path)", urls)
	}
}

func TestSitemap_DuplicateChildNotTruncated(t *testing.T) {
	// Two indexes listing the same child is ordinary sitemap hygiene —
	// the visited-set skip must not flag truncation for complete results.
	f := &fakeSitemapFetcher{bodies: map[string]fakeSitemapResp{
		"example.com/robots.txt": {body: []byte("Sitemap: https://example.com/idx.xml\n")},
		"example.com/idx.xml": {body: []byte(indexListing(
			"https://example.com/u.xml", "https://example.com/u.xml"))},
		"example.com/u.xml": {body: []byte(urlsetOf("https://example.com/a"))},
	}}
	urls, truncated, err := ListSitemapURLs(context.Background(), f, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("truncated = true, want false (results are complete; dup-skip is not truncation)")
	}
	if len(urls) != 1 {
		t.Errorf("urls = %v, want 1", urls)
	}
}
