package clean

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Metadata rides on CleanedPage (all fields omitempty).
type Metadata struct {
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	Date        string `json:"date,omitempty"`
	Lang        string `json:"lang,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Image       string `json:"image,omitempty"`
	Favicon     string `json:"favicon,omitempty"`
	WordCount   int    `json:"word_count,omitempty"`
}

// HarvestMetadata pulls meta/OG/time/link-icon fields from raw HTML.
// pageURL resolves a relative favicon to absolute; markdown feeds WordCount.
func HarvestMetadata(html []byte, pageURL string, markdown string) Metadata {
	var m Metadata
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		m.WordCount = WordCount(markdown)
		return m
	}
	meta := func(attr, key string) string {
		var v string
		doc.Find("meta").EachWithBreak(func(_ int, s *goquery.Selection) bool {
			if strings.EqualFold(s.AttrOr(attr, ""), key) {
				if c, ok := s.Attr("content"); ok && strings.TrimSpace(c) != "" {
					v = strings.TrimSpace(c)
					return false
				}
			}
			return true
		})
		return v
	}
	m.Description = meta("name", "description")
	if m.Description == "" {
		m.Description = meta("property", "og:description")
	}
	m.Author = meta("name", "author")
	m.Date = meta("property", "article:published_time")
	if m.Date == "" {
		if dt, ok := doc.Find("time[datetime]").First().Attr("datetime"); ok {
			m.Date = strings.TrimSpace(dt)
		}
	}
	if lang, ok := doc.Find("html").First().Attr("lang"); ok {
		m.Lang = strings.TrimSpace(lang)
	}
	m.SiteName = meta("property", "og:site_name")
	m.Image = meta("property", "og:image")
	if href, ok := doc.Find(`link[rel~="icon"]`).First().Attr("href"); ok && strings.TrimSpace(href) != "" {
		m.Favicon = resolveURL(pageURL, strings.TrimSpace(href))
	}
	if m.Author == "" {
		m.Author = sidecarAuthor(HarvestSidecar(html))
	}
	m.WordCount = WordCount(markdown)
	return m
}

// sidecarAuthor falls back to a JSON-LD author name when no meta tag exists.
func sidecarAuthor(sidecar json.RawMessage) string {
	if len(sidecar) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(sidecar, &v); err != nil {
		return ""
	}
	var walk func(x any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case map[string]any:
			if a, ok := t["author"]; ok {
				switch a := a.(type) {
				case string:
					return strings.TrimSpace(a)
				case map[string]any:
					if n, ok := a["name"].(string); ok {
						return strings.TrimSpace(n)
					}
				case []any:
					for _, e := range a {
						if s := walk(e); s != "" {
							return s
						}
					}
				}
			}
			for _, c := range t {
				if s := walk(c); s != "" {
					return s
				}
			}
		case []any:
			for _, e := range t {
				if s := walk(e); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

func resolveURL(base, href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if u.IsAbs() {
		return href
	}
	b, err := url.Parse(base)
	if err != nil {
		return href
	}
	return b.ResolveReference(u).String()
}
