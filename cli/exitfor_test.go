package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gomagpie/clean"
	"gomagpie/crawl"
	"gomagpie/fetch"
	"gomagpie/scrape"
	"gomagpie/vertical"
)

// Table over the single exit-code map (exitCode — pure, no printing).
// Codes here are API surface for scripts; the httpd smokes
// (TestScrape_Exit8, TestMissingKeyExit7, TestScrape_BadRenderExit2,
// crawl exits 3-6 in cmd_test.go) prove the same codes end-to-end.
func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"unknown error", errors.New("boom"), 1},
		{"validation", scrape.ValidateOptions(scrape.Options{Render: "nope"}), 2},
		{"validation wrapped", fmt.Errorf("scrape: %w", scrape.ValidateOptions(scrape.Options{PageFormat: "xml"})), 2},
		{"missing key", fmt.Errorf("scrape: provider openai: %w", scrape.ErrMissingKey), 7},
		{"missing key preflight", missingKeyErr("openai"), 7},
		{"quality", &clean.QualityError{Issue: clean.IssueEmpty, URL: "http://x"}, 8},
		{"cost ceiling", fmt.Errorf("scrape: running 1 > max 0.5: %w", crawl.ErrCostCeiling), 6},
		{"robots", fmt.Errorf("crawl: seed h: %w", crawl.ErrRobotsBlocked), 5},
		{"bad scope", fmt.Errorf("crawl: %w", crawl.ErrBadScope), 2},
		{"private address", fmt.Errorf("fetch: %w", fetch.ErrPrivateAddress), 2},
		{"vertical mismatch", fmt.Errorf("scrape: vertical reddit: %w", vertical.ErrURLMismatch), 2},
		{"usage cmdError", fail(3, "crawl: no records extracted"), 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestScrape_BadRenderExit2(t *testing.T) {
	testEnv(t, "cache.db")
	// Nonexistent file: any I/O would exit 1, so exit 2 proves validation
	// runs before any fetch.
	err := runScrape(t.Context(), "file:///nonexistent-render-probe.html", scrapeOptions{Format: "json", Render: "bogus"})
	if codeOf(err) != 2 {
		t.Fatalf("exit = %d, want 2 (err=%v)", codeOf(err), err)
	}
	if !strings.Contains(err.Error(), `scrape: render "bogus" must be auto|static|browser`) {
		t.Errorf("error = %v, want scrape's exact validation string", err)
	}
}

// Phase G: search missing keys ride the same ErrMissingKey → 7 mapping.
func TestExitCode_SearchMissingKey(t *testing.T) {
	err := fmt.Errorf("scrape: search provider brave: %w", scrape.ErrMissingKey)
	if got := exitCode(err); got != 7 {
		t.Errorf("exitCode(search missing key) = %d, want 7", got)
	}
	err = fmt.Errorf("mcp: search: scrape: search provider serper: %w", scrape.ErrMissingKey)
	if got := exitCode(err); got != 7 {
		t.Errorf("exitCode(mcp search missing key) = %d, want 7", got)
	}
}

// Phase G: a surviving typed challenge is a page-usability failure (8).
func TestExitCode_Challenge(t *testing.T) {
	err := &fetch.ChallengeError{Vendor: "cloudflare", StatusCode: 403, URL: "https://x"}
	if got := exitCode(fmt.Errorf("scrape: %w", err)); got != 8 {
		t.Errorf("exitCode(challenge) = %d, want 8", got)
	}
}

// Phase G: proxy pool misconfiguration is a usage error (2).
func TestExitCode_ProxyConfig(t *testing.T) {
	err := fmt.Errorf("fetch: proxy pool line 3: %w", fetch.ErrProxyConfig)
	if got := exitCode(err); got != 2 {
		t.Errorf("exitCode(proxy config) = %d, want 2", got)
	}
}
