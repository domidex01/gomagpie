package clean

import (
	"encoding/json"
	"strings"
)

// PlayerResponseHTML locates ytInitialPlayerResponse in page HTML and parses
// the balanced-brace object that follows it. Single home for the scan —
// vertical/youtube.go reuses this (vertical → clean is the allowed import
// direction; clean must never import vertical).
func PlayerResponseHTML(html []byte) (map[string]any, bool) {
	const marker = "ytInitialPlayerResponse"
	i := strings.Index(string(html), marker)
	if i < 0 {
		return nil, false
	}
	rest := string(html)[i+len(marker):]
	// Skip to the first opening brace (past "=" and whitespace).
	j := strings.IndexByte(rest, '{')
	if j < 0 {
		return nil, false
	}
	frag := rest[j:]
	depth, inStr, esc := 0, false, false
	for k := 0; k < len(frag); k++ {
		c := frag[k]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				var m map[string]any
				if err := json.Unmarshal([]byte(frag[:k+1]), &m); err != nil {
					return nil, false
				}
				return m, true
			}
		}
	}
	return nil, false // truncated braces — fail closed, never panic
}

// PlayerDetails picks videoDetails{title, author, shortDescription,
// viewCount} from a watch-shaped page. Nil when absent or title-less.
func PlayerDetails(html []byte) map[string]string {
	m, ok := PlayerResponseHTML(html)
	if !ok {
		return nil
	}
	vd, _ := m["videoDetails"].(map[string]any)
	if vd == nil {
		return nil
	}
	title, _ := vd["title"].(string)
	if strings.TrimSpace(title) == "" {
		return nil
	}
	author, _ := vd["author"].(string)
	desc, _ := vd["shortDescription"].(string)
	views, _ := vd["viewCount"].(string)
	return map[string]string{"title": title, "author": author, "description": desc, "views": views}
}

// IslandText walks sidecar JSON leaf strings (JSON-LD, __NEXT_DATA__,
// __NUXT__) and returns the content-bearing ones: drop <40-char noise and
// CSS/JS-smelling strings, dedupe preserving order, cap at maxChars on a
// paragraph boundary.
func IslandText(sidecar json.RawMessage, maxChars int) string {
	if len(sidecar) == 0 || maxChars <= 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(sidecar, &v); err != nil {
		return ""
	}
	var leaves []string
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			if keepLeaf(t) {
				leaves = append(leaves, strings.TrimSpace(t))
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	if len(leaves) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(leaves))
	out := leaves[:0]
	for _, l := range leaves {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	joined := strings.Join(out, "\n\n")
	if len(joined) <= maxChars {
		return joined
	}
	cut := strings.LastIndex(joined[:maxChars], "\n\n")
	if cut <= 0 {
		return joined[:maxChars]
	}
	return joined[:cut]
}

func keepLeaf(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 40 {
		return false
	}
	// ponytail: unranked leaf order with substring noise filters; ceiling =
	// boilerplate may precede content — upgrade = score leaves by length/density.
	if strings.ContainsAny(s, "{};") || strings.HasPrefix(s, "function(") {
		return false
	}
	return true
}

// isYouTubeHost reports watch-shaped hosts for the player-prepend rule.
func isYouTubeHost(rawURL string) bool {
	h := strings.ToLower(rawURL)
	return strings.Contains(h, "youtube.com") || strings.Contains(h, "youtu.be")
}
