package fetch_test

// SSRF guard tests: every rejection decided WITHOUT a packet (fakeLookup
// or pre-dial asserts). Strictness is always explicit via
// NewStaticFetcherWithOptions — no test here rides the test-binary hatch.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"magpie/fetch"
)

// TestSecurityFilesUseExplicitOptions bolts the test-binary hatch shut:
// the SSRF/proxy test files must never use the bare constructor, which
// rides the relaxed hatch and would pass vacuously. Non-security tests
// (fetch_test, profiles_test) may keep the hatch — this guard covers
// only the files where strictness is the thing under test.
func TestSecurityFilesUseExplicitOptions(t *testing.T) {
	for _, f := range []string{"ssrf_test.go", "proxy_test.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bareFetcherCtor.Match(src) {
			t.Errorf("%s uses the bare constructor: use NewStaticFetcherWithOptions", f)
		}
	}
}

var bareFetcherCtor = regexp.MustCompile(`NewStaticFetcher\(\)`)

// fakeLookup scripts host→IPs (the only new fake in Phase D); absent
// hosts resolve to the NXDOMAIN shape (no addresses). Compile-locked to
// the production seam.
type fakeLookup struct {
	mu    sync.Mutex
	hosts map[string][]net.IP
	errs  map[string]error
	calls []string
}

var _ fetch.LookupFunc = (*fakeLookup)(nil).lookup

func (f *fakeLookup) lookup(_ context.Context, host string) ([]net.IP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host)
	if err, ok := f.errs[host]; ok {
		return nil, err
	}
	return f.hosts[host], nil
}

// hitOrigin counts every request served — the pre-dial proof: rejection
// tests pair the error assert with hits == 0.
func hitOrigin(t *testing.T, hits *atomic.Int64, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func strictFetcher(t *testing.T) *fetch.StaticFetcher {
	t.Helper()
	f, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// relaxedFetcher allows private hosts explicitly (NOT via the hatch) so
// tests can reach httptest origins and exercise mid-transport guards.
func relaxedFetcher(t *testing.T) *fetch.StaticFetcher {
	t.Helper()
	f, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestValidateURL_Table is the phase's load-bearing unit test: every
// rejection reason, decided without a single packet, plus the rebind
// shape that justifies DNS-checking at all.
func TestValidateURL_Table(t *testing.T) {
	lk := &fakeLookup{
		hosts: map[string][]net.IP{
			"ok.example":     {net.ParseIP("93.184.216.34")},
			"rebind.example": {net.ParseIP("93.184.216.34"), net.ParseIP("10.9.9.9")},
		},
		errs: map[string]error{"down.example": errors.New("dns: servfail")},
	}
	cases := []struct {
		name    string
		url     string
		opts    fetch.SSRFOptions
		wantErr bool
	}{
		{"public literal ok", "http://93.184.216.34/", fetch.SSRFOptions{}, false},
		{"public DNS ok", "http://ok.example/", fetch.SSRFOptions{}, false},
		{"public DNS with port ok", "http://ok.example:8443/", fetch.SSRFOptions{}, false},
		{"loopback literal", "http://127.0.0.1/", fetch.SSRFOptions{}, true},
		// netip cannot parse integer-form IPv4, so this falls to DNS and the
		// fake returns NXDOMAIN → rejected at the resolve layer either way.
		{"loopback decimal-obfuscated", "http://2130706433/", fetch.SSRFOptions{}, true},
		{"private 10/8", "http://10.1.2.3/", fetch.SSRFOptions{}, true},
		{"private 192.168", "http://192.168.0.1/", fetch.SSRFOptions{}, true},
		{"link-local metadata", "http://169.254.169.254/", fetch.SSRFOptions{}, true},
		{"ipv6 loopback", "http://[::1]/", fetch.SSRFOptions{}, true},
		{"ipv6 link-local", "http://[fe80::1]/", fetch.SSRFOptions{}, true},
		{"multicast", "http://224.0.0.1/", fetch.SSRFOptions{}, true},
		{"unspecified", "http://0.0.0.0/", fetch.SSRFOptions{}, true},
		{"localhost name", "http://localhost:8080/", fetch.SSRFOptions{}, true},
		{"localhost subdomain", "http://api.localhost/", fetch.SSRFOptions{}, true},
		{"metadata name", "http://metadata.google.internal/", fetch.SSRFOptions{}, true},
		{"metadata trailing dot", "http://metadata.google.internal./", fetch.SSRFOptions{}, true},
		{"metadata subdomain", "http://x.metadata.google.internal/", fetch.SSRFOptions{}, true},
		{"dns rebind set", "http://rebind.example/", fetch.SSRFOptions{}, true},
		{"dns nxdomain", "http://absent.example/", fetch.SSRFOptions{}, true},
		{"dns error propagates", "http://down.example/", fetch.SSRFOptions{}, true},
		{"allowPrivate relaxes dns", "http://rebind.example/", fetch.SSRFOptions{AllowPrivate: true}, false},
		// AllowPrivate is the explicit opt-out (and the test-binary hatch):
		// it relaxes private literals too — httptest origins ARE private
		// literals, so the relaxation must cover them or the suite breaks.
		{"allowPrivate relaxes private literal", "http://10.1.2.3/", fetch.SSRFOptions{AllowPrivate: true}, false},
		{"file gated by default", "file:///etc/hostname", fetch.SSRFOptions{}, true},
		{"file allowed explicitly", "file:///etc/hostname", fetch.SSRFOptions{AllowFile: true}, false},
		{"allowFile does not relax private", "http://127.0.0.1/", fetch.SSRFOptions{AllowFile: true}, true},
		{"bad scheme", "ftp://93.184.216.34/", fetch.SSRFOptions{}, true},
		{"empty host", "http:///path", fetch.SSRFOptions{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fetch.ValidateURL(t.Context(), tc.url, lk.lookup, tc.opts)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateURL(%q) = nil, want SSRF rejection", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateURL(%q) = %v, want nil", tc.url, err)
			}
			if tc.wantErr && !errors.Is(err, fetch.ErrPrivateAddress) {
				t.Errorf("ValidateURL(%q) err = %v, want errors.Is(ErrPrivateAddress)", tc.url, err)
			}
		})
	}
	// Anti-vacuous lock: a ValidateURL that never consults DNS would pass
	// every row except rebind — this catches that.
	if len(lk.calls) == 0 {
		t.Error("fake lookup never consulted — DNS path untested, suite is vacuous on rebind rows")
	}
}

// TestSSRF_StrictFetcherPreDialRejects is the transport proof: the error
// must arrive with the origin server seeing ZERO hits — rejection before
// dial, not a refused connection after.
func TestSSRF_StrictFetcherPreDialRejects(t *testing.T) {
	var hits atomic.Int64
	srv := hitOrigin(t, &hits, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>pwned</body></html>")) //nolint:errcheck // test server
	})
	f := strictFetcher(t)
	_, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL + "/secret"})
	if !errors.Is(err, fetch.ErrPrivateAddress) {
		t.Fatalf("Fetch(private) = %v, want errors.Is(ErrPrivateAddress)", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("origin hits = %d, want 0 (rejection must precede dial)", n)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error %q should name the blocked address (debuggability)", err)
	}
}

// TestSSRF_RedirectAbort: a redirect to a blocklisted host aborts the
// chain. The origin is reached with an explicitly AllowPrivate fetcher
// (strict would reject the origin itself pre-dial); the redirect target
// is `localhost`, which NO option combination allows.
func TestSSRF_RedirectAbort(t *testing.T) {
	var hits atomic.Int64
	srv := hitOrigin(t, &hits, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, "http://localhost:9/escape", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("plain")) //nolint:errcheck // test server
	})
	f, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL + "/hop"})
	if !errors.Is(err, fetch.ErrPrivateAddress) {
		t.Fatalf("Fetch(redirect→localhost) = %v, want errors.Is(ErrPrivateAddress)", err)
	}
	if !strings.Contains(err.Error(), "localhost") {
		t.Errorf("error %q should name the redirect target", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("origin hits = %d, want exactly 1 (chain started, then aborted)", n)
	}
}

// TestSSRF_FileGate: file:// is gated by explicit options in production
// shape. Env resolution (MAGPIE_ALLOW_FILE=1, exact string) rides the
// bare constructor and is covered by the existing suite (TestStaticFileURL
// et al.) — the hatch's one-directional rule means no NEW test depends on
// it.
func TestSSRF_FileGate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "page.html")
	want := "<html><body><p>local</p></body></html>"
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	fileURL := "file://" + p

	f, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Fetch(t.Context(), fetch.FetchRequest{URL: fileURL})
	if !errors.Is(err, fetch.ErrPrivateAddress) || !strings.Contains(err.Error(), "MAGPIE_ALLOW_FILE") {
		t.Fatalf("default file:// = %v, want ErrPrivateAddress naming the env var", err)
	}

	allowed, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{AllowFile: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := allowed.Fetch(t.Context(), fetch.FetchRequest{URL: fileURL})
	if err != nil {
		t.Fatalf("AllowFile fetch: %v", err)
	}
	if string(resp.HTML) != want {
		t.Fatalf("got %q, want %q", resp.HTML, want)
	}
}
