package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"gomagpie/crawl"
	"gomagpie/extract"
	pluginExec "gomagpie/plugin/exec"
	"gomagpie/store"

	"github.com/spf13/cobra"
)

func newCrawlCmd() *cobra.Command {
	var schema, format, out, resume string
	var maxPages, maxDepth, concurrency int
	var sameHost bool
	var rate float64
	var ignoreRobots bool
	var provider, model, exporterCmd string
	cmd := &cobra.Command{
		Use:   "crawl <url>",
		Short: "BFS crawl + extract a site",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCrawl(cmd.Context(), args[0], crawlCLIOptions{
				Schema: schema, Format: format, Out: out, Resume: resume,
				MaxPages: maxPages, MaxDepth: maxDepth, Concurrency: concurrency,
				SameHost: sameHost, Rate: rate, IgnoreRobots: ignoreRobots,
				Provider: provider, Model: model, ExporterCmd: exporterCmd,
			})
		},
	}
	cmd.Flags().StringVar(&schema, "schema", "", "JSON Schema file (yaml/json, required)")
	cmd.Flags().StringVar(&format, "format", "jsonl", "jsonl|json|csv|sqlite")
	cmd.Flags().StringVar(&out, "out", "", "output path (default stdout)")
	cmd.Flags().StringVar(&resume, "resume", "", "resume a checkpointed run_id")
	cmd.Flags().IntVar(&maxPages, "max-pages", 100, "max pages to claim/fetch (counts pages that fed synthesis too)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 3, "max link depth from seed")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "fetch workers")
	cmd.Flags().BoolVar(&sameHost, "same-host", true, "follow only same-host links")
	cmd.Flags().Float64Var(&rate, "rate", 1, "per-host requests/sec")
	cmd.Flags().BoolVar(&ignoreRobots, "ignore-robots", false, "fetch despite robots.txt (prints a warning)")
	cmd.Flags().StringVar(&provider, "provider", "", ProviderHelp)
	cmd.Flags().StringVar(&model, "model", "", "model name")
	// ponytail: argv = strings.Fields (no quoting) — paths with spaces need a wrapper script.
	cmd.Flags().StringVar(&exporterCmd, "exporter-cmd", "", "pipe each record as JSONL to this program's stdin (in addition to --out)")
	return cmd
}

type crawlCLIOptions struct {
	Schema       string
	Format       string
	Out          string
	Resume       string
	MaxPages     int
	MaxDepth     int
	Concurrency  int
	SameHost     bool
	Rate         float64
	IgnoreRobots bool
	Provider     string
	Model        string
	ExporterCmd  string
}

func runCrawl(ctx context.Context, seedURL string, o crawlCLIOptions) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	if o.Schema != "" {
		cfg.Schema = o.Schema
	}
	if o.Format != "" {
		cfg.Format = o.Format
	}
	if o.Out != "" {
		cfg.Out = o.Out
	}
	if cfg.Schema == "" {
		return fail(2, "crawl: --schema is required")
	}
	switch cfg.Format {
	case "jsonl", "json", "csv", "sqlite":
	default:
		return fail(2, "crawl: --format must be jsonl|json|csv|sqlite")
	}
	provider := cfg.ExtractProvider
	if o.Provider != "" {
		provider = o.Provider
	}
	model := cfg.Model
	if o.Model != "" {
		model = o.Model
	}
	key := cfg.APIKey(provider)
	if key == "" && needsAPIKey(provider) {
		return fail(7, "missing API key for %s: set via --api-key flag, GOMAGPIE_* env, or `magpie config set-key`", provider)
	}
	sch, err := extract.LoadSchema(cfg.Schema)
	if err != nil {
		return err
	}
	if o.ExporterCmd != "" {
		cfg.ExporterCmd = o.ExporterCmd
	}
	if o.IgnoreRobots {
		fmt.Fprintln(os.Stderr, "WARNING: --ignore-robots set; fetching despite robots.txt")
	}

	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // end of command; close error unactionable

	runID := o.Resume
	resuming := runID != ""
	if !resuming {
		runID = uuidNew()
	}
	ex, err := newExtractor(provider, key, model, sch, db, runID)
	if err != nil {
		return err
	}
	propose := func(pctx context.Context, fields []string, trimmed string) (map[string]string, error) {
		props := map[string]any{}
		for _, f := range fields {
			props[f] = map[string]any{"type": "string"}
		}
		raw, merr := json.Marshal(map[string]any{
			"type": "object", "properties": props,
			"required": fields, "additionalProperties": false,
		})
		if merr != nil {
			return nil, merr
		}
		asch, merr := extract.ParseSchema(raw)
		if merr != nil {
			return nil, merr
		}
		pex, err := newExtractor(provider, key, model, asch, db, runID)
		if err != nil {
			return nil, err
		}
		if cerr := checkCostCeiling(db, runID, provider, model, trimmed, cfg.MaxCost); cerr != nil {
			return nil, cerr
		}
		res, xerr := pex.Extract(pctx, extract.ExtractInput{
			Markdown: "Propose one CSS selector per required field that extracts its value from the page below.\n\n" + trimmed,
			Schema:   asch, Purpose: "synth",
		})
		if xerr != nil {
			return nil, xerr
		}
		out := map[string]string{}
		for _, f := range fields {
			if v, ok := res.Record[f].(string); ok && v != "" {
				out[f] = v
			}
		}
		return out, nil
	}

	// Subprocess exporter tee: records flow to --out AND the child stdin.
	// The exporter is a copy — its failure warns (exit 4) but never blocks
	// the primary sink.
	var exportCh chan map[string]any
	var exportDone chan error
	var onRecord func(map[string]any)
	if cfg.ExporterCmd != "" {
		argv := strings.Fields(cfg.ExporterCmd)
		exp := &pluginExec.Exporter{Cmd: argv}
		exportCh = make(chan map[string]any, 1000)
		exportDone = make(chan error, 1)
		go func() { exportDone <- exp.Export(ctx, exportCh) }()
		onRecord = func(r map[string]any) { exportCh <- r }
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	res, err := crawl.Run(ctx, crawl.Options{
		SeedURL: seedURL, Schema: sch, MaxPages: o.MaxPages, MaxDepth: o.MaxDepth,
		SameHost: o.SameHost, FetchWorkers: o.Concurrency, Rate: o.Rate,
		Format: cfg.Format, Out: cfg.Out, RunID: runID,
		Resume: resuming, ResumeID: o.Resume, IgnoreRobots: o.IgnoreRobots,
		Provider: provider, Model: model, MaxCost: cfg.MaxCost,
		DB: db, Extractor: ex, Propose: propose, OnRecord: onRecord,
	})
	if exportCh != nil {
		close(exportCh)
		if exportErr := <-exportDone; exportErr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: exporter: %v\n", exportErr)
			if err == nil && res.Records > 0 && res.PagesErr == 0 {
				return fail(4, "crawl: exporter failed: %v", exportErr)
			}
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, crawl.ErrRobotsBlocked):
			return fail(5, "crawl: %v", err)
		case errors.Is(err, crawl.ErrCostCeiling):
			return fail(6, "crawl: %v", err)
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "crawl: run_id=%s pages_ok=%d pages_err=%d records=%d\n", res.RunID, res.PagesOK, res.PagesErr, res.Records)
	if res.Records == 0 {
		return fail(3, "crawl: no records extracted")
	}
	if res.PagesErr > 0 {
		return fail(4, "crawl: partial success (%d pages errored)", res.PagesErr)
	}
	return nil
}
