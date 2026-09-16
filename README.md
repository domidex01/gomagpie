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
| `magpie extract [--schema f] [--content-type html\|markdown]` | Extract from stdin/file, no fetch |
| `magpie config set-key <provider> \| show` | Store key in OS keyring / show redacted config |

Exit codes: 0 ok · 1 runtime · 2 usage · 3 all-failed · 6 cost ceiling · 7 credentials.

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
