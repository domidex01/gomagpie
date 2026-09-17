package cli

import (
	"context"

	"gomagpie/extract"
	"gomagpie/scrape"
	"gomagpie/store"

	"github.com/spf13/cobra"
)

func newBrandCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "brand <url>",
		Short: "Extract brand colors, fonts, logo and favicon (zero LLM)",
		Long:  `Fetch a URL and print its brand surface as JSON. Blocked pages fail loudly, never empty.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrand(cmd.Context(), args[0], brandOptions{Out: out})
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output path (default stdout)")
	return cmd
}

type brandOptions struct {
	Out string
}

func runBrand(ctx context.Context, rawURL string, o brandOptions) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // end of command; close error unactionable

	res, err := scrape.BrandPage(ctx, scrape.Deps{
		DB: db,
		ExtractorFor: func(p, key, m string, s *extract.Schema, runID string) (extract.Extractor, error) {
			return newExtractor(p, key, m, s, db, runID)
		},
		APIKeyFor: cfg.APIKey,
	}, rawURL)
	if err != nil {
		return scrapeExit(err, rawURL, "")
	}
	doc, merr := marshalOut(map[string]any{
		"url": res.URL, "final_url": res.FinalURL, "title": res.Title,
		"colors": res.Colors, "fonts": res.Fonts, "logo": res.Logo, "favicon": res.Favicon,
	}, "brand")
	if merr != nil {
		return merr
	}
	return writeOut(o.Out, doc)
}
