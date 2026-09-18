package clean_test

import (
	"encoding/json"
	"strings"
	"testing"

	"magpie/clean"
)

func TestMetadata_Article(t *testing.T) {
	// article.html declares lang + title only: assert what the fixture has.
	got := cleanFile(t, "article")
	m := got.Metadata
	if m.Lang != "en" {
		t.Errorf("Lang = %q, want en", m.Lang)
	}
	// WordCount drops link targets (see TestWordCount_Vectors), so it sits
	// below strings.Fields on link-bearing pages; lock the wiring instead.
	if m.WordCount != clean.WordCount(got.Markdown) {
		t.Errorf("WordCount = %d, want WordCount(markdown) = %d", m.WordCount, clean.WordCount(got.Markdown))
	}
	if m.WordCount <= 0 {
		t.Error("WordCount = 0 on article")
	}
}

func TestMetadata_FullTags(t *testing.T) {
	html := `<html lang="en"><head>` +
		`<meta name="description" content="A test page">` +
		`<meta name="author" content="Jane Doe">` +
		`<meta property="article:published_time" content="2026-01-02">` +
		`<meta property="og:site_name" content="Example">` +
		`<meta property="og:image" content="https://example.com/img.png">` +
		`<link rel="icon" href="/favicon.ico">` +
		`</head><body><p>` + strings.Repeat("word ", 10) + `</p></body></html>`
	m := clean.HarvestMetadata([]byte(html), "https://example.com/x", "md")
	if m.Description != "A test page" || m.Author != "Jane Doe" || m.Date != "2026-01-02" ||
		m.Lang != "en" || m.SiteName != "Example" || m.Image != "https://example.com/img.png" ||
		m.Favicon != "https://example.com/favicon.ico" {
		t.Errorf("full-tags metadata wrong: %+v", m)
	}
}

func TestMetadata_FaviconAbsolute(t *testing.T) {
	html := `<html><head><link rel="icon" href="/favicon.ico"></head><body><p>` +
		strings.Repeat("word ", 10) + `</p></body></html>`
	m := clean.HarvestMetadata([]byte(html), "https://example.com/article", "text")
	if m.Favicon != "https://example.com/favicon.ico" {
		t.Errorf("Favicon = %q, want absolute", m.Favicon)
	}
}

func TestMetadata_MissingTags(t *testing.T) {
	html := `<html><body><p>` + strings.Repeat("word ", 300) + `</p></body></html>`
	m := clean.HarvestMetadata([]byte(html), "https://example.com/x", "md")
	if m.Description != "" || m.Author != "" || m.Lang != "" {
		t.Errorf("missing-tags metadata not zero: %+v", m)
	}
}

func TestMetadata_Golden(t *testing.T) {
	got := cleanFile(t, "article")
	pretty, err := json.MarshalIndent(got.Metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldenDir(t, "llm", "metadata.json", string(pretty))
}
