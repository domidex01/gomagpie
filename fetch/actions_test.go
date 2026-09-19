package fetch_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/domidex01/magpie/fetch"
)

func TestParseActions(t *testing.T) {
	t.Run("seven verbs field-by-field", func(t *testing.T) {
		acts, err := fetch.ParseActions([]string{
			"click #consent-accept",
			"type #search multi word query text",
			"scroll 500",
			"scroll top",
			"scroll bottom",
			"wait 1200",
			"wait-for .item:nth-child(30)",
			"screenshot /tmp/shot one.png",
			"eval-js window.scrollTo(0, document.body.scrollHeight)",
		})
		if err != nil {
			t.Fatalf("ParseActions: %v", err)
		}
		want := []fetch.Action{
			{Verb: "click", Sel: "#consent-accept"},
			{Verb: "type", Sel: "#search", Text: "multi word query text"},
			{Verb: "scroll", Text: "500", Ms: 500},
			{Verb: "scroll", Text: "top"},
			{Verb: "scroll", Text: "bottom"},
			{Verb: "wait", Ms: 1200},
			{Verb: "wait-for", Sel: ".item:nth-child(30)"},
			{Verb: "screenshot", Text: "/tmp/shot one.png"},
			{Verb: "eval-js", Text: "window.scrollTo(0, document.body.scrollHeight)"},
		}
		if len(acts) != len(want) {
			t.Fatalf("got %d actions, want %d: %+v", len(acts), len(want), acts)
		}
		for i := range want {
			if acts[i] != want[i] {
				t.Errorf("action %d = %+v, want %+v", i, acts[i], want[i])
			}
		}
	})

	t.Run("comments blanks skipped; empty input", func(t *testing.T) {
		acts, err := fetch.ParseActions([]string{
			"# a comment line",
			"",
			"   ",
			"click #x",
			"  # indented comment",
		})
		if err != nil {
			t.Fatalf("ParseActions: %v", err)
		}
		if len(acts) != 1 || acts[0].Verb != "click" || acts[0].Sel != "#x" {
			t.Errorf("actions = %+v, want one click #x", acts)
		}
		empty, err := fetch.ParseActions(nil)
		if err != nil || len(empty) != 0 {
			t.Errorf("nil input = (%+v, %v), want empty,nil", empty, err)
		}
		empty, err = fetch.ParseActions([]string{"", "# only comments"})
		if err != nil || len(empty) != 0 {
			t.Errorf("comment-only input = (%+v, %v), want empty,nil", empty, err)
		}
	})

	t.Run("errors carry line number and verb", func(t *testing.T) {
		cases := []struct {
			name  string
			lines []string
			verb  string // substring the error must name ("" = none)
			line  int    // 1-based input line the error must name
		}{
			{"unknown verb", []string{"click #a", "frobnicate #x"}, "frobnicate", 2},
			{"empty click selector", []string{"click"}, "click", 1},
			{"empty type text", []string{"type #field"}, "type", 1},
			{"empty screenshot path", []string{"screenshot"}, "screenshot", 1},
			{"empty eval-js expr", []string{"eval-js   "}, "eval-js", 1},
			{"non-numeric wait", []string{"wait soon"}, "wait", 1},
			{"negative wait", []string{"wait -5"}, "wait", 1},
			{"wait cap", []string{"wait 99999"}, "30000", 1},
			{"bad scroll arg", []string{"scroll sideways"}, "scroll", 1},
			{"negative scroll", []string{"scroll -3"}, "scroll", 1},
			{"empty wait-for", []string{"wait-for"}, "wait-for", 1},
			{"line number counts comments", []string{"# c", "", "bogus #x"}, "bogus", 3},
		}
		for _, c := range cases {
			_, err := fetch.ParseActions(c.lines)
			if err == nil {
				t.Errorf("%s: want error, got nil", c.name)
				continue
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("line %d", c.line)) {
				t.Errorf("%s: error %q lacks line number %d", c.name, err, c.line)
			}
			if c.verb != "" && !strings.Contains(err.Error(), c.verb) {
				t.Errorf("%s: error %q lacks verb %q", c.name, err, c.verb)
			}
		}
	})

	t.Run("wait cap boundary accepted", func(t *testing.T) {
		acts, err := fetch.ParseActions([]string{"wait 30000"})
		if err != nil || len(acts) != 1 || acts[0].Ms != 30000 {
			t.Errorf("wait 30000 = (%+v, %v), want one 30000ms wait", acts, err)
		}
	})
}
