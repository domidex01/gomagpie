package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gomagpie/config"
)

var (
	cfgFile string
	cacheDB string
	maxCost float64
	apiKey  string
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(exitFor(err))
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "magpie",
		Short: "Fetch → clean → extract structured data from the web",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
	root.PersistentFlags().StringVar(&cacheDB, "cache-db", "", "SQLite cache DB path")
	root.PersistentFlags().Float64Var(&maxCost, "max-cost", 0, "USD cost ceiling (abort before exceeding)")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "provider API key (overrides env/keyring)")
	root.AddCommand(newScrapeCmd(), newExtractCmd(), newConfigCmd())
	return root
}

// resolveConfig loads file+env then overlays global flags.
func resolveConfig() (config.Config, error) {
	path := cfgFile
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	f := config.Flags{}
	if cacheDB != "" {
		f.CacheDB, f.CacheDBChanged = cacheDB, true
	}
	if maxCost != 0 {
		f.MaxCost, f.MaxCostChanged = maxCost, true
	}
	if apiKey != "" {
		f.APIKey, f.APIKeyChanged = apiKey, true
	}
	cfg.ApplyFlags(f)
	return cfg, nil
}

func exitFor(err error) int {
	if ce, ok := err.(*cmdError); ok {
		fmt.Fprintln(os.Stderr, ce.msg)
		return ce.code
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

type cmdError struct {
	code int
	msg  string
}

func (e *cmdError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return &cmdError{code: code, msg: fmt.Sprintf(format, args...)}
}
