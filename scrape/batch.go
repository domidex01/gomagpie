package scrape

import (
	"context"
	"fmt"

	"magpie/clean"

	"golang.org/x/sync/errgroup"
)

// MaxBatchURLs bounds one batch call: collect-then-write holds every
// result, so the cap is the memory bound.
const MaxBatchURLs = 100

// DefaultBatchConcurrency matches crawl's --concurrency default.
const DefaultBatchConcurrency = 8

// BatchOptions configures a batch scrape: markdown-only runs sharing one
// scope/profile, bounded by Concurrency.
type BatchOptions struct {
	Concurrency int
	Render      string
	Profile     string
	Browser     string // TLS fingerprint: chrome|firefox|random ("" = stock)
	Cookies     string
	Lang        string // verbatim Accept-Language override
	Scope       clean.Scope
}

// BatchItem is one per-URL record: errors are data (ok:false + error
// string), never a whole-batch abort.
type BatchItem struct {
	URL      string `json:"url"`
	OK       bool   `json:"ok"`
	Title    string `json:"title,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Batch scrapes every URL with a bounded errgroup fan-out over Run (nil
// schema: markdown-only, zero LLM) and returns records in input order.
// Input validation fails pre-I/O, before any fetch.
func Batch(ctx context.Context, d Deps, urls []string, o BatchOptions) ([]BatchItem, error) {
	if len(urls) > MaxBatchURLs {
		return nil, fmt.Errorf("scrape: batch of %d URLs exceeds max %d", len(urls), MaxBatchURLs)
	}
	if o.Concurrency <= 0 {
		return nil, fmt.Errorf("scrape: batch concurrency %d must be positive", o.Concurrency)
	}
	out := make([]BatchItem, len(urls))
	var g errgroup.Group
	g.SetLimit(o.Concurrency)
	for i, u := range urls {
		g.Go(func() error {
			res, err := Run(ctx, d, u, Options{
				Render: o.Render, Profile: o.Profile, Browser: o.Browser, Cookies: o.Cookies, Lang: o.Lang, Scope: o.Scope,
			})
			if err != nil {
				out[i] = BatchItem{URL: u, Error: err.Error()}
				return nil
			}
			out[i] = BatchItem{URL: u, OK: true, Title: res.Title, Markdown: res.Markdown}
			return nil
		})
	}
	_ = g.Wait() //nolint:errcheck // item errors are data in out[i]; Wait only reports group misuse
	return out, nil
}
