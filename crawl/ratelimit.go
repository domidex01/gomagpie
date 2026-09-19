package crawl

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// HostLimiters holds one token bucket per host. Default 1 req/s burst 3.
// With SetAuto, Report adaptively paces per-host delay: ×2 on 429/5xx,
// decay on success — future requests only (FetchWithRetry handles the
// same-request retries).
type HostLimiters struct {
	mu    sync.Mutex
	m     map[string]*rate.Limiter
	rps   float64
	burst int
	auto  bool
	// auto-only state: adaptive delay and crawl-delay floor per host.
	delays map[string]time.Duration
	floors map[string]time.Duration
}

// NewHostLimiters builds limiters with the given per-host rate.
func NewHostLimiters(rps float64, burst int) *HostLimiters {
	if rps <= 0 {
		rps = 1
	}
	if burst <= 0 {
		burst = 3
	}
	return &HostLimiters{m: map[string]*rate.Limiter{}, rps: rps, burst: burst,
		delays: map[string]time.Duration{}, floors: map[string]time.Duration{}}
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
// With auto on, d also becomes the decay floor (auto never goes faster
// than robots asks).
func (h *HostLimiters) SetFloor(host string, d time.Duration) {
	if d <= 0 {
		return
	}
	floor := rate.Every(d)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.auto {
		h.floors[host] = d
	}
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

// Caps for adaptive pacing: the backoff ceiling and the ceiling for an
// explicit Retry-After instruction (server-quoted, so allowed higher).
const (
	maxAutoDelay     = 60 * time.Second
	maxAutoRetryWait = 5 * time.Minute
)

// SetAuto enables adaptive pacing (called once per crawl).
func (h *HostLimiters) SetAuto() {
	h.mu.Lock()
	h.auto = true
	h.mu.Unlock()
}

// Report feeds the adaptive delay (no-op unless SetAuto): the delay
// doubles on 429/5xx (cap 60s), an explicit Retry-After wins over the
// guess (cap 5m), and 2xx decays ×3/4 toward the floor — max(configured
// 1/rps, crawl-delay). The new delay applies EAGERLY via SetLimit so
// Limit() reflects it (testable; the Wait call site stays untouched).
func (h *HostLimiters) Report(host string, code int, retryAfter time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.auto {
		return
	}
	lim, ok := h.m[host]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(h.rps), h.burst)
		h.m[host] = lim
	}
	floor := time.Duration(float64(time.Second) / h.rps)
	if f, ok := h.floors[host]; ok && f > floor {
		floor = f
	}
	delay, ok := h.delays[host]
	if !ok || delay < floor {
		delay = floor // auto starts from the floored rate
	}
	switch {
	case code == 429 || code/100 == 5:
		delay *= 2
		if delay > maxAutoDelay {
			delay = maxAutoDelay
		}
		if retryAfter > 0 {
			delay = min(retryAfter, maxAutoRetryWait)
		}
	case code/100 == 2:
		delay = delay * 3 / 4
		if delay < floor {
			delay = floor // decay approaches the floor, never below
		}
	default:
		return // 3xx / non-429 4xx: no block signal, no success signal
	}
	h.delays[host] = delay
	lim.SetLimit(rate.Every(delay))
}
