package cli

import (
	"context"
	"fmt"

	"gomagpie/crawl"

	"github.com/spf13/cobra"
)

func newMapCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "map <site>",
		Short: "List sitemap-derived URLs for a site",
		Long: `Print robots-declared sitemap URLs for a site origin, one per line
(default) or as JSON with --format json. Listings past the sitemap caps
are flagged truncated. Zero LLM.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMap(cmd.Context(), args[0], mapOptions{Format: format})
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "lines|json (default lines)")
	return cmd
}

type mapOptions struct {
	Format string
}

func runMap(ctx context.Context, site string, o mapOptions) error {
	format := o.Format
	if format == "" {
		format = "lines"
	}
	if format != "lines" && format != "json" {
		return fail(2, "map: --format %q must be lines|json", o.Format)
	}
	// Nil fetcher: ListSitemapURLs falls back to the static fetcher.
	urls, truncated, err := crawl.ListSitemapURLs(ctx, nil, site)
	if err != nil {
		return err
	}
	if format == "json" {
		if urls == nil {
			urls = []string{}
		}
		doc, merr := marshalOut(map[string]any{"site": site, "urls": urls, "truncated": truncated}, "map")
		if merr != nil {
			return merr
		}
		fmt.Println(doc)
		return nil
	}
	for _, u := range urls {
		fmt.Println(u)
	}
	return nil
}
