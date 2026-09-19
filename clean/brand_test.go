package clean_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/motherlodelab/magpie/clean"
)

func brandFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "brand", name+".html"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func brandGolden(t *testing.T, name string, info clean.BrandInfo) {
	t.Helper()
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldenDir(t, "brand", name+".json", string(raw))
}

func TestBrand_LogoPrecedence(t *testing.T) {
	shop := clean.Brand(brandFile(t, "shop"))
	if shop.Logo != "https://shop.example.com/assets/logo.png" {
		t.Errorf("shop logo = %q, want img-logo (og:image must not win)", shop.Logo)
	}
	svg := clean.Brand(brandFile(t, "svg-logo"))
	if svg.Logo != "brand-logo" {
		t.Errorf("svg logo = %q, want svg class hook", svg.Logo)
	}
	minimal := clean.Brand(brandFile(t, "minimal"))
	if minimal.Logo != "" {
		t.Errorf("minimal logo = %q, want empty (no img, no svg, no og:image here)", minimal.Logo)
	}
	noise := clean.Brand(brandFile(t, "noise"))
	if noise.Logo != "" {
		t.Errorf("noise logo = %q, want empty (logo-ad/decoy immunity)", noise.Logo)
	}
}

func TestBrand_ColorsFonts(t *testing.T) {
	shop := clean.Brand(brandFile(t, "shop"))
	wantColors := []string{"#1a2b3c", "#ff6600", "#ffffff"}
	if !reflect.DeepEqual(shop.Colors, wantColors) {
		t.Errorf("shop colors = %v, want %v (deduped, lowercased, sorted)", shop.Colors, wantColors)
	}
	wantFonts := []string{"Arial", "Helvetica", "Inter", "sans-serif"}
	if !reflect.DeepEqual(shop.Fonts, wantFonts) {
		t.Errorf("shop fonts = %v, want %v (quoted/unquoted dedupe)", shop.Fonts, wantFonts)
	}
	if got := clean.Brand(brandFile(t, "minimal")); len(got.Colors) != 0 || len(got.Fonts) != 0 {
		t.Errorf("minimal colors/fonts = %v/%v, want empty", got.Colors, got.Fonts)
	}
}

func TestBrand_FaviconAbsolute(t *testing.T) {
	// Favicon resolution is HarvestMetadata's job (Brand is URL-less):
	// a relative icon href must come back absolute.
	meta := clean.HarvestMetadata(brandFile(t, "shop"), "https://shop.example.com/products", "")
	if meta.Favicon != "https://shop.example.com/favicon.ico" {
		t.Errorf("favicon = %q, want absolute URL (relative href must resolve)", meta.Favicon)
	}
	if meta.Image != "https://shop.example.com/og-cover.png" {
		t.Errorf("og:image = %q, want harvested value", meta.Image)
	}
}

func TestBrand_Goldens(t *testing.T) {
	for _, name := range []string{"shop", "minimal", "svg-logo", "noise"} {
		t.Run(name, func(t *testing.T) {
			brandGolden(t, name, clean.Brand(brandFile(t, name)))
		})
	}
}
