package scrape_test

import (
	"fmt"
	"strings"
	"testing"

	"magpie/scrape"
)

// words builds n distinct words for boundary rows — generated, never fixtures.
func words(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("w" + fmt.Sprintf("%04d", i) + " ")
	}
	return strings.TrimSpace(sb.String())
}

func TestDiffWords(t *testing.T) {
	flank := strings.Repeat("same ", 5000)
	middlePrev := "alpha beta gamma"
	middleCur := "alpha BETA gamma"
	tests := []struct {
		name        string
		prev, cur   string
		wantEmpty   bool
		wantContain []string
		wantErr     string
	}{
		{name: "identical", prev: "hello world", cur: "hello world", wantEmpty: true},
		{name: "empty", prev: "", cur: "", wantEmpty: true},
		{name: "one-word", prev: "the cat sat", cur: "the dog sat", wantContain: []string{"- cat", "+ dog"}},
		{name: "insert", prev: "a b", cur: "a x b", wantContain: []string{"+ x"}},
		{name: "window", prev: flank + middlePrev + flank, cur: flank + middleCur + flank, wantContain: []string{"- beta", "+ BETA"}},
		{name: "boundary-ok", prev: words(1999), cur: words(1999) + " extra", wantContain: []string{"+ extra"}},
		{name: "boundary-err", prev: words(2001), cur: "q" + strings.ReplaceAll(words(2001), " ", " q"), wantErr: "too large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scrape.DiffWords(tc.prev, tc.cur)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DiffWords: %v", err)
			}
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("got %q, want empty diff", got)
				}
				return
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("diff missing %q:\n%s", want, got)
				}
			}
		})
	}
}
