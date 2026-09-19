package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/domidex01/magpie/clean"
	"github.com/domidex01/magpie/fetch"
	"github.com/domidex01/magpie/vertical"

	"github.com/jimsmart/grobotstxt"
)

// Sitemap caps: an index may legally list 50k sitemaps of 50k URLs each,
// so map follows ≤100 children and returns ≤10k URLs with truncated set
// past either cap, recursion bounded at depth 5, all under a 25s internal
// budget (partial results beat timeout drops).
const (
	maxSitemapChildren = 100
	maxSitemapURLs     = 10000
	maxSitemapDepth    = 5
)

// sitemapBudget is a var only so tests can inject a short budget; the
// production value is 25s.
var sitemapBudget = 25 * time.Second

var errSitemapBudget = errors.New("crawl: map: sitemap budget exceeded")

// ListSitemapURLs returns page URLs listed by siteURL's sitemaps:
// robots.txt seeds (grobotstxt ∪ a tolerant fallback scanner, missing
// robots falls back to /sitemap.xml), then a budgeted BFS over sitemap
// documents — indexes recursed to depth 5, urlsets' <loc> entries
// collected. Child fetch/parse failures are skipped (truncated:true),
// never fatal; the only hard errors are a bad site URL and a totally
// empty result when at least one error occurred. Budget expiry returns
// the partial list with truncated:true and nil error; a canceled parent
// context returns the context error.
func ListSitemapURLs(ctx context.Context, f vertical.Fetcher, siteURL string) ([]string, bool, error) {
	if f == nil {
		static, err := fetch.NewStaticFetcher()
		if err != nil {
			return nil, false, err
		}
		f = static
	}
	site, err := url.Parse(siteURL)
	if err != nil || site.Host == "" {
		return nil, false, fmt.Errorf("crawl: map: bad site URL %q", siteURL)
	}
	base := site.Scheme + "://" + site.Host
	seeds, err := robotSeeds(ctx, f, base)
	if err != nil {
		return nil, false, err
	}
	bctx, cancel := context.WithTimeoutCause(ctx, sitemapBudget, errSitemapBudget)
	defer cancel()

	var locs []string
	seen := map[string]bool{}
	visited := map[string]bool{}
	firstErr := error(nil)
	truncated := false
	children := 0 // non-seed document fetches, capped at maxSitemapChildren

	add := func(raw string) {
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		locs = append(locs, raw)
	}

	type node struct {
		url   string
		depth int
	}
	queue := make([]node, 0, len(seeds))
	for _, s := range seeds {
		queue = append(queue, node{url: s, depth: 0})
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err // parent cancel/expiry: surface the parent error
		}
		if bctx.Err() != nil {
			return locs, true, nil // our own budget: partial results, no error
		}
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.url] {
			continue // listed from another index too: results unaffected
		}
		if cur.depth > maxSitemapDepth {
			truncated = true
			continue
		}
		visited[cur.url] = true
		if cur.depth > 0 {
			if children >= maxSitemapChildren {
				truncated = true
				continue
			}
			children++
		}
		body, err := fetchBody(bctx, f, cur.url)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			if bctx.Err() != nil {
				return locs, true, nil
			}
			if firstErr == nil {
				firstErr = err
			}
			truncated = true
			continue
		}
		// A fetch that lands past the budget is discarded: stop-now
		// semantics, partial results win.
		if bctx.Err() != nil {
			return locs, true, nil
		}
		if set, ok := parseURLSet(body); ok {
			for _, l := range set {
				add(clean.ResolveURL(cur.url, l))
				if len(locs) >= maxSitemapURLs {
					return locs, true, nil
				}
			}
			continue
		}
		if children, ok := parseIndex(body); ok {
			for _, child := range children {
				queue = append(queue, node{url: clean.ResolveURL(cur.url, child), depth: cur.depth + 1})
			}
			continue
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("crawl: map: %s is neither urlset nor sitemapindex", cur.url)
		}
		truncated = true
	}
	if len(locs) == 0 && firstErr != nil {
		return nil, false, firstErr
	}
	return locs, truncated, nil
}

// robotSeeds fetches base/robots.txt through the seam and returns the
// union of grobotstxt's Sitemap: lines and a tolerant fallback scan
// (case/leading-space/inline-comment tolerance verified by probe test,
// not assumed). Relative seeds are absolutized against base. A
// missing/unfetchable or sitemap-less robots.txt falls back to
// base/sitemap.xml (the overwhelmingly common default).
func robotSeeds(ctx context.Context, f vertical.Fetcher, base string) ([]string, error) {
	robotsURL := base + "/robots.txt"
	resp, err := f.Fetch(ctx, fetch.FetchRequest{URL: robotsURL})
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return []string{base + "/sitemap.xml"}, nil
	}
	var seeds []string
	seen := map[string]bool{}
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		seeds = append(seeds, v)
	}
	for _, s := range grobotstxt.Sitemaps(string(resp.HTML)) {
		add(clean.ResolveURL(base, s))
	}
	for _, s := range fallbackSitemaps(string(resp.HTML)) {
		add(clean.ResolveURL(base, s))
	}
	if len(seeds) == 0 {
		return []string{base + "/sitemap.xml"}, nil
	}
	return seeds, nil
}

// fallbackSitemaps scans robots lines without a full parser: strip `#`
// comments, trim space, match `sitemap:` case-insensitively. The belt to
// grobotstxt's suspenders — kept regardless of the probe result.
func fallbackSitemaps(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		field, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(field), "sitemap") {
			continue
		}
		if v := strings.TrimSpace(value); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// fetchBody GETs one sitemap URL: non-2xx is an error naming the URL
// (skipped by the BFS, fatal only when nothing was listed at all), gzip
// is sniffed (0x1f8b), never trusted by extension. Content-Encoding:
// gzip responses arrive pre-decoded through the static fetcher.
func fetchBody(ctx context.Context, f vertical.Fetcher, rawURL string) ([]byte, error) {
	resp, err := f.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
	if err != nil {
		return nil, fmt.Errorf("crawl: map: GET %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("crawl: map: GET %s: HTTP %d", rawURL, resp.StatusCode)
	}
	body := resp.HTML
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("crawl: map: %s: gunzip: %w", rawURL, err)
		}
		defer func() { _ = zr.Close() }() //nolint:errcheck // bytes-backed; close unactionable
		body, err = io.ReadAll(io.LimitReader(zr, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("crawl: map: %s: gunzip: %w", rawURL, err)
		}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("crawl: map: %s: empty body", rawURL)
	}
	return body, nil
}

type urlSet struct {
	URLs []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

type sitemapIndex struct {
	Maps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

func parseURLSet(body []byte) ([]string, bool) {
	var set urlSet
	if err := xml.Unmarshal(body, &set); err != nil || len(set.URLs) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(set.URLs))
	for _, u := range set.URLs {
		if strings.TrimSpace(u.Loc) != "" {
			out = append(out, strings.TrimSpace(u.Loc))
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func parseIndex(body []byte) ([]string, bool) {
	var idx sitemapIndex
	if err := xml.Unmarshal(body, &idx); err != nil || len(idx.Maps) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(idx.Maps))
	for _, m := range idx.Maps {
		if strings.TrimSpace(m.Loc) != "" {
			out = append(out, strings.TrimSpace(m.Loc))
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
