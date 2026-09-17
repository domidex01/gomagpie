package fetch

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// decodeBody unwraps a Content-Encoding chain. Needed for browser-path
// responses only: the wrapper's h2 transport never decompresses, and its
// h1 fallback sees the profile's own Accept-Encoding, which disables the
// stock transport's auto-decode. Output capped at 50 MB — the bomb guard
// applies to the DECODED stream, not just the wire bytes.
func decodeBody(b []byte, hdr http.Header) ([]byte, error) {
	enc := strings.TrimSpace(hdr.Get("Content-Encoding"))
	if enc == "" || strings.EqualFold(enc, "identity") {
		return b, nil
	}
	parts := strings.Split(enc, ",")
	var r io.Reader = bytes.NewReader(b)
	var closers []func() error
	// The server applies encodings left-to-right, so peel right-to-left.
	for i := len(parts) - 1; i >= 0; i-- {
		switch strings.ToLower(strings.TrimSpace(parts[i])) {
		case "gzip", "x-gzip":
			zr, err := gzip.NewReader(r)
			if err != nil {
				return nil, err
			}
			r = zr
			closers = append(closers, zr.Close)
		case "deflate":
			fr := flate.NewReader(r)
			r = fr
			closers = append(closers, fr.Close)
		case "br":
			r = brotli.NewReader(r)
		case "zstd":
			zr, err := zstd.NewReader(r)
			if err != nil {
				return nil, err
			}
			r = zr
			closers = append(closers, func() error { zr.Close(); return nil })
		case "identity":
		default:
			return nil, fmt.Errorf("fetch: unsupported Content-Encoding %q", parts[i])
		}
	}
	out, err := io.ReadAll(io.LimitReader(r, 50<<20))
	// Close errors surface only when the read itself succeeded — the
	// stream error is the one that matters.
	var cerr error
	for _, c := range closers {
		if e := c(); e != nil && cerr == nil {
			cerr = e
		}
	}
	if err != nil {
		return nil, err
	}
	if cerr != nil {
		return nil, cerr
	}
	return out, nil
}
