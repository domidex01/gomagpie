package clean_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/motherlodelab/magpie/clean"
)

func sidecarOf(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// thinShell builds trafilatura-thin HTML carrying the given script blobs.
func thinShell(scripts ...string) string {
	var b strings.Builder
	b.WriteString(`<html><head><title>Thin App</title></head><body><div id="root"></div><p>tiny stub</p>`)
	for _, s := range scripts {
		b.WriteString(s)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func TestIslandText_Table(t *testing.T) {
	longA := strings.Repeat("alpha content leaf ", 4) // ≥40 chars
	longB := strings.Repeat("beta content leaf ", 4)  // ≥40 chars
	longC := strings.Repeat("gamma nested leaf ", 4)  // ≥40 chars
	rows := []struct {
		name string
		in   any
		want []string // substrings that must appear
		drop []string // substrings that must not appear
	}{
		{"array", []any{longA, longB}, []string{"alpha", "beta"}, nil},
		{"single", map[string]any{"text": longA}, []string{"alpha"}, nil},
		{"nested-next-data", map[string]any{"props": map[string]any{"pageProps": map[string]any{"body": longC}}}, []string{"gamma"}, nil},
		{"dedupe", []any{longA, longA, longB}, []string{"alpha", "beta"}, nil},
		{"short-drop", []any{"tiny", longA}, []string{"alpha"}, []string{"tiny"}},
		{"css-noise", []any{longA, strings.Repeat(".x{color:red};", 6)}, []string{"alpha"}, []string{"color"}},
		{"js-noise", []any{longA, "function(openh)((this is a very long fake js payload here yes))"}, []string{"alpha"}, []string{"function("}},
	}
	for _, r := range rows {
		got := clean.IslandText(sidecarOf(t, r.in), 4000)
		for _, w := range r.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in %q", r.name, w, got)
			}
		}
		for _, d := range r.drop {
			if strings.Contains(got, d) {
				t.Errorf("%s: noise %q leaked into %q", r.name, d, got)
			}
		}
	}
	// Dedupe holds: alpha appears exactly once.
	got := clean.IslandText(sidecarOf(t, []any{longA, longA}), 4000)
	if strings.Count(got, "alpha") != 4 { // 4 repeats inside one leaf, one leaf total
		t.Errorf("dedupe: alpha count wrong in %q", got)
	}
}

func TestIslandText_Cap(t *testing.T) {
	var leaves []any
	for i := 0; i < 50; i++ {
		leaves = append(leaves, strings.Repeat("content leaf number ", 4)+strings.Repeat("x", i))
	}
	got := clean.IslandText(sidecarOf(t, leaves), 4000)
	if len(got) > 4000 {
		t.Errorf("len = %d, want ≤4000", len(got))
	}
	if len(got) == 4000 {
		t.Error("cap cut mid-paragraph: want \\n\\n boundary, got exact 4000")
	}
	if !strings.Contains(got, "content leaf") {
		t.Error("capped output lost all content")
	}
}

func TestIslandText_Empty(t *testing.T) {
	if got := clean.IslandText(nil, 4000); got != "" {
		t.Errorf("nil sidecar = %q, want empty", got)
	}
	if got := clean.IslandText(json.RawMessage(`{oops`), 4000); got != "" {
		t.Errorf("bad JSON = %q, want empty", got)
	}
}

func TestPlayerResponseHTML_Table(t *testing.T) {
	full := `<script>var ytInitialPlayerResponse = {"videoDetails": {"title": "T", "author": "A"}};</script>`
	m, ok := clean.PlayerResponseHTML([]byte(full))
	if !ok {
		t.Fatal("full blob: ok = false")
	}
	if vd, _ := m["videoDetails"].(map[string]any); vd["title"] != "T" {
		t.Errorf("videoDetails = %v", m["videoDetails"])
	}
	if _, ok := clean.PlayerResponseHTML([]byte(`<script>console.log("noise")</script>`)); ok {
		t.Error("noise-only: ok = true, want false")
	}
	// Truncated braces must fail closed, never panic.
	if _, ok := clean.PlayerResponseHTML([]byte(`<script>var ytInitialPlayerResponse = {"videoDetails": {"title": "T"`)); ok {
		t.Error("truncated: ok = true, want false")
	}
}

func TestPlayerDetails_NilCases(t *testing.T) {
	if got := clean.PlayerDetails([]byte(`<html><body><p>plain page</p></body></html>`)); got != nil {
		t.Errorf("non-YT HTML = %v, want nil", got)
	}
	noTitle := `<script>var ytInitialPlayerResponse = {"videoDetails": {"author": "A"}};</script>`
	if got := clean.PlayerDetails([]byte(noTitle)); got != nil {
		t.Errorf("title-less blob = %v, want nil", got)
	}
}

func TestClean_ThinAppendsIslands(t *testing.T) {
	leaf := strings.Repeat("island rescue content ", 4)
	next := `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"article": "` + leaf + `"}}}</script>`
	before, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(thinShell(`<script>var x = 1;</script>`)), FinalURL: "https://example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(thinShell(next)), FinalURL: "https://example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if clean.WordCount(after.Markdown) <= clean.WordCount(before.Markdown) {
		t.Errorf("words before=%d after=%d, want growth", clean.WordCount(before.Markdown), clean.WordCount(after.Markdown))
	}
	if !strings.Contains(after.Markdown, "island rescue content") {
		t.Errorf("island leaf missing:\n%s", after.Markdown)
	}
}

func TestClean_ScopedSkipsIslands(t *testing.T) {
	leaf := strings.Repeat("island rescue content ", 4)
	next := `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"article": "` + leaf + `"}}}</script>`
	html := `<html><head><title>Thin</title></head><body><article><p>scoped stub text here</p></article>` + next + `</body></html>`
	got, err := clean.Clean(t.Context(), clean.RawPage{
		HTML: []byte(html), FinalURL: "https://example.com/app",
		Scope: clean.Scope{Include: []string{"article"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Markdown, "island rescue content") {
		t.Errorf("scoped run gained island text:\n%s", got.Markdown)
	}
}

func TestClean_RichIgnoresIslands(t *testing.T) {
	// Synthetic rich page (≥200 words) with a sidecar injected: the hook
	// must not fire, so output is identical to the plain rich run.
	var b strings.Builder
	b.WriteString(`<html><head><title>Rich</title></head><body><article>`)
	for i := 0; i < 8; i++ {
		b.WriteString(`<p>Paragraph synthesis block number ` + strconv.Itoa(i) + strings.Repeat(" with honest filler prose about widgets", 3) + ` keeps extraction busy here.</p>`)
	}
	b.WriteString(`</article></body></html>`)
	richBase := b.String()
	plain, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(richBase), FinalURL: "https://example.com/rich"})
	if err != nil {
		t.Fatal(err)
	}
	if n := clean.WordCount(plain.Markdown); n < 200 {
		t.Fatalf("synthetic rich page only %d words, want ≥200", n)
	}
	leaf := strings.Repeat("island rescue content ", 4)
	next := `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"article": "` + leaf + `"}}}</script>`
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(richBase + next), FinalURL: "https://example.com/rich"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Markdown != plain.Markdown {
		t.Error("rich page with sidecar changed — hook leaked into a rich page")
	}
}

func TestClean_YTPrepend(t *testing.T) {
	html := `<html><head><title>Demo Video</title></head><body><div id="player"></div><p>stub</p>` +
		`<script>var ytInitialPlayerResponse = {"videoDetails": {"title": "Demo Video Title Here", "author": "DemoChannel", "shortDescription": "A demo video description with enough words for the test.", "viewCount": "99"}};</script>` +
		`</body></html>`
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(html), FinalURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(got.Markdown), "# Demo Video Title Here") {
		t.Errorf("YT thin page missing title prepend:\n%s", got.Markdown)
	}
}

func TestClean_SpaShellUntouchedByIslands(t *testing.T) {
	got := cleanFile(t, "spa-shell")
	if n := clean.WordCount(got.Markdown); n != 7 {
		t.Errorf("spa-shell words = %d, want 7 (hook must not fire on the real fixture)", n)
	}
}
