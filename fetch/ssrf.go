package fetch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// SSRFOptions configures the fetch-side security guard. The zero value is
// strict: only public http(s) hosts. Both flags exist for explicit opt-in
// and for the test-binary hatch (see resolvedSSRFOptions) that keeps the
// hermetic suite's 100+ httptest/file call sites green; production
// binaries always resolve to the strict zero value.
type SSRFOptions struct {
	AllowPrivate bool // allow loopback/private/link-local literal and resolved hosts
	AllowFile    bool // allow file:// URLs (set via MAGPIE_ALLOW_FILE=1 in production)
}

// LookupFunc resolves a hostname to IPs; nil means
// net.DefaultResolver.LookupIP. Injectable so ValidateURL tests stay
// hermetic (no real DNS).
type LookupFunc func(ctx context.Context, host string) ([]net.IP, error)

// ErrPrivateAddress is the sentinel wrapped by every SSRF rejection; the
// CLI maps it to exit 2 (usage error) via errors.Is.
var ErrPrivateAddress = errors.New("fetch: non-public address")

func ssrfErr(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrPrivateAddress)...)
}

// ValidateURL enforces the default-deny egress policy on raw:
// scheme (http/https, file only when allowed), hostname blocklist,
// IP-literal predicate, and — for names — every resolved IP must pass the
// same public predicate. DNS rebinds are caught here AND again post-dial
// (peer check in guardedTransport). lookup nil = default resolver, run
// with ctx (network I/O must honor the caller's deadline).
// Every rejection wraps ErrPrivateAddress.
func ValidateURL(ctx context.Context, raw string, lookup LookupFunc, o SSRFOptions) error {
	u, err := url.Parse(raw)
	if err != nil {
		return ssrfErr("fetch: bad url %q (%v)", raw, err)
	}
	switch u.Scheme {
	case "http", "https":
	case "file":
		if !o.AllowFile {
			return ssrfErr("fetch: file:// blocked (set MAGPIE_ALLOW_FILE=1 to allow)")
		}
		return nil
	default:
		return ssrfErr("fetch: scheme %q not allowed", u.Scheme)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return ssrfErr("fetch: %q has no host", raw)
	}
	if blockedHost(host) {
		return ssrfErr("fetch: host %q is a blocked metadata/loopback name", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !o.AllowPrivate && !isPublicIP(ip) {
			return ssrfErr("fetch: address %q is not a public IP", host)
		}
		return nil // literal: no DNS
	}
	if o.AllowPrivate {
		return nil // private-net checks (literal + resolved) are relaxed
	}
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		// Fail closed: unresolvable ≠ public.
		return ssrfErr("fetch: resolve %q: %v", host, err)
	}
	if len(addrs) == 0 {
		return ssrfErr("fetch: %q resolved to no addresses", host)
	}
	for _, ip := range addrs {
		a, ok := netip.AddrFromSlice(ip)
		if !ok || !isPublicIP(a) {
			return ssrfErr("fetch: %q resolves to non-public address", host)
		}
	}
	return nil
}

// blockedHost rejects the loopback/metadata hostnames that never appear
// in public crawls. Short, documented list: IP-level tricks are covered
// by the literal predicate above.
func blockedHost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal" || strings.HasSuffix(host, ".metadata.google.internal")
}

// isPublicIP is the single IP predicate: global unicast minus everything
// private/loopback/link-local/multicast/unspecified. Explicit membership
// checks (not IsGlobalUnicast alone — its link-local handling differs
// between net.IP and netip) so cloud metadata (169.254.169.254) is always
// rejected. Phase E's transport swap must key on this same predicate.
func isPublicIP(a netip.Addr) bool {
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() ||
		a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return false
	}
	return true
}

// isTestBinary reports whether we're running under `go test`. The hatch:
// test binaries auto-relax private-net + file checks so the existing
// httptest/file suite needs zero churn. Test binaries never ship; the
// production binary is always strict. MAGPIE_STRICT_SSRF=1 forces the
// strict path even under go test (lets end-to-end tests exercise it).
func isTestBinary() bool {
	if os.Getenv("MAGPIE_STRICT_SSRF") == "1" {
		return false
	}
	return flag.Lookup("test.v") != nil
}

func resolvedSSRFOptions() SSRFOptions {
	return SSRFOptions{
		AllowPrivate: isTestBinary(),
		AllowFile:    isTestBinary() || os.Getenv("MAGPIE_ALLOW_FILE") == "1",
	}
}

// NewStaticFetcher builds the default fetcher: strict in production,
// hatch-relaxed under go test (or MAGPIE_ALLOW_FILE=1 for file://).
func NewStaticFetcher() (*StaticFetcher, error) {
	return NewStaticFetcherWithOptions(resolvedSSRFOptions())
}

// NewStaticFetcherWithOptions builds a fetcher with explicit SSRF
// options — the constructor security tests must use, so no test can pass
// vacuously by riding the test-binary hatch.
func NewStaticFetcherWithOptions(o SSRFOptions) (*StaticFetcher, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("fetch: cookiejar: %w", err)
	}
	client := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     guardedTransport(o),
		Jar:           jar,
		CheckRedirect: redirectGuard(o),
	}
	// The jar is shared with Phase E's browser clients so challenge-warmup
	// cookies carry over between the stock and impersonation paths.
	return &StaticFetcher{client: client, jar: jar, ua: defaultHeaders["User-Agent"], ssrf: o}, nil
}

// redirectGuard validates every redirect hop against the SSRF policy.
// Shared by the stock client and Phase E's browser (uTLS) client so a
// redirect chain can never smuggle either transport onto a private host.
func redirectGuard(o SSRFOptions) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if err := ValidateURL(req.Context(), req.URL.String(), nil, o); err != nil {
			return fmt.Errorf("fetch: redirect to %s: %w", req.URL.Host, err)
		}
		return nil
	}
}
