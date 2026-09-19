package fetch

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestValidateXHRPatterns(t *testing.T) {
	t.Run("empty is nil", func(t *testing.T) {
		res, err := ValidateXHRPatterns(nil)
		if err != nil || res != nil {
			t.Errorf("got %v, %v; want nil, nil", res, err)
		}
	})
	t.Run("valid patterns compile", func(t *testing.T) {
		res, err := ValidateXHRPatterns([]string{"/api/", `\.json$`})
		if err != nil || len(res) != 2 {
			t.Fatalf("got %v, %v", res, err)
		}
	})
	t.Run("bad pattern names itself", func(t *testing.T) {
		_, err := ValidateXHRPatterns([]string{"/ok/", "[bad"})
		if err == nil || !strings.Contains(err.Error(), `"[bad"`) {
			t.Errorf("err = %v, want the bad pattern named", err)
		}
	})
}

func TestTruncBody(t *testing.T) {
	exact := strings.Repeat("a", maxXHRBodyBytes)
	if got, tr := truncBody(exact); tr || len(got) != maxXHRBodyBytes {
		t.Errorf("exactly 64KiB: truncated=%v len=%d, want false/64KiB", tr, len(got))
	}
	over := strings.Repeat("b", maxXHRBodyBytes+1)
	got, tr := truncBody(over)
	if !tr || len(got) != maxXHRBodyBytes {
		t.Errorf("64KiB+1: truncated=%v len=%d, want true/64KiB", tr, len(got))
	}
}

func TestBuildCapture(t *testing.T) {
	p := xhrPending{id: "1", url: "https://x/api/data", status: 200, mime: "application/json"}

	t.Run("plain body", func(t *testing.T) {
		c := buildCapture(p, `{"ok":true}`, false)
		if c.Body != `{"ok":true}` || c.Truncated || c.URL != p.url || c.Status != 200 || c.MIMEType != p.mime {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("base64 decoded", func(t *testing.T) {
		enc := base64.StdEncoding.EncodeToString([]byte("hello"))
		c := buildCapture(p, enc, true)
		if c.Body != "hello" {
			t.Errorf("body = %q, want decoded hello", c.Body)
		}
	})
	t.Run("invalid base64 keeps raw", func(t *testing.T) {
		c := buildCapture(p, "!!!not-base64!!!", true)
		if c.Body != "!!!not-base64!!!" {
			t.Errorf("body = %q, want raw fallback", c.Body)
		}
	})
	t.Run("evicted body is metadata-only", func(t *testing.T) {
		// drain() turns a CDP eviction ("-32000 No resource…") into
		// buildCapture(p, "", false) — the pin: metadata survives, no
		// error, no phantom truncation.
		c := buildCapture(p, "", false)
		if c.Body != "" || c.Truncated || c.URL != p.url || c.Status != 200 {
			t.Errorf("got %+v, want metadata-only capture", c)
		}
	})
}

func TestCollectorAdmit(t *testing.T) {
	c := &xhrCollector{}
	pats, err := ValidateXHRPatterns([]string{"/api/"})
	if err != nil {
		t.Fatal(err)
	}
	c.patterns = pats

	if !c.admit(proto.NetworkResourceTypeXHR, "https://x/api/data") {
		t.Error("matching XHR must be admitted")
	}
	if !c.admit(proto.NetworkResourceTypeFetch, "https://x/api/data") {
		t.Error("matching Fetch must be admitted")
	}
	if c.admit(proto.NetworkResourceTypeDocument, "https://x/api/data") {
		t.Error("Document resource type must not be admitted")
	}
	if c.admit(proto.NetworkResourceTypeXHR, "https://x/other") {
		t.Error("non-matching URL must not be admitted")
	}
	for i := 0; i < maxXHRCaptures; i++ {
		c.record(xhrPending{})
	}
	if len(c.pending) != maxXHRCaptures {
		t.Fatalf("pending = %d, want %d", len(c.pending), maxXHRCaptures)
	}
	c.record(xhrPending{url: "https://x/api/data"})
	if len(c.pending) != maxXHRCaptures {
		t.Error("capture cap (50) must drop further records")
	}
	if c.pending[maxXHRCaptures-1].url != "" {
		t.Error("the 51st record must not displace an earlier capture")
	}
}
