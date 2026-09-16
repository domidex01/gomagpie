package extract

import (
	"fmt"
	"os"
	"strings"
)

// Zen routing: OpenCode Zen/Go serve chat models on the OpenAI-compatible
// /chat/completions endpoint and a /messages set on the Anthropic-compatible
// endpoint. Only the base URL differs between Go (flat plan) and Zen
// (pay-as-you-go credits); the adapter choice is per model.

// ZenUsesMessages reports whether a Zen/Go model is served on /messages.
// Unknown names fall back to /chat/completions with a stderr note — a new
// model name must degrade to the most common endpoint, not a failed crawl.
func ZenUsesMessages(model string) bool {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "minimax-") || strings.HasPrefix(m, "claude-") {
		return true
	}
	if strings.HasPrefix(m, "qwen") && (strings.HasSuffix(m, "-max") || strings.HasSuffix(m, "-flash")) {
		return true
	}
	switch {
	case strings.HasPrefix(m, "glm-"), strings.HasPrefix(m, "kimi-"),
		strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "deepseek-"),
		strings.HasPrefix(m, "gemini-"):
		return false
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown zen model %q, trying chat/completions\n", model)
		return false
	}
}
