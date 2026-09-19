package clean

import (
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// Inline-style vectors that hide text from sighted readers — the cheap,
// high-precision carriers of prompt-injection text. aria-hidden and
// computed styles are out of scope: icon fonts and legit decorative
// markup make them low-precision, and selector-level matching cannot see
// computed styles anyway.
// ponytail: this is a selector-level strip, not a CSS engine — white-on-
// white / off-screen positioning are the known ceiling; the upgrade path
// is a browser-computed-style pass, only if a real case appears.
var hiddenStyleRes = []*regexp.Regexp{
	regexp.MustCompile(`display\s*:\s*none`),
	regexp.MustCompile(`visibility\s*:\s*hidden`),
	regexp.MustCompile(`font-size\s*:\s*0`),
	regexp.MustCompile(`opacity\s*:\s*0(?:\.0+)?\s*(;|$)`),
}

// StripHidden removes elements whose inline style hides them (display:
// none, visibility:hidden, font-size:0, opacity:0), elements carrying
// the [hidden] attribute, and HTML comment nodes. Styles are matched on
// their lowercased text with precompiled regexps — no CSS parsing, no
// new dependency. Reports whether anything was removed (callers keep the
// original document untouched when it wasn't, so serialization never
// perturbs clean pages).
func StripHidden(doc *goquery.Document) bool {
	stripped := false
	doc.Find("[style]").Each(func(_ int, s *goquery.Selection) {
		style := strings.ToLower(s.AttrOr("style", ""))
		for _, re := range hiddenStyleRes {
			if re.MatchString(style) {
				s.Remove()
				stripped = true
				break // subtree gone with the node
			}
		}
	})
	doc.Find("[hidden]").Each(func(_ int, s *goquery.Selection) {
		s.Remove()
		stripped = true
	})
	// Comments: selectors match element nodes only (Find("comment") is a
	// no-op), so the walk is manual.
	for _, root := range doc.Nodes {
		if removeComments(root) {
			stripped = true
		}
	}
	return stripped
}

// StripHiddenHTML parses, strips, and re-serializes. When nothing was
// hidden the original string is returned untouched — byte-identical
// output for clean pages. Parse/serialize hiccups also return the
// original: a strip must never fail a page.
func StripHiddenHTML(htmlStr string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return htmlStr
	}
	if !StripHidden(doc) {
		return htmlStr
	}
	out, err := goquery.OuterHtml(doc.Children())
	if err != nil || strings.TrimSpace(out) == "" {
		return htmlStr
	}
	return out
}

// removeComments detaches comment nodes under n; reports whether any
// were removed.
func removeComments(n *html.Node) bool {
	removed := false
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.CommentNode {
			n.RemoveChild(c)
			removed = true
		} else {
			if removeComments(c) {
				removed = true
			}
		}
		c = next
	}
	return removed
}
