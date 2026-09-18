package fetch

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// RodFetcher renders JS via go-rod. The browser launches on first Fetch,
// never at import or startup. Pool size 1 in Phase 1.
type RodFetcher struct {
	browser *rod.Browser
}

// NewRodFetcher constructs without launching (launch is lazy).
func NewRodFetcher() *RodFetcher { return &RodFetcher{} }

// CanHandle is true; the caller decides escalation via ScoreJSRequired.
func (r *RodFetcher) CanHandle(req FetchRequest) bool { return true }

// Fetch navigates, waits for DOMContentLoaded + 2s settle, returns HTML.
// Thin delegate to FetchWithActions(nil): one code path, so non-action
// browser behavior cannot drift from the actions path (rod_smoke_test.go
// is the behavior contract).
func (r *RodFetcher) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	return r.FetchWithActions(ctx, req, nil)
}

// Close shuts the browser down (no zombies).
func (r *RodFetcher) Close() error {
	if r.browser == nil {
		return nil
	}
	if err := r.browser.Close(); err != nil {
		return fmt.Errorf("fetch: close browser: %w", err)
	}
	r.browser = nil
	return nil
}

// ensureBrowser lazily launches and connects the browser exactly once
// per RodFetcher (pool size 1).
func (r *RodFetcher) ensureBrowser() error {
	if r.browser != nil {
		return nil
	}
	l := launcher.New()
	controlURL, err := l.Launch()
	if err != nil {
		return fmt.Errorf("fetch: launch browser: %w", err)
	}
	r.browser = rod.New().ControlURL(controlURL)
	if err := r.browser.Connect(); err != nil {
		return fmt.Errorf("fetch: connect browser: %w", err)
	}
	return nil
}

// screenshotBudget is the per-capture attempt ceiling — larger than the
// fetch budget because full-page captures include rendering + settle.
const screenshotBudget = 45 * time.Second

// Screenshot captures a full-page PNG of the URL (rod-only capability —
// the Fetcher interface is untouched; StaticFetcher can't screenshot).
// Non-positive width/height keeps the browser default viewport.
func (r *RodFetcher) Screenshot(ctx context.Context, rawURL string, width, height int) ([]byte, error) {
	return r.screenshot(ctx, rawURL, width, height, nil)
}

// ScreenshotPage news + closes a throwaway RodFetcher for one capture —
// the same per-call browser pattern as scrape's fetchBrowser.
// ponytail: fresh browser per screenshot is the known ceiling; client
// reuse is the upgrade path if MCP load ever demands it.
func ScreenshotPage(ctx context.Context, rawURL string, width, height int) ([]byte, error) {
	return ScreenshotActions(ctx, rawURL, width, height, nil)
}
