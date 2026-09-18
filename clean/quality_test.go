package clean_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"magpie/clean"
)

func qualityFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "quality", name+".html"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func richMD(t *testing.T) string {
	t.Helper()
	got, err := clean.Clean(t.Context(), clean.RawPage{
		HTML: qualityFixture(t, "rich-with-marker"), FinalURL: "https://example.com/rich",
	})
	if err != nil {
		t.Fatalf("Clean rich: %v", err)
	}
	if clean.WordCount(got.Markdown) < 200 {
		t.Fatalf("rich fixture only %d words, want ≥200", clean.WordCount(got.Markdown))
	}
	return got.Markdown
}

func TestClassify_Table(t *testing.T) {
	rich := qualityFixture(t, "rich-with-marker")
	richMarkdown := richMD(t)
	cases := []struct {
		name   string
		md     string
		status int
		body   []byte
		want   clean.Issue
	}{
		{"akamai 403 thin", "# Blocked\n\nJust a moment.", 403, qualityFixture(t, "challenge-akamai"), clean.IssueAccessDenied},
		{"login wall 401", "# Sign in\n\nLog in to continue.", 401, qualityFixture(t, "login-wall"), clean.IssueLoginRequired},
		{"unavailable 503", "# Down\n\nTry later.", 503, []byte("<html><body>down</body></html>"), clean.IssueUnavailable},
		{"empty spa shell", "# App\n\nLoading.", 200, qualityFixture(t, "empty-shell"), clean.IssueEmpty},
		{"rich article mentioning Just a moment", richMarkdown, 200, rich, clean.IssueNone},
		{"rich article served 403 is still fine", richMarkdown, 403, rich, clean.IssueNone},
		{"clean 200 article", "# News\n\n" + strings.Repeat("honest prose ", 60), 200, []byte("<html></html>"), clean.IssueNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clean.Classify(clean.CleanedPage{Markdown: c.md}, c.status, c.body, false)
			if got != c.want {
				t.Errorf("Classify() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClean_AttachesQualityWithoutFailing(t *testing.T) {
	raw := qualityFixture(t, "challenge-akamai")
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: raw, StatusCode: 403, FinalURL: "https://example.com/waf"})
	if err != nil {
		t.Fatalf("Clean must not fail on quality input: %v", err)
	}
	if got.Quality == "" {
		t.Error("Quality empty on a 403 challenge page — gate can never fire downstream")
	}
	wrapped := fmt.Errorf("scrape: quality blocked (%s): %w", got.Quality, clean.ErrQuality)
	if !errors.Is(wrapped, clean.ErrQuality) {
		t.Errorf("Quality %q does not wrap ErrQuality", got.Quality)
	}
}

func TestClean_DefaultQualityEmpty(t *testing.T) {
	if got := cleanFile(t, "article"); got.Quality != clean.IssueNone {
		t.Errorf("article Quality = %q, want empty", got.Quality)
	}
}

func TestWordCount_Vectors(t *testing.T) {
	cases := map[string]struct {
		in   string
		want int
	}{
		"empty":        {"", 0},
		"markdown mix": {"# Hi\n\n**bold** and [link](http://x) here", 5},
		"code fences":  {"```go\nfmt.Println()\n```", 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := clean.WordCount(c.in); got != c.want {
				t.Errorf("WordCount(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
