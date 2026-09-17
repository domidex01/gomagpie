package vertical

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

func init() {
	register(Extractor{
		Info: Info{
			Name:     "arxiv",
			Label:    "arXiv",
			Desc:     "Paper metadata via the arXiv export API (Atom, stdlib encoding/xml).",
			Patterns: []string{"https://arxiv.org/abs/{id}", "https://arxiv.org/pdf/{id}"},
		},
		Match:   matchArxiv,
		Extract: extractArxiv,
	})
}

var arxivVersionRe = regexp.MustCompile(`v\d+$`)

func matchArxiv(u *url.URL) bool {
	if !hostIs(u, "arxiv.org", "www.arxiv.org", "export.arxiv.org") {
		return false
	}
	// New-style IDs only (2401.12345); legacy hep-th/9901001 paths don't match.
	segs := pathSegs(u.Path)
	return len(segs) == 2 && (segs[0] == "abs" || segs[0] == "pdf") && segs[1] != ""
}

type arxivFeed struct {
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	Title     string `xml:"title"`
	Summary   string `xml:"summary"`
	Published string `xml:"published"`
	Authors   []struct {
		Name string `xml:"name"`
	} `xml:"author"`
}

func extractArxiv(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	segs := pathSegs(u.Path)
	fullID := strings.TrimSuffix(segs[1], ".pdf")
	queryID := arxivVersionRe.ReplaceAllString(fullID, "")
	body, err := fetchBytes(ctx, f, "https://export.arxiv.org/api/query?id_list="+queryID)
	if err != nil {
		return nil, err
	}
	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("vertical: arxiv decode: %w", err)
	}
	if len(feed.Entries) == 0 {
		return nil, fmt.Errorf("vertical: arxiv: no entries for %s", fullID)
	}
	first := feed.Entries[0] // first entry only
	authors := make([]any, 0, len(first.Authors))
	for _, a := range first.Authors {
		if n := strings.TrimSpace(a.Name); n != "" {
			authors = append(authors, n)
		}
	}
	return map[string]any{
		"title":     strings.TrimSpace(first.Title),
		"authors":   authors,
		"summary":   strings.TrimSpace(first.Summary),
		"published": strings.TrimSpace(first.Published),
		"url":       "https://arxiv.org/abs/" + fullID,
	}, nil
}
