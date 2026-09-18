//go:build browser

package fetch_test

// Live JA3/JA4 verification against tls.peet.ws — the only network-gated
// proof that --browser really impersonates. Sits behind -tags browser
// (next to rod_smoke_test.go) so the default suite stays hermetic.
//
// Constants recorded at implementation time (impersonate-http v0.4.0,
// utls v1.8.2). A dependency bump legitimately drifts them: if they
// mismatch but the fingerprint is still browser-class (t13d… TLS 1.3
// prefix), update the constants WITH the new values in the commit
// message. Hard-fail only on handshake errors or a non-browser-class JA4.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gomagpie/fetch"
)

const (
	// Chrome randomizes its ClientHello extension order per handshake
	// (uTLS HelloChrome_Auto reproduces that), so its JA3 hash changes
	// every connection — that is faithful Chrome behavior, not drift.
	// JA4 sorts extensions, which is exactly why it stays stable.
	ja4Chrome = "t13d1516h2_8daaf6152771_d8a2da3f94cd"
	// Firefox does not randomize: both hashes are stable.
	ja3Firefox = "b5001237acdf006056b409cc433726b0"
	ja4Firefox = "t13d1715h2_5b57614c22b0_5c2c66f702b0"
)

func TestUTLSImpersonation(t *testing.T) {
	cases := []struct {
		name, wantJA4, wantJA3 string // wantJA3 empty = randomized, log only
	}{
		{"chrome", ja4Chrome, ""},
		{"firefox", ja4Firefox, ja3Firefox},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := fetch.NewStaticFetcher()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			resp, err := f.Fetch(ctx, fetch.FetchRequest{URL: "https://tls.peet.ws/api/all", Browser: tc.name})
			if err != nil {
				t.Fatalf("%s handshake/fetch: %v", tc.name, err)
			}
			var doc struct {
				TLS struct {
					JA3Hash string `json:"ja3_hash"`
					JA4     string `json:"ja4"`
				} `json:"tls"`
			}
			if err := json.Unmarshal(resp.HTML, &doc); err != nil {
				t.Fatalf("peet.ws body: %v (%.200s)", err, resp.HTML)
			}
			t.Logf("ja3_hash=%s ja4=%s (%s)", doc.TLS.JA3Hash, doc.TLS.JA4, tc.name)
			if doc.TLS.JA4 != tc.wantJA4 {
				t.Errorf("ja4 = %s, want %s — if a dep bump drifted it but the fingerprint is still browser-class (t13d…), update the constant (see file comment)", doc.TLS.JA4, tc.wantJA4)
			}
			if !strings.HasPrefix(doc.TLS.JA4, "t13d") {
				t.Errorf("ja4 = %s, want browser-class t13d… prefix", doc.TLS.JA4)
			}
			if tc.wantJA3 != "" && doc.TLS.JA3Hash != tc.wantJA3 {
				t.Errorf("ja3_hash = %s, want %s", doc.TLS.JA3Hash, tc.wantJA3)
			}
		})
	}
}

// Phase G G.3: the four new profiles ride the same impersonation path.
// Constants recorded from live peet.ws runs (impersonate-http v0.4.0);
// same drift rule as the file header — update WITH the dep bump.
const (
	ja3Safari        = "773906b0efdefa24a7f2b8eb6985bf37"
	ja4Safari        = "t13d2014h2_a09f3c656075_14788d8d241b"
	ja3Edge          = "b32309a26951912be7dba376398abc3b"
	ja4Edge          = "t13d1515h2_8daaf6152771_de4a06bb82e3"
	ja3IOS           = "656b9a2f4de6ed4909e157482860ab3d"
	ja4IOS           = "t13d2613h2_2802a3db6c62_845d286b0d67"
	ja4ChromeAndroid = "t13d1516h2_8daaf6152771_d8a2da3f94cd" // HelloChrome_Auto — identical to chrome's, JA3 randomized
)

func TestUTLSImpersonationNewProfiles(t *testing.T) {
	cases := []struct {
		name, wantJA4, wantJA3 string
	}{
		{"safari", ja4Safari, ja3Safari},
		{"edge", ja4Edge, ja3Edge},
		{"ios", ja4IOS, ja3IOS},
		{"chrome_android", ja4ChromeAndroid, ""}, // chrome-family randomizes
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := fetch.NewStaticFetcher()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			resp, err := f.Fetch(ctx, fetch.FetchRequest{URL: "https://tls.peet.ws/api/all", Browser: tc.name})
			if err != nil {
				t.Fatalf("%s handshake/fetch: %v", tc.name, err)
			}
			var doc struct {
				TLS struct {
					JA3Hash string `json:"ja3_hash"`
					JA4     string `json:"ja4"`
				} `json:"tls"`
			}
			if err := json.Unmarshal(resp.HTML, &doc); err != nil {
				t.Fatalf("peet.ws body: %v (%.200s)", err, resp.HTML)
			}
			t.Logf("ja3_hash=%s ja4=%s (%s)", doc.TLS.JA3Hash, doc.TLS.JA4, tc.name)
			if doc.TLS.JA4 != tc.wantJA4 {
				t.Errorf("ja4 = %s, want %s — if a dep bump drifted it but the fingerprint is still browser-class (t13d…), update the constant (see file comment)", doc.TLS.JA4, tc.wantJA4)
			}
			if tc.wantJA3 != "" && doc.TLS.JA3Hash != tc.wantJA3 {
				t.Errorf("ja3_hash = %s, want %s", doc.TLS.JA3Hash, tc.wantJA3)
			}
		})
	}
}
