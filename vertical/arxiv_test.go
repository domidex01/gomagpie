package vertical_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"magpie/vertical"
)

func TestArxivMatch_Table(t *testing.T) {
	ex, _ := vertical.Lookup("arxiv")
	yes := []string{
		"https://arxiv.org/abs/2401.12345v2",
		"https://arxiv.org/abs/2401.12345",
		"https://arxiv.org/pdf/2401.12345",
		"https://export.arxiv.org/abs/2401.12345v2",
	}
	no := []string{
		"https://arxiv.org/",
		"https://arxiv.org/search/?q=test",
		"https://example.com/abs/2401.12345",
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

func TestArxivExtract(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"export.arxiv.org": {body: verticalFixture(t, "arxiv.xml")},
	}}
	ex, _ := vertical.Lookup("arxiv")
	got, err := ex.Extract(context.Background(), fx, mustURL(t, "https://arxiv.org/abs/2401.12345v2"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := map[string]any{
		"title":     "Demo Paper on Testing Things",
		"authors":   []any{"A. One", "B. Two"},
		"summary":   "This is the abstract summary of the demo paper for fixture purposes.",
		"published": "2024-01-15T00:00:00Z",
		"url":       "https://arxiv.org/abs/2401.12345v2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	// Version suffix stripped for the query, full ID kept in output.
	reqs := fx.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], "id_list=2401.12345") || strings.Contains(reqs[0], "v2") {
		t.Errorf("requests = %v, want id_list=2401.12345 without version", reqs)
	}
}

func TestArxivExtract_Malformed(t *testing.T) {
	fx := &fakeVerticalFetcher{bodies: map[string]fakeResp{
		"export.arxiv.org": {body: []byte("<feed><entry><title>truncated")},
	}}
	ex, _ := vertical.Lookup("arxiv")
	if _, err := ex.Extract(context.Background(), fx, mustURL(t, "https://arxiv.org/abs/2401.1")); err == nil {
		t.Error("malformed XML: want hard error")
	}
}
