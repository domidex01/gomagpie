package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// OpenAIAdapter serves OpenAI and Ollama (base-URL switch) via the
// chat-completions response_format json_schema path.
type OpenAIAdapter struct {
	BaseURL string
	APIKey  string
	Model   string
	Log     func(purpose string, usage TokenUsage)
	schema  *Schema
}

func NewOpenAI(baseURL, apiKey, model string, sch *Schema) *OpenAIAdapter {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	// Test-only env override for base URL — no test hooks beyond env config.
	if v := os.Getenv("GOMAGPIE_BASE_URL"); v != "" {
		baseURL = v
	}
	return &OpenAIAdapter{BaseURL: baseURL, APIKey: apiKey, Model: model, schema: sch}
}

func (o *OpenAIAdapter) Name() string { return "openai" }

func (o *OpenAIAdapter) Extract(ctx context.Context, in ExtractInput) (ExtractResult, error) {
	if in.Schema == nil {
		in.Schema = o.schema
	}
	if in.Schema == nil {
		return ExtractResult{}, fmt.Errorf("extract: nil schema")
	}
	raw, err := json.Marshal(in.Schema.Raw)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("extract: marshal schema: %w", err)
	}
	var schemaDoc any
	if err := json.Unmarshal(raw, &schemaDoc); err != nil {
		return ExtractResult{}, fmt.Errorf("extract: decode schema: %w", err)
	}
	call := func(ctx context.Context, system, user string) (string, int, int, error) {
		body := map[string]any{
			"model": o.Model,
			"messages": []any{
				map[string]any{"role": "system", "content": system},
				map[string]any{"role": "user", "content": user},
			},
			"response_format": map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "gomagpie",
					"strict": true,
					"schema": schemaDoc,
				},
			},
		}
		headers := map[string]string{}
		if o.APIKey != "" {
			headers["Authorization"] = "Bearer " + o.APIKey
		}
		out, err := postJSON(ctx, o.BaseURL+"/chat/completions", o.APIKey, headers, body)
		if err != nil {
			return "", 0, 0, err
		}
		var env struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			return "", 0, 0, fmt.Errorf("decode chat-completions: %w", err)
		}
		if len(env.Choices) == 0 {
			return "", 0, 0, fmt.Errorf("empty choices")
		}
		if env.Choices[0].FinishReason == "length" {
			return "", env.Usage.PromptTokens, env.Usage.CompletionTokens,
				&truncError{"response truncated (finish_reason=length)"}
		}
		return env.Choices[0].Message.Content, env.Usage.PromptTokens, env.Usage.CompletionTokens, nil
	}
	return runRepairLoop(ctx, call, o.Log, in, "openai", o.Model)
}
