package crawl

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// HostLimiters holds one token bucket per host. Default 1 req/s burst 3.
type HostLimiters struct {
	mu    sync.Mutex
	m     map[string]*rate.Limiter
	rps   float64
	burst int
}

// NewHostLimiters builds limiters with the given per-host rate.
func NewHostLimiters(rps float64, burst int) *HostLimiters {
	if rps <= 0 {
		rps = 1
	}
	if burst <= 0 {
		burst = 3
	}
	return &HostLimiters{m: map[string]*rate.Limiter{}, rps: rps, burst: burst}
}

// Wait blocks until the host bucket admits a request.
func (h *HostLimiters) Wait(ctx context.Context, host string) error {
	h.mu.Lock()
	lim, ok := h.m[host]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(h.rps), h.burst)
		h.m[host] = lim
	}
	h.mu.Unlock()
	return lim.Wait(ctx) //nolint:wrapcheck // rate error is already contextual
}

// SetFloor lowers the host rate to at most 1/d (crawl-delay); never raises.
func (h *HostLimiters) SetFloor(host string, d time.Duration) {
	if d <= 0 {
		return
	}
	floor := rate.Every(d)
	h.mu.Lock()
	defer h.mu.Unlock()
	lim, ok := h.m[host]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(h.rps), h.burst)
		h.m[host] = lim
	}
	if floor < lim.Limit() {
		lim.SetLimit(floor)
	}
}

// Limit reports the current host limit (test helper).
func (h *HostLimiters) Limit(host string) rate.Limit {
	h.mu.Lock()
	defer h.mu.Unlock()
	if lim, ok := h.m[host]; ok {
		return lim.Limit()
	}
	return rate.Limit(h.rps)
}
