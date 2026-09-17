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

// randomBrowser resolves "random" exactly once per process — flipping
// hello mid-run would look like a fingerprint anomaly. ponytail:
// per-request rotation is the upgrade path if ever needed.
var randomBrowser = sync.OnceValue(func() string {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil || b[0]%2 == 0 {
		return "chrome"
	}
	return "firefox"
})

// ValidBrowser reports whether name is an accepted --browser value
// (the same literal set resolveBrowser executes). Validation lives here
// so scrape, crawl, batch, and the CLI edges can't drift apart.
func ValidBrowser(name string) bool {
	switch name {
	case "", "chrome", "firefox", "random":
		return true
	}
	return false
}

// resolveBrowser maps a Browser option to an impersonate profile. The
// accepted set is exactly chrome|firefox|random; anything else fails so
// typos never silently fall back to a stock fingerprint.
func resolveBrowser(name string) (impersonate.Profile, bool) {
	switch name {
	case "chrome", "firefox":
		return impersonate.Profiles[name], true
	case "random":
		p, _ := impersonate.Profiles[randomBrowser()]
		return p, true
	}
	return impersonate.Profile{}, false
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
		return nil, fmt.Errorf("fetch: browser %q must be chrome|firefox|random", name)
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
// as the stock transport under GOMAGPIE_PROXY); everything else gets the
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
