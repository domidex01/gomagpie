package crawl

import (
	"math"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const floatEps = 1e-9

func limitNear(t *testing.T, got rate.Limit, want float64, label string) {
	t.Helper()
	if math.Abs(float64(got)-want) > floatEps {
		t.Errorf("%s: Limit() = %v, want %v", label, float64(got), want)
	}
}

// TestHostLimiters_AutoBackoff — US-3 math table, asserted via Limit()
// (Report applies the delay eagerly via SetLimit, which is what makes
// Limit a valid oracle).
func TestHostLimiters_AutoBackoff(t *testing.T) {
	h := NewHostLimiters(1, 3) // base delay 1s
	h.SetAuto()

	limitNear(t, h.Limit("h"), 1, "base")

	// Two 429s double the delay twice: 1s → 2s → 4s (Limit 0.25).
	h.Report("h", 429, 0)
	limitNear(t, h.Limit("h"), 0.5, "after one 429")
	h.Report("h", 429, 0)
	limitNear(t, h.Limit("h"), 0.25, "after two 429s")

	// An explicit Retry-After sets the delay exactly (10s → Limit 0.1).
	h.Report("h", 429, 10*time.Second)
	limitNear(t, h.Limit("h"), 0.1, "Retry-After 10s exact")

	// Five 2xx decay ×¾ each: monotonic, never below the 1s floor.
	prev := 10 * time.Second
	for i := 0; i < 5; i++ {
		h.Report("h", 200, 0)
		d := time.Duration(float64(time.Second) / float64(h.Limit("h")))
		if d >= prev {
			t.Errorf("decay step %d: delay %v not below previous %v", i, d, prev)
		}
		if d < time.Second {
			t.Errorf("decay step %d: delay %v below 1s floor", i, d)
		}
		prev = d
	}
}

// TestHostLimiters_AutoCap — 31 block reports cap at 60s (Limit 1/60),
// and an explicit Retry-After caps at 5m.
func TestHostLimiters_AutoCap(t *testing.T) {
	h := NewHostLimiters(1, 3)
	h.SetAuto()
	for i := 0; i < 31; i++ {
		h.Report("b", 500, 0)
	}
	limitNear(t, h.Limit("b"), 1.0/60, "60s delay cap")

	h.Report("b", 429, 10*time.Minute)
	limitNear(t, h.Limit("b"), 1.0/300, "Retry-After 5m cap")
}

// TestHostLimiters_AutoSignalMatrix — 503 and 500 both back off; 3xx and
// plain 4xx are no-signals (neither backoff nor decay).
func TestHostLimiters_AutoSignalMatrix(t *testing.T) {
	h := NewHostLimiters(1, 3)
	h.SetAuto()
	for _, code := range []int{500, 502, 503, 504, 408} {
		h.Report("m", code, 0)
	}
	// 408 is a no-signal; the four 5xx doubled 1s four times → 16s.
	limitNear(t, h.Limit("m"), 1.0/16, "four 5xx backoff, 408 ignored")

	h.Report("m", 301, 0)
	h.Report("m", 404, 0)
	limitNear(t, h.Limit("m"), 1.0/16, "3xx/4xx no-signal")
}

// TestHostLimiters_AutoDecayFloor — with a crawl-delay floor of 5s the
// delay starts AT the floor and decay never crosses below it.
func TestHostLimiters_AutoDecayFloor(t *testing.T) {
	h := NewHostLimiters(1, 3)
	h.SetAuto()
	h.SetFloor("f", 5*time.Second)
	limitNear(t, h.Limit("f"), 1.0/5, "SetFloor applied")

	for i := 0; i < 4; i++ {
		h.Report("f", 200, 0)
		limitNear(t, h.Limit("f"), 1.0/5, "decay pinned at crawl-delay floor")
	}
}

// TestHostLimiters_AutoOffNoop — without SetAuto, Report is a no-op.
func TestHostLimiters_AutoOffNoop(t *testing.T) {
	h := NewHostLimiters(1, 3)
	h.Report("n", 429, 0)
	h.Report("n", 429, 30*time.Second)
	h.Report("n", 200, 0)
	limitNear(t, h.Limit("n"), 1, "auto-off Report must not touch the limiter")
}
