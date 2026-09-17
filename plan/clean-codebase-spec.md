# Clean-Codebase Spec — gomagpie learns from flyscrape

Source: comparison of `gomagpie` (this repo, ~9.8k non-test LOC, 14 pkgs, 19 direct deps)
vs [`philippta/flyscrape@master`](https://github.com/philippta/flyscrape/tree/master)
(~4–5k LOC total, 1 root pkg + 15 single-file modules, ~12 direct deps).
Date: 2026-09-17.

Goal: a **super-clean, working, well-structured, ultra-simplified** codebase.
Not a rewrite — a convergence: keep our features (LLM extract, selector cache,
verticals, MCP, crawl), adopt flyscrape's *shape*.

---

## 1. What flyscrape does better (facts)

| # | Flyscrape pattern (file) | What it is | Our equivalent | Verdict |
|---|---|---|---|---|
| 1 | `module.go` — 6 tiny interfaces (~70 LOC) | `Provisioner / TransportAdapter / RequestBuilder / RequestValidator / ResponseReceiver / Finalizer`. Module = struct + `ModuleInfo()`. Zero cross-module imports; every module imports only root `flyscrape` + stdlib. | `core/registry.go` (kind-agnostic `ModuleInfo{ID, APIVersion, Kind, New func() any}` + semver gate + 7 kind strings) + `plugin/exec.Exporter` + `plugin/wasm.Runner` + `fetch.Fetcher` — **three** plugin systems that don't interoperate. | **Adopt flyscrape's.** One interface family, typed hooks, `New() Module`. Delete the `Kind string` + `any` registry. |
| 2 | Transport chaining (`retry/retry.go`, `ratelimit`, `cache`, `proxy` as `AdaptTransport`) | Retry/ratelimit/cache/proxy are `http.RoundTripper` wrappers, composed in fixed order (`moduleOrder`: proxy→browser→retry→ratelimit→cache→cookies→headers). ~50–120 LOC each, no goroutines of their own. | `crawl/backoff.go` + `crawl/ratelimit.go` + `fetch/http.go` + `fetch/profiles.go` + `store/sqlite.go` — same concerns spread over 3 packages, custom token buckets, custom backoff, custom gate (`core.BrowserGate`). | **Adopt chaining.** Retry/ratelimit/cache/headers/cookies become `AdaptTransport` wrappers in `modules/`. Delete `crawl/backoff.go`, `crawl/ratelimit.go`, `core.BrowserGate`. |
| 3 | `scrape.go` engine (~200 LOC) | One `Scraper` struct: `jobs chan target (1M buf)` + `visited hashmap` + `process()` + `processImmediate()`. Request built by `RequestBuilder` hooks, filtered by `RequestValidator`, observed by `ResponseReceiver`. No generics, no errgroup layers. | `core/pipeline.go` (generic 4-stage errgroup channel pipeline: source→fetch→clean→extract→sink, 223 LOC) **plus** `crawl/crawl.go` (712 LOC: second engine with frontier, bloom, robots, resume, progress callbacks) **plus** `scrape/scrape.go` (329 LOC: third flow). Three engines for one pipeline. | **One engine.** Keep the `crawl` feature set, but implement it as *one* `Engine` (~250 LOC max) with flyscrape's hook shape, not three parallel implementations. |
| 4 | `js.go` — one extension point | Single `ScrapeFunc(params) (any, error)` + jQuery-like `doc` map. All extraction (incl. nested `scrape(url, fn)` + `follow(url)`) flows through it. JS script is config **and** code: `export const config` unmarshalled per-module via `json.Unmarshal(cfg, mod)`. | **Four** extraction paths: `extract/` (4 provider files with duplicated HTTP: openai/anthropic/codex/zen + schema/coerce/cost ≈ 1000 LOC) + `selector/` (cache/synth/heal/validate ≈ 700 LOC) + `vertical/` (5 typed extractors ≈ 1070 LOC) + `clean/` sidecars/islands/llm. Plus **two** plugin systems (wazero WASM 308 LOC + exec exporter). | **One `Extractor` interface, one call site.** Providers become ~40-line `RoundTripper`-style adapters over a shared JSON caller. Verticals become `Extractor` impls that don't need an LLM (no separate `vertical.Fetcher` seam, no `Result.Vertical` plumbing). WASM + exec collapse to one `Exporter` (see §3.5). |
| 5 | Flat `modules/<name>/<name>.go` | 15 modules, each **one file, 50–150 LOC**, own package, `init()` self-register. `depth`, `domainfilter`, `urlfilter`, `followlinks`, `headers`, `cookies`, `proxy`, `retry`, `ratelimit`, `cache`, `output/json`, `output/ndjson`, `browser`, `starturl`, `hook`. | `clean/` = 8 files / 1285 LOC. `crawl/` = 7 files / 1418 LOC. `cli/` = 8 files / 1405 LOC with a 733-line `cmd_test.go`. `vertical/` = 12 files / 1067 LOC. `extract/` = 8 files / 1007 LOC. Deep import fan-in: `extract`×19, `clean`×19, `store`×17, `fetch`×13 import sites. | **Flat modules, LOC budgets** (§3.1). No package over 400 non-test LOC. No file over 250 LOC without a waiver comment. |
| 6 | One config surface | JS `export const config` → JSON bytes → each module `json.Unmarshal`s what it needs. CLI overrides are a `map[string]any` patched via `sjson`. No `config` package, no second validation layer. | `config/config.go` (332 LOC: file+env+keyring+`ApplyFlags`) **plus** per-command flag structs **plus** re-validation in `runScrape`/`runCrawl` switches (render/format/page-format/vertical switches repeated in CLI *and* MCP *and* scrape). `scrapeOptions` is flag-only fields that "must never enter config" (comment in code = the design telling you it's wrong). | **One `Options` struct, validated once.** Cobra flags bind directly to it; MCP builds the same struct. Validation lives on `Options.Validate() error`, called at exactly one place (engine entry). Delete `config.ApplyFlags` + per-command re-switches. |
| 7 | Small dep set (`go.mod`: ~12 direct) | `goquery, goja, goja_nodejs, esbuild, rod, kooky, bbolt, fsnotify, sjson, hashmap, screen, whatwg-url`. One job per dep (JS runtime, browser, cookies, cache, UI). | 19 direct deps incl. **two** markdown/HTML stacks (`go-trafilatura` + `html-to-markdown` + `goquery` + `go-readability` indirect), **two** keyring paths, bloom + sqlite + backoff + jsonschema + cobra + wazero + MCP SDK + yaml. `go.mod` has 61 `require` lines. | **One job per dep.** Audit each direct dep against "which user-visible feature dies without it" (§3.6). |
| 8 | Errors are dumb and local | `log.Println` + `response.Error` field on the per-page struct; stage never dies because a page failed. (Their *mistakes*: `os.Exit` inside a module, `panic` in `LoadModules`, fixed 500 goroutines, no `ctx` — do **not** copy these.) | We actually do this part right: per-page `Err` fields (`core.FetchedPage/Cleaned/PageResult`), `ErrRobotsBlocked/ErrCostCeiling/ErrMissingKey` taxonomy, ctx plumbed everywhere. | **Keep ours.** This is the one place we're cleaner — codify it as the rule (§3.4) and fix flyscrape's mistakes (no `os.Exit`/`panic` in library, ctx everywhere, bounded workers from `Options`, not constants). |

### Flyscrape flaws to deliberately NOT copy

- `scrape()` spawns **500 fixed goroutines**; queue-full drops URLs with a log line. → Bounded workers from `Options` (`FetchWorkers`, default 8).
- `log.Println` / `os.Exit(1)` / `panic()` inside library code (`json.go` Provision, `LoadModules`). → Return errors; per-page `Err` field; fatal only in `main`.
- No `context.Context` anywhere; `time.Sleep` in retry (uncancellable). → ctx everywhere, `backoff` with `ctx.Done()`.
- `visited` is URL-string exact-match (no normalization, no bloom, no resume). → Keep our `store` visited table, but hide it *inside* the `cache` module, not as a cross-cutting import.

---

## 2. Our structural problems (ranked by cost)

1. **Three engines, one pipeline.** `core/pipeline.go` + `crawl/crawl.go` + `scrape/scrape.go` overlap ~60%. A bug fix in retry/progress/ctx must land in 2–3 places.
2. **Fan-in imports.** `clean` and `extract` each imported 19 times; `store` 17. Everything knows about everything: `scrape` imports `clean+crawl+extract+fetch+selector+store+vertical` (7 pkgs). A change to `CleanedPage` ripples to CLI, MCP, crawl, scrape, tests.
3. **Validation in three places.** Same `render/format/vertical` switch in `cli/scrape.go`, `cli/crawl.go`, `mcp/tools.go`. Adding one flag = editing 3 files + 2 test files.
4. **Provider duplication.** `extract/openai.go` + `anthropic.go` + `codex.go` + `zen.go` each own HTTP + retry + JSON + error mapping (~700 LOC combined for ~4×40 LOC of real difference).
5. **`core` does two unrelated jobs.** Registry (Caddy-style modules) + pipeline (errgroup channels) share a package and a name but no logic. Its own doc comment admits the registry is "scaffolding for consumers that don't exist."
6. **Flag-only fields.** `scrapeOptions.PageFormat/Vertical` "must never enter config" — two parallel option types that must be kept in sync by hand.

---

## 3. Target spec — the clean codebase

### 3.1 Layout (flat, flyscrape-shaped)

```
cmd/magpie/main.go        # 10-line shim (unchanged)
magpie.go                 # Engine + Request/Response + hook interfaces (~200 LOC)
module.go                 # Module registry: typed, no `any`, no semver gate (~60 LOC)
modules/
  fetch/fetch.go          # static http + profiles + cookies passthrough (<200 LOC)
  browser/browser.go      # rod behind Fetcher iface; ONLY pkg importing rod (<200 LOC)
  clean/clean.go          # trafilatura→markdown + sidecar + metadata, ONE file (<250 LOC)
  extract/extract.go      # Extractor iface + shared JSON caller + coerce (<200 LOC)
  extract/openai.go anthropic.go codex.go zen.go   # ~40 LOC adapters each
  selector/selector.go    # cache + synth + heal in ONE file, null-rate rule (<250 LOC)
  vertical/vertical.go    # typed Extractors (no-LLM), registry map (<250 LOC)
  crawl/crawl.go          # frontier + depth + follow + robots + visited (<250 LOC)
  cache/cache.go          # sqlite visited+selector store, hidden here (<250 LOC)
  output/output.go        # json|jsonl|csv|sqlite writers (<200 LOC)
  retry/retry.go          # RoundTripper wrapper (<80 LOC)
  ratelimit/ratelimit.go  # RoundTripper wrapper (<80 LOC)
cli/                      # cobra thin adapters: flags→Options→Engine (<400 LOC total)
  root.go scrape.go crawl.go extract.go cache.go serve.go
mcp/                      # ONE file: Options builders over the Engine (<250 LOC)
testdata/                 # unchanged (goldens are the contract)
```

Budgets (hard): no package > 400 non-test LOC (today: clean 1285, crawl 1418, cli 1405,
vertical 1067, extract 1007). No file > 250 LOC without a `// ponytail:` waiver.
Total non-test target: **≤ 4.5k LOC** (today 9.8k).

### 3.2 Core interfaces (copy flyscrape's shape, fix its mistakes)

```go
// magpie.go
package magpie

type Request struct {
    URL     string
    Depth   int
    Headers http.Header
    Timeout time.Duration
}

type Response struct {
    Request    *Request
    StatusCode int
    Headers    http.Header
    Body       []byte
    URL        string // final URL after redirects
    Data       any    // extraction result
    Err        error  // per-page failure; NEVER stage-fatal
    Visit      func(url string, depth int) // enqueue follow-up
}

type Module interface{ ModuleInfo() ModuleInfo }
type ModuleInfo struct {
    ID  string
    New func() Module
}

// Hooks (flyscrape's six, renamed minimally, ctx added):
type Provisioner      interface{ Provision(*Options) error }   // was Provision(Context)
type TransportAdapter interface{ AdaptTransport(http.RoundTripper) http.RoundTripper }
type RequestBuilder   interface{ BuildRequest(context.Context, *Request) error }
type RequestValidator interface{ ValidateRequest(*Request) bool }
type ResponseReceiver interface{ ReceiveResponse(context.Context, *Response) error }
type Finalizer        interface{ Finalize() error }
```

Rules: modules import only `magpie` root + stdlib (+ their one job dep).
Cross-module imports are a **vet failure**. `RegisterModule` returns error
(never panics, never `os.Exit`). Engine threads `ctx` through every hook.

### 3.3 One engine (replaces core/pipeline + crawl/crawl + scrape/scrape)

```go
type Options struct {
    Seeds []string
    Render string // auto|static|browser — Validate() enforces
    Format string // json|jsonl|csv|sqlite
    Depth, MaxPages int
    FetchWorkers int  // default 8; browser slots default 2
    Selector SchemaRef
    Provider, Model string
    Vertical string   // ""|auto|<name>
    // ... all former cobra flags AND config keys live here, nowhere else
}
func (o Options) Validate() error // THE single validation site
type Engine struct { Client *http.Client; Modules []Module }
func (e *Engine) Run(ctx context.Context, o Options, out io.Writer) error
```

`scrape` = `Run` with `MaxPages=1, Depth=0`. `crawl` = same `Run` with higher
limits. `mcp scrape_url/crawl_site` build `Options` and call `Run`. One code
path, one retry policy, one progress/cost accounting.

### 3.4 Error + ctx rules (keep our good parts, codify)

- Per-page failure → `Response.Err`. Engine continues. Only `ctx` cancel,
  `Options.Validate` failure, or sink `io` error aborts `Run`.
- Keep `ErrRobotsBlocked / ErrCostCeiling / ErrMissingKey` — move to `magpie.go`,
  document exit-code mapping in `cli/root.go` ONLY.
- No `log.*`, `os.Exit`, or `panic` outside `cmd/` + `cli/` fatal path.
- Every blocking call takes `ctx`; retry sleeps select on `ctx.Done()`.

### 3.5 Extension collapse (biggest deletion win)

- Delete `plugin/wasm` + `plugin/exec` as separate systems → single
  `modules/exporter/exporter.go`: `type Exporter interface { Export(context.Context, *Response) error }`
  with an `exec:` subprocess impl. WASM support returns only when a second
  consumer exists (rule of three — today there is one `Exporter` use site).
- Delete `core/registry.go` semver gate (`CoreAPIVersion`, `parseSemver`,
  7 kind strings) → `module.go` (~60 LOC, flyscrape's shape).
- Delete `core.BrowserGate`, `crawl/backoff.go`, `crawl/ratelimit.go` →
  `TransportAdapter` chain ordered in ONE place (`moduleOrder`-like list in `magpie.go`).
- Merge `clean/llm.go + scope.go + quality.go + fallback.go + islands.go + metadata.go + markdown.go`
  (7 files) → `modules/clean/clean.go` + `modules/clean/sidecar.go` (2 files max).
  Scope/quality stay as pure functions, not packages.
- Merge `selector/cache.go + synth.go + heal.go + validate.go` → `modules/selector/selector.go`
  (cache open, lookup, null-rate check, re-synth trigger — one flow, one file).
- Merge `vertical/*.go` (12 files) → `modules/vertical/vertical.go` + per-site
  `OptIn` funcs in the SAME file (they're 30–60 LOC each; files add nothing).
  Verticals implement `extract.Extractor`; the `vertical.Fetcher` seam and
  `Result.Vertical` plumbing disappear.
- Collapse `extract/openai|anthropic|codex|zen` → shared `callJSON(ctx, client, endpoint, payload)`
  + 4 thin adapters. `coerce.go/cost.go/schema.go` stay (real logic).

### 3.6 Dependency audit (one job per dep)

| Keep | Why (feature that dies without it) |
|---|---|
| cobra, goquery/cascadia, trafilatura, html-to-markdown, rod, jsonschema, backoff, grobotstxt, x/sync, x/time, modernc/sqlite, MCP SDK, yaml | each owns exactly one user-visible feature |
| Question: `bloom/v3` | visited-set lives in sqlite already; bloom is a hot-path optimization — keep ONLY with a benchmark proving >20% crawl speedup, else delete |
| Question: `go-keyring` | used only for API-key storage — replace with env-file convention? Keep ONLY if keyring path is tested on all 3 OSes in CI |
| Delete candidate: second HTML stack | trafilatura already vendors readability-equivalent; drop `go-readability` indirect + any direct duplicate |
| Never add | JS runtime (goja/esbuild — flyscrape needs it for user scripts; we have YAML schemas + verticals instead; do NOT import their scripting model, only their module shape) |

### 3.7 Config collapse

- `config.Config` + `config.Flags` + `ApplyFlags` + per-command `*Options` structs →
  ONE `magpie.Options` (see §3.3). Cobra flags bind directly (`StringVar(&o.Render, ...)`).
  File+env loading is `LoadOptions(path) (Options, error)` — 60 LOC, no keyring unless §3.6 keeps it.
- `Options.Validate()` is called once, at `Engine.Run` entry. CLI/MCP do NOT pre-validate.
  Exit-code mapping (`fail(2,...)` usage errors vs `fail(7,...)` missing key vs `fail(8,...)` quality)
  lives in exactly one function (`cli/root.go: exitFor`, extended).

---

## 4. Migration plan (working code at every step, smallest diffs first)

| Step | Action | LOC delta | Verifies |
|---|---|---|---|
| M1 | Add `magpie.go` hook interfaces + `module.go` typed registry alongside `core/` (no deletion). Port ONE module (`retry`) as `AdaptTransport` proof. | +150 | `go build ./...`, new `magpie_test.go` |
| M2 | Collapse providers: shared `callJSON` + 4 adapters; no behavior change. | −300 | `go test ./extract/` goldens unchanged |
| M3 | Merge `clean/` 7 files → 2 files (pure moves, no logic change). | −0 moves, sets up −400 | `go test ./...`, `testdata/clean` drift EMPTY |
| M4 | Merge `selector/` 4 files → 1; `vertical/` 12 files → 1+registry. Wire verticals as `Extractor`s; delete `vertical.Fetcher` seam + `Result.Vertical`. | −400 | `go test ./...`, fixtures 15/15 |
| M5 | Single `Options.Validate()`: move switches out of `cli/*` + `mcp/*`; cobra binds directly. Delete `config.ApplyFlags`. | −250 | `cli/*_test.go` green, `magpie scrape --help` unchanged |
| M6 | One engine: re-implement `crawl.Run` + `scrape.Run` as `Engine.Run` over hooks; delete `core/pipeline.go`, `core.BrowserGate`, `crawl/backoff+ratelimit`. | −800 | full suite + `test -tags browser` + 3-way CGO cross-builds |
| M7 | Collapse `plugin/wasm+exec` → `modules/exporter`; delete `core/registry.go` semver gate. | −400 | `go test ./...`, `magpie build --help` |
| M8 | Dep audit (§3.6): drop losers, `go mod tidy`, re-run cross-builds. | −deps | `CGO_ENABLED=0` × 3 platforms |
| Total | | **≈ −2000 LOC + deleted pkgs**, toward ≤4.5k target | |

Each step: RED (new shape test) → GREEN → `go test ./... && go vet ./... && gofmt -l .` empty → commit.
Never mix moves with logic changes in one commit (review rule).

---

## 5. Definition of done

- [ ] `go build ./...`, `go test ./...` (304+ RUN lines), `go vet`, `gofmt -l .` empty, `golangci-lint` clean.
- [ ] CGO cross-builds (windows/amd64, linux/amd64, darwin/arm64) pass.
- [ ] `testdata/` goldens byte-identical (clean drift EMPTY, fixtures 15/15).
- [ ] No package > 400 non-test LOC; no file > 250 LOC without `ponytail:` waiver.
- [ ] `grep -r gomagpie/modules/ modules/ | grep import` shows zero cross-module imports.
- [ ] `grep -rn "os.Exit\|panic(" --include=*.go magpie.go module.go modules/` empty.
- [ ] CLI help text + exit codes (2/7/8) unchanged; MCP tool surface unchanged.
- [ ] Spec §13 file tree rewritten to the §3.1 layout.

## 6. Non-goals (explicit)

- No JS user-script runtime (flyscrape's superpower, but a new dep tree + sandbox for a feature nobody requested — YAGNI).
- No framework upgrades, no new output formats, no TUI (phase-4-tui is separate).
- No behavior change visible to `magpie scrape|crawl|extract` users except fewer bugs.
