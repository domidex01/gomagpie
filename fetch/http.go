package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"time"

	"golang.org/x/net/publicsuffix"
)

// StaticFetcher is the spec §1.3 net/http fetcher.
type StaticFetcher struct {
	client *http.Client
	ua     string
}

var headerBundles = []map[string]string{
	{
		"User-Agent":      "magpie/1.0 (+https://github.com/you/gomagpie)",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "fr-FR,fr;q=0.9,en;q=0.8",
	},
	{
		"User-Agent":      "magpie/1.0 (+https://github.com/you/gomagpie)",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9,fr;q=0.8",
	},
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
	return &StaticFetcher{client: client, ua: headerBundles[0]["User-Agent"]}, nil
}

// CanHandle is always true for the static fetcher.
func (s *StaticFetcher) CanHandle(req FetchRequest) bool { return true }

// Close is a no-op (no persistent resources).
func (s *StaticFetcher) Close() error { return nil }

// Fetch performs one GET with a per-attempt timeout budget.
func (s *StaticFetcher) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("fetch: empty URL")
	}
	timeout := budget(req)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	b := headerBundles[0]
	for k, v := range b {
		hreq.Header.Set(k, v)
	}
	resp, err := s.client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
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
