package extract_test

// Prompt-mode tests: schema-less PromptText on the 3 adapter types.
//
// DEVIATION from plan/phase-C-tests.md §3 (load-bearing paragraph):
// PromptText never calls Log — it has no Purpose to log, and the purpose
// lives with the caller (Summarize's ExtractorFor closure cannot receive
// one). If adapters logged a fixed purpose here, Summarize would either
// double-log (adapter "extract" + its own "summarize" row, corrupting
// RunCost accounting) or misattribute. So attribution is proven one level
// up instead, end-to-end: scrape.TestSummarize_LogsPurpose asserts the
// llm_calls row carries Purpose "summarize", and cli.TestExtract_PromptLogs
// asserts "extract". What this file proves: verbatim text, returned usage,
// and schema-key ABSENCE from the wire body (the validator-bypass proof).

import (
	"context"
	"strings"
	"testing"

	"gomagpie/extract"
)

func schemaKeysAbsent(t *testing.T, body map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := body[k]; ok {
			t.Errorf("prompt-mode request body contains schema key %q: %v", k, body)
		}
	}
}

func TestOpenAIPromptText(t *testing.T) {
	srv, fp := newFakeProvider(t, openAIEnvelope("plain summary"))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	a := extract.NewOpenAI("", "k", "gpt-4o-mini", nil)
	text, usage, err := a.PromptText(context.Background(), "sys", "summarize this")
	if err != nil {
		t.Fatalf("PromptText: %v", err)
	}
	if text != "plain summary" {
		t.Errorf("text = %q, want verbatim script", text)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v, want 10/5", usage)
	}
	body := fp.lastBody(t)
	schemaKeysAbsent(t, body, "response_format", "json_schema", "output_config", "require_parameters")
	if fp.callCount() != 1 {
		t.Errorf("provider calls = %d, want 1", fp.callCount())
	}
}

func TestAnthropicPromptText(t *testing.T) {
	srv, fp := newFakeProvider(t, anthropicEnvelope("plain summary"))
	t.Setenv("GOMAGPIE_BASE_URL", srv.URL)
	a := extract.NewAnthropic("", "k", "claude-sonnet-5", nil)
	text, usage, err := a.PromptText(context.Background(), "sys", "summarize this")
	if err != nil {
		t.Fatalf("PromptText: %v", err)
	}
	if text != "plain summary" {
		t.Errorf("text = %q, want verbatim script", text)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v, want 10/5", usage)
	}
	body := fp.lastBody(t)
	schemaKeysAbsent(t, body, "response_format", "json_schema", "output_config")
	if fp.callCount() != 1 {
		t.Errorf("provider calls = %d, want 1", fp.callCount())
	}
}

func TestCodexPromptText_Schemaless(t *testing.T) {
	logPath := stubCodex(t, "ok")
	a := extract.NewCodexExec("gpt-5", nil, nil)
	text, _, err := a.PromptText(context.Background(), "sys", "say ok")
	if err != nil {
		t.Fatalf("PromptText: %v", err)
	}
	// The stub writes a JSON record to -o; prompt mode must return it
	// verbatim (unvalidated) like any other provider text.
	if !strings.Contains(text, "Widget") {
		t.Errorf("text = %q, want stub output verbatim", text)
	}
	calls := stubCalls(t, logPath)
	if strings.Contains(calls, "--output-schema") {
		t.Errorf("schemaless codex call passed --output-schema:\n%s", calls)
	}
	if !strings.Contains(calls, "-o") {
		t.Errorf("codex call missing -o capture:\n%s", calls)
	}
}
