package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"gomagpie/clean"
	"gomagpie/extract"
	"gomagpie/store"

	"github.com/spf13/cobra"
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
	cmd.Flags().StringVar(&provider, "provider", "", ProviderHelp)
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
	if key == "" && needsAPIKey(provider) {
		return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
	}

	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // end of command; close error unactionable
	runID := uuidNew()
	if err := db.BeginRun(runID, "extract"); err != nil {
		return err
	}

	ex, err := newExtractor(provider, key, model, sch, db, runID)
	if err != nil {
		return err
	}

	if err := checkCostCeiling(db, runID, provider, model, cleaned.Markdown, cfg.MaxCost); err != nil {
		if ferr := db.FinishRun(runID, 0, 0, "error"); ferr != nil {
			fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", ferr)
		}
		return err
	}

	res, err := ex.Extract(ctx, extract.ExtractInput{
		Markdown: cleaned.Markdown, StructuredData: cleaned.StructuredData, Schema: sch,
	})
	if err != nil {
		if ferr := db.FinishRun(runID, 0, 1, "error"); ferr != nil {
			fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", ferr)
		}
		return err
	}
	if ferr := db.FinishRun(runID, 1, 0, "finished"); ferr != nil {
		fmt.Fprintf(os.Stderr, "warning: finish run: %v\n", ferr)
	}
	doc, merr := marshalOut(map[string]any{"extracted": res.Record}, "extract")
	if merr != nil {
		return merr
	}
	return writeOut(o.Out, doc)
}
