package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ExtractInput is one LLM extraction job.
type ExtractInput struct {
	Markdown       string
	StructuredData json.RawMessage
	Schema         *Schema
	PromptExtra    string // repair context appended on retries
}

// ExtractResult is validated, coerced output.
type ExtractResult struct {
	Record   map[string]any
	Raw      json.RawMessage
	Usage    TokenUsage
	Provider string
	Model    string
	Attempts int
}

// Extractor runs the 3-attempt repair loop.
type Extractor interface {
	Extract(ctx context.Context, in ExtractInput) (ExtractResult, error)
	Name() string
}

// providerCall is one raw LLM round-trip returning output text + token counts.
type providerCall func(ctx context.Context, system, user string) (text string, prompt, completion int, err error)

// runRepairLoop implements spec §3.3: max 3 attempts; repair prompt embeds
// validator err.Error() verbatim.
func runRepairLoop(ctx context.Context, call providerCall, log func(purpose string, usage TokenUsage), in ExtractInput, provider, model string) (ExtractResult, error) {
	system := "You emit ONLY JSON matching the provided JSON schema. No prose, no code fences."
	user := buildPrompt(in)
	var lastErr error
	var total TokenUsage
	for attempt := 0; attempt < 3; attempt++ {
		text, pt, ct, err := call(ctx, system, user)
		if err != nil {
			// Transport errors fail loudly (no validator text to repair with).
			if isTruncation(err) {
				return ExtractResult{}, fmt.Errorf("extract: %w", err)
			}
			return ExtractResult{}, fmt.Errorf("extract: provider: %w", err)
		}
		usage := TokenUsage{PromptTokens: pt, CompletionTokens: ct, USDEstimate: EstimateCost(model, pt, ct)}
		total.PromptTokens += pt
		total.CompletionTokens += ct
		total.USDEstimate += usage.USDEstimate
		purpose := "extract"
		if attempt > 0 {
			purpose = "repair"
		}
		if log != nil {
			log(purpose, usage)
		}
		if verr := in.Schema.Validate([]byte(text)); verr != nil {
			lastErr = verr
			user = buildPrompt(in) + "\n\nYour previous output was invalid:\n" + text + "\n\nValidation errors:\n" + verr.Error() + "\nFix the specific fields above and emit ONLY the corrected JSON."
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(text), &rec); err != nil {
			return ExtractResult{}, fmt.Errorf("extract: parse valid output: %w", err)
		}
		// jsonld_path pre-fill: pull missing fields from the sidecar first.
		for field, path := range in.Schema.Hints.JSONLDPath {
			if _, ok := rec[field]; !ok || rec[field] == nil {
				if v, found := JSONLDWalk(in.StructuredData, path); found {
					rec[field] = v
				}
			}
		}
		rec, err = ApplyCoercions(rec, in.Schema.Hints)
		if err != nil {
			return ExtractResult{}, err
		}
		// Re-validate after coercion? Coercion fixes types, so marshal and check.
		if coerced, merr := json.Marshal(rec); merr == nil {
			if verr := in.Schema.Validate(coerced); verr != nil {
				lastErr = verr
				user = buildPrompt(in) + "\n\nCoerced output still invalid:\n" + string(coerced) + "\n\nValidation errors:\n" + verr.Error()
				continue
			}
			text = string(coerced)
		}
		return ExtractResult{Record: rec, Raw: json.RawMessage(text), Usage: total, Provider: provider, Model: model, Attempts: attempt + 1}, nil
	}
	return ExtractResult{}, fmt.Errorf("extract: still invalid after 3 attempts: %w", lastErr)
}

func buildPrompt(in ExtractInput) string {
	var sb bytes.Buffer
	sb.WriteString("Extract structured data from the following page content.\n\n")
	if len(in.StructuredData) > 0 {
		sb.WriteString("Structured data (always complete, prefer these facts):\n")
		sb.Write(in.StructuredData)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Page markdown:\n")
	sb.WriteString(in.Markdown)
	if in.PromptExtra != "" {
		sb.WriteString("\n\n" + in.PromptExtra)
	}
	return sb.String()
}

type truncError struct{ msg string }

func (e *truncError) Error() string { return e.msg }

func isTruncation(err error) bool {
	_, ok := err.(*truncError)
	return ok
}

func postJSON(ctx context.Context, url, apiKey string, headers map[string]string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	_ = apiKey
	client := &http.Client{Timeout: 120 * time.Second}
	// Test hook: allow logging via env only; no test hooks in production paths beyond env config.
	if os.Getenv("GOMAGPIE_HTTP_DEBUG") != "" {
		fmt.Fprintln(os.Stderr, "POST", url)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(out, 500))
	}
	return out, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
