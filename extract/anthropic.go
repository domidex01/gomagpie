package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// AnthropicAdapter uses POST /v1/messages with GA output_config.format
// json_schema — no beta header, no tool wrapping.
type AnthropicAdapter struct {
	BaseURL string
	APIKey  string
	Model   string
	Log     func(purpose string, usage TokenUsage)
	schema  *Schema
}

func NewAnthropic(baseURL, apiKey, model string, sch *Schema) *AnthropicAdapter {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if v := os.Getenv("GOMAGPIE_BASE_URL"); v != "" {
		baseURL = v
	}
	return &AnthropicAdapter{BaseURL: baseURL, APIKey: apiKey, Model: model, schema: sch}
}

func (a *AnthropicAdapter) Name() string { return "anthropic" }

func (a *AnthropicAdapter) Extract(ctx context.Context, in ExtractInput) (ExtractResult, error) {
	if in.Schema == nil {
		in.Schema = a.schema
	}
	if in.Schema == nil {
		return ExtractResult{}, fmt.Errorf("extract: nil schema")
	}
	var schemaDoc any
	raw, _ := json.Marshal(in.Schema.Raw)
	_ = json.Unmarshal(raw, &schemaDoc)
	call := func(ctx context.Context, system, user string) (string, int, int, error) {
		body := map[string]any{
			"model":      a.Model,
			"max_tokens": 16000,
			"system":     system,
			"messages":   []any{map[string]any{"role": "user", "content": user}},
			"output_config": map[string]any{
				"format": map[string]any{"type": "json_schema", "schema": schemaDoc},
			},
		}
		headers := map[string]string{
			"x-api-key":         a.APIKey,
			"anthropic-version": "2023-06-01",
		}
		out, err := postJSON(ctx, a.BaseURL+"/v1/messages", a.APIKey, headers, body)
		if err != nil {
			return "", 0, 0, err
		}
		var env struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			return "", 0, 0, fmt.Errorf("decode messages: %w", err)
		}
		if env.StopReason == "max_tokens" {
			return "", env.Usage.InputTokens, env.Usage.OutputTokens,
				&truncError{"response truncated (stop_reason=max_tokens)"}
		}
		if env.StopReason == "refusal" {
			return "", env.Usage.InputTokens, env.Usage.OutputTokens,
				fmt.Errorf("model refused")
		}
		for _, c := range env.Content {
			if c.Type == "text" {
				return c.Text, env.Usage.InputTokens, env.Usage.OutputTokens, nil
			}
		}
		return "", env.Usage.InputTokens, env.Usage.OutputTokens, fmt.Errorf("no text content block")
	}
	return runRepairLoop(ctx, call, a.Log, in, "anthropic", a.Model)
}
