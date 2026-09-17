package scrape

import (
	"errors"
	"testing"
)

// Characterization: these strings are API surface (CLI exit map + MCP
// clients see them verbatim through Run). Captured from Run's pre-I/O
// validation on 2026-09-17. Change only with a declared message change,
// and update cli/mcp expectations in the same commit.
func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string // "" = must pass
	}{
		{"defaults pass", func(o *Options) {}, ""},
		{"render empty is auto", func(o *Options) { o.Render = "" }, ""},
		{"render static", func(o *Options) { o.Render = "static" }, ""},
		{"render browser", func(o *Options) { o.Render = "browser" }, ""},
		{"render bogus", func(o *Options) { o.Render = "nope" },
			`scrape: render "nope" must be auto|static|browser`},
		{"format empty", func(o *Options) { o.PageFormat = "" }, ""},
		{"format llm", func(o *Options) { o.PageFormat = "llm" }, ""},
		{"format text", func(o *Options) { o.PageFormat = "text" }, ""},
		{"format json", func(o *Options) { o.PageFormat = "json" }, ""},
		{"format bogus", func(o *Options) { o.PageFormat = "xml" },
			`scrape: page format "xml" must be markdown|llm|text|json`},
		{"browser empty", func(o *Options) { o.Browser = "" }, ""},
		{"browser firefox", func(o *Options) { o.Browser = "firefox" }, ""},
		{"browser random", func(o *Options) { o.Browser = "random" }, ""},
		{"browser bogus", func(o *Options) { o.Browser = "safari" },
			`scrape: browser "safari" must be chrome|firefox|random`},
		{"vertical off", func(o *Options) { o.Vertical = "" }, ""},
		{"vertical auto", func(o *Options) { o.Vertical = "auto" }, ""},
		{"vertical known", func(o *Options) { o.Vertical = "reddit" }, ""},
		{"vertical bogus", func(o *Options) { o.Vertical = "tumblr" },
			`scrape: vertical "tumblr" unknown (see ` + "`magpie vertical --list`" + `)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o Options
			tt.mutate(&o)
			err := ValidateOptions(o)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("want %q, got %v", tt.wantErr, err)
			}
			// The typed error is the contract: cli.exitFor maps it to 2.
			var oe *OptionsError
			if !errors.As(err, &oe) {
				t.Fatalf("error %v is not *OptionsError", err)
			}
		})
	}
}

func TestRunNilDBStillFirst(t *testing.T) {
	// nil DB must win over a bad option so the run row is never created
	// for a request that cannot execute.
	_, err := Run(t.Context(), Deps{}, "https://example.com", Options{Render: "nope"})
	if err == nil || err.Error() != "scrape: nil DB" {
		t.Fatalf("want nil-DB error, got %v", err)
	}
}
