package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"gomagpie/clean"
	"gomagpie/scrape"

	"github.com/spf13/cobra"
)

func newBatchCmd() *cobra.Command {
	var file, format, render, profile, cookies, browser string
	var concurrency int
	var include, exclude []string
	var onlyMainContent bool
	cmd := &cobra.Command{
		Use:   "batch [urls...]",
		Short: "Scrape up to 100 URLs with bounded concurrency (markdown only)",
		Long: `Scrape many URLs in one call. One record per URL is printed; a bad
URL yields {"ok":false,"error":...} — never a whole-batch failure. Zero LLM.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBatch(cmd.Context(), args, batchOptions{
				File: file, Format: format, Concurrency: concurrency,
				Render: render, Profile: profile, Cookies: cookies, Browser: browser,
				Include: include, Exclude: exclude, OnlyMainContent: onlyMainContent,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "URL list file, one per line (- = stdin)")
	cmd.Flags().StringVar(&format, "format", "jsonl", "jsonl|json")
	cmd.Flags().IntVar(&concurrency, "concurrency", scrape.DefaultBatchConcurrency, "max parallel scrapes")
	cmd.Flags().StringVar(&render, "render", "", "auto|static|browser")
	cmd.Flags().StringVar(&profile, "profile", "", "request header bundle: default|chrome|firefox")
	cmd.Flags().StringVar(&cookies, "cookies", "", "raw Cookie header value")
	cmd.Flags().StringVar(&browser, "browser", "", "TLS-impersonating browser fingerprint: chrome|firefox|random")
	cmd.Flags().StringSliceVar(&include, "include", nil, "comma-separated CSS selectors: scrape only matching subtrees")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "comma-separated CSS selectors: drop matching nodes")
	cmd.Flags().BoolVar(&onlyMainContent, "only-main-content", false, "main-content only")
	return cmd
}

type batchOptions struct {
	File            string
	Format          string
	Concurrency     int
	Render          string
	Profile         string
	Browser         string
	Cookies         string
	Include         []string
	Exclude         []string
	OnlyMainContent bool
}

func runBatch(ctx context.Context, urls []string, o batchOptions) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	if o.File != "" {
		more, err := readURLFile(o.File)
		if err != nil {
			return err
		}
		urls = append(urls, more...)
	}
	// Input validation fails pre-I/O, before any fetch or DB row.
	if len(urls) == 0 {
		return fail(2, "batch: at least one URL is required (args or --file)")
	}
	if len(urls) > scrape.MaxBatchURLs {
		return fail(2, "batch: %d URLs exceeds max %d", len(urls), scrape.MaxBatchURLs)
	}
	if o.Concurrency <= 0 {
		return fail(2, "batch: --concurrency %d must be positive", o.Concurrency)
	}
	format := o.Format
	if format == "" {
		format = "jsonl"
	}
	if format != "jsonl" && format != "json" {
		return fail(2, "batch: --format %q must be jsonl|json", o.Format)
	}
	if err := checkBrowser("batch", o.Browser); err != nil {
		return err
	}

	db, err := openCmdDB(cfg)
	if err != nil {
		return err
	}
	defer closeDB(db)

	items, err := scrape.Batch(ctx, scrapeDeps(db, cfg), urls, scrape.BatchOptions{
		Concurrency: o.Concurrency, Render: o.Render, Profile: o.Profile,
		Browser: o.Browser, Cookies: o.Cookies,
		Scope: clean.Scope{Include: o.Include, Exclude: o.Exclude, OnlyMainContent: o.OnlyMainContent},
	})
	if err != nil {
		return err // validation only — item errors are data in items
	}
	if format == "json" {
		doc, merr := marshalOut(map[string]any{"results": items}, "batch")
		if merr != nil {
			return merr
		}
		fmt.Println(doc)
		return nil
	}
	for _, it := range items {
		raw, merr := json.Marshal(it)
		if merr != nil {
			return fmt.Errorf("batch: marshal record: %w", merr)
		}
		fmt.Println(string(raw))
	}
	return nil
}

// readURLFile reads one URL per line ("-" = stdin); blanks skipped.
func readURLFile(path string) ([]string, error) {
	var r io.Reader
	if path == "-" {
		r = io.LimitReader(os.Stdin, 1<<20)
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("batch: read %s: %w", path, err)
		}
		defer func() { _ = f.Close() }() //nolint:errcheck // read-only; close unactionable
		r = f
	}
	var urls []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			urls = append(urls, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("batch: read %s: %w", path, err)
	}
	return urls, nil
}
