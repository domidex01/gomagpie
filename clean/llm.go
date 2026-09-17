package clean

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// llmStructuredCap caps the ## Structured Data block (16 KB).
const llmStructuredCap = 16 * 1024

// ToLLMText renders a token-optimized plain-text view: metadata header →
// cleaned body → deduped ## Links → gated ## Structured Data.
func ToLLMText(p CleanedPage) string {
	var b strings.Builder
	if h := llmHeader(p); h != "" {
		b.WriteString(h)
		b.WriteString("\n\n")
	}
	body, links := llmBody(p.Markdown)
	b.WriteString(strings.TrimSpace(body))
	if len(links) > 0 {
		b.WriteString("\n\n## Links\n\n")
		for _, l := range links {
			fmt.Fprintf(&b, "- [%s](%s)\n", l.text, l.url)
		}
	}
	if sd := llmStructured(p.StructuredData, body); sd != "" {
		b.WriteString("\n## Structured Data\n\n")
		b.WriteString(sd)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func llmHeader(p CleanedPage) string {
	if p.Title == "" && p.Metadata.Description == "" && p.Metadata.Author == "" &&
		p.Metadata.Date == "" && p.FinalURL == "" {
		return ""
	}
	var b strings.Builder
	title := p.Title
	if title == "" {
		title = "Untitled"
	}
	b.WriteString("# " + title + "\n")
	if p.FinalURL != "" {
		b.WriteString("> Source: " + p.FinalURL + "\n")
	}
	if p.Metadata.Description != "" {
		b.WriteString("> " + oneLine(p.Metadata.Description) + "\n")
	}
	var byline []string
	if p.Metadata.Author != "" {
		byline = append(byline, "By "+p.Metadata.Author)
	}
	if p.Metadata.Date != "" {
		byline = append(byline, p.Metadata.Date)
	}
	if len(byline) > 0 {
		b.WriteString("> " + strings.Join(byline, " · ") + "\n")
	}
	return strings.TrimSpace(b.String())
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

type llmLink struct {
	text, url string
}

var (
	mdLinkRe     = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	mdImageRe    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	boldRe       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italicRe     = regexp.MustCompile(`(^|[\s(])\*([^*\n]+)\*([\s).,;:!?]|$)`)
	codeRe       = regexp.MustCompile("`([^`\n]+)`")
	cssClassRe   = regexp.MustCompile(`^\s*(\.[a-zA-Z][\w-]*)(\s+\.[a-zA-Z][\w-]*)+\s*$`)
	cssBlobRe    = regexp.MustCompile(`^\s*\{[^}]{20,}\}\s*$`)
	paginateRe   = regexp.MustCompile(`(?i)(/page/\d+|#comments|#reply|reply-to|comment-\d+)`)
	blockquoteRe = regexp.MustCompile(`^\s*>\s?`)
	tableSepRe   = regexp.MustCompile(`^\s*\|(?:[\s:\-]*\|)+\s*$`)
	statNumRe    = regexp.MustCompile(`^\s*[\d.,]+%?\s*$`)
	statLabelRe  = regexp.MustCompile(`^\s*[A-Za-z][\w\s&/-]{1,40}\s*$`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	quoteRe      = regexp.MustCompile(`(?m)^\s*>\s?`)
	fenceRe      = regexp.MustCompile("`{3}[^`]*`{3}")
)

// llmBody cleans markdown line-by-line and extracts deduped links.
func llmBody(md string) (string, []llmLink) {
	lines := strings.Split(md, "\n")
	var out []string
	seenPara := map[string]bool{}
	var links []llmLink
	seenURL := map[string]bool{}
	collectLinks := func(line string) {
		// Anchor links only: image sources (![alt](src)) stay out of the
		// footer — their alt text already preserves them in place, and an
		// LLM cannot navigate to an image URL.
		for _, idx := range mdLinkRe.FindAllStringSubmatchIndex(line, -1) {
			if idx[0] > 0 && line[idx[0]-1] == '!' {
				continue
			}
			m := mdLinkRe.FindStringSubmatch(line[idx[0]:idx[1]])
			u := m[2]
			if u == "" || strings.HasPrefix(u, "#") || seenURL[u] {
				continue
			}
			if paginateRe.MatchString(u) || strings.Contains(strings.ToLower(m[1]), "reply") {
				continue
			}
			seenURL[u] = true
			text := strings.TrimSpace(m[1])
			if text == "" {
				text = u
			}
			links = append(links, llmLink{text: text, url: u})
		}
	}
	for _, ln := range lines {
		collectLinks(ln)
		// Drop decorative images (empty alt); keep informative ones as alt text.
		if m := mdImageRe.FindStringSubmatch(ln); m != nil {
			if strings.TrimSpace(m[1]) == "" {
				continue
			}
			ln = mdImageRe.ReplaceAllString(ln, "$1")
		}
		// Links become plain text in the body (targets live in ## Links).
		ln = mdLinkRe.ReplaceAllString(ln, "$1")
		// Strip bold/italic/code markers, keep text.
		ln = boldRe.ReplaceAllString(ln, "$1")
		ln = italicRe.ReplaceAllString(ln, "${1}$2$3")
		ln = codeRe.ReplaceAllString(ln, "$1")
		ln = blockquoteRe.ReplaceAllString(ln, "")
		ln = strings.TrimSpace(ln)
		// GFM tables: drop the zero-content separator row, compact padding.
		if strings.HasPrefix(ln, "|") {
			if strings.Contains(ln, "-") && tableSepRe.MatchString(ln) {
				continue
			}
			ln = compactTableRow(ln)
		}
		if ln == "" {
			out = append(out, "")
			continue
		}
		// Drop CSS-class noise lines and {…} blobs.
		if cssClassRe.MatchString(ln) || cssBlobRe.MatchString(ln) {
			continue
		}
		// Drop pagination/comment links.
		if paginateRe.MatchString(ln) {
			continue
		}
		out = append(out, ln)
	}
	out = collapseLogoRuns(out)
	out = mergeStatLines(out)
	// Dedup paragraphs/headings preserving order.
	var deduped []string
	for _, ln := range out {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "- ") || strings.HasPrefix(t, ">") {
			if t != "" && strings.HasPrefix(t, "#") {
				if seenPara["h:"+t] {
					continue
				}
				seenPara["h:"+t] = true
			}
			deduped = append(deduped, ln)
			continue
		}
		if seenPara["p:"+t] {
			continue
		}
		seenPara["p:"+t] = true
		deduped = append(deduped, ln)
	}
	// Collapse 3+ blank runs to one.
	var final []string
	blanks := 0
	for _, ln := range deduped {
		if strings.TrimSpace(ln) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		final = append(final, ln)
	}
	return strings.TrimSpace(strings.Join(final, "\n")), links
}

// compactTableRow trims per-cell padding: "| a  | b |" → "| a | b |".
func compactTableRow(ln string) string {
	cells := strings.Split(ln, "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	// Drop the empties from leading/trailing pipes, keep inner empties
	// (colspan mirrors) so column counts survive.
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return "| " + strings.Join(cells, " | ") + " |"
}

// collapseLogoRuns drops runs of ≥2 consecutive short site-name/logo lines.
func collapseLogoRuns(lines []string) []string {
	var out []string
	run := 0
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		short := t != "" && len(t) < 40 && !strings.ContainsAny(t, ".!?|:") &&
			!strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "|") && !strings.HasPrefix(t, "- ") && !strings.HasPrefix(t, ">")
		if short {
			// Peek: run of shorts.
			j := i
			for j < len(lines) && isShortLine(lines[j]) {
				j++
			}
			if j-i >= 2 {
				if run == 0 {
					// Keep the first, drop the rest of the run.
					out = append(out, ln)
				}
				run++
				if i+1 < j {
					continue
				}
				run = 0
				continue
			}
		}
		run = 0
		out = append(out, ln)
	}
	return out
}

func isShortLine(ln string) bool {
	t := strings.TrimSpace(ln)
	return t != "" && len(t) < 40 && !strings.ContainsAny(t, ".!?|:") &&
		!strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "|") && !strings.HasPrefix(t, "- ") && !strings.HasPrefix(t, ">")
}

// mergeStatLines joins "12.99" + "Price" pairs into "Price: 12.99".
func mergeStatLines(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		if i+1 < len(lines) && statNumRe.MatchString(lines[i]) && statLabelRe.MatchString(lines[i+1]) {
			out = append(out, strings.TrimSpace(lines[i+1])+": "+strings.TrimSpace(lines[i]))
			i++
			continue
		}
		out = append(out, lines[i])
	}
	return out
}

// llmStructured gates the sidecar: drops WebSite/WebPage chrome blocks,
// scrubs >500-char strings already contained in the body, caps at 16 KB.
func llmStructured(raw json.RawMessage, body string) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	v = scrubStructured(v, body)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > llmStructuredCap {
		s = s[:llmStructuredCap] + "\n…(truncated)"
	}
	if strings.TrimSpace(s) == "null" || strings.TrimSpace(s) == "{}" || strings.TrimSpace(s) == "[]" {
		return ""
	}
	return "```json\n" + s + "\n```\n"
}

func scrubStructured(v any, body string) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range t {
			if k == "articleBody" {
				continue
			}
			if typ, ok := val.(map[string]any); ok {
				if ty, ok := typ["@type"].(string); ok && (ty == "WebSite" || ty == "WebPage") {
					continue
				}
			}
			if ty, ok := t["@type"].(string); ok && (ty == "WebSite" || ty == "WebPage") && k != "@type" && k != "@context" {
				continue
			}
			sv := scrubStructured(val, body)
			if s, ok := sv.(string); ok && len(s) > 500 && strings.Contains(body, s[:200]) {
				continue
			}
			out[k] = sv
		}
		return out
	case []any:
		var out []any
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				if ty, ok := m["@type"].(string); ok && (ty == "WebSite" || ty == "WebPage") {
					continue
				}
			}
			out = append(out, scrubStructured(e, body))
		}
		if out == nil {
			return []any{}
		}
		return out
	case string:
		if len(t) > 500 && strings.Contains(body, t[:200]) {
			return ""
		}
		return t
	default:
		return v
	}
}

// ToText strips markdown markers to plain text (link→text, images dropped).
func ToText(md string) string {
	s := mdImageRe.ReplaceAllString(md, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = headingRe.ReplaceAllString(s, "")
	s = boldRe.ReplaceAllString(s, "$1")
	s = italicRe.ReplaceAllString(s, "${1}$2$3")
	s = codeRe.ReplaceAllString(s, "$1")
	s = quoteRe.ReplaceAllString(s, "")
	s = fenceRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "*", "")
	s = regexp.MustCompile(`[ \t]+`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?m)^[ \t]+`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Render renders a CleanedPage in the named format: ""/markdown = identity,
// llm = ToLLMText, text = ToText, json = page envelope.
func Render(p CleanedPage, format string) (string, error) {
	switch format {
	case "", "markdown":
		return p.Markdown, nil
	case "llm":
		return ToLLMText(p), nil
	case "text":
		return ToText(p.Markdown), nil
	case "json":
		return renderJSON(p, format), nil
	default:
		return "", fmt.Errorf("clean: page format %q must be markdown|llm|text|json", format)
	}
}

func renderJSON(p CleanedPage, format string) string {
	links, images := pageLinks(p.Markdown)
	env := map[string]any{
		"url":             p.FinalURL,
		"final_url":       p.FinalURL,
		"title":           p.Title,
		"markdown":        p.Markdown,
		"structured_data": jsonRaw(p.StructuredData),
		"page_format":     format,
		"content":         p.Markdown,
		"metadata":        p.Metadata,
		"links":           links,
		"images":          images,
		"word_count":      WordCount(p.Markdown),
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func jsonRaw(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(r, &v); err != nil {
		return string(r)
	}
	return v
}

func pageLinks(md string) ([]string, []string) {
	var links, images []string
	seen := map[string]bool{}
	for _, m := range mdLinkRe.FindAllStringSubmatch(md, -1) {
		if !seen[m[2]] {
			seen[m[2]] = true
			links = append(links, m[2])
		}
	}
	seenImg := map[string]bool{}
	for _, m := range mdImageRe.FindAllStringSubmatch(md, -1) {
		if !seenImg[m[2]] {
			seenImg[m[2]] = true
			images = append(images, m[2])
		}
	}
	if links == nil {
		links = []string{}
	}
	if images == nil {
		images = []string{}
	}
	return links, images
}
