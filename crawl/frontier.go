package crawl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gomagpie/store"

	"github.com/PuerkitoBio/goquery"
)

// Canonicalize normalizes a URL for dedup: lowercase host, strip default
// ports, sort query params, drop tracking params + fragment.
func Canonicalize(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("crawl: bad url %q", rawURL)
	}
	if u.Scheme == "file" {
		u.Fragment = ""
		if u.Path == "" {
			return "", fmt.Errorf("crawl: bad url %q", rawURL)
		}
		return u.String(), nil
	}
	if u.Host == "" {
		return "", fmt.Errorf("crawl: bad url %q", rawURL)
	}
	u.Host = strings.ToLower(u.Host)
	if h, p, err := splitHostPort(u.Host); err == nil {
		if (u.Scheme == "http" && p == "80") || (u.Scheme == "https" && p == "443") {
			u.Host = h
		}
	}
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "gclid" || lk == "fbclid" || lk == "msclkid" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func splitHostPort(host string) (string, string, error) {
	i := strings.LastIndex(host, ":")
	if i < 0 {
		return host, "", fmt.Errorf("no port")
	}
	return host[:i], host[i+1:], nil
}

func hashURL(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// Frontier owns canonicalization + two-tier dedup (bloom front, SQLite truth)
// + link extraction. Add returns rows actually inserted.
type Frontier struct {
	db     *store.DB
	filter *Filter
	runID  string
}

// NewFrontier builds a Frontier for a run.
func NewFrontier(db *store.DB, filter *Filter, runID string) *Frontier {
	return &Frontier{db: db, filter: filter, runID: runID}
}

// Add canonicalizes urls, dedups, and enqueues survivors at depth.
func (f *Frontier) Add(urls []string, depth int) (int, error) {
	var fresh []string
	for _, raw := range urls {
		c, err := Canonicalize(raw)
		if err != nil {
			continue // fail loud elsewhere; never enqueue garbage
		}
		h := hashURL(c)
		if f.filter.Test(h) {
			seen, err := f.db.Seen(f.runID, h)
			if err != nil {
				return 0, err
			}
			if seen {
				continue
			}
		} else {
			f.filter.Add(h)
		}
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return 0, nil
	}
	return f.db.Enqueue(f.runID, fresh, depth) //nolint:wrapcheck // store error already contextual
}

// Claim delegates to the store.
func (f *Frontier) Claim(n int) ([]store.ClaimedURL, error) {
	return f.db.Claim(f.runID, n) //nolint:wrapcheck // store error already contextual
}

// ExtractLinks parses raw HTML once, resolves a[href] against pageURL, and
// enqueues links that pass the compiled Scope (host rule → prefix → globs →
// ext skip) at depth+1, bounded by maxDepth.
func (f *Frontier) ExtractLinks(rawHTML []byte, pageURL string, depth, maxDepth int, scope Scope) (int, error) {
	if depth+1 > maxDepth {
		return 0, nil
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return 0, nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(rawHTML))
	if err != nil {
		return 0, nil
	}
	seen := map[string]bool{}
	var out []string
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok || href == "" {
			return
		}
		ref, err := url.Parse(strings.TrimSpace(href))
		if err != nil {
			return
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" && abs.Scheme != "file" {
			return
		}
		// Match globs against the canonical form — the exact string that
		// gets enqueued — so utm stripping and query reordering are
		// visible to scope matching.
		c, err := Canonicalize(abs.String())
		if err != nil {
			return
		}
		cu, err := url.Parse(c)
		if err != nil {
			return
		}
		if !scope.Allows(base, cu) {
			return // silent non-enqueue: never fetched, never a page error
		}
		if SkipExtension(cu.Path) {
			return
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	})
	// Sort for deterministic claim order in tests.
	sort.Strings(out)
	return f.Add(out, depth+1)
}
