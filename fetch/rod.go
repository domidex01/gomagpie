package fetch

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
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
func (r *RodFetcher) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if err := r.ensureBrowser(); err != nil {
		return nil, err
	}
	timeout := budget(req)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	page, err := r.browser.Context(cctx).Page(proto.TargetCreateTarget{URL: req.URL})
	if err != nil {
		return nil, fmt.Errorf("fetch: navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("fetch: wait load: %w", err)
	}
	// ponytail: fixed 2s settle, no network-idle heuristic; ceiling = slow hydration, tunable later.
	select {
	case <-cctx.Done():
		return nil, fmt.Errorf("fetch: settle: %w", cctx.Err())
	case <-time.After(2 * time.Second):
	}
	html, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("fetch: read html: %w", err)
	}
	if err := page.Close(); err != nil {
		return nil, fmt.Errorf("fetch: close page: %w", err)
	}
	return &FetchResponse{URL: req.URL, FinalURL: req.URL, StatusCode: 200, HTML: []byte(html)}, nil
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
	if err := r.ensureBrowser(); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, screenshotBudget)
	defer cancel()
	page, err := r.browser.Context(cctx).Page(proto.TargetCreateTarget{URL: rawURL})
	if err != nil {
		return nil, fmt.Errorf("fetch: navigate: %w", err)
	}
	defer func() { _ = page.Close() }() //nolint:errcheck // page teardown; failure unactionable
	if width > 0 && height > 0 {
		if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: width, Height: height}); err != nil {
			return nil, fmt.Errorf("fetch: viewport: %w", err)
		}
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("fetch: wait load: %w", err)
	}
	// Same fixed settle as Fetch — one policy, no divergence.
	select {
	case <-cctx.Done():
		return nil, fmt.Errorf("fetch: settle: %w", cctx.Err())
	case <-time.After(2 * time.Second):
	}
	png, err := page.Screenshot(true, nil) // full-page, defaults
	if err != nil {
		return nil, fmt.Errorf("fetch: screenshot: %w", err)
	}
	return png, nil
}

// ScreenshotPage news + closes a throwaway RodFetcher for one capture —
// the same per-call browser pattern as scrape's fetchBrowser.
// ponytail: fresh browser per screenshot is the known ceiling; client
// reuse is the upgrade path if MCP load ever demands it.
func ScreenshotPage(ctx context.Context, rawURL string, width, height int) ([]byte, error) {
	r := NewRodFetcher()
	defer func() { _ = r.Close() }() //nolint:errcheck // browser teardown; failure unactionable
	return r.Screenshot(ctx, rawURL, width, height)
}
