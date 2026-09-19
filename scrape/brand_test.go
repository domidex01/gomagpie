package scrape_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/domidex01/magpie/clean"
	"github.com/domidex01/magpie/extract"
	"github.com/domidex01/magpie/scrape"
)

func brandFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "brand", name+".html"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBrandPage_MatchesPure(t *testing.T) {
	html := brandFixtureBytes(t, "shop")
	db := openScrapeDB(t)
	deps := scrape.Deps{
		DB: db,
		Fetcher: &fakeVerticalFetcher{bodies: map[string]fakeResp{
			"example.com/shop": {body: html},
		}},
		ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
			t.Error("ExtractorFor called on zero-LLM brand path")
			return nil, errors.New("must not be called")
		},
		APIKeyFor: func(string) string { return "" },
	}
	res, err := scrape.BrandPage(context.Background(), deps, "https://example.com/shop")
	if err != nil {
		t.Fatalf("BrandPage: %v", err)
	}
	// Same BrandInfo as the pure function (seam wiring, not logic)...
	want := clean.Brand(html)
	// ...plus the favicon/og:image HarvestMetadata owns.
	meta := clean.HarvestMetadata(html, res.FinalURL, "")
	want.Favicon = meta.Favicon
	if !reflect.DeepEqual(res.BrandInfo, want) {
		t.Errorf("BrandPage info = %+v, want pure %+v", res.BrandInfo, want)
	}
	if res.Title == "" {
		t.Error("BrandPage title empty")
	}
}

func TestBrandPage_FetchError(t *testing.T) {
	db := openScrapeDB(t)
	deps := scrape.Deps{
		DB:      db,
		Fetcher: &fakeVerticalFetcher{bodies: map[string]fakeResp{}},
		ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
			return nil, errors.New("must not be called")
		},
		APIKeyFor: func(string) string { return "" },
	}
	if _, err := scrape.BrandPage(context.Background(), deps, "https://example.com/nope"); err == nil {
		t.Error("fetch error: want loud failure")
	}
}

func TestBrandPage_QualityBlocked(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "quality", "challenge-akamai.html"))
	if err != nil {
		t.Fatal(err)
	}
	db := openScrapeDB(t)
	deps := scrape.Deps{
		DB: db,
		Fetcher: &fakeVerticalFetcher{bodies: map[string]fakeResp{
			"example.com/blocked": {status: 403, body: raw},
		}},
		ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
			t.Error("ExtractorFor called on quality-blocked brand page")
			return nil, errors.New("must not be called")
		},
		APIKeyFor: func(string) string { return "" },
	}
	_, err = scrape.BrandPage(context.Background(), deps, "https://example.com/blocked")
	var qerr *clean.QualityError
	if !errors.As(err, &qerr) {
		t.Fatalf("err = %v, want *clean.QualityError", err)
	}
	if !strings.Contains(err.Error(), "access-denied") {
		t.Errorf("err = %v, want typed issue", err)
	}
}
