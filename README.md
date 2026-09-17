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
| `magpie scrape <url> [--schema f] [--render auto\|static\|browser] [--browser chrome\|firefox\|random] [--provider …] [--model …] [--format json\|jsonl] [--max-cost usd] [--out f]` | Fetch → clean → extract one URL |
| `magpie extract [--schema f \| --prompt t] [--content-type html\|markdown]` | Extract from stdin/file, no fetch (prompt = plain text, no schema) |
| `magpie batch [urls...] [--file f] [--concurrency 8] [--format jsonl\|json] [--browser …]` | Scrape ≤100 URLs, one ok/error record each (markdown only) |
| `magpie map <site> [--format lines\|json]` | List sitemap-derived page URLs (BFS to depth 5, gzip + entity aware; partial results on dead children or a 25s budget, flagged `truncated`) |
| `magpie summarize <url> [--max-sentences 3] [--provider …]` | Summarize in ≤N sentences |
| `magpie diff <url> --against <file>` | Word-level diff vs a markdown snapshot |
| `magpie brand <url>` | Brand colors, fonts, logo, favicon (zero LLM) |
| `magpie vertical [--list] [<url> --name]` | Zero-LLM typed extraction |
| `magpie crawl <url> --schema f [--path-prefix /docs] [--include '**/docs/**'] [--exclude '**/api/**'] [--allow-subdomains] [--no-sitemap] [--browser …] [--exporter-cmd prog]` | BFS crawl + extract, scoped to matching links only; binary assets (pdf/images/video/fonts/archives) never enqueue; sitemap expansion is scope-filtered and best-effort; tee records as JSONL to prog's stdin |
| `magpie serve [--transport stdio\|http] [--addr :8080]` | Serve the pipeline over MCP |
| `magpie build --with module@version --output f` | Compile a custom static binary with extra modules |
| `magpie config set-key <provider> \| show` | Store key in OS keyring / show redacted config |

Exit codes: 0 ok · 1 runtime · 2 usage (incl. non-public/SSRF-rejected URLs) · 3 all-failed · 4 partial · 5 robots-blocked · 6 cost ceiling · 7 credentials · 8 quality-blocked.

## Network & security

Every fetch (static, robots, crawl) goes through one guarded transport:

- **SSRF guard (default-deny):** only public `http(s)` hosts; loopback,
  private, link-local (cloud metadata), and multicast addresses are
  rejected before dialing — including on every redirect hop and again on
  the connected peer IP (DNS-rebind safe). Rejections wrap a typed
  sentinel and exit 2. `file://` URLs are gated behind
  `GOMAGPIE_ALLOW_FILE=1`.
- **`GOMAGPIE_PROXY=http(s)://host:port`** routes all traffic through one
  proxy (wins over the standard `HTTP_PROXY`/`HTTPS_PROXY` env, which is
  honored otherwise); `NO_PROXY` entries (exact or `.suffix` host match,
  `*` = all) bypass it. Invalid values fail loudly before any request.
- **Body cap:** 50 MB on the decoded stream, so gzip bombs are truncated,
  not downloaded.
- **Run telemetry:** `run_history` rows accumulate `fetch_pages`,
  `fetch_bytes`, and `fetch_ms` next to LLM tokens/cost; databases created
  before Phase D gain the columns automatically on open.

## TLS impersonation & PDF

**`--browser chrome|firefox|random`** (scrape, batch, crawl, MCP) swaps the
stock TLS stack for a byte-exact browser fingerprint: Chrome/Firefox TLS
ClientHello (JA3/JA4) *plus* matching HTTP/2 framing (SETTINGS,
WINDOW_UPDATE, pseudo-header order) and header set. Sites that block the
stock Go handshake pass. Notes:

- `random` picks chrome or firefox once per process.
- Headers come from the fingerprint profile, not `--header-profile` —
  a stale UA next to a fresh hello is itself a fingerprint tell. Set
  `--header-profile` explicitly to override; cleartext `http://` targets
  always use our header profiles (the impersonation transport only speaks
  TLS).
- Every security property holds: pre-dial SSRF check, redirect
  re-validation, peer-IP check, proxy policy (`GOMAGPIE_PROXY` tunnels
  browser connections via CONNECT; `NO_PROXY` bypasses), 50 MB cap.
- Credits: [`North-web-dev/impersonate-http`](https://github.com/North-web-dev/impersonate-http)
  (MIT, wrapping `refraction-networking/utls`), verified against
  tls.peet.ws; imported only inside `fetch/`.

**PDF extraction:** `magpie scrape` on an `application/pdf` URL (or `%PDF-`
magic) returns one `## Page N` markdown section per page, titled from the
URL path stem, flowing through the same quality gate as HTML — a text-less
scan PDF is a typed `empty` quality error (exit 8), an encrypted PDF is a
loud error (never empty markdown). Crawl does not follow `.pdf` links.
Credits: [`ledongthuc/pdf`](https://github.com/ledongthuc/pdf) (BSD-3,
stdlib-only), imported only inside `clean/`.

## Checks

```bash
go test ./...                                  # hermetic: no network, browser, or keys
go test -tags browser ./fetch/ -run 'TestRodSmoke|TestUTLS'  # needs Chrome + network; skips otherwise
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
