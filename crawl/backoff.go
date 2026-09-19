package crawl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"magpie/fetch"

	"github.com/cenkalti/backoff/v5"
)

// FetchWithRetry runs do with exponential backoff+jitter. A fresh
// ExponentialBackOff is built per call — the type is stateful and must
// never be shared across workers.
func FetchWithRetry(ctx context.Context, do func() (*fetch.FetchResponse, error)) (*fetch.FetchResponse, error) {
	return fetchWithRetry(ctx, do, 4, 500*time.Millisecond, 30*time.Second)
}

// fetchWithRetry is the testable core (ms-scale backoff in tests).
func fetchWithRetry(ctx context.Context, do func() (*fetch.FetchResponse, error), maxTries uint, initial, maxInterval time.Duration) (*fetch.FetchResponse, error) {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = initial
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.5
	b.MaxInterval = maxInterval
	op := func() (*fetch.FetchResponse, error) {
		if err := ctx.Err(); err != nil {
			return nil, backoff.Permanent(err)
		}
		resp, err := do()
		return resp, classify(resp, err)
	}
	return backoff.Retry(ctx, op,
		backoff.WithBackOff(b),
		backoff.WithMaxTries(maxTries),
		backoff.WithMaxElapsedTime(2*time.Minute),
	)
}

// classify maps a fetch outcome to nil (success), backoff.Permanent
// (fail fast), backoff.RetryAfter (429/503 with parseable header), or the
// raw error (retryable with normal exponential wait).
func classify(resp *fetch.FetchResponse, err error) error {
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsTemporary {
			return err // retryable
		}
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			return err // retryable
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return backoff.Permanent(err)
		}
		// Transport errors (reset, refused mid-crawl) are retryable.
		return err
	}
	if resp == nil {
		return fmt.Errorf("crawl: nil response")
	}
	switch resp.StatusCode {
	case 400, 401, 403, 404, 410:
		return backoff.Permanent(fmt.Errorf("crawl: fetch HTTP %d", resp.StatusCode))
	case 429, 503:
		if d, ok := retryAfterOf(resp.Headers); ok {
			return backoff.RetryAfter(int(d / time.Second))
		}
		return fmt.Errorf("crawl: fetch HTTP %d", resp.StatusCode)
	case 408, 500, 502, 504:
		return fmt.Errorf("crawl: fetch HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("crawl: fetch HTTP %d", resp.StatusCode)
}

// retryAfterOf parses an integer-seconds Retry-After header; ok=false
// when absent or unparseable (HTTP-date form is ignored — classify's
// long-standing contract). Shared by classify and AutoThrottle's Report
// wiring — one parser, no drift.
func retryAfterOf(h http.Header) (time.Duration, bool) {
	secs, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}
