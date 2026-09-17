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
	client *http.Client
	ua     string
	ssrf   SSRFOptions
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
		Proxy: proxyFunc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if !peerAllowed(conn.RemoteAddr(), o) {
				_ = conn.Close() //nolint:errcheck // rejection path; close error unactionable
				return nil, ssrfErr("fetch: dial peer %s is not a public address (DNS rebind?)", conn.RemoteAddr())
			}
			return conn, nil
		},
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

// peerAllowed re-checks the CONNECTED peer address, closing the TOCTOU
// window between ValidateURL's DNS answer and the actual socket: no HTTP
// byte is written before this passes.
func peerAllowed(addr net.Addr, o SSRFOptions) bool {
	if o.AllowPrivate {
		return true
	}
	ip, ok := addrIP(addr)
	return ok && isPublicIP(ip)
}

func addrIP(addr net.Addr) (netip.Addr, bool) {
	if ta, ok := addr.(*net.TCPAddr); ok {
		a, ok := netip.AddrFromSlice(ta.IP)
		return a, ok
	}
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr(), true
}

// proxyFunc resolves the proxy per request: GOMAGPIE_PROXY (http(s) URL,
// validated loudly) wins over the standard HTTP(S)_PROXY environment,
// with a minimal NO_PROXY exact/dot-suffix bypass. Read per request so
// env changes take effect without rebuilding the transport.
func proxyFunc(req *http.Request) (*url.URL, error) {
	if v := strings.TrimSpace(os.Getenv("GOMAGPIE_PROXY")); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("fetch: bad GOMAGPIE_PROXY %q: want http(s)://host:port", v)
		}
		if noProxyMatch(req.URL.Hostname()) {
			return nil, nil
		}
		return u, nil
	}
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

// Close is a no-op (no persistent resources).
func (s *StaticFetcher) Close() error { return nil }

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
	if req.Profile == "" || !IsChallengePage(resp.HTML, resp.StatusCode) {
		return resp, nil
	}
	if home := homepageOf(req.URL); home != "" {
		_, _ = s.do(ctx, FetchRequest{URL: home, Timeout: req.Timeout, Profile: req.Profile, Cookies: req.Cookies}) //nolint:errcheck // warmup best-effort; retry proceeds regardless
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
	if err := ValidateURL(req.URL, nil, s.ssrf); err != nil {
		return nil, err
	}
	timeout := budget(req)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	for k, v := range profileHeaders(req.Profile) {
		hreq.Header.Set(k, v)
	}
	if req.Cookies != "" {
		hreq.Header.Set("Cookie", req.Cookies)
	}
	resp, err := s.client.Do(hreq)
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
