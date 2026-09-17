package cli

import (
	"fmt"

	"gomagpie/vertical"

	"github.com/spf13/cobra"
)

func newVerticalCmd() *cobra.Command {
	var list bool
	var name string
	cmd := &cobra.Command{
		Use:   "vertical [--list] [<url> --name]",
		Short: "Zero-LLM typed extraction (list extractors or scrape one URL)",
		Long: `magpie vertical --list prints all registered extractors.
magpie vertical <url> --name <extractor> extracts with zero LLM calls.
Omit --name for strict auto-dispatch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if list {
				return runVerticalList()
			}
			if len(args) != 1 {
				return fail(2, "vertical: one URL is required (or --list)")
			}
			name := name
			if name == "" {
				name = "auto"
			}
			// Explicit/auto vertical rides the shared scrape flow: unknown
			// names fail pre-I/O, mismatches exit 2, hits print the record.
			return runScrape(cmd.Context(), args[0], scrapeOptions{Vertical: name})
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "list all registered extractors")
	cmd.Flags().StringVar(&name, "name", "", "extractor name (default auto)")
	return cmd
}

func runVerticalList() error {
	doc, err := marshalOut(map[string]any{"extractors": vertical.List()}, "vertical")
	if err != nil {
		return err
	}
	fmt.Println(doc)
	return nil
}
