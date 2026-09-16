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

func trafilaturaToMarkdown(_ context.Context, htmlStr, pageURL string) (string, error) {
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
		return "", fmt.Errorf("extract: %w", err)
	}
	if result == nil || result.ContentNode == nil {
		// Empty/SPA shell: fall back to raw-text markdown of the whole doc.
		return fallbackMarkdown(htmlStr), nil
	}
	frag := dom.OuterHTML(result.ContentNode)
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(table.WithSpanCellBehavior(table.SpanBehaviorMirror)),
	))
	md, err := conv.ConvertString(frag)
	if err != nil {
		return "", fmt.Errorf("markdown: %w", err)
	}
	md = strings.TrimSpace(md)
	if md == "" {
		return fallbackMarkdown(htmlStr), nil
	}
	return md, nil
}
