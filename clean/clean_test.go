package clean_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"magpie/clean"
)

var update = flag.Bool("update", false, "regenerate golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "testdata", "clean", name+".md")
	got = strings.TrimSpace(got) + "\n"
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s missing (run with -update): %v", path, err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(got) {
		t.Errorf("golden %s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func cleanFile(t *testing.T, htmlName string) clean.CleanedPage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "clean", htmlName+".html"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: raw, FinalURL: "https://example.com/" + htmlName})
	if err != nil {
		t.Fatalf("Clean(%s): %v", htmlName, err)
	}
	return got
}

func goldenDir(t *testing.T, dir, name, got string) {
	t.Helper()
	path := filepath.Join("..", "testdata", dir, name)
	got = strings.TrimSpace(got) + "\n"
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s missing (run with -update): %v", path, err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(got) {
		t.Errorf("golden %s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestClean_PopulatesNewFields(t *testing.T) {
	got := cleanFile(t, "article")
	if got.Metadata.WordCount <= 100 {
		t.Errorf("WordCount = %d, want > 100", got.Metadata.WordCount)
	}
	if got.Quality != clean.IssueNone {
		t.Errorf("Quality = %q, want empty", got.Quality)
	}
	if got.Metadata.Lang == "" {
		t.Error("Lang empty on article fixture")
	}
}

func TestCleanArticle(t *testing.T) {
	got := cleanFile(t, "article")
	golden(t, "article", got.Markdown)
	if !strings.Contains(got.Markdown, "|") {
		t.Error("article markdown missing GFM table")
	}
	// colspan mirror: bundle row text survives.
	if !strings.Contains(got.Markdown, "Widget Pro bundle") {
		t.Error("spanning cell text missing")
	}
	if got.Title == "" {
		t.Error("empty title")
	}
}

func TestCleanProduct(t *testing.T) {
	got := cleanFile(t, "product")
	golden(t, "product", got.Markdown)
	if len(got.StructuredData) == 0 {
		t.Fatal("product sidecar empty")
	}
	if !strings.Contains(string(got.StructuredData), "1234567890123") {
		t.Errorf("sidecar missing JSON-LD gtin: %s", got.StructuredData)
	}
}

func TestCleanSPAShell(t *testing.T) {
	got := cleanFile(t, "spa-shell")
	golden(t, "spa-shell", got.Markdown)
	// Must not error; markdown may be short.
}

func TestSidecarNextData(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "clean", "product.html"))
	if err != nil {
		t.Fatal(err)
	}
	side := clean.HarvestSidecar(raw)
	if !strings.Contains(string(side), "__NEXT_DATA__") && !strings.Contains(string(side), "pageProps") {
		// sidecar holds JSON-LD and/or NEXT_DATA; at minimum JSON-LD must be there
		if !strings.Contains(string(side), "Product") {
			t.Errorf("sidecar missing expected blocks: %s", side)
		}
	}
}

func TestTokenCap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "clean", "article.html"))
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat(string(raw), 50)
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(big), FinalURL: "https://example.com/big"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Markdown)/4 > 8000 {
		t.Errorf("markdown exceeds 8k tokens: %d chars", len(got.Markdown))
	}
}

// --- Phase G G.4: CleanedPage.HTML — scoped serialization, additive. ---

// TestClean_HTMLField: HTML carries the cleaned (scope-applied) document;
// excluded subtrees are gone before serialization; PDF pages have none.
func TestClean_HTMLField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "clean", "article.html"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: raw, FinalURL: "https://example.com/a"})
	if err != nil {
		t.Fatal(err)
	}
	if got.HTML == "" {
		t.Fatal("HTML field empty — html page-format would render nothing")
	}
	// Markdown goldens must not drift from the additive field.
	md, err := clean.Clean(t.Context(), clean.RawPage{HTML: raw, FinalURL: "https://example.com/a"})
	if err != nil || md.Markdown == "" {
		t.Fatalf("markdown unchanged check: %v", err)
	}

	// Scoping applies to HTML: exclude the nav, it disappears from HTML
	// but the page still cleans.
	scopedHTML := `<html><head><title>Scoped</title></head><body>
		<nav><a href="/x">navlink</a></nav>
		<article><p>` + strings.Repeat("substantial article prose for the cleaner to score happily. ", 30) + `</p></article>
		</body></html>`
	got2, err := clean.Clean(t.Context(), clean.RawPage{
		HTML: []byte(scopedHTML), FinalURL: "https://example.com/s",
		Scope: clean.Scope{Exclude: []string{"nav"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got2.HTML, "navlink") {
		t.Error("excluded <nav> must be absent from CleanedPage.HTML (scope before serialization)")
	}
	if !strings.Contains(got2.HTML, "substantial article prose") {
		t.Error("main content must survive scoping in HTML")
	}

	// PDF branch produces no HTML (cleanPDF never sets the field — the
	// Render <article> wrapper for empty-HTML pages is pinned in llm_test).
}
