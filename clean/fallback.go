package clean

import (
	"bytes"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// fallbackMarkdown renders headings/paragraphs/lists/tables text when
// trafilatura finds no main content (e.g. SPA shells).
func fallbackMarkdown(htmlStr string) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(htmlStr)))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	doc.Find("h1,h2,h3,p,li,a").Each(func(_ int, s *goquery.Selection) {
		t := strings.TrimSpace(s.Text())
		if t == "" {
			return
		}
		tag := goquery.NodeName(s)
		switch tag {
		case "h1":
			sb.WriteString("# " + t + "\n\n")
		case "h2":
			sb.WriteString("## " + t + "\n\n")
		case "h3":
			sb.WriteString("### " + t + "\n\n")
		case "li":
			sb.WriteString("- " + t + "\n")
		default:
			sb.WriteString(t + "\n\n")
		}
	})
	// Preserve at least table text for GFM-table fixtures.
	doc.Find("table").Each(func(_ int, t *goquery.Selection) {
		t.Find("tr").Each(func(_ int, tr *goquery.Selection) {
			var cells []string
			tr.Find("th,td").Each(func(_ int, c *goquery.Selection) {
				cells = append(cells, strings.TrimSpace(c.Text()))
			})
			if len(cells) > 0 {
				sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
			}
		})
		sb.WriteString("\n")
	})
	return strings.TrimSpace(sb.String())
}
