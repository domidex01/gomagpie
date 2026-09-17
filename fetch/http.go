package fetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// StaticFetcher is the spec §1.3 net/http fetcher.
type StaticFetcher struct {
	client   *http.Client
	browsers browserClients // per-profile impersonating clients (fetch/utls.go)
	jar      http.CookieJar // shared stock + browser so warmup cookies carry over
	ua       string
	ssrf     SSRFOptions
}

// defaultHeaders is the single static-fetch header bundle (spec §1.3).
var defaultHeaders = map[string]string{
	"User-Agent":      "magpie/1.0 (+https://github.com/you/gomagpie)",
	"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	"Accept-Language": "fr-FR,fr;q=0.9,en;q=0.8",
}

// GuardedTransport builds the shared transport with the SSRF dial guard,
// proxy resolution, and the file:// handler. Used by the static fetcher
// AND the crawl robots Checker so every outbound connection honors the
// same policy. Options come from the environment/test-binary hatch; see
// NewStaticFetcherWithOptions for explicit options.
// Phase E note: a uTLS transport swap must preserve the DialContext peer
// check and the Proxy func — they are the SSRF/egress policy, not
// incidentals of the stock transport.
func GuardedTransport() *http.Transport {
	return guardedTransport(resolvedSSRFOptions())
}

func guardedTransport(o SSRFOptions) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy:                 proxyFunc,
		DialContext:           guardedDialFunc(o, dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
	}
	transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	return transport
}

// guardedDialFunc is the raw-TCP dial behind both the stock transport
// and Phase E's browser dial: plain dial + post-connect peer check. The
// check is the TOCTOU backstop shared by every transport we run.
func guardedDialFunc(o SSRFOptions, dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// GOMAGPIE_PROXY set → this peer is the operator-configured
		// proxy (trusted egress, often localhost/internal), not the
		// SSRF target: the policy applies to target URLs via
		// ValidateURL + CheckRedirect, and NO_PROXY-exempt hosts are
		// equally operator-chosen (curl semantics).
		proxied := os.Getenv("GOMAGPIE_PROXY") != ""
		if !dialPeerAllowed(conn.RemoteAddr(), o, proxied) {
			_ = conn.Close() //nolint:errcheck // rejection path; close error unactionable
			return nil, ssrfErr("fetch: dial peer %s is not a public address (DNS rebind?)", conn.RemoteAddr())
		}
		return conn, nil
	}
}

// dialPeerAllowed re-checks the CONNECTED peer address, closing the TOCTOU
// window between ValidateURL's DNS answer and the actual socket: no HTTP
// byte is written before this passes. Proxied connections skip the check —
// their peer is the trusted proxy, and the proxy (not us) resolves the
// target, so a post-connect check tells us nothing about the target anyway.
func dialPeerAllowed(addr net.Addr, o SSRFOptions, proxied bool) bool {
	if proxied || o.AllowPrivate {
		return true
	}
	ip, ok := addrIP(addr)
	return ok && isPublicIP(ip)
}

// addrIP extracts the IP from a dial peer. http.Transport only dials tcp,
// so anything else fails closed.
func addrIP(addr net.Addr) (netip.Addr, bool) {
	ta, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ta.IP)
	return a, ok
}

// proxyFunc resolves the proxy per request: GOMAGPIE_PROXY (http(s) URL,
// validated loudly) wins over the standard HTTP(S)_PROXY environment,
// with a minimal NO_PROXY exact/dot-suffix bypass. Read per request so
// env changes take effect without rebuilding the transport.
func proxyFunc(req *http.Request) (*url.URL, error) {
	return proxyForHost(req.URL.Hostname())
}

// proxyForHost is proxyFunc keyed by bare hostname — the browser dial
// needs the tunnel decision before any URL object exists.
func proxyForHost(hostname string) (*url.URL, error) {
	if v := strings.TrimSpace(os.Getenv("GOMAGPIE_PROXY")); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("fetch: bad GOMAGPIE_PROXY %q: want http(s)://host:port", v)
		}
		if noProxyMatch(hostname) {
			return nil, nil
		}
		return u, nil
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: hostname}}
	return http.ProxyFromEnvironment(req)
}

// noProxyMatch: comma-separated NO_PROXY entries, exact or dot-suffix
// host match, "*" bypasses everything. Deliberately dumb (no CIDR, no
// port matching) — same shape as the plan spec.
func noProxyMatch(host string) bool {
	np := os.Getenv("NO_PROXY")
	if np == "" {
		np = os.Getenv("no_proxy")
	}
	for _, e := range strings.Split(np, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" || host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}

// homepageOf returns scheme://host/ for warmup, or "" for non-http URLs.
func homepageOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}

// CanHandle is always true for the static fetcher.
func (s *StaticFetcher) CanHandle(req FetchRequest) bool { return true }

// Close drains the stock transport's idle connections. The browser
// clients' h2 connections have no CloseIdleConnections passthrough in
// the wrapper — ponytail: they die with the process; ceiling is CLI-
// lifetime connections only, hence --browser documented scrape-first.
func (s *StaticFetcher) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// Fetch performs one GET with a per-attempt timeout budget. On a
// challenge-classified response with a non-empty profile, it warms the
// cookie jar with one homepage GET then retries the original URL exactly
// once, returning whatever arrives (success or final failure — no loops).
func (s *StaticFetcher) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("fetch: empty URL")
	}
	resp, err := s.do(ctx, req)
	if err != nil {
		return nil, err
	}
	// Warmup fires for profiles AND browser fingerprints: --browser
	// requests are exactly the blocked-page case, so gating on Profile
	// alone would make --browser useless against the sites it exists for.
	if req.Profile == "" && req.Browser == "" || !IsChallengePage(resp.HTML, resp.StatusCode) {
		return resp, nil
	}
	if home := homepageOf(req.URL); home != "" {
		_, _ = s.do(ctx, FetchRequest{URL: home, Timeout: req.Timeout, Profile: req.Profile, Cookies: req.Cookies, Browser: req.Browser}) //nolint:errcheck // warmup best-effort; retry proceeds regardless
	}
	retry, rerr := s.do(ctx, req)
	if rerr != nil {
		return nil, rerr
	}
	return retry, nil
}

func (s *StaticFetcher) do(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	// Pre-dial SSRF gate: a rejected URL never opens a socket (the strict
	// fetcher test proves the origin's hit counter stays 0). go-rod
	// escalation only runs on content already fetched through this client,
	// so a blocked URL never reaches the browser.
	if err := ValidateURL(ctx, req.URL, nil, s.ssrf); err != nil {
		return nil, err
	}
	timeout := budget(req)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := s.clientFor(req)
	if err != nil {
		return nil, err
	}
	browserPath := client != s.client
	hreq, err := http.NewRequestWithContext(cctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	// Effective headers: an impersonated https request without an explicit
	// profile gets NO headers from us — the wrapper injects its matching
	// browser set (a stale Phase-A UA next to a fresh hello is a fingerprint
	// tell). An explicit Profile is the caller's override and wins.
	// Cleartext never reaches the wrapper, so cleartext+browser falls back
	// to our matching header profile (a real UA beats none).
	headers := profileHeaders(req.Profile)
	if browserPath && req.Profile == "" {
		headers = nil
	} else if !browserPath && req.Profile == "" && req.Browser != "" {
		if p, ok := resolveBrowser(req.Browser); ok {
			headers = profileHeaders(p.Name)
		}
	}
	for k, v := range headers {
		hreq.Header.Set(k, v)
	}
	cookies := req.Cookies
	if cookies != "" {
		hreq.Header.Set("Cookie", cookies)
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // body fully read above; close error unactionable
	// 50 MB cap on the DECODED stream: a gzip bomb (50 KB on the wire →
	// 60 MB+ decoded) is truncated at exactly the cap. Pinned by
	// TestStaticGzipBomb; if the cap changes intentionally, that test
	// should be updated with it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, fmt.Errorf("fetch: read body: %w", err)
	}
	// Browser-path responses arrive encoded: the wrapper's h2 never
	// decompresses and its h1 sees the profile's Accept-Encoding. Decode
	// here (re-capped) so callers always see plain bytes.
	if browserPath {
		if body, err = decodeBody(body, resp.Header); err != nil {
			return nil, fmt.Errorf("fetch: decode body: %w", err)
		}
	}
	finalURL := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &FetchResponse{
		URL:        req.URL,
		FinalURL:   finalURL,
		StatusCode: resp.StatusCode,
		HTML:       body,
		Headers:    resp.Header,
	}, nil
}
