package clean

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/go-shiori/dom"
	"github.com/markusmobius/go-trafilatura/v2"
)

// trafilaturaToMarkdown returns the markdown, whether the SPA-shell fallback
// ran (trafilatura found no main content), and any error.
func trafilaturaToMarkdown(_ context.Context, htmlStr, pageURL string) (string, bool, error) {
	var parsed *url.URL
	if pageURL != "" {
		if u, err := url.Parse(pageURL); err == nil {
			parsed = u
		}
	}
	result, err := trafilatura.Extract(strings.NewReader(htmlStr), trafilatura.Options{
		ExcludeComments: true,
		IncludeImages:   true,
		IncludeLinks:    true,
		EnableFallback:  true,
		OriginalURL:     parsed,
	})
	if err != nil {
		return "", false, fmt.Errorf("extract: %w", err)
	}
	if result == nil || result.ContentNode == nil {
		// Empty/SPA shell: fall back to raw-text markdown of the whole doc.
		return fallbackMarkdown(htmlStr), true, nil
	}
	frag := dom.OuterHTML(result.ContentNode)
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(table.WithSpanCellBehavior(table.SpanBehaviorMirror)),
	))
	md, err := conv.ConvertString(frag)
	if err != nil {
		return "", false, fmt.Errorf("markdown: %w", err)
	}
	md = strings.TrimSpace(md)
	if md == "" {
		return fallbackMarkdown(htmlStr), true, nil
	}
	return md, false, nil
}
