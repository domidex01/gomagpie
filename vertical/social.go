package vertical

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func init() {
	register(Extractor{
		Info: Info{
			Name:     "reddit",
			Label:    "Reddit",
			Desc:     "Post or subreddit via old.reddit server-rendered HTML, with a .json retry for comment permalinks.",
			Patterns: []string{"https://www.reddit.com/r/{sub}/comments/{id}/", "https://www.reddit.com/r/{sub}/"},
		},
		Match:   matchReddit,
		Extract: extractReddit,
	})
	register(Extractor{
		Info: Info{
			Name:     "hackernews",
			Label:    "Hacker News",
			Desc:     "Story or comment via the Algolia HN items API.",
			Patterns: []string{"https://news.ycombinator.com/item?id={n}"},
		},
		Match:   matchHN,
		Extract: extractHN,
	})
}

var redditHosts = []string{"reddit.com", "www.reddit.com", "old.reddit.com", "new.reddit.com"}

// matchReddit accepts canonical hosts with an /r/{sub} path. sh.reddit.com
// shortlinks and bare /r/ are out.
func matchReddit(u *url.URL) bool {
	if !hostIs(u, redditHosts...) {
		return false
	}
	segs := pathSegs(u.Path)
	return len(segs) >= 2 && segs[0] == "r" && segs[1] != ""
}

func isPermalink(u *url.URL) bool {
	segs := pathSegs(u.Path)
	return len(segs) >= 4 && segs[0] == "r" && segs[2] == "comments"
}

func extractReddit(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	old := "https://old.reddit.com" + u.RequestURI()
	body, err := fetchBytes(ctx, f, old)
	if err == nil {
		if isPermalink(u) {
			return redditPostFromHTML(body, "https://www.reddit.com"+u.RequestURI()), nil
		}
		return redditSubredditFromHTML(body, "https://www.reddit.com"+u.RequestURI()), nil
	}
	// .json retry fires ONLY for comment permalinks on HTML failure.
	if !isPermalink(u) {
		return nil, err
	}
	jbody, jerr := fetchBytes(ctx, f, old+".json")
	if jerr != nil {
		return nil, jerr
	}
	return redditPostFromJSON(jbody, "https://www.reddit.com"+u.RequestURI())
}

func redditPostFromHTML(body []byte, url string) map[string]any {
	doc, derr := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if derr != nil {
		return map[string]any{"kind": "post", "url": url}
	}
	title := strings.TrimSpace(doc.Find("a.title").First().Text())
	if title == "" {
		title = strings.TrimSpace(doc.Find("title").First().Text())
	}
	return map[string]any{
		"kind":     "post",
		"title":    title,
		"author":   strings.TrimSpace(doc.Find("a.author").First().Text()),
		"score":    parseCount(doc.Find("div.score").First().Text()),
		"comments": parseCount(doc.Find("a.comments, a.bylink").First().Text()),
		"selftext": strings.TrimSpace(doc.Find("div.usertext-body").First().Text()),
		"url":      url,
	}
}

func redditSubredditFromHTML(body []byte, url string) map[string]any {
	doc, derr := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if derr != nil {
		return map[string]any{"kind": "subreddit", "url": url}
	}
	title := strings.TrimSpace(doc.Find("title").First().Text())
	desc := strings.TrimSpace(doc.Find(".side .usertext-body").First().Text())
	if desc == "" {
		if c, ok := doc.Find("meta[name='description']").First().Attr("content"); ok {
			desc = strings.TrimSpace(c)
		}
	}
	return map[string]any{
		"kind":     "subreddit",
		"title":    title,
		"author":   "",
		"score":    parseCount(doc.Find(".subscribers .number, span.subscribers").First().Text()),
		"comments": float64(0),
		"selftext": desc,
		"url":      url,
	}
}

func redditPostFromJSON(body []byte, url string) (map[string]any, error) {
	first, err := firstJSONArray(body)
	if err != nil {
		return nil, fmt.Errorf("vertical: reddit .json: %w", err)
	}
	data0 := child(first, "data")
	kids, _ := data0["children"].([]any)
	if len(kids) == 0 {
		return nil, fmt.Errorf("vertical: reddit .json: no children")
	}
	d, _ := kids[0].(map[string]any)
	data, _ := d["data"].(map[string]any)
	if data == nil {
		return nil, fmt.Errorf("vertical: reddit .json: bad child shape")
	}
	return map[string]any{
		"kind":     "post",
		"title":    str(data, "title"),
		"author":   str(data, "author"),
		"score":    num(data, "score"),
		"comments": num(data, "num_comments"),
		"selftext": str(data, "selftext"),
		"url":      url,
	}, nil
}

// parseCount pulls the first integer from label text ("42 comments" → 42,
// "12.3k" → 12300, "•" → 0); never errors.
func parseCount(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(strings.ToLower(s)), ",", "")
	if s == "" || s == "•" {
		return 0
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9') && r != 'k' && r != 'm'
	})
	for _, tok := range fields {
		if tok == "" || tok == "." {
			continue
		}
		mult := 1.0
		if strings.HasSuffix(tok, "k") {
			mult, tok = 1000, strings.TrimSuffix(tok, "k")
		} else if strings.HasSuffix(tok, "m") {
			mult, tok = 1000000, strings.TrimSuffix(tok, "m")
		}
		if f, err := strconv.ParseFloat(tok, 64); err == nil {
			return f * mult
		}
	}
	return 0
}

func matchHN(u *url.URL) bool {
	return hostIs(u, "news.ycombinator.com") && strings.Trim(u.Path, "/") == "item" && u.Query().Get("id") != ""
}

func extractHN(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	id := u.Query().Get("id")
	m, err := fetchJSON(ctx, f, "https://hn.algolia.com/api/v1/items/"+id)
	if err != nil {
		return nil, err
	}
	kind := "story"
	if _, hasParent := m["parent_id"]; hasParent && m["parent_id"] != nil {
		kind = "comment"
	}
	link := str(m, "url")
	if link == "" {
		link = "https://news.ycombinator.com/item?id=" + id
	}
	var comments float64
	if kids, _ := m["children"].([]any); kids != nil {
		comments = float64(len(kids)) // top-level only, no recursion
	}
	out := map[string]any{
		"kind":     kind,
		"title":    str(m, "title"),
		"url":      link,
		"points":   num(m, "points"),
		"author":   str(m, "author"),
		"comments": comments,
		"text":     str(m, "text"),
	}
	if kind == "comment" {
		out["parent_id"] = num(m, "parent_id")
	}
	return out, nil
}
