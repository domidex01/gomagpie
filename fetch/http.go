package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"golang.org/x/net/publicsuffix"
)

// StaticFetcher is the spec §1.3 net/http fetcher.
type StaticFetcher struct {
	client *http.Client
	ua     string
}

// defaultHeaders is the single static-fetch header bundle (spec §1.3).
var defaultHeaders = map[string]string{
	"User-Agent":      "magpie/1.0 (+https://github.com/you/gomagpie)",
	"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	"Accept-Language": "fr-FR,fr;q=0.9,en;q=0.8",
}

// NewStaticFetcher builds the spec §1.3 transport verbatim, plus a file://
// transport so local fixtures scrape hermetically.
func NewStaticFetcher() (*StaticFetcher, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("fetch: cookiejar: %w", err)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
	}
	transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
	return &StaticFetcher{client: client, ua: defaultHeaders["User-Agent"]}, nil
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
