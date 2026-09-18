package vertical_test

import (
	"context"
	"errors"
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

func TestRedditExtract_FallbackJSON(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"x/$":   {err: errors.New("connection reset")},
		".json": {body: verticalFixture(t, "reddit-post.json")},
	}}
	ex, _ := vertical.Lookup("reddit")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.reddit.com/r/demo/comments/abc123/x/"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"kind": "post", "title": "JSON fallback title", "author": "jsonuser",
		"score": float64(77), "comments": float64(12),
		"selftext": "Fallback selftext body content here for the test.",
		"url":      "https://www.reddit.com/r/demo/comments/abc123/x/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	reqs := fx.requests()
	if len(reqs) != 2 || !strings.HasSuffix(reqs[1], ".json") {
		t.Errorf("requests = %v, want 2-request HTML-then-.json shape", reqs)
	}
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
