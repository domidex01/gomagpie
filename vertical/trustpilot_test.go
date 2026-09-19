package vertical_test

import (
	"strings"
	"testing"

	"github.com/domidex01/magpie/vertical"
)

// Fixture trustpilot.html (recorded 2026-09-19): a synthetic shape
// fixture mirroring the server-rendered JSON-LD on review pages — not
// a live capture.

func TestTrustpilotMatch_Table(t *testing.T) {
	ex, _ := vertical.Lookup("trustpilot")
	yes := []string{
		"https://www.trustpilot.com/review/acme.com",
		"https://trustpilot.com/review/acme.com",
	}
	no := []string{
		"https://www.trustpilot.com/about",
		"https://www.trustpilot.com/",
		"https://example.com/review/acme.com",
	}
	for _, raw := range yes {
		if !ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if ex.Match(mustURL(t, raw)) {
			t.Errorf("Match(%s) = true, want false", raw)
		}
	}
}

func TestTrustpilotExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"trustpilot.com": {body: verticalFixture(t, "trustpilot.html")},
	}}
	ex, _ := vertical.Lookup("trustpilot")
	got, err := ex.Extract(t.Context(), fx, mustURL(t, "https://www.trustpilot.com/review/acme.com"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got["name"] != "Acme Corp" {
		t.Errorf("name = %v, want Acme Corp", got["name"])
	}
	if got["rating_value"] != 4.6 || got["review_count"] != float64(1203) {
		t.Errorf("aggregate = %#v, want 4.6/1203", got)
	}
	reviews, _ := got["reviews"].([]any)
	if len(reviews) != 2 {
		t.Fatalf("reviews = %#v, want 2", reviews)
	}
	r0, _ := reviews[0].(map[string]any)
	if r0["author"] != "Dana R." || r0["rating"] != float64(5) || r0["date"] != "2026-08-14" {
		t.Errorf("reviews[0] = %#v", r0)
	}
	if !strings.Contains(r0["body"].(string), "Fantastic widgets") {
		t.Errorf("reviews[0] body = %#v", r0["body"])
	}
}

// A page without JSON-LD is a loud error, never an empty record.
func TestTrustpilot_NoJSONLDErrors(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"trustpilot.com": {body: []byte("<html><body><p>redesign dropped the ld+json</p></body></html>")},
	}}
	ex, _ := vertical.Lookup("trustpilot")
	_, err := ex.Extract(t.Context(), fx, mustURL(t, "https://www.trustpilot.com/review/acme.com"))
	if err == nil || !strings.Contains(err.Error(), "JSON-LD") {
		t.Errorf("err = %v, want a loud no-JSON-LD error", err)
	}
}
