package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"magpie/config"
	"magpie/extract"
	magpiemcp "magpie/mcp"
	"magpie/scrape"
	"magpie/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var transport, addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the pipeline over MCP (stdio or Streamable HTTP)",
		Long: `Serve magpie tools over MCP.

Tools: scrape_url, crawl_site, extract_structured, get_cached_selectors,
batch, map, summarize, diff, brand, list_extractors, vertical_scrape.

crawl_site runs synchronously to completion (no background jobs): keep
MaxPages bounded or use HTTP mode, and set generous client timeouts for
large crawls.

HTTP mode is stateless (no sticky sessions needed).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context(), serveOptions{Transport: transport, Addr: addr})
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "", "stdio|http (default stdio)")
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen addr (default :8080)")
	return cmd
}

type serveOptions struct {
	Transport string
	Addr      string
}

func runServe(ctx context.Context, o serveOptions) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	f := config.Flags{}
	if o.Transport != "" {
		f.ServeTransport, f.ServeTransportChanged = o.Transport, true
	}
	if o.Addr != "" {
		f.ServeAddr, f.ServeAddrChanged = o.Addr, true
	}
	cfg.ApplyFlags(f)

	switch cfg.ServeTransport {
	case "stdio", "http", "":
	default:
		return fail(2, "serve: --transport must be stdio|http")
	}
	transport := cfg.ServeTransport
	if transport == "" {
		transport = "stdio"
	}
	addr := cfg.ServeAddr
	if addr == "" {
		addr = ":8080"
	}

	db, err := store.Open(cfg.CacheDB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // server lifetime; close unactionable

	provider, model := cfg.ExtractProvider, cfg.Model
	srv := magpiemcp.NewServer(magpiemcp.Deps{
		DB: db,
		ScrapeDeps: scrape.Deps{
			DB: db,
			ExtractorFor: func(p, key, m string, sch *extract.Schema, runID string) (extract.Extractor, error) {
				return newExtractor(p, key, m, sch, db, runID)
			},
			APIKeyFor: cfg.APIKey,
		},
		DefaultProvider: provider, DefaultModel: model, MaxCost: cfg.MaxCost,
	})

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	if transport == "http" {
		// Stateless handler (confirmed StreamableHTTPOptions.Stateless on the
		// pinned SDK): no sticky sessions, plain ListenAndServe.
		handler := sdk.NewStreamableHTTPHandler(func(_ *http.Request) *sdk.Server {
			return srv
		}, &sdk.StreamableHTTPOptions{Stateless: true})
		fmt.Fprintf(os.Stderr, "magpie: serving MCP over http on %s\n", addr)
		if err := http.ListenAndServe(addr, handler); err != nil { //nolint:gosec // addr is operator-configured
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}
	if err := srv.Run(ctx, &sdk.StdioTransport{}); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
