package main

import (
	"encoding/json"
	"fmt"
	"os"

	"gomagpie/config"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Manage configuration and API keys"}
	setKey := &cobra.Command{
		Use:   "set-key <provider>",
		Short: "Prompt and store provider key in OS keyring",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stderr, "Enter API key for %s: ", args[0])
			var key string
			// ponytail: plain Scanln, ceiling = key echoes on screen.
			if _, err := fmt.Scanln(&key); err != nil {
				return fmt.Errorf("config set-key: read key: %w", err)
			}
			if key == "" {
				return fail(2, "config set-key: empty key")
			}
			if err := config.SetKey(args[0], key); err != nil {
				return fail(1, "%v", err)
			}
			fmt.Println("stored")
			return nil
		},
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print resolved config (keys redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(cfg.Redacted(), "", "  ")
			if err != nil {
				return fmt.Errorf("config show: %w", err)
			}
			fmt.Println(string(b))
			return nil
		},
	}
	cmd.AddCommand(setKey, show)
	return cmd
}
