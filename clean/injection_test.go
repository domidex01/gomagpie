package clean_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/motherlodelab/magpie/clean"
)

// TestInjection_Stripped — US-1: every hidden vector is absent from BOTH
// output surfaces (the choke-point contract: CleanedPage.HTML feeds
// sidecars, Markdown feeds LLM/CLI — stripping only one reopens the hole
// through the other).
func TestInjection_Stripped(t *testing.T) {
	got := cleanFile(t, "injection")
	for i := 1; i <= 6; i++ {
		marker := fmt.Sprintf("MAGPIE_HIDDEN_%d", i)
		if strings.Contains(got.Markdown, marker) {
			t.Errorf("marker %s leaked into Markdown", marker)
		}
		if strings.Contains(got.HTML, marker) {
			t.Errorf("marker %s leaked into HTML", marker)
		}
	}
	// Survivors: precision pins. FLEX/HALF reach both surfaces; Widget and
	// the aria-hidden icon survive the STRIP (asserted on HTML — whether
	// trafilatura prunes a lone word from markdown is not our contract).
	for _, s := range []string{"FLEX-SURVIVOR", "HALF-SURVIVOR"} {
		if !strings.Contains(got.Markdown, s) {
			t.Errorf("survivor %s lost from Markdown", s)
		}
	}
	for _, s := range []string{"FLEX-SURVIVOR", "ICON-SURVIVOR", "HALF-SURVIVOR", "<h1>Widget</h1>"} {
		if !strings.Contains(got.HTML, s) {
			t.Errorf("survivor %s lost from HTML", s)
		}
	}
	if !strings.Contains(got.HTML, `aria-hidden="true"`) {
		t.Error("aria-hidden attribute must survive (out of scope by design)")
	}
	if !strings.Contains(got.Markdown, "Visible paragraph") {
		t.Error("visible paragraph lost from Markdown")
	}
	if strings.Contains(strings.ToLower(got.HTML), "display:none") {
		t.Error("display:none style still present in HTML")
	}
}

// TestInjection_RegexpRobustness — whitespace and case variants strip;
// opacity:0.5 (the (?:\.0+)? boundary) survives.
func TestInjection_RegexpRobustness(t *testing.T) {
	tests := []struct {
		style string
		want  bool // true = must be stripped
	}{
		{`display : none`, true},
		{`DISPLAY:NONE`, true},
		{`display:none`, true},
		{`visibility :  HIDDEN`, true},
		{`opacity:0`, true},
		{`opacity:0.0`, true},
		{`opacity:0.5`, false},
		{`display:flex`, false},
		{`font-size: 12px`, false},
	}
	for _, tt := range tests {
		html := fmt.Sprintf(`<html><body><p>Filler prose so trafilatura finds a main block for the page body text.</p><div style="%s">MARKER</div></body></html>`, tt.style)
		got, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(html), FinalURL: "https://example.com/x"})
		if err != nil {
			t.Fatalf("style %q: %v", tt.style, err)
		}
		if leaker := strings.Contains(got.HTML, "MARKER"); leaker == tt.want {
			if tt.want {
				t.Errorf("style %q: MARKER survived, want stripped", tt.style)
			} else {
				t.Errorf("style %q: MARKER stripped, want survived", tt.style)
			}
		}
	}
}

// TestInjection_CleanPageByteIdentical — when nothing is hidden the
// cleaned HTML is the original untouched: serialization must never
// perturb clean pages (that's the zero-golden-drift guarantee).
func TestInjection_CleanPageByteIdentical(t *testing.T) {
	plain := `<html><body><p>No hidden text here, just honest prose for the quality heuristics to chew on today.</p></body></html>`
	got, err := clean.Clean(t.Context(), clean.RawPage{HTML: []byte(plain), FinalURL: "https://example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.HTML != plain {
		t.Errorf("clean page re-serialized:\n got %q\nwant %q", got.HTML, plain)
	}
}
