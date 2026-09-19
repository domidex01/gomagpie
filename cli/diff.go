package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/motherlodelab/magpie/extract"
	"github.com/motherlodelab/magpie/scrape"
	"github.com/motherlodelab/magpie/store"

	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	var against string
	cmd := &cobra.Command{
		Use:   "diff <url> --against <file>",
		Short: "Word-level diff of a URL against a previous markdown snapshot",
		Long: `Fetch a URL and diff its current markdown against a snapshot file
(--against <file>, - = stdin). Prints -old +new word pairs; identical
input prints nothing and exits 0. Zero LLM.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd.Context(), args[0], diffOptions{Against: against})
		},
	}
	cmd.Flags().StringVar(&against, "against", "", "snapshot file to diff against (- = stdin)")
	return cmd
}

type diffOptions struct {
	Against string
}

func runDiff(ctx context.Context, rawURL string, o diffOptions) error {
	if o.Against == "" {
		return fail(2, "diff: --against is required")
	}
	var snap []byte
	var err error
	if o.Against == "-" {
		snap, err = io.ReadAll(io.LimitReader(os.Stdin, 50<<20))
		if err != nil {
			return fmt.Errorf("diff: read stdin: %w", err)
		}
	} else {
		snap, err = os.ReadFile(o.Against)
		if err != nil {
			return fmt.Errorf("diff: read %s: %w", o.Against, err)
		}
	}
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // end of command; close error unactionable

	res, err := scrape.Run(ctx, scrape.Deps{
		DB: db,
		ExtractorFor: func(p, key, m string, s *extract.Schema, runID string) (extract.Extractor, error) {
			return newExtractor(p, key, m, s, db, runID)
		},
		APIKeyFor: cfg.APIKey,
	}, rawURL, scrape.Options{})
	if err != nil {
		return keyHint(err)
	}
	diff, err := scrape.DiffWords(string(snap), res.Markdown)
	if err != nil {
		return err
	}
	if diff != "" {
		fmt.Print(diff)
	}
	return nil
}
