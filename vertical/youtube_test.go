package vertical_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/motherlodelab/magpie/vertical"
)

func TestYouTubeMatch_Table(t *testing.T) {
	ex, _ := vertical.Lookup("youtube")
	id := "dQw4w9WgXcQ"
	yes := []string{
		"https://www.youtube.com/watch?v=" + id,
		"https://youtu.be/" + id,
		"https://www.youtube.com/shorts/" + id,
		"https://www.youtube.com/embed/" + id,
	}
	no := []string{
		"https://www.youtube.com/",
		"https://www.youtube.com/feed/trending",
		"https://www.youtube.com/watch?v=short",
		"https://example.com/watch?v=" + id,
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

func TestYouTubeExtract_Blob(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"youtube.com/watch": {body: verticalFixture(t, "youtube-watch.html")},
	}}
	ex, _ := vertical.Lookup("youtube")
	for _, raw := range []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
	} {
		got, err := ex.Extract(context.Background(), fx, mustURL(t, raw))
		if err != nil {
			t.Fatalf("Extract(%s): %v", raw, err)
		}
		want := map[string]any{
			"title": "Demo Video Title", "author": "DemoChannel",
			"description": "This is the demo video description with enough detail.",
			"views":       float64(1234567), "duration_s": float64(372),
			"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Extract(%s) = %#v, want %#v", raw, got, want)
		}
	}
}

func TestYouTubeExtract_GarbageNumbers(t *testing.T) {
	html := `<html><body><script>var ytInitialPlayerResponse = {"videoDetails": {"title": "Garbage Numbers Video Title Here", "author": "Ch", "shortDescription": "desc", "viewCount": "12ab", "lengthSeconds": "xyz"}};</script></body></html>`
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"youtube.com/watch": {body: []byte(html)},
	}}
	ex, _ := vertical.Lookup("youtube")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["views"] != float64(0) || got["duration_s"] != float64(0) {
		t.Errorf("garbage numbers = (%v, %v), want (0, 0) with no error", got["views"], got["duration_s"])
	}
}

func TestYouTubeExtract_OEmbedFallback(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"youtube.com/watch":  {body: []byte(`<html><head><title>nothing here</title></head><body><p>no player blob</p></body></html>`)},
		"youtube.com/oembed": {body: verticalFixture(t, "youtube-oembed.json")},
	}}
	ex, _ := vertical.Lookup("youtube")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"title": "OEmbed Demo Title", "author": "OEmbedChannel",
		"description": "", "views": float64(0), "duration_s": float64(0),
		"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestYouTubeExtract_NoDataError(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"youtube.com/watch":  {body: []byte(`<html><body><p>no blob</p></body></html>`)},
		"youtube.com/oembed": {status: 404, body: []byte("nope")},
	}}
	ex, _ := vertical.Lookup("youtube")
	if _, err := ex.Extract(context.Background(), fx, mustURL(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ")); err == nil {
		t.Error("no blob + oEmbed 404: want hard error")
	}
	if reqs := fx.requests(); len(reqs) != 2 || !strings.Contains(reqs[1], "oembed") {
		t.Errorf("requests = %v, want [watch, oembed]", reqs)
	}
}
