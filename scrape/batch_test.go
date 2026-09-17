package scrape_test

import (
	"context"
	"errors"
	"testing"

	"gomagpie/extract"
	"gomagpie/scrape"
)

// TestBatch_ConcurrentSharedDeps locks the Deps thread-safety contract:
// one Deps fanned out over concurrent Runs sharing the DB must return
// every record in input order with no item errors. Run with -race in CI.
func TestBatch_ConcurrentSharedDeps(t *testing.T) {
	origin := scrapeOrigin(t, scrapeHTML)
	urls := make([]string, 16)
	for i := range urls {
		urls[i] = origin + "/p" + string(rune('a'+i))
	}
	db := openScrapeDB(t)
	d := scrape.Deps{
		DB: db,
		ExtractorFor: func(provider, key, model string, sch *extract.Schema, runID string) (extract.Extractor, error) {
			return nil, errors.New("unreachable: nil schema never extracts")
		},
		APIKeyFor: func(provider string) string { return "" },
	}
	items, err := scrape.Batch(context.Background(), d, urls, scrape.BatchOptions{Concurrency: 8})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	for i, it := range items {
		if !it.OK {
			t.Errorf("item %d: not ok: %s", i, it.Error)
		}
		if it.URL != urls[i] {
			t.Errorf("item %d: order broke: got %s want %s", i, it.URL, urls[i])
		}
		if it.Markdown == "" {
			t.Errorf("item %d: empty markdown", i)
		}
	}
}
