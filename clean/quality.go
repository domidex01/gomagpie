package clean

import (
	"errors"
	"regexp"
	"strings"
)

// Issue classifies a cleaned page. "" (IssueNone) = ok.
type Issue string

const (
	IssueNone          Issue = ""
	IssueEmpty         Issue = "empty"
	IssueAccessDenied  Issue = "access-denied"
	IssueUnavailable   Issue = "unavailable"
	IssueLoginRequired Issue = "login-required"
)

// ErrQuality wraps quality blocks at the edges (scrape.Run, crawl).
var ErrQuality = errors.New("quality blocked")

// Classify maps a cleaned page + fetch status + raw body to an Issue.
// Rule order: login-required → access-denied → unavailable → empty → none.
// Challenge/deny markers only count on thin content (<200 scored words) —
// a rich article mentioning "Just a moment" stays IssueNone.
func Classify(p CleanedPage, statusCode int, body []byte) Issue {
	words := WordCount(p.Markdown)
	thin := words < 200
	lower := strings.ToLower(string(body) + "\n" + p.Markdown + "\n" + p.Title)

	hasAny := func(markers ...string) bool {
		for _, m := range markers {
			if strings.Contains(lower, m) {
				return true
			}
		}
		return false
	}

	if (statusCode == 401 || statusCode == 407) &&
		hasAny("sign in", "log in", "login required", "log in to continue") {
		return IssueLoginRequired
	}
	if statusCode == 401 && thin && hasAny("unauthorized", "login", "sign in") {
		return IssueLoginRequired
	}
	if statusCode == 403 && thin &&
		hasAny("just a moment", "attention required", "verify you are human",
			"_abck", "akamai", "cf-chl", "__cf_chl", "datadome", "perimeterx",
			"captcha", "access denied", "access-denied", "blocked") {
		return IssueAccessDenied
	}
	if statusCode == 429 || statusCode == 503 || (statusCode >= 500 && statusCode <= 599) {
		return IssueUnavailable
	}
	if thin && bodyIsRicher(p.Markdown, body) {
		return IssueEmpty
	}
	if thin && hasAny("just a moment", "attention required", "verify you are human",
		"_abck", "akamai", "cf-chl", "__cf_chl", "datadome", "perimeterx", "captcha") {
		// Thin page with challenge markers but no status signal: treat as
		// denied when the title/body looks like a challenge shell.
		if hasAny("<title>just a moment", "checking your browser", "enable javascript and cookies") {
			return IssueAccessDenied
		}
		// Thin content with no richer body: empty.
		if words < 30 {
			return IssueEmpty
		}
	}
	if statusCode == 404 && thin {
		return IssueEmpty
	}
	return IssueNone
}

// bodyIsRicher reports whether the raw HTML body carries substantially more
// text than the scored markdown (trafilatura kept only a shell), or the
// scored output is a stub (<20 words) with a richer body behind it.
func bodyIsRicher(markdown string, body []byte) bool {
	mdWords := WordCount(markdown)
	if mdWords >= 200 {
		return false
	}
	text := stripTags(strings.ToLower(string(body)))
	bodyWords := len(strings.Fields(text))
	if bodyWords <= mdWords {
		return false
	}
	// Substantial gap, or a stub (<20 scored words) dwarfed 2:1 by its body.
	return bodyWords-mdWords > 50 || (mdWords < 20 && bodyWords > mdWords*2)
}

var tagRe = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)

func stripTags(s string) string {
	return tagRe.ReplaceAllString(s, " ")
}

var mdNoiseRe = regexp.MustCompile("[#*_`>~\\-|\\[\\]()!]+")

// WordCount counts words in markdown, ignoring fence/marker noise.
// Links count as their text (targets dropped), images count nothing.
func WordCount(s string) int {
	s = mdImageRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	lines := strings.Split(s, "\n")
	n := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "```") {
			continue
		}
		ln = mdNoiseRe.ReplaceAllString(ln, " ")
		n += len(strings.Fields(ln))
	}
	return n
}
