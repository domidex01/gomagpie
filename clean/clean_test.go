package clean_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gomagpie/clean"
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
