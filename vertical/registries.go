package vertical

import (
	"context"
	"net/url"
)

func init() {
	register(Extractor{
		Info: Info{
			Name:     "pypi",
			Label:    "PyPI",
			Desc:     "Python package metadata via the PyPI JSON API.",
			Patterns: []string{"https://pypi.org/project/{name}/"},
		},
		Match:   matchPypi,
		Extract: extractPypi,
	})
	register(Extractor{
		Info: Info{
			Name:     "npm",
			Label:    "npm",
			Desc:     "JavaScript package metadata via the npm registry.",
			Patterns: []string{"https://www.npmjs.com/package/{name}"},
		},
		Match:   matchNpm,
		Extract: extractNpm,
	})
	register(Extractor{
		Info: Info{
			Name:     "crates_io",
			Label:    "crates.io",
			Desc:     "Rust crate metadata via the crates.io API (requires a User-Agent, which the static fetcher sends).",
			Patterns: []string{"https://crates.io/crates/{name}"},
		},
		Match:   matchCrates,
		Extract: extractCrates,
	})
}

func matchPypi(u *url.URL) bool {
	return hostIs(u, "pypi.org", "www.pypi.org") && lastSegment(u.Path) != ""
}

func matchNpm(u *url.URL) bool {
	return hostIs(u, "www.npmjs.com", "npmjs.com", "www.npmjsjs.com") && lastSegment(u.Path) != ""
}

func matchCrates(u *url.URL) bool {
	// Canonical crate pages only — lib.rs passthrough is out of scope.
	return hostIs(u, "crates.io", "www.crates.io") && lastSegment(u.Path) != ""
}

// pypiName unwraps /project/{name}/ prefixes; otherwise the last segment.
func pypiName(u *url.URL) string {
	segs := pathSegs(u.Path)
	for i, s := range segs {
		if s == "project" && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return lastSegment(u.Path)
}

func npmName(u *url.URL) string {
	segs := pathSegs(u.Path)
	for i, s := range segs {
		if s == "package" && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return lastSegment(u.Path)
}

func extractPypi(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	name := pypiName(u)
	m, err := fetchJSON(ctx, f, "https://pypi.org/pypi/"+name+"/json")
	if err != nil {
		return nil, err
	}
	info := child(m, "info")
	if info == nil {
		info = m
	}
	return map[string]any{
		"name":            str(info, "name"),
		"version":         str(info, "version"),
		"summary":         str(info, "summary"),
		"author":          str(info, "author"),
		"requires_python": str(info, "requires_python"),
		"url":             "https://pypi.org/project/" + name + "/",
	}, nil
}

func extractNpm(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	name := npmName(u)
	m, err := fetchJSON(ctx, f, "https://registry.npmjs.org/"+name)
	if err != nil {
		return nil, err
	}
	latest := str(child(m, "dist-tags"), "latest")
	ver := child(child(m, "versions"), latest)
	desc := str(m, "description")
	if d := str(ver, "description"); d != "" {
		desc = d
	}
	version := str(ver, "version")
	if version == "" {
		version = latest
	}
	return map[string]any{
		"name":        str(m, "name"),
		"version":     version,
		"description": desc,
		"latest":      latest,
		"url":         "https://www.npmjs.com/package/" + name,
	}, nil
}

func extractCrates(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error) {
	name := lastSegment(u.Path)
	m, err := fetchJSON(ctx, f, "https://crates.io/api/v1/crates/"+name)
	if err != nil {
		return nil, err
	}
	crate := child(m, "crate")
	if crate == nil {
		crate = m
	}
	return map[string]any{
		"name":           str(crate, "name"),
		"newest_version": str(crate, "newest_version"),
		"description":    str(crate, "description"),
		"downloads":      num(crate, "downloads"),
		"url":            "https://crates.io/crates/" + name,
	}, nil
}
