package fetch

// Hermetic Phase-E browser-path tests: routing, guards, decode. No TLS
// handshake here — the wrapper hardcodes its uTLS config (no
// InsecureSkipVerify hook), so self-signed https is unreachable offline;
// the JA3/JA4 proof lives in utls_smoke_test.go behind -tags browser.

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func compressed(t *testing.T, enc string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	close := func(w interface{ Close() error }) {
		if err := w.Close(); err != nil {
			t.Fatalf("%s close: %v", enc, err)
		}
	}
	switch enc {
	case "gzip":
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(data) //nolint:errcheck // in-memory buffer cannot reject
		close(w)
	case "deflate":
		w, err := flate.NewWriter(&buf, flate.DefaultCompression)
		if err != nil {
			t.Fatalf("flate writer: %v", err)
		}
		_, _ = w.Write(data) //nolint:errcheck // in-memory buffer cannot reject
		close(w)
	case "br":
		w := brotli.NewWriter(&buf)
		_, _ = w.Write(data) //nolint:errcheck // in-memory buffer cannot reject
		close(w)
	case "zstd":
		w, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatalf("zstd writer: %v", err)
		}
		_, _ = w.Write(data) //nolint:errcheck // in-memory buffer cannot reject
		close(w)
	default:
		t.Fatalf("bad enc %q", enc)
	}
	return buf.Bytes()
}

func TestDecodeBody_Table(t *testing.T) {
	for _, enc := range []string{"gzip", "deflate", "br", "zstd"} {
		got, err := decodeBody(compressed(t, enc, []byte("hello decoded")), http.Header{"Content-Encoding": {enc}})
		if err != nil || string(got) != "hello decoded" {
			t.Errorf("%s: got %q, %v; want hello decoded, nil", enc, got, err)
		}
	}
	// Stacked: "gzip, br" = gzip applied first, br last (RFC 7231 order),
	// so wire = br(gzip(data)) and decoding peels br then gzip.
	stacked := compressed(t, "br", compressed(t, "gzip", []byte("twice cooked")))
	got, err := decodeBody(stacked, http.Header{"Content-Encoding": {"gzip, br"}})
	if err != nil || string(got) != "twice cooked" {
		t.Errorf("stacked: got %q, %v", got, err)
	}
	for _, enc := range []string{"", "identity"} {
		raw := []byte("as sent")
		got, err := decodeBody(raw, http.Header{"Content-Encoding": {enc}})
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("%q: got %q, %v; want passthrough", enc, got, err)
		}
	}
	if _, err := decodeBody([]byte("x"), http.Header{"Content-Encoding": {"zopus"}}); err == nil {
		t.Error("unknown encoding must fail loud, not pass through")
	}
}

// The decoded stream carries its own 50 MB cap: a small wire body that
// inflates huge is truncated, never OOMs.
func TestDecodeBody_Cap(t *testing.T) {
	wire := compressed(t, "br", bytes.Repeat([]byte{0}, 60<<20))
	got, err := decodeBody(wire, http.Header{"Content-Encoding": {"br"}})
	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if len(got) != 50<<20 {
		t.Errorf("decoded len = %d, want capped %d", len(got), 50<<20)
	}
}

func TestResolveBrowser(t *testing.T) {
	for _, name := range []string{"chrome", "firefox", "random"} {
		if _, ok := resolveBrowser(name); !ok {
			t.Errorf("resolveBrowser(%q) not ok", name)
		}
	}
	for _, name := range []string{"safari", "edge", "ios", "Chrome", ""} {
		if _, ok := resolveBrowser(name); ok {
			t.Errorf("resolveBrowser(%q) ok, want rejection (fail loud on typos)", name)
		}
	}
	r1, _ := resolveBrowser("random")
	r2, _ := resolveBrowser("random")
	if r1.Name != r2.Name {
		t.Errorf("random resolved %q then %q — must resolve once per process", r1.Name, r2.Name)
	}
	if r1.Name != "chrome" && r1.Name != "firefox" {
		t.Errorf("random resolved %q, want chrome|firefox", r1.Name)
	}
}

func TestBrowserClientCacheAndValidation(t *testing.T) {
	f := relaxedInternalFetcher(t)
	c1, err := f.browserClient("chrome")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := f.browserClient("chrome")
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Error("chrome client not cached — connections would not pool")
	}
	cf, err := f.browserClient("firefox")
	if err != nil {
		t.Fatal(err)
	}
	if cf == c1 {
		t.Error("firefox and chrome share one client — fingerprints would bleed")
	}
	if _, err := f.browserClient("safari"); err == nil {
		t.Error("safari must fail loud (accepted set is chrome|firefox|random)")
	}
}

// relaxedInternalFetcher mirrors ssrf_test.go's relaxedFetcher without
// the import cycle of an external helper.
func relaxedInternalFetcher(t *testing.T) *StaticFetcher {
	t.Helper()
	f, err := NewStaticFetcherWithOptions(SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestRedirectGuard_Shared is the extracted guard's unit pin: the browser
// client passes this exact func as CheckRedirect, so redirect-to-private
// aborts on both paths. Client-level proof for the stock path lives in
// ssrf_test.go's TestSSRF_RedirectAbort.
func TestRedirectGuard_Shared(t *testing.T) {
	guard := redirectGuard(SSRFOptions{})
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9/escape", nil)
	err := guard(req, []*http.Request{httptest.NewRequest(http.MethodGet, "http://origin.example/", nil)})
	if !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("guard(localhost) = %v, want ErrPrivateAddress", err)
	}
	req = httptest.NewRequest(http.MethodGet, "http://example.com/ok", nil)
	if err := guard(req, nil); err != nil {
		t.Errorf("guard(example.com) = %v, want nil", err)
	}
	via := make([]*http.Request, 10)
	if err := guard(req, via); err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Errorf("guard(11th hop) = %v, want redirect cap", err)
	}
}

// acceptRecorder is a TCP listener recording every accepted connection —
// the dial-target proof for browserDial's branch decision.
func acceptRecorder(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.Close() //nolint:errcheck // recorder teardown; close error unactionable
	})
	var hits atomic.Int64
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			_ = conn.Close() //nolint:errcheck // recorder; the dialer's error is the point
		}
	}()
	return l.Addr().String(), &hits
}

func TestBrowserDialProxyRouting(t *testing.T) {
	proxyAddr, proxyHits := acceptRecorder(t)
	directAddr, directHits := acceptRecorder(t)
	d := browserDial(SSRFOptions{AllowPrivate: true})
	ctx := context.Background()
	// The dial can complete via the kernel backlog before the recorder
	// goroutine Accept()s — poll instead of asserting immediately.
	eventually := func(h *atomic.Int64, want int64, what string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for h.Load() < want && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if n := h.Load(); n != want {
			t.Errorf("%s hits = %d, want %d", what, n, want)
		}
	}

	t.Setenv("GOMAGPIE_PROXY", "http://"+proxyAddr)
	// Error is expected (the recorder isn't a real CONNECT proxy); the
	// assertion is WHERE the connection landed.
	_, _ = d(ctx, "tcp", "example.com:443") //nolint:errcheck // routing proof, not a fetch
	eventually(proxyHits, 1, "proxy")

	// NO_PROXY=* → proxyForHost nil → guarded direct dial.
	t.Setenv("NO_PROXY", "*")
	_, _ = d(ctx, "tcp", directAddr) //nolint:errcheck // routing proof
	eventually(directHits, 1, "direct")
	eventually(proxyHits, 1, "proxy-still")
}
