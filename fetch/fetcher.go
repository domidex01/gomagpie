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
}

// FetchResponse is the fetched page.
type FetchResponse struct {
	URL        string
	FinalURL   string
	StatusCode int
	HTML       []byte
	Headers    http.Header
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
