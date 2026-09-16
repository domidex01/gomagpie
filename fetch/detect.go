package fetch

import (
	"bytes"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// ScoreJSRequired implements the spec §1.2 heuristic over raw HTML + headers.
// Returns (score, hasEmbeddedData). Embedded __NEXT_DATA__/__NUXT__ JSON means
// the caller harvests inline and skips the browser entirely.
func ScoreJSRequired(page []byte, headers http.Header) (score int, hasEmbeddedData bool) {
	s := string(page)
	lower := strings.ToLower(s)

	if strings.Contains(s, `id="__NEXT_DATA__"`) || strings.Contains(s, "window.__NUXT__") ||
		strings.Contains(s, `id="__NUXT__"`) {
		hasEmbeddedData = true
	}

	visible := visibleTextLength(page)

	// Visible-text length after boilerplate strip: < 200 chars → +2.
	if visible < 200 {
		score += 2
	}
	// Script-bytes to text-bytes ratio > 3.0 → +1.
	scriptBytes := scriptBytes(page)
	denom := visible
	if denom < 1 {
		denom = 1
	}
	if float64(scriptBytes)/float64(denom) > 3.0 {
		score++
	}
	// SPA mount node with little else → +2.
	if hasSPAMount(lower) && visible < 500 {
		score += 2
	}
	// noscript enable/requires javascript → +1.
	if strings.Contains(lower, "enable javascript") || strings.Contains(lower, "requires javascript") {
		score++
	}
	// < 3 non-anchor <a href> targets → +1.
	if countLinks(page) < 3 {
		score++
	}
	// Raw HTML < 5KB but ≥ 10 <script src> → +1.
	if len(page) < 5*1024 && strings.Count(lower, "<script") >= 10 {
		score++
	}
	// Legacy AJAX-crawl marker → +1.
	if strings.Contains(lower, `name="fragment"`) {
		score++
	}
	return score, hasEmbeddedData
}

// NeedsBrowser escalates iff score >= 2.
func NeedsBrowser(score int) bool { return score >= 2 }

func hasSPAMount(lower string) bool {
	for _, m := range []string{
		`id="root"`, `id="app"`, "data-reactroot", "__react_devtools_global_hook__",
		"data-v-", "__vue__", "<app-root", "ng-version",
		`id="__next"`, `id="__nuxt"`, `id="svelte"`, "__sveltekit_",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func visibleTextLength(page []byte) int {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return len(bytes.TrimSpace(page))
	}
	var n int
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.TextNode && nd.Parent != nil &&
			nd.Parent.Data != "script" && nd.Parent.Data != "style" {
			n += len([]rune(strings.TrimSpace(nd.Data)))
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return n
}

func scriptBytes(page []byte) int {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return 0
	}
	var n int
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.ElementNode && nd.Data == "script" {
			for c := nd.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					n += len(c.Data)
				}
			}
			for _, a := range nd.Attr {
				if a.Key == "src" {
					n += len(a.Val)
				}
			}
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return n
}

func countLinks(page []byte) int {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return 99
	}
	n := 0
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.ElementNode && nd.Data == "a" {
			for _, a := range nd.Attr {
				if a.Key == "href" && a.Val != "" && !strings.HasPrefix(a.Val, "#") {
					n++
				}
			}
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return n
}
