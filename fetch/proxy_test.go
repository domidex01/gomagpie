package fetch_test

// Proxy tests: GOMAGPIE_PROXY honored (hit), NO_PROXY bypass, invalid
// value loud, robots Checker rides the same guarded transport. All env
// via t.Setenv (parallel-safe, auto-restore).

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"gomagpie/crawl"
	"gomagpie/fetch"
)

// newProxyOrigin is a stdlib reverse proxy with a hit counter — the
// test-only egress stand-in (no proxy stub to maintain).
func newProxyOrigin(t *testing.T, hits *atomic.Int64, target string) string {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	p := httputil.NewSingleHostReverseProxy(u)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		p.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func TestProxy_Hit(t *testing.T) {
	var originHits, proxyHits atomic.Int64
	origin := hitOrigin(t, &originHits, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("via proxy")) //nolint:errcheck // test server
	})
	proxyURL := newProxyOrigin(t, &proxyHits, origin.URL)
	t.Setenv("GOMAGPIE_PROXY", proxyURL)

	f := relaxedFetcher(t)
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL + "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.HTML) != "via proxy" {
		t.Errorf("body = %q, want via proxy", resp.HTML)
	}
	if n := proxyHits.Load(); n != 1 {
		t.Errorf("proxy hits = %d, want 1", n)
	}
	if n := originHits.Load(); n != 1 {
		t.Errorf("origin hits = %d, want 1", n)
	}
}

func TestProxy_NoProxyBypass(t *testing.T) {
	var originHits, proxyHits atomic.Int64
	origin := hitOrigin(t, &originHits, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("direct")) //nolint:errcheck // test server
	})
	proxyURL := newProxyOrigin(t, &proxyHits, origin.URL)
	t.Setenv("GOMAGPIE_PROXY", proxyURL)
	// Hostname WITHOUT port: noProxyMatch keys on u.Hostname().
	t.Setenv("NO_PROXY", "127.0.0.1")

	f := relaxedFetcher(t)
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL + "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.HTML) != "direct" {
		t.Errorf("body = %q, want direct", resp.HTML)
	}
	if n := proxyHits.Load(); n != 0 {
		t.Errorf("proxy hits = %d, want 0 (NO_PROXY bypass)", n)
	}
}

func TestProxy_InvalidValue(t *testing.T) {
	var originHits atomic.Int64
	origin := hitOrigin(t, &originHits, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never be served")) //nolint:errcheck // test server
	})
	t.Setenv("GOMAGPIE_PROXY", "://bogus")

	f := relaxedFetcher(t)
	_, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL + "/x"})
	if err == nil || !strings.Contains(err.Error(), "GOMAGPIE_PROXY") {
		t.Fatalf("err = %v, want loud failure naming GOMAGPIE_PROXY", err)
	}
	if n := originHits.Load(); n != 0 {
		t.Errorf("origin hits = %d, want 0 (garbage proxy config must not reach the origin)", n)
	}
	// Sentinel-shaped configs fail too: wrong scheme, no host.
	for _, bad := range []string{"ftp://p.example:3128", "http://"} {
		t.Setenv("GOMAGPIE_PROXY", bad)
		if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL + "/x"}); err == nil || !strings.Contains(err.Error(), "GOMAGPIE_PROXY") {
			t.Errorf("GOMAGPIE_PROXY=%q: err = %v, want loud failure", bad, err)
		}
	}
}

func TestProxy_RobotsViaProxy(t *testing.T) {
	var proxyHits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nDisallow:\n")) //nolint:errcheck // test server
	})
	origin := httptest.NewServer(mux)
	t.Cleanup(origin.Close)
	proxyURL := newProxyOrigin(t, &proxyHits, origin.URL)
	t.Setenv("GOMAGPIE_PROXY", proxyURL)

	// The Checker shares GuardedTransport → robots fetches honor the proxy.
	c := crawl.NewChecker()
	ok, err := c.Allowed(t.Context(), origin.URL+"/page")
	if err != nil || !ok {
		t.Fatalf("Allowed via proxy = %v,%v, want true", ok, err)
	}
	if n := proxyHits.Load(); n < 1 {
		t.Errorf("proxy hits = %d, want ≥1 (robots fetch must ride the proxy)", n)
	}
}
