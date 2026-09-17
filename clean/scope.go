package clean

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/go-shiori/dom"
)

// Scope selects subtrees of the raw HTML before trafilatura runs.
// Empty Scope is the identity (today's behavior, zero-cost).
// OnlyMainContent is a no-op marker: trafilatura already extracts main
// content, so the flag documents intent and keeps CLI/MCP parity.
type Scope struct {
	Include         []string
	Exclude         []string
	OnlyMainContent bool
}

// ApplyScope is a goquery pre-pass on raw HTML: include selects the union
// of matched subtrees (exclude wins), exclude removes matched nodes.
// Invalid selectors warn-and-skip via cascadia.Compile (goquery.Find
// returns zero nodes for both invalid and no-match, so without the
// pre-check the invalid-selector case is untestable). warn may be nil.
func ApplyScope(html string, s Scope, warn func(string)) string {
	if warn == nil {
		warn = func(string) {}
	}
	if len(s.Include)+len(s.Exclude) == 0 {
		return html
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		warn(fmt.Sprintf("scope: parse: %v", err))
		return html
	}
	includes := validSelectors(s.Include, warn)
	excludes := validSelectors(s.Exclude, warn)
	if len(includes) == 0 && len(excludes) == 0 {
		// All selectors invalid: fail open with full content.
		return html
	}
	if len(includes) > 0 {
		var parts []string
		matched := false
		for _, sel := range includes {
			doc.Find(sel).Each(func(_ int, n *goquery.Selection) {
				for _, node := range n.Nodes {
					parts = append(parts, dom.OuterHTML(node))
					matched = true
				}
			})
		}
		if !matched {
			warn("scope: include selectors matched nothing")
			return ""
		}
		joined := strings.Join(parts, "\n")
		sub, serr := goquery.NewDocumentFromReader(strings.NewReader(joined))
		if serr != nil {
			warn(fmt.Sprintf("scope: reparse: %v", serr))
			return html
		}
		doc = sub
	}
	for _, sel := range excludes {
		doc.Find(sel).Each(func(_ int, n *goquery.Selection) {
			n.Remove()
		})
	}
	out, oerr := doc.Html()
	if oerr != nil {
		warn(fmt.Sprintf("scope: render: %v", oerr))
		return html
	}
	return out
}

// validSelectors compiles each selector, warning and dropping the invalid
// ones, and truncates each list at 100 with one warning.
func validSelectors(sels []string, warn func(string)) []string {
	if len(sels) > 100 {
		warn(fmt.Sprintf("scope: %d selectors, truncated to 100", len(sels)))
		sels = sels[:100]
	}
	var out []string
	for _, sel := range sels {
		if _, err := cascadia.Compile(sel); err != nil {
			warn(fmt.Sprintf("invalid selector %q, skipped", sel))
			continue
		}
		out = append(out, sel)
	}
	return out
}
