package fetch

import (
	"strings"

	"github.com/domidex01/magpie/clean"
)

// HeaderProfiles are header bundles applied to static fetches. "default" is
// today's header set verbatim; the browser names mirror impersonate-http's
// Profiles (same UA/client-hint bundles) so the cleartext fallback matches
// the fingerprint. Stock TLS throughout — these are plain header maps, NOT
// uTLS impersonation. Accept-Encoding is deliberately absent: the Go
// transport negotiates its own.
var HeaderProfiles = map[string]map[string]string{
	"default": {
		"User-Agent":      "github.com/domidex01/magpie/1.0 (+https://github.com/you/magpie)",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "fr-FR,fr;q=0.9,en;q=0.8",
	},
	"chrome": {
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.9",
		"Sec-CH-UA":                 `"Chromium";v="126", "Google Chrome";v="126", "Not-A.Brand";v="99"`,
		"Sec-CH-UA-Mobile":          "?0",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	},
	"firefox": {
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.7",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Upgrade-Insecure-Requests": "1",
	},
	// G.3 breadth: UA/client-hint bundles copied from impersonate-http
	// v0.4.0 profile.go so header and TLS fingerprint agree.
	"safari": {
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	},
	"edge": {
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"Accept-Language":           "en-US,en;q=0.9",
		"Sec-CH-UA":                 `"Microsoft Edge";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		"Sec-CH-UA-Mobile":          "?0",
		"Sec-CH-UA-Platform":        `"Windows"`,
		"Upgrade-Insecure-Requests": "1",
	},
	"ios": {
		"User-Agent":      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	},
	"chrome_android": {
		"User-Agent":                "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"Accept-Language":           "en-US,en;q=0.9",
		"Sec-CH-UA":                 `"Not(A:Brand";v="99", "Google Chrome";v="131", "Chromium";v="131"`,
		"Sec-CH-UA-Mobile":          "?1",
		"Sec-CH-UA-Platform":        `"Android"`,
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	},
}

// profileHeaders resolves a profile name to its header map.
// Unknown names fall back to "default" with no error.
func profileHeaders(profile string) map[string]string {
	if h, ok := HeaderProfiles[profile]; ok {
		return h
	}
	return HeaderProfiles["default"]
}

// IsChallengePage reports whether a response looks like a bot-protection
// challenge: body <15 KB AND (challenge status OR marker match).
// Rich pages (≥200 scored words) never count — an article mentioning
// "Just a moment" must pass clean. Vocabulary shared with clean.Classify.
func IsChallengePage(body []byte, status int) bool {
	if clean.WordCount(string(body)) >= clean.ThinPageWords {
		return false
	}
	if clean.ChallengeStatuses(status) && len(body) < 15*1024 {
		return true
	}
	if len(body) >= 15*1024 {
		return false
	}
	return clean.HasChallengeMarkers(strings.ToLower(string(body)))
}
