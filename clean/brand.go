package clean

import (
	"bytes"
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// BrandInfo is the zero-LLM brand surface: colors, fonts, and logo.
// Favicon and og:image stay owned by HarvestMetadata (metadata.go) —
// BrandPage fills those in; Brand itself never resolves URLs (pure,
// no page URL in, raw attribute values out).
type BrandInfo struct {
	Colors  []string `json:"colors"`
	Fonts   []string `json:"fonts"`
	Logo    string   `json:"logo,omitempty"`
	Favicon string   `json:"favicon,omitempty"`
}

var (
	hexColorRe   = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	fontFamilyRe = regexp.MustCompile(`(?i)font-family\s*:\s*([^;{}]+)`)
)

// Brand pulls brand signals from raw HTML: hex colors from <style> blocks
// and style attributes, font families from font-family declarations, and
// the logo by precedence img[src*=logo] → svg[class*=logo] (class or id)
// → "" (the og:image fallback lives in BrandPage, next to HarvestMetadata).
func Brand(html []byte) BrandInfo {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return BrandInfo{}
	}
	var css strings.Builder
	doc.Find("style").Each(func(_ int, s *goquery.Selection) {
		css.WriteString(s.Text())
		css.WriteString("\n")
	})
	doc.Find("[style]").Each(func(_ int, s *goquery.Selection) {
		css.WriteString(s.AttrOr("style", ""))
		css.WriteString("\n")
	})
	style := css.String()

	seen := map[string]bool{}
	var colors []string
	for _, m := range hexColorRe.FindAllString(style, -1) {
		c := strings.ToLower(m)
		if !seen[c] {
			seen[c] = true
			colors = append(colors, c)
		}
	}
	sort.Strings(colors)

	seenFonts := map[string]bool{}
	var fonts []string
	for _, m := range fontFamilyRe.FindAllStringSubmatch(style, -1) {
		for _, fam := range strings.Split(m[1], ",") {
			fam = strings.Trim(strings.TrimSpace(fam), `"'`)
			if fam == "" || seenFonts[fam] {
				continue
			}
			seenFonts[fam] = true
			fonts = append(fonts, fam)
		}
	}
	sort.Strings(fonts)

	info := BrandInfo{}
	if len(colors) > 0 {
		info.Colors = colors
	}
	if len(fonts) > 0 {
		info.Fonts = fonts
	}
	if src, ok := doc.Find(`img[src*="logo" i]`).First().Attr("src"); ok && strings.TrimSpace(src) != "" {
		info.Logo = strings.TrimSpace(src)
	} else if svg := doc.Find(`svg[class*="logo" i]`).First(); svg.Length() > 0 {
		// Inline SVG has no URL: report the hook an agent would select on.
		if class := strings.TrimSpace(svg.AttrOr("class", "")); class != "" {
			info.Logo = class
		} else if id := strings.TrimSpace(svg.AttrOr("id", "")); id != "" {
			info.Logo = "#" + id
		}
	}
	return info
}
