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
	"strings"
	"sync"
	"testing"

	"gomagpie/fetch"
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
