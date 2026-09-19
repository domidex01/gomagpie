package vertical_test

import (
	"testing"

	"github.com/motherlodelab/magpie/vertical"
)

// TestOG_NeverAutoFires — H.7 gate 3 (same-commit pin with the
// extractor): og matches EVERYTHING, so it MUST be OptIn or every
// scrape would grow a Record and break the default output shape.
func TestOG_NeverAutoFires(t *testing.T) {
	if _, ok := vertical.MatchURL("https://example.com/anything"); ok {
		t.Fatal("MatchURL(example.com) hit — og must NEVER auto-fire (OptIn contract broken)")
	}
	ex, ok := vertical.Lookup("og")
	if !ok {
		t.Fatal("Lookup(og) = false")
	}
	if !ex.OptIn {
		t.Error("og.OptIn = false, want true")
	}
}

func TestOGExtract(t *testing.T) {
	// Fixture og.html (recorded 2026-09-19): synthetic OG/meta shape
	// fixture, not a live capture.
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"example.com/og-fixture": {body: verticalFixture(t, "og.html")},
	}}
	ex, _ := vertical.Lookup("og")
	got, err := ex.Extract(t.Context(), fx, mustURL(t, "https://example.com/og-fixture"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"og_title":       "OG Fixture Title",
		"og_description": "An OG description for the fixture page.",
		"og_image":       "https://img.example/cover.png",
		"og_url":         "https://example.com/og-fixture",
		"og_type":        "article",
		"twitter_card":   "summary_large_image",
		"twitter_title":  "OG Fixture Title",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if got["twitter_image"] != "https://img.example/card.png" {
		t.Errorf("twitter_image = %v", got["twitter_image"])
	}
	if got["twitter_description"] != "Twitter card description." {
		t.Errorf("twitter_description = %v", got["twitter_description"])
	}
	if got["title"] != "OG Fixture Title" {
		t.Errorf("title = %v", got["title"])
	}
	if got["description"] != "Meta description fallback text." {
		t.Errorf("description = %v", got["description"])
	}
	if got["canonical"] != "https://example.com/canonical" {
		t.Errorf("canonical = %v", got["canonical"])
	}
	if got["h1"] != "Fixture H1" {
		t.Errorf("h1 = %v", got["h1"])
	}
}
