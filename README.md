# gomagpie (`magpie`)

Go CLI web scraper: static fetch → JS detection → optional go-rod browser → trafilatura boilerplate removal → GFM markdown + JSON-LD sidecar → provider-agnostic LLM structured extraction. Pure-Go, zero CGO, single static binary.

## Quick start

```bash
go build -o magpie ./cmd/magpie

# Cleaned markdown only (no LLM call)
./magpie scrape https://example.com/article --render static

# Structured extraction
./magpie scrape https://example.com/product --schema testdata/extract/price.yaml \
  --provider openai --model gpt-4o-mini

# From stdin, no fetch
echo "$HTML" | ./magpie extract --schema testdata/extract/price.yaml --content-type html

# Config (keys redacted)
GOMAGPIE_EXTRACT_PROVIDER=ollama ./magpie config show
./magpie config set-key anthropic
```

API keys resolve as: `--api-key` flag > `GOMAGPIE_<PROVIDER>_API_KEY` env > OS keyring > config file.
(Dashes become underscores: `opencode-go` → `GOMAGPIE_OPENCODE_GO_API_KEY`.)

## Providers

`--provider` takes `anthropic|openai|ollama|openrouter|codex|opencode-go|opencode-zen`.

| Provider | Auth | Billing | Notes |
| :-- | :-- | :-- | :-- |
| `anthropic`, `openai` | API key | Metered, price table | Native structured output |
| `ollama` | none (local) | Free | Base-URL switch on the OpenAI adapter |
| `openrouter` | `GOMAGPIE_OPENROUTER_API_KEY` | Metered, costed from `usage.cost` | Sends `provider.require_parameters` + Referer/Title so the schema is enforced, not a hint |
| `codex` | none — uses your logged-in Codex CLI | Your subscription | Shells out to `codex exec` (`--output-schema --ephemeral --ignore-user-config`); needs a current CLI, no API key, exempt from `--max-cost` |
| `opencode-go` | `GOMAGPIE_OPENCODE_GO_API_KEY` | Flat plan, exempt from `--max-cost` | Flat-plan traffic is monitored for abuse — extraction is tiny, but if in doubt use Zen |
| `opencode-zen` | `GOMAGPIE_OPENCODE_ZEN_API_KEY` | Pay-as-you-go credits (metered) | Same endpoints under `/zen/v1`; model prefix picks `/chat/completions` vs `/messages`; calls carry `x-opencode-session` + magpie UA |

Claude models are reached via API key, OpenRouter, or Zen only — reusing a
Claude Pro/Max subscription token outside Claude Code is banned by Anthropic.
Zen `/responses`-only models are unsupported (different API shape).

## Commands

| Command | Action |
| :-- | :-- |
| `magpie scrape <url> [--schema f] [--render auto\|static\|browser] [--provider …] [--model …] [--format json\|jsonl] [--max-cost usd] [--out f]` | Fetch → clean → extract one URL |
| `magpie extract [--schema f \| --prompt t] [--content-type html\|markdown]` | Extract from stdin/file, no fetch (prompt = plain text, no schema) |
| `magpie batch [urls...] [--file f] [--concurrency 8] [--format jsonl\|json]` | Scrape ≤100 URLs, one ok/error record each (markdown only) |
| `magpie map <site> [--format lines\|json]` | List sitemap-derived URLs |
| `magpie summarize <url> [--max-sentences 3] [--provider …]` | Summarize in ≤N sentences |
| `magpie diff <url> --against <file>` | Word-level diff vs a markdown snapshot |
| `magpie brand <url>` | Brand colors, fonts, logo, favicon (zero LLM) |
| `magpie vertical [--list] [<url> --name]` | Zero-LLM typed extraction |
| `magpie crawl <url> --schema f [--exporter-cmd prog]` | BFS crawl + extract; tee records as JSONL to prog's stdin |
| `magpie serve [--transport stdio\|http] [--addr :8080]` | Serve the pipeline over MCP |
| `magpie build --with module@version --output f` | Compile a custom static binary with extra modules |
| `magpie config set-key <provider> \| show` | Store key in OS keyring / show redacted config |

Exit codes: 0 ok · 1 runtime · 2 usage · 3 all-failed · 6 cost ceiling · 7 credentials · 8 quality-blocked.

## Checks

```bash
go test ./...                                  # hermetic: no network, browser, or keys
go test -tags browser ./fetch/ -run TestRodSmoke  # needs Chrome; skips otherwise
go test ./clean/ -update                       # regenerate markdown goldens
golangci-lint run ./... && go vet ./... && test -z "$(gofmt -l .)"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...  # + linux/amd64, darwin/arm64
```

## Layout

`cmd/magpie/` CLI · `fetch/` static + detect + rod (rod lives only here) · `clean/` trafilatura→markdown · `extract/` schema + providers + repair loop + coerce + cost · `config/` file/env/flag/keyring · `store/` SQLite · `testdata/` goldens · `plan/` phase plans. Source of truth: `spec.md`; conventions: `AGENTS.md`.

## MCP server

`magpie serve` exposes the pipeline to agents over the Model Context
Protocol: stdio for local clients, stateless Streamable HTTP for remote
ones. `crawl_site` runs synchronously to completion (no background jobs),
returns a `run_id`, and re-invoking it with that `run_id` reports stored
status without touching the extractor.

Claude Desktop config (`{ "mcpServers": { "gomagpie": {
"command": "magpie", "args": ["serve"] } } }`):

| Tool | Action |
| :-- | :-- |
| `scrape_url` | Fetch → clean → extract one URL (schema optional) |
| `crawl_site` | Crawl a site, or poll a previous run via `run_id` (with progress) |
| `extract_structured` | Extract from HTML/markdown, no fetch (schema) or plain text (prompt) |
| `get_cached_selectors` | List cached selectors for a domain |
| `batch` | Scrape ≤100 URLs with bounded concurrency (markdown only, zero LLM) |
| `map` | List sitemap-derived URLs for a site |
| `summarize` | Summarize one URL in ≤N sentences |
| `diff` | Word-level diff of a URL vs a previous snapshot |
| `brand` | Brand colors, fonts, logo, favicon (zero LLM) |
| `list_extractors` | List zero-LLM vertical extractors |
| `vertical_scrape` | Extract one URL with a named vertical (zero LLM) |

HTTP mode: `magpie serve --transport http --addr 127.0.0.1:8089`.
Details: `magpie serve --help`.

## Custom builds

Compile-time modules register via `core.RegisterModule` in `init()` and
ship inside custom static binaries:

```bash
magpie build --with example.com/rodfetcher@v1.2.0 --output magpie-custom
```

`--with` must be `module@version` (bare paths are rejected — they would
silently resolve to `latest`). Remote modules need network; the build is
otherwise hermetic.

## WASM plugins

Untrusted `.wasm` transforms run in a wazero sandbox: exactly four host
functions (`gomagpie_log`, `gomagpie_get_input`, `gomagpie_set_output`,
`gomagpie_config_get`), WASI with zero preopened dirs (no filesystem,
sockets, or env), version-gated against `core.CoreAPIVersion`. Guests
export `gomagpie_api_version` and `run`. Full ABI contract:
`plugin/wasm/host.go`. TinyGo and Rust guests work (both import
`wasi_snapshot_preview1`, which is linked but capability-free).
