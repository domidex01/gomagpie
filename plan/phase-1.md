# Phase 1 — Single-URL fetch→clean→extract

**Duration:** Days 1–7 (~36 hours)
**Depends on:** Nothing (greenfield; `spec.md` is the sole input, `go.mod` is `module gomagpie`, Go 1.26.5)
**Blocks:** Phase 2 (selector cache + crawl), Phase 3 (MCP + plugins)
**Risk Level:** MEDIUM — multi-component pipeline with five brand-new pinned deps, but no binary research gate; every DoD item is buildable with confirmed APIs.
**Stack:** `go` — NOTE (per `AGENTS.md`): phase-plan/run-phase skills only accept `python|nextjs|react|typescript`, so `run-phase` will hard-block on this file. Execute this phase manually via the Execution Prompt at the end.

---

## 1. Objective + What Success Looks Like

Build spec Milestone 1: a `magpie` binary that fetches one URL (static first, go-rod browser only when the JS detector scores ≥ 2), strips boilerplate with go-trafilatura, serializes to Markdown with GFM tables, harvests the JSON-LD sidecar, extracts structured JSON via an LLM with schema validation + a 3-attempt repair loop, logs cost to SQLite, and reads API keys from flag/env/keyring. This is the pipeline every later phase reuses — Phase 2 caches its outputs, Phase 3 serves them over MCP.

1. [`magpie scrape https://example.com/article --schema testdata/extract/price.yaml` prints schema-valid JSON to stdout and exits 0]
2. [`magpie scrape <spa-shell-url>` escalates to the go-rod fetcher; `magpie scrape <static-url>` never launches a browser (assert via `CanHandle`/detector unit test + log line)]
3. [`go test ./...` exits 0 with >0 tests in `fetch`, `clean`, `extract` — no network, no browser in the default suite]
4. [`go test -update ./clean/` regenerates `testdata/clean/*.md` goldens and a subsequent `go test ./...` passes]
5. [`echo "$HTML" | magpie extract --schema price.yaml --content-type html` prints valid JSON without any fetch]
6. [`sqlite3 $(cache-db-path) "select count(*) from llm_calls;"` is ≥ 1 after a scrape with `--schema`, and `run_history` has one finished row]
7. [`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...`, `GOOS=linux GOARCH=amd64`, and `GOOS=darwin GOARCH=arm64` all succeed]
8. [`go vet ./...` exits 0 and `gofmt -l .` prints nothing]

---

## 2. Key Design Decisions

### Pipeline (this phase builds the left-to-right path; no channels/errgroup yet — that's Phase 2)

```
URL → fetch/http.go (static) → detect.go (score ≥ 2?) ──no──→ clean/ ──→ extract/ ──→ stdout/file
                                        └──yes──→ fetch/rod.go (browser) ──┘
                                              ▲
                          __NEXT_DATA__/__NUXT__ JSON present → skip browser, harvest inline
```

### Decisions (with spec section refs)

- **No provider SDK deps — stdlib `net/http` adapters instead (spec §3 override).** Spec §13 lists "official OpenAI Go, Anthropic Go" SDKs, but both APIs are plain JSON-over-HTTP and the OpenAI chat-completions shape also covers Ollama (`/v1` base-URL switch). One `openai.go` adapter with a configurable base URL serves OpenAI **and** Ollama; only `anthropic.go` (messages API + `output_config.format` JSON-schema mode — GA, no beta header, no tool wrapping) is bespoke. Saves two permanent deps per the repo's "no new dep without asking" rule. If a provider ships a breaking API change, the adapter is ~100 lines to fix.
- **Browser is lazy and interface-bound (spec §1.1, §6).** `fetch/fetcher.go` declares `Fetcher{Fetch, CanHandle}`; `rod.go` constructs the browser on first escalated fetch, never at startup. `go-rod` is imported **only** in `fetch/` (repo rule). Pin `go-rod/rod v0.116.x` exactly; do not follow the `rah-0/rod` fork unless upstream breaks.
- **Cleaning output feeds markdown, never raw HTML, to the LLM (spec §2.2, §3.4).** `clean.Clean` returns `{Markdown, StructuredData}`; JSON-LD sidecar is harvested **before** trafilatura (from `<script type="application/ld+json">`, `__NEXT_DATA__`, `window.__NUXT__`) and always sent in full. `ponytail:` page-over-8k-token windowing is naive top-down fill after sidecar+first-heading (no semantic ranking; ceiling = long list pages, which Phase 2 solves properly via one-record synthesis).
- **Validation errors drive repair verbatim (spec §3.3, §3.5).** santhosh-tekuri v6 `*ValidationError` already renders JSON-pointer paths (verified 2026-09-16: `- at '/price': got string, want number`; the `[I#/price]` form quoted in spec §3.3 is v5 output); the repair prompt embeds `err.Error()` directly — no custom error formatting.
- **Single SQLite writer from day one (spec §9.1).** `store` opens with `SetMaxOpenConns(1)` + pragmas in the DSN (`_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)` — `foreign_keys` is per-connection, so the DSN form re-applies it on every reconnect); include the **full §9.2 DDL now** (all five tables) so Phase 2 needs no migration.
- **`x-gomagpie.coerce` is a post-extraction string/number fixup, not schema logic (spec §3.7).** Implement `eur_decimal` (strip `€`/nbsp, `,`→`.`, parse float), `int`, `float`, `iso_date`, `bool`, `trim`. Unknown `coerce` value = loud error at schema load, never silent skip.
- **Cost ceiling checked before every LLM call (spec §3.6).** `--max-cost` compares running `run_history.usd_estimate` **plus the projected cost of the next call** (prompt bytes/4 × input price); exceed → exit code 6 before the call, not after. Without the projection a ceiling of `0.000001` would never abort the first call (running total is 0).

### Data Model Strategy (Go)

| Layer | Pattern | Why |
|---|---|---|
| Domain types (`FetchResponse`, `CleanedPage`, `ExtractInput/Result`, `Record`, `TokenUsage`) | Plain structs with `json`/`yaml` tags | Zero-cost, `encoding/json` native, no reflection framework |
| Stage boundaries (`Fetcher`, `Cleaner`, `Extractor`) | Small interfaces **declared in the consuming package** | Accept interfaces, return structs; Phase 2/3 swap impls without touching callers |
| Config (`Config` struct) | Plain struct + YAML unmarshal + `GOMAGPIE_` env overlay in code | No viper/cobra-env dep; precedence flags > env > file > defaults hand-rolled in ~40 lines |
| Hot path (none in Phase 1 — single URL, no loop) | No pooling, no `sync` beyond stdlib | Concurrency arrives in Phase 2; don't pre-build it |
| Errors | `fmt.Errorf("clean: %w", err)` + sentinel `Err*` for the retry taxonomy later | Fail loudly; never swallow (`_ =` on a meaningful error is a review failure) |

**Other critical rules:** no CGO ever (`CGO_ENABLED=0` is the test, not a hope); no live-network/browser in default `go test ./...` (browser tests get `//go:build browser` even now, while there's only one trivial rod smoke test); golden files regenerate with `go test ./clean/ -update` (flag defined in `clean_test.go`, default off).

---

## 3. Tasks

### Task 1.1 — Module setup + pinned deps + cross-compile gate (2h)

Goal: `go.mod` resolves exactly the pinned set and all three target platforms build an empty skeleton.

```bash
# Pinned adds (all resolved on proxy.golang.org 2026-09-16; go.mod says go 1.26.5, go-trafilatura's go.mod says go 1.26.0)
go get github.com/spf13/cobra@v1.10.2 \
  github.com/go-rod/rod@v0.116.2 \
  github.com/markusmobius/go-trafilatura/v2@v2.2.1 \
  github.com/JohannesKaufmann/html-to-markdown/v2@v2.5.2 \
  github.com/santhosh-tekuri/jsonschema/v6@v6.0.3 \
  modernc.org/sqlite@v1.59.0 \
  github.com/zalando/go-keyring@v0.2.8 \
  github.com/PuerkitoBio/goquery@v1.13.0 \
  gopkg.in/yaml.v3@v3.0.1 \
  golang.org/x/net@latest   # html parser (detect) + publicsuffix (cookie jar); promote from indirect
# modernc.org/libc v1.75.7 is selected by MVS from sqlite's own go.mod — never `go get -u` it independently (spec §9.1 caveat)
```

Implementation notes: create `cmd/magpie/main.go` (cobra root, no subcommands yet) + empty package stubs so `go build ./...` passes. Immediately verify the gate: `CGO_ENABLED=0` builds for windows/amd64, linux/amd64, darwin/arm64. Pre-verified 2026-09-16: a scratch module importing every package above (incl. `rod/lib/launcher`, `plugin/table`, `x/net/publicsuffix`) builds for all three targets with `CGO_ENABLED=0`; the only `import "C"` in the graph is a `//go:build none` file in `modernc.org/libc`. If a later bump breaks this, stop and flag.

**Sanity check:** `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && go build -o /tmp/magpie ./cmd/magpie && /tmp/magpie --help`

### Task 1.2 — `config`: file + env + flags + keyring (3h)

Goal: resolved `Config` with precedence flags > env (`GOMAGPIE_` prefix) > file (`%APPDATA%\gomagpie\config.yaml` / `$XDG_CONFIG_HOME/gomagpie/config.yaml`) > defaults; `magpie config set-key <provider>` stores in OS keyring.

```go
// Confirmed via keyring docs (2026-09-16): zalando/go-keyring v0.2.8, one API, all OSes.
import "github.com/zalando/go-keyring"
err := keyring.Set("gomagpie", "anthropic", apiKey)   // service=gomagpie, username=provider
key, err := keyring.Get("gomagpie", "anthropic")
```
Key read order: explicit flag > `GOMAGPIE_<PROVIDER>_API_KEY` env > keyring > config file (warn + require 0600 perms if used). Never log keys; `config show` prints `***redacted***`.

Implementation notes: YAML via `gopkg.in/yaml.v3` (only new dep beyond spec list — it's the standard, tiny, no CGO; alternative is hand-rolled JSON, don't). Headless-Linux caveat: Secret Service may be absent → `Get` error falls through to env var, and `set-key` on that error prints "no secret service; export GOMAGPIE_X_API_KEY instead" (fail loud, don't crash). `ponytail:` no config-file watching/reload (single-run CLI; ceiling = `serve` in Phase 3 re-reads per invocation — fine).

**Sanity check:** `GOMAGPIE_EXTRACT_PROVIDER=ollama go run ./cmd/magpie config show | grep provider` prints `ollama`

### Task 1.3 — `store`: SQLite open + full DDL + cost writers (3h)

Goal: `store.Open(path)` returns handle with all five §9.2 tables migrated; `LogLLMCall` + `BeginRun/FinishRun` used by extract/cmd.

```go
// Confirmed pattern (modernc.org/sqlite, pure-Go, database/sql native):
import (
    "database/sql"
    _ "modernc.org/sqlite"
)
// Verified live 2026-09-16: DSN _pragma params apply on every new connection (foreign_keys is per-connection).
dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
db, err := sql.Open("sqlite", dsn)
db.SetMaxOpenConns(1)               // single writer: hard SQLite constraint (spec §9.1)
_, err = db.Exec(ddl)               // multi-statement Exec works with this driver (verified)
```

Implementation notes: paste the **entire §9.2 DDL verbatim** (selector_cache, crawl_state, dedup, run_history, llm_calls) behind `CREATE TABLE IF NOT EXISTS` + version guard. Default DB path: `%LOCALAPPDATA%\gomagpie\cache.db` / `$XDG_CACHE_HOME/gomagpie/cache.db` (fallback `~/.cache/gomagpie`); override `--cache-db`. Unit test with temp-file DB (real driver, no mock — it's pure Go and fast).

**Sanity check:** `go test ./store/ -v` passes; `sqlite3 $f .schema` shows all five tables after `Open`

### Task 1.4 — `fetch/http.go`: static client per §1.3 (3h)

Goal: `StaticFetcher` with the exact transport config, 10-redirect cap, in-memory cookiejar (publicsuffix), rotating desktop header bundles with `fr-FR` Accept-Language, bot-identifying default UA.

Implementation notes: copy the `DefaultHTTPClient` literal from spec §1.3 verbatim (30s `client.Timeout`, `ForceAttemptHTTP2`, 100/10 conns, 90s idle, 10s TLS handshake, `DisableCompression:false`). Per-attempt `context.WithTimeout(20s)` wraps each call (caller supplies ctx; retries in Phase 2 get fresh budgets). Cookie jar: `cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})`. Build the transport in a constructor (`NewStaticFetcher`) rather than a package-level literal so you can add one stdlib line: `transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))` — `net/http` otherwise rejects `file://` with `unsupported protocol scheme "file"` (verified), and local-file scrapes give a hermetic end-to-end test of detect→clean with zero network. Tests: `httptest.Server` covering redirect chain >10 → error, gzip auto-decode, 429 passthrough (no retry yet — taxonomy is Phase 2; just return status+body faithfully), plus one `file:///abs/path` fetch.

**Sanity check:** `go test ./fetch/ -run TestStatic -v` passes with zero network (all `httptest`)

### Task 1.5 — `fetch/detect.go`: JS-required heuristic (3h)

Goal: `ScoreJSRequired(html []byte, headers) int` + `NeedsBrowser(score) = score >= 2`, implementing every row of the §1.2 table plus all six SPA fingerprints, with the `__NEXT_DATA__`/`__NUXT__` fast path returning `hasEmbeddedData=true` (caller skips the browser entirely).

Implementation notes: parse once with `golang.org/x/net/html` (already an indirect dep via goquery — promote to direct). Visible-text length = text nodes minus script/style. `noscript` scan is case-insensitive substring on "enable javascript"/"requires javascript". Tests: golden-ish table test with inline fixtures — empty React root (+2), `<app-root>` (+2), text-rich static page (0), `__NEXT_DATA__` page (embedded=true), `<meta name="fragment">` (+1). Deterministic, no network.

**Sanity check:** `go test ./fetch/ -run TestDetect -v` — 6+ subtests, all pass

### Task 1.6 — `fetch/rod.go`: lazy browser behind `Fetcher` (4h)

Goal: `RodFetcher` implements `Fetcher`; browser launches on first `Fetch`, never at import/startup; closes via `Close()`/`defer` (no zombie class).

```go
// fetch/fetcher.go — the ONLY place rod-adjacent types are referenced in signatures
type FetchRequest struct {
    URL string
    // Timeout time.Duration — per-attempt budget, default 20s
}
type FetchResponse struct {
    URL, FinalURL string
    StatusCode    int
    HTML          []byte
    Headers       http.Header
}
type Fetcher interface {
    Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error)
    CanHandle(req FetchRequest) bool // static: true; rod: NeedsBrowser(score)>=2 path
    Close() error
}
```

Implementation notes: `CanHandle` for rod runs static fetch internally? No — keep it dumb: the **caller** (`cmd/scrape`) runs static → scores → escalates. `RodFetcher.Fetch` navigates, waits for `DOMContentLoaded` + 2s settle (`ponytail:` fixed settle, no network-idle heuristic; ceiling = slow-Commerce hydration, tunable later), returns rendered HTML. Pool size 1 (Phase 2 raises to 2 workers). Browser test file gets `//go:build browser` + trivial "launches and renders data: URL" smoke test only — full coverage stays in httptest/static tests. **First line of that test:** `if _, ok := launcher.LookPath(); !ok { t.Skip("no Chrome/Chromium found") }` — rod's `Launch()` with an empty `Bin` calls `browser.Get()`, which silently downloads a ~150 MB Chromium (verified in `lib/launcher/launcher.go` `getBin`). Production `rod.go` keeps rod's default (auto-download is the spec §1.1 "pinned browser" feature); only the test must not trigger it.

**Sanity check:** `go test ./fetch/ && go test -tags browser ./fetch/ -run TestRodSmoke -v` (second needs Chrome; the `LookPath` guard skips it otherwise — there is no Chrome on the current WSL dev box, so expect SKIP there)

### Task 1.7 — `clean`: trafilatura → sidecar → markdown (5h)

Goal: `Clean(ctx, RawPage) (CleanedPage, error)` where `CleanedPage{Markdown, StructuredData json.RawMessage, Title, FinalURL}`; tables → GFM, JSON-LD always complete, markdown capped at 8k tokens naive top-down.

```go
// Confirmed via `go doc` on go-trafilatura/v2 v2.2.1 (2026-09-16):
import "github.com/markusmobius/go-trafilatura/v2"
opts := trafilatura.Options{
    ExcludeComments: true,
    IncludeImages:   true,  // keeps ![alt](src) provenance
    IncludeLinks:    true,
    EnableFallback:  true,  // readability/dom-distiller fallback is OPT-IN — spec §2.1 "for free" only holds with this set
    OriginalURL:     parsedURL, // *url.URL context for metadata/relative URLs
}
result, err := trafilatura.Extract(strings.NewReader(htmlStr), opts)
// result.ContentNode is the retained DOM node; render with OuterHTML for the next step.
_ = dom.OuterHTML(result.ContentNode) // github.com/go-shiori/dom (trafilatura dep)

// Confirmed via `go doc` on html-to-markdown/v2 v2.5.2 plugin/table (2026-09-16):
import (
    "github.com/JohannesKaufmann/html-to-markdown/v2/converter"
    "github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
    "github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
    "github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)
conv := converter.NewConverter(converter.WithPlugins(
    base.NewBasePlugin(),
    commonmark.NewCommonmarkPlugin(),
    // GFM tables. colspan/rowspan are handled natively by the plugin: "mirror" repeats the
    // spanning cell's text into each covered cell so field↔value adjacency survives for the LLM.
    // This replaces spec §2.2's "keep the original HTML fragment inline" rule (custom code, dropped).
    table.NewTablePlugin(table.WithSpanCellBehavior(table.SpanBehaviorMirror)),
))
md, err := conv.ConvertString(contentHTML)
```

Implementation notes: **harvest sidecar first** (goquery on raw HTML: all `script[type="application/ld+json"]` verbatim + `#__NEXT_DATA__` text + `window.__NUXT__` regex) — before trafilatura strips scripts. The v2.5.0 empty-text-node panic (trafilatura output → html-to-markdown) was fixed upstream in v2.5.1 (`fix: panic on empty text node #210`); the pinned v2.5.2 carries the fix, so **no recover/fallback guard** — the golden tests (article with tables/images/nested lists/empty nodes, product page with JSON-LD, SPA shell) lock the contract across future upgrades instead. Token cap: `ponytail:` count ≈ len/4 chars (no tokenizer dep; ceiling = ±20% budget error — irrelevant at 8k scale).

**Sanity check:** `go test ./clean/ -v` passes; `go test ./clean/ -update` rewrites `testdata/clean/*.md` and diff shows only intended changes

### Task 1.8 — `extract/schema.go`: schema load + x-gomagpie + validation (3h)

Goal: `LoadSchema(path)` (YAML or JSON, draft 2020-12 subset) → compiled validator + parsed `FieldHints`; rejects non-portable constructs loudly (discriminated unions, numeric bounds you depend on — warn; `additionalProperties` missing — default it to false per provider strict-mode needs).

```go
// Confirmed via santhosh-tekuri/jsonschema/v6 pkg.go.dev examples (2026-09-16):
import "github.com/santhosh-tekuri/jsonschema/v6"
c := jsonschema.NewCompiler()
sch, err := c.Compile(schemaPath)            // loads from file path/URL directly
// — or for in-memory bytes (stdin/embedded tests):
// c.AddResource("schema.json", schemaAny) + c.Compile("schema.json")
inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(docBytes)) // NOT encoding/json: preserves number kinds
err = sch.Validate(inst)                     // *ValidationError; err.Error() (verified v6.0.3):
//   jsonschema validation failed with 'file:///.../schema.json#'
//   - at '/price': got string, want number
// Programmatic path: err.(*jsonschema.ValidationError).Causes[0].InstanceLocation == []string{"price"}
```

Implementation notes: YAML→JSON via `yaml.v3` → `json.Marshal` round-trip (unknown `x-gomagpie` keys survive; validator ignores them per spec). `jsonld_path` uses a minimal `$.a.b.0.c` walker hand-rolled (~30 lines, no jsonpath dep). `Trim`/`multiple` handled in coerce step (Task 1.9).

**Sanity check:** `go test ./extract/ -run TestSchema -v` — valid doc passes, wrong-type doc fails with `err.Error()` containing `at '/price'` (v6 prints `/price`, never `#/price`)

### Task 1.9 — `extract` providers + repair loop + coerce (6h)

Goal: `Extractor` interface + `openai.go` (OpenAI **and** Ollama via base URL) + `anthropic.go` + 3-attempt repair loop + `coerce.go`; steady-state invalid JSON after 3 attempts → loud error with validator output.

Provider wire format (stdlib HTTP, no SDKs):
- OpenAI: `POST {base}/chat/completions` with `response_format: {"type":"json_schema","json_schema":{"name":"gomagpie","strict":true,"schema":<user-schema>}}` (+ `additionalProperties:false`, all fields in `required` — enforce at schema-compile time). Ollama: same adapter, `base=http://localhost:11434/v1`. `ponytail:` OpenAI-compatible path only for Ollama in Phase 1 — Ollama's compat layer passes `json_schema` through to its native `format`, but its docs list only `response_format` generically and ollama/ollama#10001 reports the schema being ignored on some models (falls back to plain JSON mode); ceiling = schema-free JSON on those models, repair loop absorbs it; upgrade path = native `POST /api/chat` with `format: <schema>` (~20 lines).
- Anthropic (verified 2026-09-16, structured outputs are GA): `POST /v1/messages` with headers `x-api-key`, `anthropic-version: 2023-06-01` — **no beta header** — and body `{"model":..., "max_tokens": 16000, "messages":[...], "output_config": {"format": {"type": "json_schema", "schema": <user-schema>}}}`. The JSON arrives in the first `content[].type == "text"` block. Do **not** wrap the schema as a forced tool: `tool_choice: {"type":"tool"}` returns HTTP 400 on Claude Fable 5.1. Same schema rules as OpenAI (`additionalProperties:false`, all fields `required`, no numeric/length constraints). Check `stop_reason` before parsing (`max_tokens` → truncated; `refusal` → not schema-shaped).

Repair loop: attempt 0 = full prompt (system: "emit ONLY JSON matching schema"; user: sidecar + markdown window); attempts 1–2 = previous output + `validator.Error()` text. Max 3 total, then return error. `coerce.go`: `eur_decimal` (strip `€`, nbsp, thin spaces; `,`→`.`; `strconv.ParseFloat`), `int/float/iso_date/bool/trim`, `regex` capture applied before coerce.

**Sanity check:** `go test ./extract/ -run 'TestRepair|TestCoerce' -v` against an `httptest` fake provider (canned invalid→valid sequence asserts exactly 2 calls); no real API keys anywhere in tests

### Task 1.10 — `extract/cost.go` + `--max-cost` + `cmd/magpie` wiring (4h)

Goal: static per-model price table, `TokenUsage` recorded to `llm_calls` with `purpose` (synth|extract|repair), running total in `run_history`, `--max-cost` abort (exit 6); Cobra tree: `scrape`, `extract`, `config set-key|show` (crawl/serve/build/cache arrive in later phases — **do not stub them**).

Price table (USD per 1M tokens in/out, updatable via config; values checked 2026-09-16 — re-confirm at build, treat as data not code):
`claude-opus-5` 5.00/25.00, `claude-sonnet-5` 2.00/10.00, `claude-haiku-4-5` 1.00/5.00, `gpt-4o-mini` 0.15/0.60 (still served; OpenAI's current budget tier is `gpt-5-nano` 0.05/0.40 — add rows as you use them), `ollama/*` = 0. Default `--model` per provider: `claude-sonnet-5` / `gpt-4o-mini`. Unknown model → log warning + cost 0, never block.

`scrape` flow: parse flags → `store.BeginRun` → static fetch → detect (escalate or embedded-data fast path) → clean → if `--schema` omitted: print markdown, exit 0, no LLM → else extract → validate → coerce → print (`--format json|jsonl`, default json; csv/sqlite are Phase 2 — reject loudly) → `FinishRun`. Exit codes per §10.2: 0/1/2/3/5(robots — Phase 2 enforces; Phase 1 never emits)/6/7(missing key). `--render auto|static|browser` honored (`browser` skips static; `static` never escalates).

**Sanity check:** `go run ./cmd/magpie scrape "file://$PWD/testdata/clean/article.html" --format json` (absolute path — `file://testdata/...` parses `testdata` as the host; needs the Task 1.4 file transport) prints markdown JSON with 0 `llm_calls`; same with `--schema` + fake `GOMAGPIE_..._API_KEY` + `GOMAGPIE_BASE_URL=http://127.0.0.1:<httptest>` prints extracted JSON (wire a test-only env override for base URL — no test hooks in production paths beyond env config)

---

## 4. Deliverables

```
gomagpie/
├── cmd/magpie/
│   ├── main.go              # Cobra root (magpie), global flags: --cache-db, --max-cost
│   ├── scrape.go            # scrape <url>: fetch→detect→clean→extract→print, exit codes §10.2
│   ├── extract.go           # extract from stdin/file, --content-type html|markdown
│   └── config_cmd.go        # config set-key <provider> | config show (redacted)
├── fetch/
│   ├── fetcher.go           # FetchRequest/Response, Fetcher interface (rod lives behind this)
│   ├── http.go              # StaticFetcher: §1.3 client, redirects, jar, header bundles
│   ├── detect.go            # ScoreJSRequired + NeedsBrowser(≥2) + embedded-data fast path
│   ├── rod.go               # RodFetcher: lazy browser, settle-wait, Close (no zombies)
│   ├── fetch_test.go        # httptest: redirects/gzip/429, detect table tests (no network)
│   └── rod_smoke_test.go    # //go:build browser — launch + render data: URL only
├── clean/
│   ├── clean.go             # Clean(): sidecar → trafilatura → markdown → 8k cap
│   ├── sidecar.go           # JSON-LD + __NEXT_DATA__ + __NUXT__ + microdata harvest
│   ├── clean_test.go        # golden tests + -update flag
├── extract/
│   ├── extractor.go         # Extractor interface + 3-attempt repair loop
│   ├── schema.go            # LoadSchema, x-gomagpie hints, portable-subset gate, Validate
│   ├── openai.go            # OpenAI-compatible adapter (also serves Ollama via base URL)
│   ├── anthropic.go         # Messages API + output_config.format json_schema (GA, no beta header, no tool_choice)
│   ├── coerce.go            # eur_decimal/int/float/iso_date/bool/trim/regex
│   ├── cost.go              # price table, USD estimate, max-cost check
│   └── extract_test.go      # fake-provider repair/coerce/schema tests (no keys, no net)
├── config/
│   ├── config.go            # Config struct, YAML+env+flag precedence, keyring get/set
│   └── config_test.go       # precedence flags>env>file>defaults + key-lookup order (keyring stubbed via env path; no OS keyring in tests)
├── store/
│   ├── sqlite.go            # Open, full §9.2 DDL, BeginRun/FinishRun/LogLLMCall
│   └── sqlite_test.go       # temp-file DB round-trip
├── testdata/
│   ├── clean/               # article.html/.md, product.html/.md, spa-shell.html/.md
│   └── extract/             # price.yaml, book.yaml, invalid-doc.json
├── go.mod / go.sum          # pinned versions incl. modernc.org/libc lockstep
└── plan/phase-1.md          # this file
```

---

## 5. Exit Criteria

- [ ] `magpie scrape <article-url> --schema testdata/extract/price.yaml` prints schema-valid JSON, exit 0 (Deliverables: cmd, fetch, clean, extract)
- [ ] `magpie scrape <spa-url>` escalates to browser; `--render static` and non-escalating pages never launch a browser (rod is linked into the binary regardless — the guarantee is "never launched", asserted via log line + `RodFetcher` constructor never called) (fetch/rod.go, detect.go)
- [ ] `echo "$HTML" | magpie extract --schema price.yaml --content-type html` works with no fetch (cmd/extract.go)
- [ ] `go test ./...` exits 0, every package with logic has `*_test.go`, zero live-network/browser tests in default suite (all `*_test.go`)
- [ ] Golden files regenerate via `-update` and lock the trafilatura→markdown contract incl. tables (clean/clean_test.go, testdata/clean/)
- [ ] Repair loop test proves invalid→valid converges in 2 fake-provider calls; 3rd failure errors loudly (extract/extract_test.go)
- [ ] `llm_calls` + `run_history` rows exist post-scrape; `--max-cost 0.000001` aborts with exit 6 before any call because the pre-check adds the projected prompt cost to the running total (store, cost.go)
- [ ] Missing API key → exit 7 with "set via flag, GOMAGPIE_* env, or `magpie config set-key`"; `config_test.go` proves flags > env > file > defaults (config/config.go, config/config_test.go)
- [ ] `CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64; `go vet` clean; `gofmt -l .` empty (go.mod)
- [ ] `magpie config set-key` round-trips on the dev machine; headless-no-secret-service path prints the env fallback hint (cmd/config_cmd.go)

---

## 6. Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are implementing Phase 1 of `gomagpie` (module `gomagpie`) — single-URL fetch→clean→extract. Repo: `/home/domidex/projects/gomagpie`. Source of truth: `spec.md` (771 lines, read it first — key sections §1 fetch, §2 clean, §3 extract, §9 storage, §10 CLI). Conventions: `AGENTS.md` + `.pi/rules/go.md` + `.pi/rules/testing.md` (read all three before writing code).

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`): static fetch → JS detection → optional go-rod browser → trafilatura boilerplate removal → markdown + JSON-LD sidecar → provider-agnostic LLM structured extraction with schema validation. Pure-Go, zero CGO, single static binary cross-compiled `CGO_ENABLED=0` for windows/amd64, linux/amd64, darwin/arm64. This phase (spec Milestone 1, week 1) builds the single-URL pipeline; Phase 2 adds selector cache/crawl, Phase 3 MCP/plugins. There are no prior phases — greenfield except `go.mod` (`module gomagpie`, `go 1.26.5`) and empty `plan/`, `testdata/` dirs.

### Data Model Rules (follow exactly)
- Domain types (`FetchResponse`, `CleanedPage`, `ExtractInput/Result`, `TokenUsage`, `Record`) are plain structs with `json`/`yaml` tags. No ORM, no codegen.
- Stage boundaries are small interfaces declared in the consuming package: `Fetcher{Fetch, CanHandle, Close}`, `Cleaner{Clean}`, `Extractor{Extract, Name}`. Accept interfaces, return structs.
- `Config` is a plain struct: YAML file → `GOMAGPIE_` env overlay → flags win. Hand-roll (~40 lines, `gopkg.in/yaml.v3` only). Precedence: flags > env > file > defaults.
- Errors wrap with context and fail loudly: `fmt.Errorf("clean: %w", err)`. Never `_ =` a meaningful error. Unknown `x-gomagpie.coerce` value = error at schema load.
- No concurrency scaffolding (no errgroup/channels) — single URL, straight-line code. No generics unless trivial. Stdlib first.
- Deliberate shortcuts get a `ponytail:` comment naming the ceiling (token counting ≈ len/4; fixed 2s browser settle; Ollama via OpenAI-compatible endpoint only — schema may be ignored on some models, upgrade path is native `/api/chat` `format`).

### Architecture
Straight-line pipeline in `cmd/magpie/scrape.go`: static fetch → `ScoreJSRequired` (escalate iff ≥ 2; `__NEXT_DATA__`/`__NUXT__` present → harvest inline, skip browser) → `clean.Clean` (sidecar FIRST, then trafilatura, then markdown, 8k-token cap, JSON-LD always complete) → if `--schema` given: `extract` (validate → up-to-3 attempts with validator-error repair → coerce) → print json|jsonl → `store.FinishRun`. go-rod imported ONLY in `fetch/`. SQLite single writer (`SetMaxOpenConns(1)` + WAL/foreign_keys via DSN `_pragma`), full 5-table spec §9.2 DDL pasted verbatim now. `--max-cost` pre-check = running total + projected next-call cost (prompt bytes/4 × input price). Static fetcher registers `http.NewFileTransport(http.Dir("/"))` for `file://` so local fixtures scrape hermetically. Exit codes: 0 ok, 1 runtime, 2 usage, 3 all-failed, 6 cost-ceiling, 7 credentials.

### Confirmed Library APIs (verified 2026-09-16 with `go doc` + live runs against the pinned versions — use exactly these)
```go
// go-trafilatura/v2 v2.2.1 — Extract takes io.Reader + Options, returns DOM node in result.ContentNode
result, err := trafilatura.Extract(strings.NewReader(htmlStr), trafilatura.Options{
    ExcludeComments: true, IncludeImages: true, IncludeLinks: true,
    EnableFallback: true,   // readability fallback is opt-in; without it there is no fallback
    OriginalURL: parsedURL, // *url.URL
})
htmlFrag := dom.OuterHTML(result.ContentNode) // github.com/go-shiori/dom (already in the graph)

// html-to-markdown/v2 v2.5.2 — converter + base + commonmark + table plugins (base+commonmark mandatory)
conv := converter.NewConverter(converter.WithPlugins(
    base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(),
    table.NewTablePlugin(table.WithSpanCellBehavior(table.SpanBehaviorMirror)))) // colspan/rowspan handled here
md, err := conv.ConvertString(htmlFrag)
// imports: converter, plugin/base, plugin/commonmark, plugin/table

// santhosh-tekuri/jsonschema/v6 v6.0.3 — Compile from path/URL, or AddResource(name, doc any)+Compile(name) for in-memory;
// UnmarshalJSON (not encoding/json); Validate returns *ValidationError.
c := jsonschema.NewCompiler()
sch, err := c.Compile(schemaPath)
inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(docBytes))
err = sch.Validate(inst) // err.Error() == "jsonschema validation failed with '...#'\n- at '/price': got string, want number"
                         // feed it verbatim into the repair prompt; tests assert on "at '/price'" (no '#/')

// modernc.org/sqlite v1.59.0 (libc v1.75.7 via MVS) — database/sql native; pragmas in the DSN (verified live)
import _ "modernc.org/sqlite"
db, _ := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
db.SetMaxOpenConns(1)
// zalando/go-keyring v0.2.8 — service=gomagpie, username=provider; keyring.ErrNotFound / ErrUnsupportedPlatform exist
keyring.Set("gomagpie", "anthropic", key); key, err := keyring.Get("gomagpie", "anthropic")
// go-rod/rod v0.116.2 — browser tests: guard with launcher.LookPath(); Launch() with empty Bin auto-downloads Chromium
if _, ok := launcher.LookPath(); !ok { t.Skip("no Chrome") }
// net/http — file:// is NOT supported by default: transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
```
Pins (all exact, all resolved 2026-09-16): `go-rod/rod v0.116.2`, `go-trafilatura/v2 v2.2.1`, `html-to-markdown/v2 v2.5.2`, `go-keyring v0.2.8`, `spf13/cobra v1.10.2`, `PuerkitoBio/goquery v1.13.0`, `santhosh-tekuri/jsonschema/v6 v6.0.3`, `modernc.org/sqlite v1.59.0`, `gopkg.in/yaml.v3 v3.0.1`, `golang.org/x/net@latest`. No OpenAI/Anthropic SDKs — stdlib HTTP adapters: OpenAI chat-completions `response_format: {type: json_schema, json_schema: {name, strict: true, schema}}` doubles for Ollama via base-URL switch; Anthropic `POST /v1/messages` with `output_config: {format: {type: json_schema, schema}}` — structured outputs are GA, **no beta header, no tool wrapping, no `tool_choice`** (forced tool use is a 400 on Claude Fable 5.1); JSON is in the first text content block. Price table uses current IDs: `claude-opus-5`, `claude-sonnet-5`, `claude-haiku-4-5`, `gpt-4o-mini`.

### Files to Create
Per-file: `cmd/magpie/main.go` (root + `--cache-db/--max-cost` globals); `scrape.go` (the pipeline above, `--schema/--render/--provider/--model/--out/--format`); `extract.go` (stdin/file × html/markdown, no fetch); `config_cmd.go` (`set-key` prompts via `golang.org/x/term`? No — `ponytail:` plain `fmt.Scanln`, ceiling = key echoes on screen; `show` redacts). `fetch/fetcher.go` (types+interface); `http.go` (spec §1.3 literal + jar + UA bundles); `detect.go` (full §1.2 table + 6 fingerprints); `rod.go` (lazy init, DOMContentLoaded+2s, `Close`); `clean/clean.go` + `sidecar.go`; `extract/extractor.go` (interface+loop) + `schema.go` (YAML/JSON load, `x-gomagpie` parse, subset gate, `$.a.b.0` walker) + `openai.go` + `anthropic.go` + `coerce.go` + `cost.go`; `config/config.go`; `store/sqlite.go` (full DDL + run/cost writers). Tests: `fetch/fetch_test.go` (httptest redirects/gzip/429 + one `file://` fetch + detect table, no net), `rod_smoke_test.go` (`//go:build browser`, `launcher.LookPath()` skip guard first, data: URL only), `clean/clean_test.go` (goldens + `-update`), `extract/extract_test.go` (httptest fake provider: fail-once-then-valid asserts 2 calls; coerce vectors; schema error text asserts `at '/price'`), `config/config_test.go` (precedence + key-lookup order, env path only), `store/sqlite_test.go` (temp DB, asserts `PRAGMA foreign_keys` == 1). Fixtures: article+product+SPA HTML under `testdata/clean/`, `price.yaml`+`book.yaml` under `testdata/extract/`. Do NOT create crawl/serve/build/cache commands or any Phase 2/3 scaffolding.

### Success Criteria
- All 10 Exit Criteria (Section 5) check out, in order.
- `go test ./... && go vet ./...` exit 0; `gofmt -l .` prints nothing; all three `CGO_ENABLED=0` cross-builds succeed.
- A capable engineer pastes this prompt into a fresh session with no other context and ships the phase with zero follow-up questions.

### Expected File Structure at End
(See Section 4 Deliverables tree — reproduce it exactly, no extra packages.)
---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — no prior phases exist; spec.md (all 13 sections), go.mod, AGENTS.md, and both `.pi/rules/` files were read and are cited inline
- [PASS] Every sub-task has a clear, testable completion condition — Tasks 1.1–1.10 each end with a one-liner `Sanity check`
- [PASS] Execution prompt is self-contained: (a) prior phases stated as greenfield + spec section pointers, (b) five confirmed API snippets with versions, (c) Go-adapted Data Model Rules table, (d) per-file guidance for all ~20 files, (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables — 10 criteria cover cmd/fetch/clean/extract/config/store/testdata/go.mod; `plan/phase-1.md` maps to this artifact itself
- [PASS] Heavy externals have fake/stub strategies — LLM via httptest fake provider (no keys/net), browser via `//go:build browser` smoke test only (Chrome needed for that one test; noted in Task 1.6), SQLite via temp-file real driver (pure Go, fast)
- [PASS] Load-bearing new libraries (trafilatura, html-to-markdown+table, jsonschema/v6, modernc sqlite, go-keyring, rod launcher) have confirmed snippets in the execution prompt, re-verified 2026-09-16 by `go doc` on the pinned versions plus live runs (jsonschema error text, sqlite DSN pragmas, `file://` rejection, `-update` flag ordering) and a `CGO_ENABLED=0` build of every dep for windows/amd64, linux/amd64, darwin/arm64. Standard-idiom deps (cobra, goquery, yaml.v3) intentionally have no snippets — plain usage, no API risk.
- [PASS] Provider wire formats match current APIs — Anthropic structured outputs are GA (`output_config.format`, no beta header; forced `tool_choice` rejected on Claude Fable 5.1); price-table model IDs are the current generation (Opus 5 / Sonnet 5 / Haiku 4.5).
