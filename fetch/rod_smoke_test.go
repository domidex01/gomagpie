//go:build browser

package fetch_test

import (
	"testing"

	"github.com/go-rod/rod/lib/launcher"
	"gomagpie/fetch"
)

func TestRodSmoke(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok {
		t.Skip("no Chrome/Chromium found")
	}
	r := fetch.NewRodFetcher()
	defer r.Close()
	resp, err := r.Fetch(t.Context(), fetch.FetchRequest{URL: "data:text/html,<html><body><h1>hi</h1></body></html>"})
	if err != nil {
		t.Fatalf("rod fetch: %v", err)
	}
	if len(resp.HTML) == 0 {
		t.Fatal("empty html")
	}
}
