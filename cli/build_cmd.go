package cli

import (
	"strings"

	"gomagpie/build"

	"github.com/spf13/cobra"
)

func newBuildCmd() *cobra.Command {
	var with []string
	var output string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Compile a custom static binary with extra modules",
		Long: `Compile a custom magpie binary with extra compile-time modules.

Each --with must be module@version (a bare path silently resolves to
latest, which is never what a reproducible build wants). Remote modules
need network access; the build itself is otherwise hermetic.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(output) == "" {
				return fail(2, "build: --output is required")
			}
			return build.Build(cmd.Context(), build.BuildOptions{With: with, Output: output})
		},
	}
	cmd.Flags().StringArrayVar(&with, "with", nil, "extra module@version (repeatable)")
	cmd.Flags().StringVar(&output, "output", "", "output binary path (required)")
	return cmd
}
