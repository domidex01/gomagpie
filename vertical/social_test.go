package vertical_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"magpie/vertical"
)

func TestRedditMatch_Table(t *testing.T) {
	ex, _ := vertical.Lookup("reddit")
	yes := []string{
		"https://www.reddit.com/r/demo/comments/abc/demo_post/",
		"https://old.reddit.com/r/demo/comments/abc/demo_post/",
		"https://www.reddit.com/r/demo/",
		"https://new.reddit.com/r/demo/",
	}
	no := []string{
		"https://sh.reddit.com/abc",
		"https://www.reddit.com/r/",
		"https://www.reddit.com/",
		"https://example.com/r/demo/",
	}
	for _, raw := range yes {
		if !ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = true, want false", raw)
		}
	}
}

func TestRedditExtract_Post(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"old.reddit.com": {body: verticalFixture(t, "reddit-post.html")},
	}}
	ex, _ := vertical.Lookup("reddit")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/comments/abc123/demo_post/"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"kind": "post", "title": "Demo post title", "author": "testuser",
		"score": float64(123), "comments": float64(45),
		"selftext": "This is the selftext body of the demo post with enough words.",
		"url":      "https://www.reddit.com/r/demo/comments/abc123/demo_post/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestRedditExtract_Subreddit(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"old.reddit.com": {body: verticalFixture(t, "reddit-subreddit.html")},
	}}
	ex, _ := vertical.Lookup("reddit")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["kind"] != "subreddit" {
		t.Errorf("kind = %v, want subreddit", got["kind"])
	}
	if got["score"] != float64(9876) {
		t.Errorf("score = %v, want 9876 (comma-thousands)", got["score"])
	}
	if !strings.Contains(got["selftext"].(string), "demo subreddit community") {
		t.Errorf("selftext = %q, want community description", got["selftext"])
	}
}

// TestRedditExtract_FallbackJSON — REWRITTEN for Phase H (the one
// sanctioned edit, phase-H.md §4 H.2): permalinks now go .json-first,
// so success is EXACTLY 1 request ending .json, and failure falls back
// .json → HTML (2 requests, that order) with the old HTML summary shape.
func TestRedditExtract_FallbackJSON(t *testing.T) {
	t.Run("json-first success is a single request", func(t *testing.T) {
		fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
			".json$": {body: verticalFixture(t, "reddit-thread.json")},
		}}
		ex, _ := vertical.Lookup("reddit")
		got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/comments/abc123/x/"))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if got["title"] != "Demo thread title" || got["author"] != "threaduser" || got["score"] != float64(321) {
			t.Errorf("post fields = %#v", got)
		}
		comments, _ := got["comments"].([]any)
		if len(comments) != 2 {
			t.Fatalf("comments = %#v, want 2 top-level (more-object ignored)", comments)
		}
		reqs := fx.requests()
		if len(reqs) != 1 || !strings.HasSuffix(reqs[0], ".json") {
			t.Errorf("requests = %v, want exactly 1 request ending .json", reqs)
		}
	})

	t.Run("json failure falls back to html in that order", func(t *testing.T) {
		fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
			".json$": {status: 404, body: []byte("gone")},
			"x/$":    {body: verticalFixture(t, "reddit-post.html")},
		}}
		ex, _ := vertical.Lookup("reddit")
		got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/comments/abc123/x/"))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		// Old HTML summary shape: flat fields, comment COUNT (no tree).
		want := map[string]any{
			"kind": "post", "title": "Demo post title", "author": "testuser",
			"score": float64(123), "comments": float64(45),
			"selftext": "This is the selftext body of the demo post with enough words.",
			"url":      "https://www.reddit.com/r/demo/comments/abc123/x/",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v, want %#v", got, want)
		}
		reqs := fx.requests()
		if len(reqs) != 2 || !strings.HasSuffix(reqs[0], ".json") || strings.HasSuffix(reqs[1], ".json") {
			t.Errorf("requests = %v, want [.json, html] in that order", reqs)
		}
	})
}

// TestRedditExtract_CommentTree (fixture reddit-thread.json, recorded
// 2026-09-19 as a shape fixture): nested replies surface under comments[].replies.
func TestRedditExtract_CommentTree(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		".json$": {body: verticalFixture(t, "reddit-thread.json")},
	}}
	ex, _ := vertical.Lookup("reddit")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/comments/abc123/x/"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	comments, _ := got["comments"].([]any)
	if len(comments) != 2 {
		t.Fatalf("top-level = %d, want 2 (the more-object child is ignored)", len(comments))
	}
	alice, _ := comments[0].(map[string]any)
	if alice["author"] != "alice" || alice["score"] != float64(50) || alice["body"] != "Top comment body here." {
		t.Errorf("alice = %#v", alice)
	}
	replies, _ := alice["replies"].([]any)
	if len(replies) != 1 {
		t.Fatalf("alice replies = %#v, want 1 nested", replies)
	}
	bob, _ := replies[0].(map[string]any)
	if bob["author"] != "bob" || bob["body"] != "Nested reply body." {
		t.Errorf("bob = %#v", bob)
	}
	carol, _ := comments[1].(map[string]any)
	carolReplies, _ := carol["replies"].([]any)
	if len(carolReplies) != 0 {
		t.Errorf("carol replies = %#v, want empty array", carolReplies)
	}
}

// redditChainJSON builds a permalink .json body whose comment tree is
// one chain `depth` levels deep (boundary sweeps > the depth cap).
func redditChainJSON(t *testing.T, depth int) []byte {
	t.Helper()
	node := map[string]any{}
	for i := depth; i >= 1; i-- {
		node = map[string]any{
			"kind": "t1",
			"data": map[string]any{
				"author": fmt.Sprintf("u%d", i), "score": i, "body": "chain", "created_utc": float64(i),
				"replies": map[string]any{"kind": "Listing", "data": map[string]any{"children": wrapKids(node)}},
			},
		}
	}
	body := map[string]any{
		"kind": "Listing", "data": map[string]any{"children": []any{node}},
	}
	post := map[string]any{
		"kind": "Listing", "data": map[string]any{"children": []any{map[string]any{
			"kind": "t3", "data": map[string]any{"title": "chain", "author": "op", "score": 1.0, "num_comments": float64(depth), "selftext": ""},
		}}},
	}
	raw, err := json.Marshal([]any{post, body})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func wrapKids(node map[string]any) []any {
	if node == nil {
		return []any{}
	}
	return []any{node}
}

// redditFlatJSON builds a permalink .json body with n flat top-level comments.
func redditFlatJSON(t *testing.T, n int) []byte {
	t.Helper()
	kids := make([]any, n)
	for i := range kids {
		kids[i] = map[string]any{
			"kind": "t1",
			"data": map[string]any{"author": fmt.Sprintf("u%d", i), "score": 1.0, "body": "flat", "created_utc": 1.0, "replies": ""},
		}
	}
	raw, err := json.Marshal([]any{
		map[string]any{"kind": "Listing", "data": map[string]any{"children": []any{map[string]any{
			"kind": "t3", "data": map[string]any{"title": "flat", "author": "op", "score": 1.0, "num_comments": float64(n), "selftext": ""},
		}}}},
		map[string]any{"kind": "Listing", "data": map[string]any{"children": kids}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func treeDepth(comments []any) int {
	max := 0
	for _, c := range comments {
		m, _ := c.(map[string]any)
		replies, _ := m["replies"].([]any)
		if d := 1 + treeDepth(replies); d > max {
			max = d
		}
	}
	return max
}

// Caps are boundary arithmetic on generated shapes (a fixture shows one
// point; generated shapes sweep the exact 10 and 200 boundaries).
func TestRedditExtract_CommentCaps(t *testing.T) {
	ex, _ := vertical.Lookup("reddit")

	t.Run("depth cap", func(t *testing.T) {
		fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{".json$": {body: redditChainJSON(t, 15)}}}
		got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/d/comments/1/x/"))
		if err != nil {
			t.Fatal(err)
		}
		comments, _ := got["comments"].([]any)
		if d := treeDepth(comments); d > 10 {
			t.Errorf("tree depth = %d, want ≤ 10", d)
		}
	})

	t.Run("total cap", func(t *testing.T) {
		fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{".json$": {body: redditFlatJSON(t, 250)}}}
		got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/d/comments/2/x/"))
		if err != nil {
			t.Fatal(err)
		}
		comments, _ := got["comments"].([]any)
		if len(comments) != 200 {
			t.Errorf("comments = %d, want exactly 200", len(comments))
		}
	})
}

func TestRedditExtract_NoFallbackOffPermalink(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"old.reddit.com": {err: errors.New("connection reset")},
		".json":          {body: verticalFixture(t, "reddit-post.json")},
	}}
	ex, _ := vertical.Lookup("reddit")
	if _, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/")); err == nil {
		t.Error("subreddit HTML failure: want hard error, no .json retry")
	}
	if reqs := fx.requests(); len(reqs) != 1 {
		t.Errorf("requests = %v, want exactly 1 (no retry off-permalink)", reqs)
	}
}

func TestHackerNewsMatch_Table(t *testing.T) {
	ex, _ := vertical.Lookup("hackernews")
	yes := []string{
		"https://news.ycombinator.com/item?id=123",
		"https://news.ycombinator.com/item?id=999",
	}
	no := []string{
		"https://news.ycombinator.com/",
		"https://news.ycombinator.com/newest",
		"https://example.com/item?id=123",
	}
	for _, raw := range yes {
		if !ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = true, want false", raw)
		}
	}
}

func TestHackerNewsExtract_Story(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"hn.algolia.com": {body: verticalFixture(t, "hn-story.json")},
	}}
	ex, _ := vertical.Lookup("hackernews")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://news.ycombinator.com/item?id=12345"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"kind": "story", "title": "Demo HN story", "url": "https://example.com/demo",
		"points": float64(256), "author": "hnuser", "comments": float64(2), "text": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestHackerNewsExtract_Comment(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"hn.algolia.com": {body: verticalFixture(t, "hn-comment.json")},
	}}
	ex, _ := vertical.Lookup("hackernews")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://news.ycombinator.com/item?id=999"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"kind": "comment", "title": "", "url": "https://news.ycombinator.com/item?id=999",
		"points": float64(12), "author": "commenter", "comments": float64(0),
		"text": "This is a thoughtful comment body.", "parent_id": float64(12345),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}
