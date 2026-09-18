package vertical

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"magpie/clean"
)

func init() {
	register(Extractor{
		Info: Info{
			Name:     "youtube",
			Label:    "YouTube",
			Desc:     "Video metadata from the watch-page player response, with an oEmbed fallback (title/author only).",
			Patterns: []string{"https://www.youtube.com/watch?v={id}", "https://youtu.be/{id}"},
		},
		Match:   matchYouTube,
		Extract: extractYouTube,
	})
}

var youTubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// youTubeID normalizes the four watch URL shapes to an 11-char video ID.
func youTubeID(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	switch {
	case h == "youtu.be":
		return lastSegment(u.Path)
	case strings.HasSuffix(h, "youtube.com"):
		p := strings.Trim(u.Path, "/")
		switch {
		case p == "watch":
			return u.Query().Get("v")
		case strings.HasPrefix(p, "shorts/"):
			return strings.TrimPrefix(p, "shorts/")
		case strings.HasPrefix(p, "embed/"):
			return strings.TrimPrefix(p, "embed/")
		case strings.HasPrefix(p, "live/"):
			return strings.TrimPrefix(p, "live/")
		}
	}
	return ""
}

func matchYouTube(u *url.URL) bool {
	return youTubeIDRe.MatchString(youTubeID(u))
}

func extractYouTube(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	id := youTubeID(u)
	watchURL := "https://www.youtube.com/watch?v=" + id
	body, err := fetchBytes(ctx, f, watchURL)
	if err != nil {
		return nil, err
	}
	if m, ok := clean.PlayerResponseHTML(body); ok {
		if vd, _ := m["videoDetails"].(map[string]any); vd != nil {
			if title, _ := vd["title"].(string); strings.TrimSpace(title) != "" {
				return map[string]any{
					"title":       title,
					"author":      str(vd, "author"),
					"description": str(vd, "shortDescription"),
					"views":       num(vd, "viewCount"),
					"duration_s":  num(vd, "lengthSeconds"),
					"url":         watchURL,
				}, nil
			}
		}
	}
	// Player-parse failure: oEmbed fallback (title/author only, never an error).
	om, oerr := fetchJSON(ctx, f, "https://www.youtube.com/oembed?url="+url.QueryEscape(watchURL)+"&format=json")
	if oerr != nil {
		return nil, fmt.Errorf("vertical: youtube: no player data and oEmbed failed: %w", oerr)
	}
	return map[string]any{
		"title":       str(om, "title"),
		"author":      str(om, "author_name"),
		"description": "",
		"views":       float64(0),
		"duration_s":  float64(0),
		"url":         watchURL,
	}, nil
}
