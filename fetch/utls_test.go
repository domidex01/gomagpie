package fetch_test

// Browser-path behavior reachable without TLS: cleartext routing (the
// impersonation transport never sees http://) and the strict pre-dial
// gate on the browser option.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	impersonate "github.com/North-web-dev/impersonate-http"

	"magpie/fetch"
)

func browserOrigin(t *testing.T, hits *atomic.Int64) (*httptest.Server, *string) {
	t.Helper()
	var gotUA string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("clear ok")) //nolint:errcheck // test server
	}))
	t.Cleanup(s.Close)
	return s, &gotUA
}

// Cleartext + browser must ride the stock client (the wrapper TLS-
// handshakes unconditionally) with our matching header profile as
// fallback — a real UA beats none, and the wrapper is never on this path.
func TestBrowserCleartextRoutesToStock(t *testing.T) {
	var hits atomic.Int64
	srv, gotUA := browserOrigin(t, &hits)
	f := relaxedFetcher(t)
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL + "/", Browser: "chrome"})
	if err != nil {
		t.Fatalf("Fetch(http, browser=chrome): %v", err)
	}
	if string(resp.HTML) != "clear ok" {
		t.Errorf("body = %q, want clear ok", resp.HTML)
	}
	if want := fetch.HeaderProfiles["chrome"]["User-Agent"]; gotUA != nil && *gotUA != want {
		t.Errorf("UA = %q, want our chrome profile fallback %q", *gotUA, want)
	}
	// Explicit profile wins over the browser fallback on any path.
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL + "/", Browser: "chrome", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	if want := fetch.HeaderProfiles["default"]["User-Agent"]; *gotUA != want {
		t.Errorf("UA = %q, want explicit default profile %q (caller override wins)", *gotUA, want)
	}
	// No browser → stock default headers, unchanged behavior.
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if want := fetch.HeaderProfiles["default"]["User-Agent"]; *gotUA != want {
		t.Errorf("UA = %q, want stock default %q", *gotUA, want)
	}
	if n := hits.Load(); n != 3 {
		t.Errorf("origin hits = %d, want 3", n)
	}
}

// The strict pre-dial gate holds when --browser is set: rejection precedes
// any packet, so a browser fingerprint can't smuggle a private hit.
func TestBrowserStrictPreDial(t *testing.T) {
	var hits atomic.Int64
	srv, _ := browserOrigin(t, &hits)
	f := strictFetcher(t)
	_, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL + "/", Browser: "chrome"})
	if !errors.Is(err, fetch.ErrPrivateAddress) {
		t.Fatalf("Fetch(private, browser=chrome) = %v, want ErrPrivateAddress", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("origin hits = %d, want 0 (pre-dial, browser or not)", n)
	}
	// Unknown names fail loud at client selection, before any dial: the
	// relaxed fetcher lets the URL past the SSRF gate so the error comes
	// from resolveBrowser (a strict fetcher would reject the private URL
	// even earlier — also correct, also pre-dial).
	relaxed := relaxedFetcher(t)
	_, err = relaxed.Fetch(t.Context(), fetch.FetchRequest{URL: "https://127.0.0.1:1/", Browser: "webkit"})
	if err == nil || !strings.Contains(err.Error(), "chrome|firefox|safari|edge|ios|chrome_android|random") {
		t.Errorf("Fetch(browser=webkit) = %v, want allowed-values error", err)
	}
}

// TestProfiles_IOSWireUA (Phase G G.3): the new ios header profile rides
// the cleartext path (stock client + header bundle), so the origin sees
// the library's iPhone UA.
func TestProfiles_IOSWireUA(t *testing.T) {
	srv := echoOrigin(t)
	body := fetchBody(t, srv.URL, fetch.FetchRequest{Profile: "ios"})
	want := impersonate.Profiles["ios"].Headers.Get("User-Agent")
	if !strings.Contains(body, want) {
		t.Errorf("wire UA = %q, want the ios profile UA %q", body, want)
	}
}
