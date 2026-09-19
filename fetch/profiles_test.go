package fetch_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	impersonate "github.com/North-web-dev/impersonate-http"

	"github.com/motherlodelab/magpie/fetch"
)

type challengeHits struct {
	mu        sync.Mutex
	challenge int
	homepage  int
	cookies   []string
}

func newChallengeOrigin(t *testing.T, h *challengeHits, challengeBody string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.homepage++
		h.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "warmed"})
		_, _ = io.WriteString(w, "<html><body>home</body></html>") //nolint:errcheck // httptest local
	})
	mux.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.challenge++
		h.cookies = append(h.cookies, r.Header.Get("Cookie"))
		n := h.challenge
		h.mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, challengeBody) //nolint:errcheck // httptest local
			return
		}
		_, _ = io.WriteString(w, "<html><head><title>Real page</title></head><body><p>"+strings.Repeat("actual article content ", 50)+"</p></body></html>") //nolint:errcheck // httptest local
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/challenge"
}

func echoOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "UA=%s|CH=%s|CK=%s", r.Header.Get("User-Agent"), r.Header.Get("Sec-CH-UA"), r.Header.Get("Cookie")) //nolint:errcheck // httptest local
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchBody(t *testing.T, url string, req fetch.FetchRequest) string {
	t.Helper()
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	req.URL = url
	resp, err := f.Fetch(t.Context(), req)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return string(resp.HTML)
}

func TestProfiles_Default(t *testing.T) {
	srv := echoOrigin(t)
	body := fetchBody(t, srv.URL, fetch.FetchRequest{})
	if !strings.Contains(body, "github.com/motherlodelab/magpie/1.0") {
		t.Errorf("default profile UA = %q, want magpie", body)
	}
}

func TestProfiles_UnknownFallsBack(t *testing.T) {
	srv := echoOrigin(t)
	// "webkit" is not a profile (safari became one in Phase G) — unknown
	// names must keep falling back to the default bundle.
	body := fetchBody(t, srv.URL, fetch.FetchRequest{Profile: "webkit"})
	if !strings.Contains(body, "github.com/motherlodelab/magpie/1.0") {
		t.Errorf("unknown profile UA = %q, want default magpie", body)
	}
}

func TestProfiles_ChromeFirefox(t *testing.T) {
	srv := echoOrigin(t)
	chrome := fetchBody(t, srv.URL, fetch.FetchRequest{Profile: "chrome"})
	if !strings.Contains(chrome, "Chrome/126") || !strings.Contains(chrome, "Chromium") {
		t.Errorf("chrome profile = %q, want Chrome/126 + Sec-CH-UA value", chrome)
	}
	firefox := fetchBody(t, srv.URL, fetch.FetchRequest{Profile: "firefox"})
	if !strings.Contains(firefox, "Firefox/128") {
		t.Errorf("firefox profile = %q, want Firefox/128", firefox)
	}
	if chrome == firefox {
		t.Error("chrome and firefox profiles identical server-side")
	}
}

func TestCookies_Passthrough(t *testing.T) {
	srv := echoOrigin(t)
	body := fetchBody(t, srv.URL, fetch.FetchRequest{Cookies: "a=b; c=d"})
	if !strings.Contains(body, "a=b; c=d") {
		t.Errorf("cookie echo = %q, want verbatim passthrough", body)
	}
}

func TestIsChallengePage_Table(t *testing.T) {
	thin := strings.Repeat("x ", 100) // ~100 words
	rich := strings.Repeat("honest prose ", 300)
	cases := []struct {
		name   string
		body   string
		status int
		want   bool
	}{
		{"403 just-a-moment thin", "<html><head><title>Just a moment</title></head><body>" + thin + "</body></html>", 403, true},
		{"200 rich mentioning captcha", "<html><body>" + rich + " captcha</body></html>", 200, false},
		{"200 thin captcha marker", "<html><body>captcha verify</body></html>", 200, true},
		{"404 plain", "<html><body>not found here</body></html>", 404, false},
		{"503 thin", "<html><body>down</body></html>", 503, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fetch.IsChallengePage([]byte(c.body), c.status); got != c.want {
				t.Errorf("IsChallengePage() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestChallenge_WarmupRetry(t *testing.T) {
	var h challengeHits
	url := newChallengeOrigin(t, &h, "<html><head><title>Just a moment</title></head><body>verifying</body></html>")
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: url, Profile: "chrome"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(string(resp.HTML), "Real page") {
		t.Errorf("retry body = %q, want real page", resp.HTML)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.challenge != 2 || h.homepage != 1 {
		t.Errorf("hits challenge=%d homepage=%d, want 2/1", h.challenge, h.homepage)
	}
	if !strings.Contains(h.cookies[1], "session=warmed") {
		t.Errorf("retry cookies = %q, want warmed session", h.cookies)
	}
}

func TestChallenge_NoRetryWhenClean(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, "<html><body>"+strings.Repeat("honest prose captcha ", 100)+"</body></html>") //nolint:errcheck // httptest local; short write unactionable
	}))
	t.Cleanup(srv.Close)
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL, Profile: "chrome"}); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want exactly 1 (no retry on clean page)", hits)
	}
}

// TestChallenge_NoProfileNoRetry is SPLIT (Phase G G.2 gate widening):
// a bare request on a vendor-signature challenge now warms up + retries
// (new contract), while a status-only challenge (no vendor signature)
// keeps the old passthrough — and clean pages never warm up.
func TestChallenge_NoProfileNoRetry(t *testing.T) {
	// Vendor-signed challenge body: the NEW contract fires warmup+retry.
	var h challengeHits
	cf := newChallengeOrigin(t, &h, "<html><head><title>Just a moment</title></head><body><script>window._cf_chl_opt={chlRay:'x'}</script><p>verifying</p></body></html>")
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: cf})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.HTML), "Real page") {
		t.Errorf("retry body = %q, want real page", resp.HTML)
	}
	h.mu.Lock()
	if h.challenge != 2 || h.homepage != 1 {
		t.Errorf("hits challenge=%d homepage=%d, want 2/1 (bare + typed challenge now warms up)", h.challenge, h.homepage)
	}
	h.mu.Unlock()

	// Status-only challenge (403 + thin markers, no vendor signature):
	// old contract — passthrough, no warmup, no retry.
	var h2 challengeHits
	statusOnly := newChallengeOrigin(t, &h2, "<html><head><title>Just a moment</title></head><body>verifying</body></html>")
	resp, err = f.Fetch(t.Context(), fetch.FetchRequest{URL: statusOnly})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 passthrough (untyped challenge, bare request)", resp.StatusCode)
	}
	h2.mu.Lock()
	if h2.challenge != 1 || h2.homepage != 0 {
		t.Errorf("hits challenge=%d homepage=%d, want 1/0 (no warmup without a vendor signature)", h2.challenge, h2.homepage)
	}
	h2.mu.Unlock()

	// The retry request carries the same Profile/Browser/Cookies as the
	// original (cookie warmup only helps if the retry matches).
	var h3 challengeHits
	cookieOrigin := newChallengeOrigin(t, &h3, "<html><body><script>_cf_chl_opt=1</script>blocked</body></html>")
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: cookieOrigin, Profile: "chrome", Cookies: "a=b"}); err != nil {
		t.Fatal(err)
	}
	h3.mu.Lock()
	defer h3.mu.Unlock()
	// The retry must carry the raw Cookies AND the warmed jar session
	// (the jar is host-scoped, and 127.0.0.1 is port-agnostic per
	// RFC 6265, so earlier warmups on other local origins may already
	// appear on attempt 1 — the contract is about the retry's contents).
	if len(h3.cookies) < 2 {
		t.Fatalf("requests = %d, want challenge + retry", len(h3.cookies))
	}
	for _, want := range []string{"a=b", "session=warmed"} {
		if !strings.Contains(h3.cookies[len(h3.cookies)-1], want) {
			t.Errorf("retry cookies = %q, want %q present", h3.cookies[len(h3.cookies)-1], want)
		}
	}
}

// TestHeaderProfiles_MatchLibrary (Phase G G.3): the header bundles for
// the four new profiles must carry the library's exact User-Agent —
// drift in either direction is what DataDome-class WAFs score.
func TestHeaderProfiles_MatchLibrary(t *testing.T) {
	for _, name := range []string{"safari", "edge", "ios", "chrome_android"} {
		want := impersonate.Profiles[name].Headers.Get("User-Agent")
		got := fetch.HeaderProfiles[name]["User-Agent"]
		if got == "" {
			t.Errorf("HeaderProfiles[%q] missing User-Agent", name)
			continue
		}
		if got != want {
			t.Errorf("HeaderProfiles[%q] UA = %q, want the library profile's %q", name, got, want)
		}
	}
}

// --- Phase H: the --lang knob (Accept-Language override). ---

// langEcho reports the Accept-Language the origin actually saw.
func langEcho(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept-Language")
		_, _ = io.WriteString(w, "ok") //nolint:errcheck // httptest local
	}))
	t.Cleanup(srv.Close)
	return srv, func() string { return got }
}

func TestAcceptLanguage_BareOverride(t *testing.T) {
	srv, saw := langEcho(t)
	if _, err := fetch.NewStaticFetcher(); err != nil {
		t.Fatal(err)
	}
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL, Lang: "fr-CA,fr;q=0.9"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if saw() != "fr-CA,fr;q=0.9" {
		t.Errorf("Accept-Language = %q, want the lang value verbatim", saw())
	}
}

// The override beats the chrome profile's en-US — overriding a same-value
// default would prove nothing; chrome's distinct en-US makes it unambiguous.
func TestAcceptLanguage_ProfileOverride(t *testing.T) {
	srv, saw := langEcho(t)
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL, Profile: "chrome", Lang: "fr-CA,fr;q=0.9"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if saw() != "fr-CA,fr;q=0.9" {
		t.Errorf("Accept-Language = %q, want lang to override the chrome en-US default", saw())
	}
	// Profile still active around the override.
	var ua string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, "ok") //nolint:errcheck // httptest local
	}))
	t.Cleanup(srv2.Close)
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv2.URL, Profile: "chrome", Lang: "fr-CA,fr;q=0.9"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(ua, "Chrome/126") {
		t.Errorf("UA = %q, want chrome profile still applied", ua)
	}
}

// Empty lang keeps the profile default (chrome = en-US).
func TestAcceptLanguage_DefaultIntact(t *testing.T) {
	srv, saw := langEcho(t)
	f, err := fetch.NewStaticFetcher()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: srv.URL, Profile: "chrome"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if saw() != "en-US,en;q=0.9" {
		t.Errorf("Accept-Language = %q, want the chrome profile default", saw())
	}
}
