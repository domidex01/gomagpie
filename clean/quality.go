package clean

import (
	"errors"
	"fmt"
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

// ChallengeMarkers is the single vocabulary shared by fetch's warmup
// detector and clean's quality gate — one list, no drift.
var ChallengeMarkers = []string{
	"just a moment", "attention required", "verify you are human",
	"_abck", "akamai", "cf-chl", "__cf_chl", "datadome", "perimeterx",
	"captcha", "access denied", "access-denied",
}

// ChallengeStatuses are the HTTP statuses that smell like bot protection.
func ChallengeStatuses(status int) bool {
	switch status {
	case 401, 403, 429, 503:
		return true
	}
	return false
}

// HasChallengeMarkers reports whether lower-cased text contains any marker.
// Callers lowercase once and share the haystack; pass explicit markers to
// narrow the vocabulary for a specific rule.
func HasChallengeMarkers(lower string, markers ...string) bool {
	if len(markers) == 0 {
		markers = ChallengeMarkers
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// ThinPageWords bounds every thin-content guard in this package: markers
// only count below it, so rich articles mentioning challenges stay clean.
const ThinPageWords = 200

// denyMarkers extends the shared vocabulary with the generic 403 signal.
// Built once with a fresh backing array — never append to ChallengeMarkers
// in place (it would corrupt the shared list).
var denyMarkers = append(append([]string{}, ChallengeMarkers...), "blocked")

// QualityError is the typed quality failure. Edges match it with errors.As
// to recover the Issue without string-parsing the message.
type QualityError struct {
	Issue Issue
	URL   string
}

func (e *QualityError) Error() string {
	return fmt.Sprintf("quality blocked (%s) for %s", e.Issue, e.URL)
}

func (e *QualityError) Unwrap() error { return ErrQuality }

// QualityIssue recovers the Issue from a wrapped QualityError, or "".
func QualityIssue(err error) Issue {
	var qe *QualityError
	if errors.As(err, &qe) {
		return qe.Issue
	}
	return IssueNone
}

// Classify maps a cleaned page + fetch status + raw body to an Issue.
// Rules are ordered data, not nested branches: the first match wins, so
// precedence is visible in one list. Challenge/deny markers only count on
// thin content (<200 scored words) — a rich article mentioning
// "Just a moment" stays IssueNone.
// isPDF comes from Clean's pre-scope branch (never re-tested here — body
// is scoped HTML, so a byte test would be fragile): PDF bodies are
// binary, so both body-bytes rules are disabled and the pdf-empty rule
// owns the empty case.
func Classify(p CleanedPage, statusCode int, body []byte, isPDF bool) Issue {
	if isPDF {
		// Binary noise must not feed bodyIsRicher or the marker rules —
		// nil the body before lower is computed, not after.
		body = nil
	}
	q := qualityCtx{
		status: statusCode,
		words:  WordCount(p.Markdown),
		md:     p.Markdown,
		title:  p.Title,
		body:   body,
		isPDF:  isPDF,
		lower:  strings.ToLower(string(body) + "\n" + p.Markdown + "\n" + p.Title),
	}
	q.thin = q.words < ThinPageWords
	for _, r := range qualityRules {
		if issue, ok := r.match(q); ok {
			return issue
		}
	}
	return IssueNone
}

type qualityCtx struct {
	status int
	words  int
	thin   bool
	md     string
	title  string
	body   []byte
	isPDF  bool
	lower  string // lower(body + markdown + title), computed once
}

type qualityRule struct {
	name  string
	match func(q qualityCtx) (Issue, bool)
}

// qualityRules fire top-down; the first match wins.
var qualityRules = []qualityRule{
	{"login-status", func(q qualityCtx) (Issue, bool) {
		if (q.status == 401 || q.status == 407) &&
			HasChallengeMarkers(q.lower, "sign in", "log in", "login required", "log in to continue") {
			return IssueLoginRequired, true
		}
		return IssueNone, false
	}},
	{"login-thin", func(q qualityCtx) (Issue, bool) {
		if q.status == 401 && q.thin &&
			HasChallengeMarkers(q.lower, "unauthorized", "login", "sign in") {
			return IssueLoginRequired, true
		}
		return IssueNone, false
	}},
	{"deny-status", func(q qualityCtx) (Issue, bool) {
		if q.status == 403 && q.thin &&
			HasChallengeMarkers(q.lower, denyMarkers...) {
			return IssueAccessDenied, true
		}
		return IssueNone, false
	}},
	{"unavailable-status", func(q qualityCtx) (Issue, bool) {
		if q.status == 429 || q.status == 503 || (q.status >= 500 && q.status <= 599) {
			return IssueUnavailable, true
		}
		return IssueNone, false
	}},
	{"pdf-empty", func(q qualityCtx) (Issue, bool) {
		if q.isPDF && q.words == 0 {
			return IssueEmpty, true // text-less/scan PDF — loud, never empty success
		}
		return IssueNone, false
	}},
	{"empty-shell", func(q qualityCtx) (Issue, bool) {
		if q.thin && bodyIsRicher(q.md, q.body) {
			return IssueEmpty, true
		}
		return IssueNone, false
	}},
	{"challenge-shell", func(q qualityCtx) (Issue, bool) {
		if !q.thin || !HasChallengeMarkers(q.lower) {
			return IssueNone, false
		}
		// Thin page with challenge markers but no status signal: denied
		// when the title/body looks like a challenge shell, empty when
		// the content is a stub, otherwise clean.
		if HasChallengeMarkers(q.lower, "<title>just a moment", "checking your browser", "enable javascript and cookies") {
			return IssueAccessDenied, true
		}
		if q.words < 30 {
			return IssueEmpty, true
		}
		return IssueNone, false
	}},
	{"not-found-thin", func(q qualityCtx) (Issue, bool) {
		if q.status == 404 && q.thin {
			return IssueEmpty, true
		}
		return IssueNone, false
	}},
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
