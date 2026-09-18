package vertical

import (
	"context"
	"fmt"
	"net/url"
)

func init() {
	register(Extractor{
		Info: Info{
			Name:     "stackoverflow",
			Label:    "Stack Overflow",
			Desc:     "Question + top-voted answers via the Stack Exchange API (bodies stay raw HTML; consumers clean).",
			Patterns: []string{"https://stackoverflow.com/questions/{id}/{slug}"},
		},
		Match:   matchStackOverflow,
		Extract: extractStackOverflow,
	})
}

// matchStackOverflow accepts canonical hosts with /questions/{id}(/slug)?
// ponytail: the rest of the SE network (serverfault, superuser…) is out
// of scope — one site, one API `site=` parameter.
func matchStackOverflow(u *url.URL) bool {
	if !hostIs(u, "stackoverflow.com", "www.stackoverflow.com") {
		return false
	}
	segs := pathSegs(u.Path)
	return len(segs) >= 2 && segs[0] == "questions" && segs[1] != ""
}

func extractStackOverflow(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	id := pathSegs(u.Path)[1]
	q, err := fetchJSON(ctx, f, fmt.Sprintf("https://api.stackexchange.com/2.3/questions/%s?site=stackoverflow&filter=withbody", id))
	if err != nil {
		return nil, err
	}
	items, _ := q["items"].([]any)
	if len(items) == 0 {
		return nil, fmt.Errorf("vertical: stackoverflow: no question for id %s", id)
	}
	qi := anyMap(items[0])
	a, err := fetchJSON(ctx, f, fmt.Sprintf("https://api.stackexchange.com/2.3/questions/%s/answers?site=stackoverflow&filter=withbody&sort=votes&pagesize=10", id))
	if err != nil {
		return nil, err
	}
	aitems, _ := a["items"].([]any)
	answers := make([]any, 0, len(aitems))
	for _, it := range aitems {
		ai := anyMap(it)
		if ai == nil {
			continue
		}
		accepted, _ := ai["is_accepted"].(bool)
		answers = append(answers, map[string]any{
			"score":       num(ai, "score"),
			"body":        str(ai, "body"),
			"is_accepted": accepted,
		})
	}
	return map[string]any{
		"kind":    "question",
		"title":   str(qi, "title"),
		"score":   num(qi, "score"),
		"tags":    qi["tags"],
		"body":    str(qi, "body"),
		"owner":   str(child(qi, "owner"), "display_name"),
		"answers": answers,
	}, nil
}
