package cli

import (
	"os"
	"strings"
	"testing"
)

func TestDocs_Phase3Surface(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	for _, want := range []string{
		"scrape_url", "crawl_site", "extract_structured", "get_cached_selectors",
		"magpie serve", "magpie build --with", "magpie_api_version",
	} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README.md does not mention %q", want)
		}
	}
	agents, err := os.ReadFile("../AGENTS.md")
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if strings.Contains(string(agents), "No plugins/WASM until core stable") {
		t.Error("AGENTS.md still carries the pre-Phase-3 plugin gotcha")
	}
}
