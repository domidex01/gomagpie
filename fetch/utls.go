package fetch

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	impersonate "github.com/North-web-dev/impersonate-http"
)

// browserNames is the rotation pool for "random" — every profile the
// library ships (chrome_android rides HelloChrome_Auto with a mobile UA).
var browserNames = []string{"chrome", "chrome_android", "firefox", "safari", "edge", "ios"}

// BrowserHelp is the canonical --browser value list, shared by every
// help text and error message so the union can't drift per surface.
const BrowserHelp = "chrome|firefox|safari|edge|ios|chrome_android|random"

// randomBrowser resolves "random" exactly once per process — flipping
// hello mid-run would look like a fingerprint anomaly. ponytail:
// per-request rotation is the upgrade path if ever needed.
var randomBrowser = sync.OnceValue(func() string {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "chrome"
	}
	return browserNames[int(b[0])%len(browserNames)]
})

// ValidBrowser reports whether name is an accepted --browser value
// (the same set resolveBrowser executes). The library's Profiles map is
// the source of truth, so a library bump can't drift the union.
func ValidBrowser(name string) bool {
	if name == "" || name == "random" {
		return true
	}
	_, ok := impersonate.Profiles[name]
	return ok
}

// resolveBrowser maps a Browser option to an impersonate profile. The
// accepted set is exactly the library's Profiles keys plus "random";
// anything else fails so typos never silently fall back to a stock
// fingerprint.
func resolveBrowser(name string) (impersonate.Profile, bool) {
	if name == "random" {
		p, _ := impersonate.Profiles[randomBrowser()]
		return p, true
	}
	p, ok := impersonate.Profiles[name]
	return p, ok
}

// browserClients caches one impersonating http.Client per resolved
// profile name so uTLS/h2 connections pool across a run. One map, one
// implementation — no interface.
type browserClients struct {
	mu sync.Mutex
	m  map[string]*http.Client
}

func (s *StaticFetcher) browserClient(name string) (*http.Client, error) {
	s.browsers.mu.Lock()
	defer s.browsers.mu.Unlock()
	if s.browsers.m == nil {
		s.browsers.m = map[string]*http.Client{}
	}
	if c, ok := s.browsers.m[name]; ok {
		return c, nil
	}
	p, ok := resolveBrowser(name)
	if !ok {
		return nil, fmt.Errorf("fetch: browser %q must be %s", name, BrowserHelp)
	}
	c := &http.Client{
		Transport:     impersonate.NewTransport(p, impersonate.WithDialer(browserDial(s.ssrf))),
		Jar:           s.jar,
		Timeout:       30 * time.Second,
		CheckRedirect: redirectGuard(s.ssrf),
	}
	s.browsers.m[name] = c
	return c, nil
}

// clientFor picks the transport for one request. The impersonation
// transport TLS-handshakes unconditionally (its RoundTrip never speaks
// cleartext), so non-https URLs always take the stock client — cleartext
// + browser falls back to stock with our matching header profile.
func (s *StaticFetcher) clientFor(req FetchRequest) (*http.Client, error) {
	if req.Browser == "" {
		return s.client, nil
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" {
		return s.client, nil
	}
	return s.browserClient(req.Browser)
}

// browserDial is the raw-TCP dial under the uTLS handshake. Proxied
// hosts tunnel via CONNECT (trusted peer, no IP check — same semantics
// as the stock transport under MAGPIE_PROXY); everything else gets the
// guarded dial + post-connect peer-IP check before any TLS byte.
func browserDial(o SSRFOptions) impersonate.DialFunc {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	guarded := guardedDialFunc(o, dialer)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		u, perr := proxyForHost(host)
		if perr != nil {
			return nil, perr
		}
		if u != nil {
			d, derr := impersonate.ProxyDialer(u.String())
			if derr != nil {
				return nil, derr
			}
			return d(ctx, network, addr)
		}
		return guarded(ctx, network, addr)
	}
}
