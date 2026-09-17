package fetch

import (
	"net/http"
	"strings"
)

// HeaderProfiles are header bundles applied to static fetches. "default" is
// today's header set verbatim; chrome/firefox add realistic browser headers.
// Stock TLS throughout — these are plain header maps, NOT uTLS impersonation.
var HeaderProfiles = map[string]map[string]string{
	"default": {
		"User-Agent":      "magpie/1.0 (+https://github.com/you/gomagpie)",
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
}

// profileHeaders resolves a profile name to its header map.
// Unknown names fall back to "default" with no error.
func profileHeaders(profile string) map[string]string {
	if h, ok := HeaderProfiles[profile]; ok {
		return h
	}
	return HeaderProfiles["default"]
}

var challengeMarkers = []string{
	"just a moment", "attention required", "verify you are human",
	"_abck", "akamai", "cf-chl", "__cf_chl", "datadome", "perimeterx",
	"captcha", "access denied",
}

// IsChallengePage reports whether a response looks like a bot-protection
// challenge: body <15 KB AND (challenge status OR title/marker match).
// Rich pages (≥200 words) never count — an article mentioning
// "Just a moment" must pass clean.
func IsChallengePage(body []byte, h http.Header, status int) bool {
	if wordCount(string(body)) >= 200 {
		return false
	}
	switch status {
	case 401, 403, 429, 503:
		if len(body) < 15*1024 {
			return true
		}
	}
	if len(body) >= 15*1024 {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, m := range challengeMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}
