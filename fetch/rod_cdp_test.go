package fetch_test

import (
	"strings"
	"testing"
	"time"

	"github.com/motherlodelab/magpie/fetch"
)

// TestRod_CDPNoLocalLaunch — US-5: with CDP set, ensureBrowser must
// connect to the endpoint and NEVER run the local launcher (no download,
// no local Chrome). A refused loopback dial is hermetic proof: the error
// is connect-time (not launch-time) and arrives in milliseconds.
func TestRod_CDPNoLocalLaunch(t *testing.T) {
	r := fetch.NewRodFetcher()
	r.CDP = "ws://127.0.0.1:1/x"
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close after failed connect: %v", err)
		}
	}()
	start := time.Now()
	_, err := r.Fetch(t.Context(), fetch.FetchRequest{URL: "data:text/html,<p>hi</p>"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("fetch against a dead endpoint succeeded; expected connect error")
	}
	if !strings.Contains(err.Error(), "connect remote browser") {
		t.Errorf("err = %v, want the remote-connect error (a launch error would mean the launcher ran)", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want the dial's connection refusal (proves the endpoint was contacted, not launched)", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("connect took %v; a local browser download/launch would be far slower", elapsed)
	}
}

// TestRod_CDPRedacted — credentials in the endpoint never reach the error.
func TestRod_CDPRedacted(t *testing.T) {
	r := fetch.NewRodFetcher()
	r.CDP = "ws://user:pass@127.0.0.1:1/x"
	defer func() { _ = r.Close() }() //nolint:errcheck // teardown unactionable
	_, err := r.Fetch(t.Context(), fetch.FetchRequest{URL: "data:text/html,<p>hi</p>"})
	if err == nil {
		t.Fatal("expected connect error")
	}
	if strings.Contains(err.Error(), "user:pass") {
		t.Errorf("error leaks credentials: %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error should keep the redacted host for operators: %v", err)
	}
}
