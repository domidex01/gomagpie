package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gomagpie/clean"
	"gomagpie/extract"
	"gomagpie/store"
)

func newExtractCmd() *cobra.Command {
	var schema, contentType, provider, model, out string
	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract structured data from stdin/file (no fetch)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExtract(cmd.Context(), extractOptions{
				Schema: schema, ContentType: contentType, Provider: provider,
				Model: model, Out: out, File: firstArg(args),
			})
		},
	}
	cmd.Flags().StringVar(&schema, "schema", "", "JSON Schema file (yaml/json)")
	cmd.Flags().StringVar(&contentType, "content-type", "html", "html|markdown")
	cmd.Flags().StringVar(&provider, "provider", "", "anthropic|openai|ollama")
	cmd.Flags().StringVar(&model, "model", "", "model name")
	cmd.Flags().StringVar(&out, "out", "", "output path (default stdout)")
	return cmd
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

type extractOptions struct {
	Schema      string
	ContentType string
	Provider    string
	Model       string
	Out         string
	File        string
}

func runExtract(ctx context.Context, o extractOptions) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	if o.Schema != "" {
		cfg.Schema = o.Schema
	}
	if o.Provider != "" {
		cfg.ExtractProvider = o.Provider
	}
	if o.Model != "" {
		cfg.Model = o.Model
	}
	if cfg.Schema == "" {
		return fail(2, "extract: --schema is required")
	}
	if o.ContentType != "html" && o.ContentType != "markdown" {
		return fail(2, "extract: --content-type must be html|markdown")
	}

	var input []byte
	if o.File != "" {
		input, err = os.ReadFile(o.File)
		if err != nil {
			return fmt.Errorf("extract: read %s: %w", o.File, err)
		}
	} else {
		input, err = io.ReadAll(io.LimitReader(os.Stdin, 50<<20))
		if err != nil {
			return fmt.Errorf("extract: read stdin: %w", err)
		}
	}

	var cleaned clean.CleanedPage
	if o.ContentType == "html" {
		cleaned, err = clean.Clean(ctx, clean.RawPage{HTML: input})
		if err != nil {
			return err
		}
	} else {
		cleaned = clean.CleanedPage{Markdown: string(input)}
	}

	sch, err := extract.LoadSchema(cfg.Schema)
	if err != nil {
		return err
	}
	provider := cfg.ExtractProvider
	model := cfg.Model
	if model == "" {
		model = "claude-sonnet-5"
	}
	key := cfg.APIKey(provider)
	if key == "" && !isFreeProvider(provider) {
		return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
	}

	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer db.Close()
	runID := uuidNew()
	if err := db.BeginRun(runID, "extract"); err != nil {
		return err
	}

	var ex extract.Extractor
	switch strings.ToLower(provider) {
	case "openai", "ollama":
		a := extract.NewOpenAI("", key, model, sch)
		a.Log = func(purpose string, u extract.TokenUsage) {
			_ = db.LogLLMCall(runID, store.LLMCall{Provider: provider, Model: model, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, USDEstimate: u.USDEstimate, Purpose: purpose})
		}
		ex = a
	default:
		a := extract.NewAnthropic("", key, model, sch)
		a.Log = func(purpose string, u extract.TokenUsage) {
			_ = db.LogLLMCall(runID, store.LLMCall{Provider: provider, Model: model, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, USDEstimate: u.USDEstimate, Purpose: purpose})
		}
		ex = a
	}

	if cfg.MaxCost > 0 {
		running, _ := db.RunCost(runID)
		proj := extract.ProjectedCost(model, cleaned.Markdown)
		if proj == 0 {
			if toks := extract.EstimatePromptTokens(cleaned.Markdown); toks > 0 {
				proj = float64(toks) / 1e6 * 2.00
			}
		}
		if running+proj > cfg.MaxCost {
			_ = db.FinishRun(runID, 0, 0, "error")
			return fail(6, "cost ceiling exceeded: running %.6f + projected %.6f > max %.6f", running, proj, cfg.MaxCost)
		}
	}

	res, err := ex.Extract(ctx, extract.ExtractInput{
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData, Schema: sch,
	})
	if err != nil {
		_ = db.FinishRun(runID, 0, 1, "error")
		return err
	}
	_ = db.FinishRun(runID, 1, 0, "finished")
	doc := map[string]any{"extracted": res.Record}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return writeOut(o.Out, string(b))
}
