package clean_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/motherlodelab/magpie/clean"
)

const scopeShell = `<html><head><title>Shell</title></head><body>` +
	`<nav><a href="/home">nav-link-1</a><a href="/about">nav-link-2</a></nav>` +
	`<article><h1>Article Headline Here</h1><p>First article paragraph with enough honest prose to survive trafilatura extraction cleanly.</p><p>Second article paragraph continues the story with more detail and substance.</p></article>` +
	`<aside class="sidebar"><p>Sidebar promo text here.</p></aside>` +
	`<footer><p>footer legal text</p></footer>` +
	`</body></html>`

func applyScope(s clean.Scope) (string, []string) {
	var warns []string
	out := clean.ApplyScope(scopeShell, s, func(w string) { warns = append(warns, w) })
	return out, warns
}

func scopedMD(t *testing.T, s clean.Scope) string {
	t.Helper()
	got, err := clean.Clean(context.Background(), clean.RawPage{
		HTML: []byte(scopeShell), FinalURL: "https://example.com/shell", Scope: s,
	})
	if err != nil {
		t.Fatalf("Clean scoped: %v", err)
	}
	return got.Markdown
}

func TestScope_Include(t *testing.T) {
	md := scopedMD(t, clean.Scope{Include: []string{"article"}})
	if !strings.Contains(md, "Article Headline") {
		t.Errorf("include article lost headline:\n%s", md)
	}
	if strings.Contains(md, "nav-link-1") || strings.Contains(md, "footer legal") {
		t.Errorf("include article kept nav/footer:\n%s", md)
	}
}

func TestScope_Union(t *testing.T) {
	// Union is an ApplyScope HTML contract: trafilatura downstream keeps only
	// the main subtree, so markdown-level union is not asserted.
	out, _ := applyScope(clean.Scope{Include: []string{"article", "aside"}})
	if !strings.Contains(out, "Article Headline") || !strings.Contains(out, "Sidebar promo") {
		t.Errorf("union include lost a branch:\n%s", out)
	}
	md := scopedMD(t, clean.Scope{Include: []string{"article", "aside"}})
	if !strings.Contains(md, "Article Headline") {
		t.Errorf("union include lost article:\n%s", md)
	}
}

func TestScope_Exclude(t *testing.T) {
	md := scopedMD(t, clean.Scope{Exclude: []string{"nav", ".sidebar", "footer"}})
	if !strings.Contains(md, "Article Headline") {
		t.Errorf("exclude dropped article:\n%s", md)
	}
	if strings.Contains(md, "nav-link-1") || strings.Contains(md, "Sidebar promo") || strings.Contains(md, "footer legal") {
		t.Errorf("exclude kept scoped nodes:\n%s", md)
	}
}

func TestScope_InvalidWarnsNotFails(t *testing.T) {
	for _, bad := range []string{"??bad[[", ":::"} {
		out, warns := applyScope(clean.Scope{Include: []string{bad}})
		joined := strings.Join(warns, "\n")
		if !strings.Contains(joined, "invalid selector") {
			t.Errorf("%q: warnings %q lack 'invalid selector'", bad, joined)
		}
		_ = out
	}
	md := scopedMD(t, clean.Scope{Include: []string{"??bad[["}})
	if !strings.Contains(md, "Article Headline") {
		t.Errorf("invalid selector changed output:\n%s", md)
	}
}

func TestScope_NoMatchEmpty(t *testing.T) {
	out, warns := applyScope(clean.Scope{Include: []string{"section.nope"}})
	if out != "" {
		t.Errorf("no-match include = %q, want empty", out)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "matched nothing") {
		t.Errorf("warnings %q lack 'matched nothing'", warns)
	}
}

func TestScope_Cap100(t *testing.T) {
	sels := make([]string, 101)
	for i := range sels {
		sels[i] = fmt.Sprintf("div.c%d", i)
	}
	_, warns := applyScope(clean.Scope{Include: sels})
	found := false
	for _, w := range warns {
		if strings.Contains(w, "truncated to 100") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %q lack truncation note", warns)
	}
}

func TestScope_ExcludeWins(t *testing.T) {
	md := scopedMD(t, clean.Scope{Include: []string{"body"}, Exclude: []string{"nav"}})
	if strings.Contains(md, "nav-link-1") {
		t.Errorf("exclude did not win over include:\n%s", md)
	}
	if !strings.Contains(md, "Article Headline") {
		t.Errorf("exclude wins dropped article:\n%s", md)
	}
}

func TestScope_OnlyMainAndEmptyIdentity(t *testing.T) {
	a, _ := applyScope(clean.Scope{OnlyMainContent: true})
	b, _ := applyScope(clean.Scope{})
	if a != scopeShell || b != scopeShell {
		t.Error("only-main-content / empty scope must be byte-identical to input")
	}
}
