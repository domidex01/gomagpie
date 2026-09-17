package extract

import "context"

// Prompter is the schema-less text path: one LLM call returning raw text,
// never validated, never coerced. Summarize and `extract --prompt` go
// through here; structured extraction keeps using Extract.
//
// Cost attribution is caller-owned: PromptText never calls Log (it has no
// Purpose to log — the purpose lives with the caller). Summarize logs
// Purpose "summarize" and the extract command logs "extract" after a
// successful call, so llm_calls carries exactly one row per prompt call.
type Prompter interface {
	PromptText(ctx context.Context, system, user string) (string, TokenUsage, error)
}

var (
	_ Prompter = (*OpenAIAdapter)(nil)
	_ Prompter = (*AnthropicAdapter)(nil)
	_ Prompter = (*CodexExecAdapter)(nil)
)
