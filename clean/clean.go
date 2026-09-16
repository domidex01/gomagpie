package clean

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// RawPage is the fetch output handed to Clean.
type RawPage struct {
	HTML          []byte
	URL           string
	FinalURL      string
	StructuredRaw json.RawMessage // pre-harvested sidecar (optional)
}

// CleanedPage is the cleaning output.
type CleanedPage struct {
	Markdown       string          `json:"markdown"`
	StructuredData json.RawMessage `json:"structured_data,omitempty"`
	Title          string          `json:"title"`
	FinalURL       string          `json:"final_url"`
}

// Cleaner cleans one page.
type Cleaner interface {
	Clean(ctx context.Context, raw RawPage) (CleanedPage, error)
}

// MaxTokens caps markdown fed to the LLM (sidecar always sent in full).
const MaxTokens = 8000

// Clean harvests the sidecar first, then trafilatura, then markdown.
func Clean(ctx context.Context, raw RawPage) (CleanedPage, error) {
	htmlStr := string(raw.HTML)
	sidecar := HarvestSidecar(raw.HTML)
	title := extractTitle(raw.HTML)
	md, err := trafilaturaToMarkdown(ctx, htmlStr, raw.FinalURL)
	if err != nil {
		return CleanedPage{}, fmt.Errorf("clean: %w", err)
	}
	md = capTokens(md, MaxTokens)
	return CleanedPage{
		Markdown:       md,
		StructuredData: sidecar,
		Title:          title,
		FinalURL:       raw.FinalURL,
	}, nil
}

func extractTitle(page []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Find("title").First().Text())
}

var nuxtRe = regexp.MustCompile(`window\.__NUXT__\s*=\s*(\{.*?\});?\s*</script>`)

// HarvestSidecar pulls JSON-LD + __NEXT_DATA__ + __NUXT__ verbatim,
// before trafilatura strips scripts.
func HarvestSidecar(page []byte) json.RawMessage {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil
	}
	var blocks []json.RawMessage
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		t := strings.TrimSpace(s.Text())
		if t != "" && json.Valid([]byte(t)) {
			blocks = append(blocks, json.RawMessage(t))
		}
	})
	if next := strings.TrimSpace(doc.Find("#__NEXT_DATA__").First().Text()); next != "" {
		if json.Valid([]byte(next)) {
			blocks = append(blocks, json.RawMessage(next))
		}
	}
	if m := nuxtRe.FindSubmatch(page); m != nil {
		if json.Valid(m[1]) {
			c := append([]byte(nil), m[1]...)
			blocks = append(blocks, c)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	if len(blocks) == 1 {
		return blocks[0]
	}
	joined, err := json.Marshal(blocks)
	if err != nil {
		return blocks[0] // elements are pre-validated JSON; unreachable
	}
	return joined
}

// capTokens is a naive top-down fill: ponytail: count ≈ len/4 chars
// (no tokenizer dep; ceiling = ±20% budget error — irrelevant at 8k scale).
func capTokens(md string, maxTokens int) string {
	if len(md)/4 <= maxTokens {
		return md
	}
	cut := maxTokens * 4
	// Avoid splitting mid-line.
	if idx := strings.LastIndex(md[:cut], "\n"); idx > cut/2 {
		cut = idx
	}
	return md[:cut]
}
