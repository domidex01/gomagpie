package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strings"

	"gomagpie/clean"
	"gomagpie/fetch"
	"gomagpie/vertical"

	"github.com/jimsmart/grobotstxt"
)

// Sitemap caps: an index may legally list 50k sitemaps of 50k URLs each,
// so map follows ≤100 children and returns ≤10k URLs with truncated set
// past either cap. Deeper recursion and partial-on-budget stay Phase D.
const (
	maxSitemapChildren = 100
	maxSitemapURLs     = 10000
)

// ListSitemapURLs returns robots-declared sitemap URLs for siteURL's host:
// robots.txt is fetched through the Fetcher seam (never Checker — Checker
// dials live HTTP, which hermetic callers cannot inject), each body is
// gunzipped on 0x1f8b sniff, and one sitemapindex level is followed.
// A missing/unfetchable robots.txt falls back to /sitemap.xml; anything
// else (bad seed fetch, non-2xx, garbage XML) is a hard error naming the
// URL. Results dedupe preserving first-seen order.
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
	var locs []string
	seen := map[string]bool{}
	add := func(locs []string, raw string) []string {
		if raw == "" || seen[raw] {
			return locs
		}
		seen[raw] = true
		return append(locs, raw)
	}
	truncated := false
	followed := 0
	for _, seed := range seeds {
		if len(locs) >= maxSitemapURLs {
			return locs, true, nil
		}
		body, err := fetchBody(ctx, f, seed)
		if err != nil {
			return nil, false, err
		}
		if set, ok := parseURLSet(body); ok {
			for _, l := range set {
				locs = add(locs, clean.ResolveURL(seed, l))
				if len(locs) >= maxSitemapURLs {
					return locs, true, nil
				}
			}
			continue
		}
		children, ok := parseIndex(body)
		if !ok {
			return nil, false, fmt.Errorf("crawl: map: %s is neither urlset nor sitemapindex", seed)
		}
		if len(children) > maxSitemapChildren-followed {
			truncated = true
		}
		for _, child := range children {
			if followed >= maxSitemapChildren || len(locs) >= maxSitemapURLs {
				return locs, true, nil
			}
			followed++
			cbody, err := fetchBody(ctx, f, child)
			if err != nil {
				return nil, false, err
			}
			set, ok := parseURLSet(cbody)
			if !ok {
				return nil, false, fmt.Errorf("crawl: map: %s is not a urlset", child)
			}
			for _, l := range set {
				locs = add(locs, clean.ResolveURL(child, l))
				if len(locs) >= maxSitemapURLs {
					return locs, true, nil
				}
			}
		}
	}
	return locs, truncated, nil
}

// robotSeeds fetches base/robots.txt through the seam and returns its
// Sitemap: lines; a missing/unfetchable robots.txt falls back to
// base/sitemap.xml (the overwhelmingly common default).
func robotSeeds(ctx context.Context, f vertical.Fetcher, base string) ([]string, error) {
	robotsURL := base + "/robots.txt"
	resp, err := f.Fetch(ctx, fetch.FetchRequest{URL: robotsURL})
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return []string{base + "/sitemap.xml"}, nil
	}
	seeds := grobotstxt.Sitemaps(string(resp.HTML))
	if len(seeds) == 0 {
		return []string{base + "/sitemap.xml"}, nil
	}
	return seeds, nil
}

// fetchBody GETs one sitemap URL: non-2xx is a hard error naming the URL,
// gzip is sniffed (0x1f8b), never trusted by extension.
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
