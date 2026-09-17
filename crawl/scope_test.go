package crawl

// Scope tests: pure tables for CompileScope caps, the Allows matrix, and
// the extension skip table. Every Allows input is canonicalized in-test
// to lock the phase-D decision that globs match the post-Canonicalize
// enqueue basis.

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func compileScope(t *testing.T, sameHost, allowSub bool, prefix string, include, exclude []string) Scope {
	t.Helper()
	s, err := CompileScope(sameHost, allowSub, prefix, include, exclude)
	if err != nil {
		t.Fatalf("CompileScope: %v", err)
	}
	return s
}

// mustCanonURL canonicalizes then re-parses so Allows inputs are exactly
// the strings ExtractLinks/enqueue see.
func mustCanonURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	c, err := Canonicalize(raw)
	if err != nil {
		t.Fatalf("Canonicalize(%q): %v", raw, err)
	}
	u, err := url.Parse(c)
	if err != nil {
		t.Fatalf("parse %q: %v", c, err)
	}
	return u
}

func TestCompileScope_Caps(t *testing.T) {
	// >4 ** → error.
	if _, err := CompileScope(true, false, "", []string{strings.Repeat("a/**/b/", 5) + "c"}, nil); err == nil {
		t.Error("5 ** pairs: nil error, want cap rejection")
	}
	// >1024 chars → error.
	if _, err := CompileScope(true, false, "", []string{strings.Repeat("a/", 600)}, nil); err == nil {
		t.Error("1200-char glob: nil error, want cap rejection")
	}
	// QuoteMeta contract: glob metacharacters that are NOT * or ? match
	// literally, so a bracket never explodes into a character class — and
	// CompileScope never errors on regexp-hostile input.
	s := compileScope(t, false, false, "", []string{"[unclosed"}, nil)
	if !s.Include[0].MatchString("[unclosed") {
		t.Error("literal '[unclosed' glob should match the literal string (QuoteMeta)")
	}
	// Prefix normalization: "docs" behaves as "/docs" (asserted behaviorally).
	sp := compileScope(t, false, false, "docs", nil, nil)
	if !sp.Allows(mustCanonURL(t, "http://ex.com/docs/a"), mustCanonURL(t, "http://ex.com/docs/a")) {
		t.Error("prefix 'docs' did not normalize to '/docs'")
	}
	if sp.Allows(mustCanonURL(t, "http://ex.com/apidocs/a"), mustCanonURL(t, "http://ex.com/apidocs/a")) {
		t.Error("prefix 'docs' matched /apidocs — normalization must anchor at a segment start")
	}
}

func TestAllows_Matrix(t *testing.T) {
	type row struct {
		name       string
		scope      Scope
		base, cand string
		want       bool
	}
	rows := []row{
		{
			name:  "exact host ok",
			scope: Scope{SameHost: true},
			base:  "http://ex.com/docs/", cand: "http://ex.com/api/b", want: true,
		},
		{
			name:  "subdomain blocked by default",
			scope: Scope{SameHost: true},
			base:  "http://ex.com/", cand: "http://sub.ex.com/docs/c", want: false,
		},
		{
			name:  "subdomain allowed with flag",
			scope: Scope{SameHost: true, AllowSubdomains: true},
			base:  "http://ex.com/", cand: "http://sub.ex.com/docs/c", want: true,
		},
		{
			name:  "deep subdomain allowed with flag",
			scope: Scope{SameHost: true, AllowSubdomains: true},
			base:  "http://ex.com/", cand: "http://a.b.ex.com/", want: true,
		},
		{
			// THE security-relevant line: suffix WITHOUT dot boundary must
			// never count as a subdomain (cookie-style suffix bug).
			name:  "evil suffix without dot boundary",
			scope: Scope{SameHost: true, AllowSubdomains: true},
			base:  "http://ex.com/", cand: "http://evil-ex.com/", want: false,
		},
		{
			name:  "foreign host",
			scope: Scope{SameHost: true},
			base:  "http://ex.com/", cand: "http://other.com/docs/d", want: false,
		},
		{
			name:  "prefix in",
			scope: Scope{SameHost: true, PathPrefix: "/docs"},
			base:  "http://ex.com/", cand: "http://ex.com/docs/a", want: true,
		},
		{
			name:  "prefix out",
			scope: Scope{SameHost: true, PathPrefix: "/docs"},
			base:  "http://ex.com/", cand: "http://ex.com/api/b", want: false,
		},
		{
			name:  "empty candidate path behaves as /",
			scope: Scope{SameHost: false, PathPrefix: "/"},
			base:  "http://ex.com/", cand: "http://ex.com", want: true,
		},
		{
			name:  "include any-of match",
			scope: Scope{SameHost: false, Include: globs(t, "**/docs/**", "**/guide/**")},
			base:  "http://ex.com/", cand: "http://ex.com/guide/x", want: true,
		},
		{
			name:  "include none match",
			scope: Scope{SameHost: false, Include: globs(t, "**/docs/**")},
			base:  "http://ex.com/", cand: "http://ex.com/api/b", want: false,
		},
		{
			name:  "exclude wins over include",
			scope: Scope{SameHost: false, Include: globs(t, "**/docs/**"), Exclude: globs(t, "**/secret/**")},
			base:  "http://ex.com/", cand: "http://ex.com/docs/secret/x", want: false,
		},
		{
			name:  "exclude alone drops match",
			scope: Scope{SameHost: false, Exclude: globs(t, "**/api/**")},
			base:  "http://ex.com/", cand: "http://ex.com/api/b", want: false,
		},
		{
			name:  "single star stays in one segment",
			scope: Scope{SameHost: false, Include: globs(t, "http://ex.com/*")},
			base:  "http://ex.com/", cand: "http://ex.com/a/b", want: false,
		},
		{
			name:  "single star matches one segment",
			scope: Scope{SameHost: false, Include: globs(t, "http://ex.com/*")},
			base:  "http://ex.com/", cand: "http://ex.com/a", want: true,
		},
		{
			name:  "question mark matches one char",
			scope: Scope{SameHost: false, Include: globs(t, "http://ex.com/p?ge")},
			base:  "http://ex.com/", cand: "http://ex.com/page", want: true,
		},
		{
			// Canonical-basis row: RAW utm'd input, canonicalized in-test.
			// utm_source is stripped so the glob sees /docs/a?x=1 — the
			// enqueue string. Without canonical basis this row is luck.
			name:  "utm-stripped canonical basis",
			scope: Scope{SameHost: false, Include: globs(t, "**/docs/**")},
			base:  "http://ex.com/", cand: "http://ex.com/docs/a?utm_source=t&x=1", want: true,
		},
		{
			// file:// pages have empty hosts: two empty hosts are same-host
			// (preserves the pre-scope EqualFold behavior for file crawls).
			name:  "file empty-host equality",
			scope: Scope{SameHost: true},
			base:  "file:///a/index.html", cand: "file:///a/p0.html", want: true,
		},
		{
			name:  "trailing-dot host matches bare",
			scope: Scope{SameHost: true},
			base:  "http://ex.com./", cand: "http://ex.com/x", want: true,
		},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := r.scope.Allows(mustCanonURL(t, r.base), mustCanonURL(t, r.cand)); got != r.want {
				t.Errorf("Allows(base=%s, cand=%s) = %v, want %v", r.base, r.cand, got, r.want)
			}
		})
	}
}

func globs(t *testing.T, gs ...string) []*regexp.Regexp {
	t.Helper()
	var out []*regexp.Regexp
	for _, g := range gs {
		re, err := compileGlob(g)
		if err != nil {
			t.Fatalf("compileGlob(%q): %v", g, err)
		}
		out = append(out, re)
	}
	return out
}

func TestSkipExtension_Table(t *testing.T) {
	exts := []string{"pdf", "png", "jpg", "jpeg", "gif", "webp", "svg", "ico", "avif",
		"mp4", "webm", "mp3", "avi", "mov", "zip", "gz", "tgz", "rar", "7z",
		"exe", "dmg", "msi", "woff", "woff2", "ttf", "eot", "otf"}
	for _, e := range exts {
		if !SkipExtension("/assets/file." + e) {
			t.Errorf("SkipExtension(/assets/file.%s) = false, want true", e)
		}
		if !SkipExtension("/assets/file." + strings.ToUpper(e)) {
			t.Errorf("SkipExtension upper %s = false, want true (case-insensitive)", e)
		}
	}
	for _, keep := range []string{"/page", "/docs", "/feed.xml", "/a/b.html", "/"} {
		if SkipExtension(keep) {
			t.Errorf("SkipExtension(%q) = true, want false", keep)
		}
	}
	// Query strings never reach SkipExtension (frontier passes u.Path), but
	// the unit pins path-only semantics too: no extension → kept.
	if SkipExtension("/f") {
		t.Error("extensionless path skipped")
	}
}
