package crawl

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ErrBadScope marks invalid scope configuration (glob cap/compile
// failures). Run fails pre-I/O with it; the CLI maps it to exit 2.
var ErrBadScope = errors.New("crawl: bad scope")

// Scope is the compiled crawl frontier filter: host rule → path prefix →
// include globs → exclude globs (exclude wins). Built once per run by
// CompileScope; Allows is called per discovered link.
type Scope struct {
	SameHost        bool
	AllowSubdomains bool
	PathPrefix      string // normalized to a leading "/" at compile time
	Include         []*regexp.Regexp
	Exclude         []*regexp.Regexp
}

// CompileScope validates caps and compiles include/exclude globs into
// anchored regexps. Raw glob strings never survive past this call.
// Called at every entry point (CLI fail(2) edge, MCP handler, crawl.Run)
// so bad globs fail before any I/O.
func CompileScope(sameHost, allowSubdomains bool, pathPrefix string, include, exclude []string) (Scope, error) {
	prefix := pathPrefix
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	compileAll := func(globs []string, what string) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for _, g := range globs {
			re, err := compileGlob(g)
			if err != nil {
				return nil, fmt.Errorf("%w: %s glob: %v", ErrBadScope, what, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	inc, err := compileAll(include, "include")
	if err != nil {
		return Scope{}, err
	}
	exc, err := compileAll(exclude, "exclude")
	if err != nil {
		return Scope{}, err
	}
	return Scope{
		SameHost:        sameHost,
		AllowSubdomains: allowSubdomains,
		PathPrefix:      prefix,
		Include:         inc,
		Exclude:         exc,
	}, nil
}

// compileGlob translates a URL glob into an anchored regexp: ** crosses
// path segments, * and ? stay within one. Caps mirror webclaw: each glob
// ≤1024 chars, ≤4 ** occurrences. Literals are QuoteMeta'd, so the built
// pattern always compiles.
func compileGlob(g string) (*regexp.Regexp, error) {
	if len(g) > 1024 {
		return nil, fmt.Errorf("glob exceeds 1024 chars: %q", g)
	}
	if strings.Count(g, "**") > 4 {
		return nil, fmt.Errorf("glob %q: at most 4 **", g)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); {
		switch {
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i += 2
		case g[i] == '*':
			b.WriteString("[^/]*")
			i++
		case g[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String()) //nolint:wrapcheck // caller adds the include/exclude context
}

// Allows reports whether a discovered link may be enqueued. base is the
// page (or seed) the link was found on; cand must be the post-Canonicalize
// canonical URL — the exact string that gets enqueued — so utm-stripped
// and query-reordered links match globs predictably.
func (s Scope) Allows(base, cand *url.URL) bool {
	if s.SameHost {
		bh := strings.ToLower(strings.TrimSuffix(base.Host, "."))
		ch := strings.ToLower(strings.TrimSuffix(cand.Host, "."))
		// Same-host rule with dot-boundary subdomains; two empty hosts
		// (file:// pages) compare equal, preserving the pre-scope EqualFold
		// behavior for file:// crawls.
		hostOK := bh == ch || (s.AllowSubdomains && bh != "" && strings.HasSuffix(ch, "."+bh))
		if !hostOK {
			return false
		}
	}
	if s.PathPrefix != "" {
		p := cand.EscapedPath()
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, s.PathPrefix) {
			return false
		}
	}
	if len(s.Include) > 0 {
		matched := false
		for _, re := range s.Include {
			if re.MatchString(cand.String()) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, re := range s.Exclude {
		if re.MatchString(cand.String()) {
			return false // exclude wins over include
		}
	}
	return true
}

// skipExtensions is the binary-asset skip table applied in ExtractLinks
// before enqueue: assets are never pages, fetching them burns --max-pages.
// "pdf" stays a deliberate non-goal: `magpie scrape <pdf-url>` extracts
// text (clean/pdf.go), but crawling does not follow PDF links (Phase E).
var skipExtensions = map[string]bool{
	"pdf": true, "png": true, "jpg": true, "jpeg": true, "gif": true,
	"webp": true, "svg": true, "ico": true, "avif": true,
	"mp4": true, "webm": true, "mp3": true, "avi": true, "mov": true,
	"zip": true, "gz": true, "tgz": true, "rar": true, "7z": true,
	"exe": true, "dmg": true, "msi": true,
	"woff": true, "woff2": true, "ttf": true, "eot": true, "otf": true,
}

// SkipExtension reports whether a URL path ends in a binary asset
// extension. The extension of the PATH is checked, never the raw URL, so
// query strings cannot smuggle an asset past the filter.
func SkipExtension(urlPath string) bool {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(urlPath)), ".")
	return skipExtensions[ext]
}
