package scrape

import (
	"errors"
	"strings"
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
		{"format html", func(o *Options) { o.PageFormat = "html" }, ""},
		{"format raw", func(o *Options) { o.PageFormat = "raw" }, ""},
		{"format screenshot", func(o *Options) { o.PageFormat = "screenshot" }, ""},
		{"format bogus", func(o *Options) { o.PageFormat = "xml" },
			`scrape: page format "xml" must be markdown|llm|text|json|html|raw|screenshot`},
		{"screenshot static conflict", func(o *Options) { o.PageFormat = "screenshot"; o.Render = "static" },
			`scrape: page format "screenshot" requires browser rendering (render auto|browser, not static)`},
		{"viewport ok", func(o *Options) { o.PageFormat = "screenshot"; o.Viewport = "1280x800" }, ""},
		{"viewport bogus", func(o *Options) { o.Viewport = "wide" },
			`scrape: viewport "wide" must be WxH (e.g. 1280x800)`},
		{"browser empty", func(o *Options) { o.Browser = "" }, ""},
		{"browser firefox", func(o *Options) { o.Browser = "firefox" }, ""},
		{"browser random", func(o *Options) { o.Browser = "random" }, ""},
		{"browser safari", func(o *Options) { o.Browser = "safari" }, ""},
		{"browser edge", func(o *Options) { o.Browser = "edge" }, ""},
		{"browser ios", func(o *Options) { o.Browser = "ios" }, ""},
		{"browser chrome_android", func(o *Options) { o.Browser = "chrome_android" }, ""},
		{"browser bogus", func(o *Options) { o.Browser = "webkit" },
			`scrape: browser "webkit" must be chrome|firefox|safari|edge|ios|chrome_android|random`},
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

// --- Phase H: actions + lang validation (append-only rows). ---

func TestValidateOptions_Actions(t *testing.T) {
	t.Run("actions with static rejected", func(t *testing.T) {
		err := ValidateOptions(Options{Actions: []string{"click #x"}, Render: "static"})
		var oe *OptionsError
		if !errors.As(err, &oe) {
			t.Fatalf("err = %v, want *OptionsError", err)
		}
		for _, want := range []string{"actions", "static"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err %q missing %q", err, want)
			}
		}
	})
	t.Run("actions with auto and browser pass", func(t *testing.T) {
		for _, render := range []string{"", "auto", "browser"} {
			if err := ValidateOptions(Options{Actions: []string{"click #x", "wait 30000"}, Render: render}); err != nil {
				t.Errorf("render %q: %v", render, err)
			}
		}
	})
	t.Run("malformed DSL surfaces pre-I/O with verb and line", func(t *testing.T) {
		err := ValidateOptions(Options{Actions: []string{"click #ok", "frobnicate #x"}})
		var oe *OptionsError
		if !errors.As(err, &oe) {
			t.Fatalf("err = %v, want *OptionsError (from ValidateOptions, not the rod path)", err)
		}
		for _, want := range []string{"line 2", "frobnicate"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err %q missing %q", err, want)
			}
		}
	})
	t.Run("wait cap surfaces as OptionsError", func(t *testing.T) {
		if err := ValidateOptions(Options{Actions: []string{"wait 99999"}}); err == nil || !strings.Contains(err.Error(), "30000") {
			t.Errorf("err = %v, want the 30000 ms cap named", err)
		}
	})
	t.Run("screenshot format plus actions pass", func(t *testing.T) {
		// One browser session: actions run, then the capture (final step).
		if err := ValidateOptions(Options{PageFormat: "screenshot", Actions: []string{"click #consent"}}); err != nil {
			t.Errorf("screenshot+actions: %v", err)
		}
	})
}

// TestValidateOptions_LangControlChars — H.7 gate test 2: the lang value
// crosses into a raw HTTP header, so a CRLF there is header injection.
// Rejected pre-I/O (zero bytes on the wire), message names the rule.
func TestValidateOptions_LangControlChars(t *testing.T) {
	err := ValidateOptions(Options{Lang: "en\r\nX-Evil: 1"})
	var oe *OptionsError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want *OptionsError", err)
	}
	for _, want := range []string{"lang", "control characters"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q missing %q", err, want)
		}
	}
	// BCP47-realistic values must never be rejected (passthrough, no
	// grammar policing).
	for _, ok := range []string{"en", "fr-CA,fr;q=0.9", "en-US,en;q=0.9,es;q=0.8"} {
		if err := ValidateOptions(Options{Lang: ok}); err != nil {
			t.Errorf("lang %q: %v", ok, err)
		}
	}
	// Same check alongside other options (profile + lang is a common combo).
	if err := ValidateOptions(Options{Profile: "chrome", Lang: "fr-CA,fr;q=0.9"}); err != nil {
		t.Errorf("profile+lang: %v", err)
	}
}
