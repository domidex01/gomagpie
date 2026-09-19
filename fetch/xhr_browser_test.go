//go:build browser

package fetch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
)

// TestCaptureXHRLive — US-2 end-to-end: an httptest page fetch()es
// /api/data (JSON) and /other (non-matching); CaptureXHR ["/api/"] must
// produce exactly one capture whose body parses as the served JSON.
// rod_smoke_test.go is the model, including both skip styles.
func TestCaptureXHRLive(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok {
		// LookPath only sees system Chrome; rod's managed download is the
		// common path — the launch failure skip below covers it.
		t.Log("no system Chrome; relying on rod's managed browser")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"nope":false}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><script>
			fetch('/api/data').then(r => r.text());
			fetch('/other').then(r => r.text());
		</script></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := NewRodFetcher()
	defer func() { _ = r.Close() }()
	resp, err := r.Fetch(t.Context(), FetchRequest{
		URL:        srv.URL + "/",
		CaptureXHR: []string{"/api/"},
	})
	if err != nil {
		if strings.Contains(err.Error(), "launch browser") || strings.Contains(err.Error(), "connect browser") {
			t.Skipf("no browser available: %v", err)
		}
		t.Fatalf("fetch: %v", err)
	}
	if len(resp.XHR) != 1 {
		t.Fatalf("captures = %d (%+v), want exactly 1 (the /other XHR must not match)", len(resp.XHR), resp.XHR)
	}
	c := resp.XHR[0]
	if !strings.HasSuffix(c.URL, "/api/data") {
		t.Errorf("url = %q, want */api/data", c.URL)
	}
	if c.Status != 200 {
		t.Errorf("status = %d, want 200", c.Status)
	}
	if c.Truncated {
		t.Error("truncated, want full body")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(c.Body), &got); err != nil {
		t.Fatalf("body %q does not parse as JSON: %v", c.Body, err)
	}
	if got["ok"] != true {
		t.Errorf("body = %v, want {\"ok\":true}", got)
	}
}
