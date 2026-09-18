package fetch_test

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"magpie/fetch"
)

func newFakeOrigin(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestStaticRedirectCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// chain of 11 redirects: /0 -> /1 -> ... -> /11
		var n int
		if _, err := fmt.Sscanf(r.URL.Path, "/%d", &n); err != nil {
			n = 0
		}
		if n < 11 {
			http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound) // writes header, no body to check
			return
		}
		if _, err := fmt.Fprint(w, "done"); err != nil {
			t.Error(err)
		}
	})
	base := newFakeOrigin(t, mux)
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Fetch(t.Context(), fetch.FetchRequest{URL: base + "/0"})
	if err == nil {
		t.Fatal("expected redirect-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "redirect") && !strings.Contains(err.Error(), "stopped after 10") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStaticGzip(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		if _, err := gz.Write([]byte("<html><body><p>" + strings.Repeat("hello world ", 100) + "</p></body></html>")); err != nil {
			t.Error(err)
		}
		if err := gz.Close(); err != nil {
			t.Error(err)
		}
	})
	base := newFakeOrigin(t, mux)
	f, ferr := fetch.NewStaticFetcher()
	if ferr != nil {
		t.Fatal(ferr)
	}
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: base + "/gzip"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.HTML), "hello world") {
		t.Fatalf("gzip not decoded: %q", resp.HTML[:100])
	}
}

func TestStatic429Passthrough(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/limited", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		if _, err := fmt.Fprint(w, "slow down"); err != nil {
			t.Error(err)
		}
	})
	base := newFakeOrigin(t, mux)
	f, ferr := fetch.NewStaticFetcher()
	if ferr != nil {
		t.Fatal(ferr)
	}
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: base + "/limited"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if string(resp.HTML) != "slow down" {
		t.Fatalf("body = %q", resp.HTML)
	}
}

func TestStaticFileURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "page.html")
	want := "<html><body><p>local fixture</p></body></html>"
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	f, ferr := fetch.NewStaticFetcher()
	if ferr != nil {
		t.Fatal(ferr)
	}
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: "file://" + p})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.HTML) != want {
		t.Fatalf("got %q, want %q", resp.HTML, want)
	}
}

// PDF bytes are final content, never an SPA shell: no rod escalation for
// any caller (scrape and crawl).
func TestDetectPDFNeverEscalates(t *testing.T) {
	score, embedded := fetch.ScoreJSRequired([]byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj"), nil)
	if score != 0 || embedded {
		t.Errorf("ScoreJSRequired(pdf) = (%d, %v), want (0, false)", score, embedded)
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name     string
		html     string
		minScore int
		maxScore int
		embedded bool
	}{
		{"react-root", `<html><body><div id="root"></div><script src="/b.js"></script></body></html>`, 2, 99, false},
		{"app-root", `<html><body><app-root></app-root><script src="/m.js"></script></body></html>`, 2, 99, false},
		{"static", `<html><body><article><h1>Hi</h1><p>` + strings.Repeat("text content here. ", 200) + `</p><a href="/a">a</a><a href="/b">b</a><a href="/c">c</a></article></body></html>`, 0, 0, false},
		{"next-data", `<html><body><div id="__next">x</div><script id="__NEXT_DATA__" type="application/json">{"a":1}</script></body></html>`, 0, 99, true},
		{"fragment", `<html><body><meta name="fragment" content="!"><p>` + strings.Repeat("word ", 300) + `</p><a href="/a">a</a><a href="/b">b</a><a href="/c">c</a></body></html>`, 1, 99, false},
		{"noscript", `<html><body><NOSCRIPT>Please ENABLE JAVASCRIPT to continue</NOSCRIPT><p>` + strings.Repeat("word ", 300) + `</p><a href="/a">a</a><a href="/b">b</a><a href="/c">c</a></body></html>`, 1, 99, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			score, embedded := fetch.ScoreJSRequired([]byte(c.html), nil)
			if embedded != c.embedded {
				t.Errorf("embedded = %v, want %v", embedded, c.embedded)
			}
			if score < c.minScore || score > c.maxScore {
				t.Errorf("score = %d, want in [%d,%d]", score, c.minScore, c.maxScore)
			}
		})
	}
}

func TestNeedsBrowserBoundary(t *testing.T) {
	if fetch.NeedsBrowser(1) {
		t.Error("score 1 must not escalate")
	}
	if !fetch.NeedsBrowser(2) {
		t.Error("score 2 must escalate")
	}
}

// TestStaticGzipBomb: ~55 KB on the wire expands to 60 MB of decoded
// zeros; the client must cap the DECODED stream at exactly the cap.
// Hardcoded 50<<20: if the cap changes intentionally this test SHOULD
// break (it pins the magic number by design).
func TestStaticGzipBomb(t *testing.T) {
	const decoded = 60 << 20 // MBs of zeros the bomb expands to
	mux := http.NewServeMux()
	mux.HandleFunc("/bomb", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }() //nolint:errcheck // client aborts mid-stream; write errors expected
		chunk := make([]byte, 1<<20)      // 1 MB of zeros
		for i := 0; i < decoded>>20; i++ {
			if _, err := gz.Write(chunk); err != nil {
				return // client stopped reading at the cap — expected
			}
		}
	})
	base := newFakeOrigin(t, mux)
	// Explicit AllowPrivate (httptest origin is a private literal): this
	// test pins the bomb cap, not the hatch.
	f, err := fetch.NewStaticFetcherWithOptions(fetch.SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: base + "/bomb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.HTML) != 50<<20 {
		t.Fatalf("decoded body = %d bytes, want exactly %d (LimitReader cap)", len(resp.HTML), 50<<20)
	}
}
