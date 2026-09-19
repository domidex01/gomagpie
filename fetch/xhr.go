package fetch

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// XHR capture caps: 50 responses × 64 KiB bodies. Past a cap the capture
// is truncated / dropped — fail-visible in the data (Truncated flag,
// missing entries), never a fetch error.
const (
	maxXHRCaptures  = 50
	maxXHRBodyBytes = 64 << 10
)

// XHRCapture is one captured XHR/fetch response body (rod-only; additive
// on FetchResponse).
type XHRCapture struct {
	URL       string `json:"url"`
	Status    int    `json:"status"`
	MIMEType  string `json:"mime,omitempty"`
	Body      string `json:"body,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ValidateXHRPatterns compiles CaptureXHR patterns; a bad regexp is an
// options-boundary error (exit 2), never a mid-fetch failure.
func ValidateXHRPatterns(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("capture-xhr pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// xhrPending records one response seen on the wire; bodies drain later.
type xhrPending struct {
	id     proto.NetworkRequestID
	url    string
	status int
	mime   string
}

// xhrCollector accumulates XHR captures for one page load. mu guards
// pending: rod event callbacks run on the consumer goroutine (subscribed
// below with `go wait()`), drain runs on the fetch goroutine.
type xhrCollector struct {
	mu       sync.Mutex
	patterns []*regexp.Regexp
	pending  []xhrPending
}

// admit reports whether a network response is capture-worthy: XHR/Fetch
// resource types only, matching any pattern. (The capture cap is checked
// under mu in record.)
func (c *xhrCollector) admit(rt proto.NetworkResourceType, rawURL string) bool {
	if rt != proto.NetworkResourceTypeXHR && rt != proto.NetworkResourceTypeFetch {
		return false
	}
	return matchesAny(rawURL, c.patterns)
}

// record appends a pending capture under the cap (rod callback side).
func (c *xhrCollector) record(p xhrPending) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) >= maxXHRCaptures {
		return
	}
	c.pending = append(c.pending, p)
}

// subscribe listens for matching XHR/fetch responses. Rod events are
// push-consumed: callbacks fire only while the returned wait() loop is
// running, and a void callback never satisfies it — so the CALLER must
// run `go wait()` and let the loop die with the page context at fetch
// end. Bodies drain later (drain); fetching inside the callback would
// stall rod's event loop.
func (c *xhrCollector) subscribe(page *rod.Page) func() {
	return page.EachEvent(func(e *proto.NetworkResponseReceived) {
		if c.admit(e.Type, e.Response.URL) {
			c.record(xhrPending{
				id: e.RequestID, url: e.Response.URL,
				status: int(e.Response.Status), mime: e.Response.MIMEType,
			})
		}
	})
}

// drain fetches each pending body. Must run after the settle and BEFORE
// page.Close() — CDP evicts response buffers on close; an evicted body
// becomes a metadata-only capture (fail-visible in data, never fatal).
func (c *xhrCollector) drain(page *rod.Page) []XHRCapture {
	c.mu.Lock()
	pending := append([]xhrPending(nil), c.pending...)
	c.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	caps := make([]XHRCapture, 0, len(pending))
	for _, p := range pending {
		raw, b64 := "", false
		if res, err := (proto.NetworkGetResponseBody{RequestID: p.id}).Call(page); err == nil {
			raw, b64 = res.Body, res.Base64Encoded
		} // else: evicted ("-32000 No resource…") → metadata only
		caps = append(caps, buildCapture(p, raw, b64))
	}
	return caps
}

// truncBody caps a body at 64 KiB, reporting whether it was cut.
func truncBody(s string) (string, bool) {
	if len(s) <= maxXHRBodyBytes {
		return s, false
	}
	return s[:maxXHRBodyBytes], true
}

// buildCapture turns one pending response plus its raw body into the
// output record: base64-decoded when flagged (invalid base64 keeps the
// raw string — a decode quirk must not drop the capture), truncated at
// 64 KiB.
func buildCapture(p xhrPending, raw string, b64 bool) XHRCapture {
	if b64 {
		if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
			raw = string(dec)
		}
	}
	body, truncated := truncBody(raw)
	return XHRCapture{
		URL: p.url, Status: p.status, MIMEType: p.mime,
		Body: body, Truncated: truncated,
	}
}

func matchesAny(rawURL string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(rawURL) {
			return true
		}
	}
	return false
}
