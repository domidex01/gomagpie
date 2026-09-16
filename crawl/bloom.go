package crawl

import (
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
)

// Filter is a mutex-guarded bloom front over url_hash hex strings.
// The library is unsynchronized for performance — never skip the lock.
type Filter struct {
	mu sync.Mutex
	f  *bloom.BloomFilter
}

// NewFilter builds a 1M-URL @ 1% FP filter (~1.2MB).
func NewFilter() *Filter {
	return &Filter{f: bloom.NewWithEstimates(1000000, 0.01)}
}

// Add inserts a url_hash hex string.
func (f *Filter) Add(hashHex string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.f.Add([]byte(hashHex))
}

// Test reports a possible hit for a url_hash hex string.
func (f *Filter) Test(hashHex string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.f.Test([]byte(hashHex))
}
