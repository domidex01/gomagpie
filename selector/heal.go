package selector

import (
	"sync"
)

// Healer tracks per-field null outcomes over a sliding window (default 50,
// trigger 0.30) and retains the last N validated samples per domain. The
// sample ring doubles as the cold-start collector: on a cache miss the
// extract worker runs direct LLM (Purpose:"synth") and Retains each page
// whose record validated with every required field non-null.
type Healer struct {
	mu        sync.Mutex
	window    int
	threshold float64
	maxRetain int
	rings     map[string][]bool // field -> last-window outcomes (true=null)
	samples   []SynthSample
}

// NewHealer builds a Healer.
func NewHealer(window int, threshold float64, maxRetain int) *Healer {
	if window <= 0 {
		window = 50
	}
	if threshold <= 0 {
		threshold = 0.30
	}
	if maxRetain <= 0 {
		maxRetain = 3
	}
	return &Healer{window: window, threshold: threshold, maxRetain: maxRetain, rings: map[string][]bool{}}
}

// Observe records one field outcome; returns the field name when its null
// rate crosses the threshold (caller triggers re-synthesis for it). A
// minimum evidence floor (window/5) keeps single-fluke nulls from healing.
func (h *Healer) Observe(field string, wasNull bool) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ring := append(h.rings[field], wasNull)
	if len(ring) > h.window {
		ring = ring[len(ring)-h.window:]
	}
	h.rings[field] = ring
	nulls := 0
	for _, b := range ring {
		if b {
			nulls++
		}
	}
	minEvidence := h.window / 5
	if minEvidence < 1 {
		minEvidence = 1
	}
	if len(ring) >= minEvidence && float64(nulls)/float64(len(ring)) > h.threshold {
		// Reset the ring so a still-broken field re-triggers only after a
		// fresh window of evidence, not on every subsequent page.
		h.rings[field] = nil
		return []string{field}
	}
	return nil
}

// FullResynth reports whether broken/total broken fields means a full
// redesign (≥50% broken → re-synthesize the whole template).
func FullResynth(total, broken int) bool {
	return total > 0 && broken*2 >= total
}

// Retain keeps a validated sample (ring of maxRetain).
func (h *Healer) Retain(s SynthSample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, s)
	if len(h.samples) > h.maxRetain {
		h.samples = h.samples[len(h.samples)-h.maxRetain:]
	}
}

// Samples returns the retained samples (oldest first).
func (h *Healer) Samples() []SynthSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]SynthSample(nil), h.samples...)
}

// NullRate reports the current null rate for a field (test helper).
func (h *Healer) NullRate(field string) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	ring := h.rings[field]
	if len(ring) == 0 {
		return 0
	}
	nulls := 0
	for _, b := range ring {
		if b {
			nulls++
		}
	}
	return float64(nulls) / float64(len(ring))
}
