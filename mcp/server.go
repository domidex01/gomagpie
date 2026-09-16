// Package mcp exposes the Phase 1–2 pipeline as an MCP server:
// `magpie serve` registers scrape_url, crawl_site, extract_structured and
// get_cached_selectors. Handlers share scrape.Run / crawl.Run with the CLI;
// the transport (stdio vs Streamable HTTP) is chosen in cli/serve.go.
package mcp

import (
	"gomagpie/scrape"
	"gomagpie/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps wires the server to storage and provider constructors.
// ScrapeDeps carries the DB + ExtractorFor/APIKeyFor func fields so mcp
// never imports the CLI package.
type Deps struct {
	DB              *store.DB
	ScrapeDeps      scrape.Deps
	DefaultProvider string
	DefaultModel    string
	MaxCost         float64
}

// NewServer registers the four tools on a fresh server.
func NewServer(d Deps) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "gomagpie", Version: "v1.0.0"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "scrape_url", Description: "Fetch, clean and extract one URL"}, handleScrape(d))
	sdk.AddTool(server, &sdk.Tool{Name: "crawl_site", Description: "Crawl a site (synchronous) or poll a previous run by run_id"}, handleCrawl(d))
	sdk.AddTool(server, &sdk.Tool{Name: "extract_structured", Description: "Extract structured data from HTML or markdown (no fetch)"}, handleExtract(d))
	sdk.AddTool(server, &sdk.Tool{Name: "get_cached_selectors", Description: "List cached selectors for a domain"}, handleSelectors(d))
	return server
}
