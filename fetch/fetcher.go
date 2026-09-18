package fetch

import (
	"context"
	"net/http"
	"time"
)

// FetchRequest is a single fetch job.
type FetchRequest struct {
	URL     string
	Timeout time.Duration // per-attempt budget; 0 = 20s default
	Profile string        // header profile: default|chrome|firefox (unknown = default)
	Browser string        // TLS fingerprint: chrome|firefox|random ("" = stock TLS)
	Cookies string        // raw Cookie header value, passed through verbatim
	// Lang overrides the profile's Accept-Language verbatim (no BCP47
	// policing — validation rejects control characters at the options
	// boundary). Rod sets it as a page-level extra header.
	Lang string
}

// FetchResponse is the fetched page.
type FetchResponse struct {
	URL        string
	FinalURL   string
	StatusCode int
	HTML       []byte
	Headers    http.Header
	// Proxy is the redacted host:port of the pool/MAGPIE_PROXY entry
	// that served this response; "" when direct. Pools rotate per
	// request (and per redirect hop), so this is the entry that served
	// the final hop — a rotation audit trail it is not.
	Proxy string
}

// Fetcher fetches one URL. Rod lives behind this interface.
type Fetcher interface {
	Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error)
	CanHandle(req FetchRequest) bool
	Close() error
}

func budget(req FetchRequest) time.Duration {
	if req.Timeout > 0 {
		return req.Timeout
	}
	return 20 * time.Second
}
