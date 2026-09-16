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

	"gomagpie/fetch"
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
			http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound)
			return
		}
		fmt.Fprint(w, "done")
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
		_, _ = gz.Write([]byte("<html><body><p>" + strings.Repeat("hello world ", 100) + "</p></body></html>"))
		_ = gz.Close()
	})
	base := newFakeOrigin(t, mux)
	f, _ := fetch.NewStaticFetcher()
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
		fmt.Fprint(w, "slow down")
	})
	base := newFakeOrigin(t, mux)
	f, _ := fetch.NewStaticFetcher()
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
	f, _ := fetch.NewStaticFetcher()
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: "file://" + p})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.HTML) != want {
		t.Fatalf("got %q, want %q", resp.HTML, want)
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
