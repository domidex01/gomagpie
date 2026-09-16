# Phase 3 — MCP server + plugin system

**Duration:** Weeks 5–6 (~34 hours)
**Depends on:** Phase 2 (lazy selector synthesis + `crawl.Run` pipeline + full `store` accessors + `cmd/magpie` with scrape/crawl/cache/config commands)
**Blocks:** Nothing (last milestone; spec §13 Milestone 3)
**Risk Level:** MEDIUM — two brand-new pinned deps, but both are stable with confirmed API snippets below (MCP Go SDK v1.8.x with official examples; wazero v1.12 already vendored as an indirect dep); WASM deny-behavior is tested deterministically with a hand-encoded fixture (no guest toolchain); exec/build are stdlib + Go-toolchain wrappers. No research gate — every DoD item is buildable.
**Stack:** `go` — NOTE (per `AGENTS.md`): phase-plan/run-phase skills only accept `python|nextjs|react|typescript`, so `run-phase` will hard-block on this file. Execute this phase manually via the Execution Prompt at the end.

> Section count: MEDIUM risk → 6 sections (no "What Failure Looks Like"; fallbacks live inline in Tasks 3.1/3.5/3.6).

---

## 1. Objective + What Success Looks Like

Build spec Milestone 3: expose the Phase 1–2 pipeline as an MCP server (`magpie serve` over stdio + Streamable HTTP with `scrape_url`, `crawl_site`, `extract_structured`, `get_cached_selectors`), add the Caddy-style compile-time module registry with a semver version gate, stream records through subprocess exporters over JSONL, compile custom static binaries via `magpie build --with`, and sandbox untrusted WASM plugins in wazero behind a hand-audited 4-function host surface. Ship MCP first, WASM last (spec Recommendation 4). This is the phase that makes gomagpie composable — Phases 1–2 proved extraction is cheap; Phase 3 proves anything (agents, pipelines, third-party code) can drive it.

1. [`go test ./mcp/ -v` exits 0: all four tools callable over `NewInMemoryTransports` with a fake `Extractor` + temp-file SQLite — no network, no keys]
2. [`magpie serve --transport http --addr 127.0.0.1:8089` starts; an HTTP POST to the handler returns 200 or 400 (never connection-refused) — proves the StreamableHTTP handler is live]
3. [`crawl_site` via MCP returns `{run_id, pages_crawled, records}`; re-invoking with that `run_id` returns status with **0 new extractor calls** (fake-extractor call counts assert it)]
4. [`crawl_site` with a progress token delivers ≥1 progress notification during a multi-page crawl (assert via `ProgressNotificationHandler` in test)]
5. [`go test ./plugin/... -v` exits 0: exec exporter round-trips records through a helper process; WASM happy-path + deny tests pass with a hand-encoded fixture (no guest toolchain)]
6. [`magpie build --output /tmp/magpie-test` with `GOPROXY=off` exits 0 and `/tmp/magpie-test --help` exits 0 (hermetic, warm module cache only)]
7. [`magpie build --with <local fixture module>` produces a binary whose run proves the fixture's `init()` registration executed (marker-file protocol, see Task 3.5)]
8. [`go test ./...` exits 0 (no network/browser/keys); `go vet` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean]
9. [`CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64]

---

## 2. Key Design Decisions

### Where Phase 3 plugs into the Phase 2 system

```
Claude Desktop / IDE / agent ──stdio──► magpie serve ──► mcp/server.go ──► scrape.Run ──► fetch→clean→extract (+cache)
         remote client ──HTTP──► StreamableHTTPHandler ─► mcp/tools.go ──► crawl.Run ──► frontier→pipeline→writer (+progress)
                                                                                    ──► extract pkg (no fetch)
                                                                                    ──► store.ListSelectors

third-party Go module ──► magpie build --with ──► temp module (replace→local root, blank imports)
                                                     ──► go build ──► magpie-custom (init() → core.RegisterModule)

records chan ──► --exporter-cmd prog ──► plugin/exec (JSONL on stdin, no shell)

.wasm bytes ──► plugin/wasm Runner ──► wazero runtime (compiled once, fresh instance per call)
                                          host module "gomagpie": log / get_input / set_output / config_get
                                          WASI with ZERO preopens (link-only, stdout→discard)
```

### Decisions (with spec section refs)

- **MCP first, WASM last; registry before both (spec Recommendation 4).** Task order is 3.1 deps → 3.2 registry → 3.3 MCP → 3.4 exec → 3.5 build → 3.6 WASM → 3.7 docs/matrix. The registry is load-bearing for `build --with` (init-registration target) and the WASM version gate, so it comes before either consumer.
- **New `scrape/` package extracted from `runScrape` — MCP and CLI share one function (spec §8.3).** `scrape_url` needs the exact fetch→clean→extract+cache-apply flow that lives in `cmd/magpie/scrape.go` (`package main`, unimportable). Duplicating ~60 lines across the CLI/MCP boundary recreates the drift class `shared.go:newExtractor` was built to kill. So: move the flow almost verbatim into `scrape/scrape.go` as `Run(ctx, Deps, rawURL, Options) (Result, error)` with sentinel errors (`ErrMissingKey`, `ErrCostCeiling` — same pattern as `crawl.ErrRobotsBlocked/ErrCostCeiling`); `cmd` maps sentinels to exit codes 7/6 and formats output with the existing `marshalOut`/`writeOut`. `mcp` calls `scrape.Run` directly. `newExtractor`, `checkCostCeiling`, `fetchBrowser`, `needsAPIKey` stay in `cmd`/`cli` and are injected as `Deps` func fields — `mcp` and `scrape` never import the CLI package (which would cycle once `serve` lives there).
- **`crawl_site` is synchronous + progress + `run_id` polling (spec §8.4).** No background-job machinery: the tool runs `crawl.Run` to completion in the call, emits `NotifyProgress` per page when the client sent a progress token, and returns `run_id` in the output. Re-invoking `crawl_site` with `run_id` set returns stored status (`run_history` + `CrawlStats`) without touching the extractor. This covers both halves of §8.4 with zero new tables and zero goroutine-lifecycle bugs. `ponytail:` no concurrent-crawl registry — two simultaneous `crawl_site` calls run independently (ceiling = duplicate work, never corrupt state; upgrade = a run-ID-keyed job table if agents actually overlap crawls).
- **Progress hook is one optional func on `crawl.Options` (spec §8.4).** `Progress func(done int)` (nil = off), invoked from the writer sink after each page's `MarkDone/MarkError`. Total is intentionally omitted — the frontier total is unknowable upfront; notifications carry `Progress: float64(done)`, no `Total`, message `"N pages"`. The tool maps each call to `req.Session.NotifyProgress` iff `req.Params.GetProgressToken() != nil` (confirmed pattern, Task 3.3 snippet).
- **Registry is kind-agnostic by deliberate deviation from spec §6.** Spec §6 lists seven Go interfaces (`Fetcher`, `Cleaner`, …) referencing stage types, but `core` already imports `fetch`+`clean` (pipeline.go) while every stage package would need to import `core` for `init()` self-registration — a direct import cycle. Resolving it "properly" (DTO types in core + adapters everywhere) is scaffolding for consumers that don't exist: exactly one implementation per kind ships in this phase. So `core/registry.go` stores `ModuleInfo{ID, APIVersion, Kind string, New func() any}` with the `Kind` string constants (`fetcher`, `cleaner`, `extractor`, `exporter`, `notifier`, `proxy`, `ratelimit`) and the semver gate (same major = accept, else loud reject). Kind-specific Go interfaces are declared at their single use site (`plugin/exec.Exporter`, `plugin/wasm.Runner.Transform`). Compile-time `--with` modules need only `core.ModuleInfo` + `RegisterModule` in `init()` (spec §6.3 pattern, snippet in Task 3.2). If a second consumer per kind ever appears, promote the use-site interface into core then (rule of three).
- **WASI is instantiated with ZERO preopened dirs — link-only sandbox (spec §7.1).** TinyGo/Rust guests import `wasi_snapshot_preview1` unconditionally, so refusing to instantiate WASI at all would brick the entire guest ecosystem. With no preopened FDs the filesystem is unreachable (`path_open` returns nonzero errno — asserted in test); stdout/stderr go to `io.Discard`. Env is left empty (assert `environ_sizes_get` returns 0/0). Sockets are denied by FD absence, not by missing imports: wazero's preview1 module DOES export `sock_accept`/`sock_recv`/`sock_send`/`sock_shutdown` by default (`wasi.go:exportFunctions`), so the deny test must import a REAL one (`sock_send`) and assert the call fails with nonzero errno (`EBADF` — no socket FDs are ever preopened), not that instantiation fails. (There is no `sock_open` in WASI snapshot-preview1 at all — a test importing it would pass vacuously.) `ponytail:` fd_write to stdout succeeds into the discard sink rather than trapping (ceiling = a noisy guest can burn a few bytes of discard; upgrade = deny-list fd 1/2 in the host shim if ever observed).
- **WASM data exchange needs no guest `malloc` — host-side bump allocator.** Host functions take numeric types only, so strings cross as `(ptr,len)` u32 pairs. The host `Grow`s the guest memory once per instance and bump-allocates regions for `get_input`/`config_get` return values; `set_output`/`log` read guest memory. Guests only implement imports + export `gomagpie_api_version() -> i32` (packed `major*10000+minor*100+patch`) and `run() -> i32` (0 = ok). Compile once (`CompileModule`, shared, safe for concurrent instantiation), **fresh instance per invocation** (spec §7 isolation). CPU bound = per-call `context.WithTimeout` passed to `Call` + watchdog `Close` on the instance at expiry; memory bound = `wazero.NewRuntimeConfig().WithMemoryLimitPages(MaxMemoryPages)` (knob confirmed on v1.12.0 in Task 3.1) with the bump-allocator cap as a second, independent floor.
- **`magpie build` requires moving the CLI into an importable package (spec §6.3).** A generated module cannot import `package main`, so the cobra tree must move: `cmd/magpie/*.go` (minus a 10-line `main.go` shim) → `cli/` package with exported `Execute() int`. The generated `main.go` is `package main` + blank `--with` imports + `cli.Execute()`, built in a temp module with `replace gomagpie => <abs repo root>`. `internal/` is unusable here (not importable cross-module) — `cli/` must be public. `AGENTS.md` structure line and the "no WASM until core stable" gotcha get one-line updates in the same task.
- **Exec exporters are stdlib-only, no shell, tee'd (spec §6.2).** `plugin/exec.Exporter{Cmd []string}` uses `exec.CommandContext`, JSON-encodes each `map[string]any` record to stdin as one JSONL line, captures stderr on failure, nonzero exit = loud error. Command words come from `strings.Fields` on the flag value (documented: no quoting — `ponytail:`, ceiling = paths with spaces need a wrapper script). The crawl writer tees records to the exporter **in addition to** `--out` (two lines at the sink call site, not a pipeline stage). Tests use the standard Go helper-process trick (`os.Args[0]` re-exec with `GO_WANT_HELPER_PROCESS`), which is hermetic and Windows-safe (no `cat`/shell dependency).
- **No new exit codes.** `serve` failures → 1, bad flags → 2 (cobra), missing key → 7 (via `ErrMissingKey`), cost ceiling → 6 (via `ErrCostCeiling`), exporter spawn failure → 1, `build` toolchain failure → 1. The full 0–7 matrix from Phase 2 is reused untouched.

### Data Model Strategy (Go — same rules as Phases 1–2)

| Layer | Pattern | Why |
|---|---|---|
| MCP tool In/Out (`ScrapeIn`, `ScrapeOut`, …) | Plain structs with `json` + `jsonschema` tags | `mcp.AddTool[In,Out]` infers input/output schemas from them via reflection (confirmed); `jsonschema:"…"` tags become property descriptions |
| `mcp.Deps`, `scrape.Deps`, `scrape.Options/Result` | Plain structs, func-field injection | `mcp`/`scrape` stay importable by both `cli` and tests; no CLI import, no cycle |
| `core.ModuleInfo`, `plugin/exec.Exporter`, `plugin/wasm.Runner` | Plain structs | Zero-cost, matches Phase 1–2 style |
| Hot path (per-record JSONL encode, per-call WASM instantiate) | `json.Encoder` reuse per stream; `CompileModule` once + `Instantiate` per call | Encode dominates exporter cost; compile dominates WASM cost — cache exactly those |
| Errors | Sentinels (`scrape.ErrMissingKey`, `crawl.ErrRobotsBlocked`) mapped to codes at the CLI edge; `fmt.Errorf("mcp: %w")` / `"wasm: %w"` / `"exec: %w"` inside | Same sentinel-at-edge pattern as `crawl.Run`; MCP tool handlers return Go errors (SDK surfaces them as tool errors) |

**Other critical rules:** no CGO ever (re-verify after `go get` — the MCP SDK pulls `google/jsonschema-go`, pure Go); no live-network/browser/LLM in default `go test ./...` (in-memory MCP transports + fake `Extractor` + helper-process + hand-encoded wasm fixtures); new `testdata/buildmod` gets its own `go.mod` so `./...` never builds it.

---

## 3. Tasks

### Task 3.1 — Module setup + pinned deps + API confirmation (2h)

Goal: `go.mod` gains exactly one module (wazero promotes to direct); all three target platforms still build with `CGO_ENABLED=0`; every load-bearing API below is re-confirmed on the pinned versions via `go doc`.

```bash
# Resolve exact versions first (spec's "v1.5.0 latest" is STALE — pkg.go.dev showed v1.8.0 sources, 2026-07-28 spec needs SDK v1.7.0+):
go list -m -versions github.com/modelcontextprotocol/go-sdk   # expect v1.8.x; take latest, minimum v1.7.0
go get github.com/modelcontextprotocol/go-sdk@v1.8.x          # EXACT version from the listing
go get github.com/tetratelabs/wazero@v1.12.x                  # promotes existing indirect v1.12.0 to direct
go mod tidy && go build ./...
# Confirm each of these on the pinned tree before writing any consumer code:
go doc github.com/modelcontextprotocol/go-sdk/mcp.StreamableHTTPOptions   # field name for stateless?
go doc github.com/tetratelabs/wazero.RuntimeConfig                        # page/memory-limit knob name?
go doc github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1       # instantiate helper name?
# If anything shows up in go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./... stop and flag.
```

Fallbacks (do not block on them — record the `go doc` result in a code comment and move on): stateless field absent → pass `nil` opts (stateful handler; note in `--help` that HTTP mode needs sticky sessions); no SDK page-limit knob → the allocator cap in Task 3.6 is the sole memory bound (it is sufficient alone); no convenient WASI helper → guests are restricted to the `gomagpie` host module only and TinyGo support becomes a documented follow-up (fixture tests don't need WASI).

**Sanity check:** `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK`

### Task 3.2 — `core/registry.go`: module registry + semver gate (3h)

Goal: third-party compile-time modules can `init()`-register; incompatible majors are rejected loudly; duplicate IDs are rejected loudly.

```go
// Confirmed pattern to mirror (spec §6.3, Caddy model):
package rodfetcher // example third-party module (in its own repo, NOT this task)

import "gomagpie/core"

func init() {
    core.MustRegister(core.ModuleInfo{
        ID:         "fetcher.rod",
        APIVersion: "1.0.0",
        Kind:       core.KindFetcher,
        New:        func() any { return new(RodFetcher) },
    })
}
```

Implementation notes (`core/registry.go`, ~100 lines, zero imports of stage packages — see §2 "kind-agnostic" decision): `const CoreAPIVersion = "1.0.0"`; `type ModuleInfo struct { ID, APIVersion, Kind string; New func() any }`; `KindFetcher/KindCleaner/KindExtractor/KindExporter/KindNotifier/KindProxy/KindRateLimit` string consts; `RegisterModule(info) error` (rejects empty ID, empty version, unparsable semver, duplicate ID, and major != core major — parse with `strings.SplitN(v,".",3)`, no new dep for semver); `MustRegister` (panics — init-time misuse must crash, never silently skip); `Lookup(id) (ModuleInfo, bool)`; `ListModules() []ModuleInfo` (sorted by ID, defensive copy); mutex-guarded map. Deliberate departure from spec §6.3's sketch (`core.RegisterModule(RodFetcher{})` + `GomagpieModule()` method): a plain `ModuleInfo` value argument is one line at the call site and needs no interface — do NOT "fix" it back to the method form. Tests (`core/registry_test.go`): same-major-minor-skew accepted, different-major rejected, duplicate rejected, lookup-miss returns false, list sorted. No other file changes in this task.

**Sanity check:** `go test ./core/ -v` — registry tests pass alongside the existing pipeline tests

### Task 3.3 — `scrape/` + `mcp/`: shared single-URL flow, four tools, `serve` (10h)

Goal: `magpie serve` exposes the full pipeline over stdio and Streamable HTTP; CLI `scrape` behavior is byte-identical before/after (its e2e tests must pass unmodified).

```go
// Confirmed via official go-sdk README + examples/http/main.go (2026-09-16) — use exactly these:
server := mcp.NewServer(&mcp.Implementation{Name: "gomagpie", Version: "v1.0.0"}, nil)
type ScrapeIn struct {
    URL     string         `json:"url" jsonschema:"absolute http(s) or file URL to scrape"`
    Schema  map[string]any `json:"schema,omitempty" jsonschema:"JSON Schema object; omit for cleaned markdown only"`
    Render  string         `json:"render,omitempty" jsonschema:"auto, static, or browser"`
    UseCache *bool         `json:"use_cache,omitempty" jsonschema:"apply cached selectors when available"`
}
type ScrapeOut struct {
    URL string `json:"url"`; FinalURL string `json:"final_url"`; Title string `json:"title"`
    Markdown string `json:"markdown,omitempty"`; Extracted map[string]any `json:"extracted,omitempty"`
    FromCache bool `json:"from_cache"`; Usage map[string]any `json:"usage,omitempty"`
}
mcp.AddTool(server, &mcp.Tool{Name: "scrape_url", Description: "Fetch, clean and extract one URL"}, handleScrape)
// handleScrape signature: func(ctx context.Context, req *mcp.CallToolRequest, in ScrapeIn) (*mcp.CallToolResult, ScrapeOut, error)
// In must be struct/map (or any); nil *CallToolResult + typed Out is the documented pattern (README SayHi returns nil, Output{...}, nil).
// Streamable HTTP (examples/http/main.go):
handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server { return server }, opts)
http.ListenAndServe(addr, handler)
// stdio: server.Run(ctx, &mcp.StdioTransport{})
// Progress (SDK Progress example):
if token := req.Params.GetProgressToken(); token != nil {
    _ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
        ProgressToken: token, Progress: float64(done), Message: "N pages",
    })
}
// Tests (SDK examples pattern):
ct, st := mcp.NewInMemoryTransports()
_, _ = srv.Connect(ctx, st, nil)
cs, _ := client.Connect(ctx, ct, nil)
res, _ := cs.CallTool(ctx, &mcp.CallToolParams{Name: "scrape_url", Arguments: map[string]any{"url": ...}})
```

Implementation notes: **step 1** — create `scrape/scrape.go` by moving the body of `runScrape` (fetch → clean → schema/no-schema → cache-apply → ceiling → extract) into `func Run(ctx context.Context, d Deps, rawURL string, o Options) (Result, error)` where `Deps{DB *store.DB, ExtractorFor func(provider, key, model string, sch *extract.Schema, runID string) (extract.Extractor, error), APIKeyFor func(provider string) string}` and `Options{Schema *extract.Schema (nil = markdown only), Render, Provider, Model string, MaxCost float64, UseCache bool}`; sentinel errors `ErrMissingKey` (cmd maps to 7) and reuse `crawl.ErrCostCeiling` (cmd maps to 6 — do NOT define a second ceiling sentinel). `cmd/magpie/scrape.go` shrinks to: resolve config → `extract.LoadSchema` (path handling stays in cmd — MCP passes schema bytes via `extract.ParseSchema`) → build `Deps` from `newExtractor`/`cfg.APIKey` → `scrape.Run` → existing `marshalOut`/`writeOut` formatting (keep `markdownOut`/`extractedOut`/`usageOut` exactly where they are). **Step 2** — `mcp/server.go`: `type Deps struct { DB *store.DB; ScrapeDeps scrape.Deps; DefaultProvider, DefaultModel string; MaxCost float64 }`; `func NewServer(d Deps) *mcp.Server` registering all four tools. `mcp/tools.go`: the four handlers. `scrape_url`: schema map → `json.Marshal` → `extract.ParseSchema` (nil map = markdown-only) → `scrape.Run`. `extract_structured`: content+schema → same extract path as the `extract` CLI command (read `cmd/magpie/extract.go`, reuse its core lines; content_type html → run `clean.Clean` first, markdown → direct). `get_cached_selectors`: `store.ListSelectors(domain)` (+ optional schema_hash filter in the handler, not the store) → Out `{Domain, SchemaHash, Fields map[string]any, SynthesizedAt string, NullRates map[string]float64}` by unmarshalling each `fields_json` doc (reuse `selector.SelectorDoc`). `crawl_site`: In `{URL, MaxPages, MaxDepth, SameHost, Schema map?, RunID?}`; if `RunID != ""` → status path only: new `store.GetRun(runID)` (add it — one `SELECT status,pages_ok,pages_err,prompt_tokens,completion_tokens,usd_estimate FROM run_history`; unknown id → loud error) + `CrawlStats` → Out `{RunID, PagesCrawled, Records: pages_ok, Errors: pages_err, Usage, Status}` with **zero** extractor involvement. Else build `crawl.Options` (fake-friendly: `Extractor` + `Propose` from Deps, `Format: "jsonl"`, `Out: os.DevNull` — REQUIRED, never `""`: `newWriter` maps empty Out to `os.Stdout`, which would corrupt the stdio transport's JSON-RPC framing; records are captured via `OnRecord`, the file is discarded. Add a minimal `OnRecord func(map[string]any)` hook invoked per record in the sink (nil = off, 3 lines)). Progress: pass `Progress: func(done int)` → NotifyProgress when token present. **Step 3** — `crawl/crawl.go`: add `Progress func(done int)` field + `OnRecord func(map[string]any)` field (both nil-safe, invoked in the existing sink goroutine — no pipeline changes). **Step 4** — `cmd/magpie/serve.go`: `serve --transport stdio|http --addr :8080`, config keys `serve_transport`/`serve_addr` + `GOMAGPIE_SERVE_TRANSPORT`/`GOMAGPIE_SERVE_ADDR` env (extend `config.Config`, `Flags`, overlays — follow the existing pattern exactly); transport switch: stdio → `srv.Run(ctx, &mcp.StdioTransport{})`, http → `ListenAndServe(addr, NewStreamableHTTPHandler(...))`; root ctx = `signal.NotifyContext` (same as crawl). `serve --help` must state that `crawl_site` runs synchronously to completion (no background jobs) and note the client-timeout expectation for huge crawls (keep `MaxPages` bounded or use HTTP mode). **Step 5** — tests `mcp/server_test.go` (httptest origin + fake extractor + temp DB): all four tools green; `crawl_site` run_id status path asserts extractor call count unchanged; progress test uses `CallToolParams{Meta: mcp.Meta{"progressToken": "p1"}}` (confirmed pattern from the Progress example) with a `ProgressNotificationHandler` capturing ≥1 notification on a 3-page origin. Add a stdout-clean assertion around the `crawl_site` call (capture os.Stdout: it must stay empty — regression guard for the `Out: os.DevNull` requirement above). Existing `cmd` scrape tests must pass unmodified (proves byte-identical CLI behavior).

**Sanity check:** `go test ./mcp/ ./crawl/ -v` — tools + hooks green, crawl suite unbroken

### Task 3.4 — `plugin/exec/exporter.go`: subprocess exporters + crawl tee (4h)

Goal: records stream as JSONL to any program's stdin; failures are loud and attributed.

```go
// Spec §6.2(3): "JSON over stdin/stdout, one record per line." Stdlib only.
type Exporter struct {
    Cmd []string // argv; Cmd[0] resolved via exec.LookPath
}
func (e *Exporter) Export(ctx context.Context, records <-chan map[string]any) error {
    cmd := exec.CommandContext(ctx, e.Cmd[0], e.Cmd[1:]...)
    stdin, _ := cmd.StdinPipe()
    cmd.Stderr = &stderrBuf // captured, quoted on failure
    if err := cmd.Start(); err != nil { return fmt.Errorf("exec: start %q: %w", e.Cmd[0], err) }
    enc := json.NewEncoder(stdin)
    for r := range records {
        if err := enc.Encode(r); err != nil { _ = cmd.Process.Kill(); return ... }
    }
    stdin.Close()
    if err := cmd.Wait(); err != nil { return fmt.Errorf("exec: %q: %w: %s", ..., stderrBuf.String()) }
    return nil
}
```

Implementation notes: new dir `plugin/exec/` (~80 lines + test). Crawl wiring: `--exporter-cmd` string flag on `crawl` (+ `exporter_cmd` config key + `GOMAGPIE_EXPORTER_CMD` env): when set, the sink goroutine tees each record to an exporter channel consumed by `Exporter.Export` in a separate goroutine; exporter error → page-level warning on stderr + exit code 4 path (partial) if records were otherwise fine — never silently dropped, never stage-fatal mid-run (the records are already in `--out`/SQLite; the exporter is a copy). `ponytail:` argv = `strings.Fields(flag)` — no quoting (ceiling = paths with spaces need a wrapper script; document in `--help`). Tests: helper-process pattern — `TestHelperExporterProcess` (guarded by `GO_WANT_HELPER_PROCESS=1`) reads stdin fully, appends to `$MAGPIE_TEST_OUT`; `TestExport_JSONLRoundTrip` runs `Export` against `os.Args[0]` re-exec and asserts line-delimited JSON equality; failure test asserts the error quotes stderr and names the program. Crawl e2e: `--exporter-cmd` pointing at the test helper via a `file://` site asserts both the `.jsonl` and the export file match.

**Sanity check:** `go test ./plugin/exec/ -v` — round-trip + failure attribution green on this box

### Task 3.5 — `cli/` move + `build/`: xcaddy-style custom binaries (5h)

Goal: `magpie build [--with module@version…] --output <file>` emits a working static binary; the default `magpie` binary is behavior-identical after the move.

Step 1 — the move (mechanical, no logic changes): `git mv cmd/magpie/{cache_cmd,config_cmd,crawl,extract,scrape,scrape_test,shared,cmd_test,serve}.go cli/` (whatever `.go` files exist except `main.go`), change their package decl to `cli`, add `func Execute() int` (the current `main()` body: `rootCmd().Execute()` → exit code via `exitFor`) + keep `rootCmd()` unexported. `cmd/magpie/main.go` becomes the 10-line shim (`package main; import "gomagpie/cli"; func main(){ os.Exit(cli.Execute()) }`). Update `AGENTS.md` structure line (`cli/ # Cobra tree (importable; cmd/magpie is a shim)`) and delete the "No plugins/WASM until core stable" gotcha (this phase IS the unlock). Run the full suite — everything must pass unmodified apart from package names.

Step 2 — `build/build.go` (~120 lines):

```go
// Generated module (text/template, golden-tested):
//   package main
//   import (_ "with1" _ "with2" "gomagpie/cli")
//   func main() { os.Exit(cli.Execute()) }
// Build(ctx, BuildOptions{With []string, Output string}) error:
//   dir = os.MkdirTemp; go mod init magpie-custom; go mod edit -require=gomagpie@v0.0.0 -replace=gomagpie=<abs repo root>
//   for each --with: go get module@version   (network; the ONLY non-hermetic path — document it)
//   write main.go; go build -o <abs Output> .
```

`--with` validation: must contain `@` (loud error otherwise — a bare path silently resolves to `latest`, which is never what a reproducible build wants); each `go` invocation runs with `cmd.Dir = dir`, combined output captured and quoted on failure. `cmd/magpie`… rather `cli/build_cmd.go`: `build --with (repeatable) --output (required)`. Tests: golden test on the rendered `main.go` (zero `--with` + two `--with`); hermetic integration test with `GOPROXY=off GOFLAGS=-mod=mod GOSUMDB=off`: `build.Build` copies the repo root's `go.sum` into the temp module first (the generated module shares the full transitive closure — without it the build fails on missing hashes even with a warm cache); zero-`--with` build → run `<bin> --help` → exit 0; fixture module `testdata/buildmod/` (own `go.mod`, `module buildmod`, `package buildmod`, `init()` writing `$MAGPIE_BUILD_TEST_MARKER` via `os.AppendFile` + `core.MustRegister(...)`) pulled in via `--with buildmod@v0.0.0` + `-replace=buildmod=<abs testdata/buildmod>` (pass-through `--replace` support? minimal: `build` auto-adds replaces only for gomagpie itself; the TEST invokes `build.Build` with an extra `Replaces map[string]string` field — 3 lines, no CLI surface). Assert marker file exists after running the built binary's `--help`. `testdata/buildmod` is excluded from `./...` by its own `go.mod`.

**Sanity check:** `GOPROXY=off go test ./build/ -v` (proves hermeticity) && `go test ./cli/ -run TestCrawl_FileSiteExit0 -v` (proves the move)

### Task 3.6 — `plugin/wasm/host.go`: wazero sandbox (7h, LAST)

Goal: untrusted `.wasm` transforms records through exactly four host functions; everything else traps or fails to instantiate.

```go
// Confirmed via wazero import-go example (2026-09-16) — host-module pattern:
r := wazero.NewRuntime(ctx)
defer r.Close(ctx)
_, err := r.NewHostModuleBuilder("gomagpie").
    NewFunctionBuilder().
    WithFunc(func(level uint32, ptr uint32, length uint32) { /* read guest mem, log */ }).
    Export("gomagpie_log").
    Instantiate(ctx)
// Guest side: (module (import "gomagpie" "gomagpie_log" (func (param i32 i32 i32)))) etc.
// Call: mod.ExportedFunction("run").Call(ctx)
// Mem: mod.Memory().Read(ptr, length), mod.Memory().Write(ptr, b), mod.Memory().Grow(pages)
// WithFunc signatures are constrained to numeric types — strings cross as (ptr,len) u32 pairs.
```

ABI contract (document verbatim atop `host.go` — guests are built against this, not against Go source): host module `"gomagpie"` exports `gomagpie_log(level:i32, ptr:i32, len:i32)`, `gomagpie_get_input() -> (ptr:i32, len:i32)` (host bump-allocates + writes the input record JSON), `gomagpie_set_output(ptr:i32, len:i32)` (host reads + stores), `gomagpie_config_get(kptr:i32, klen:i32) -> (vptr:i32, vlen:i32)` (whitelisted keys only; miss → `(0,0)`); guest exports `gomagpie_api_version() -> i32` = `major*10000+minor*100+patch` and `run() -> i32` (`0` = ok, else error). `Runner{ Compiled wazero.CompiledModule; Config map[string]string; MaxMemoryPages (default 64 = 4 MiB); Timeout (default 10s) }`; `NewRunner(ctx, wasmBytes)` compiles once (missing `gomagpie_api_version` export → loud "not a gomagpie plugin" error; major mismatch vs `core.CoreAPIVersion` → loud reject — the WASM half of the version gate); `Transform(ctx, input []byte) ([]byte, error)` instantiates fresh (spec §7 isolation), seeds input/config, calls `run` with timeout ctx + `Close`-watchdog, returns stored output (empty output + code 0 → return input unchanged? NO — fail loud: a transform that sets nothing is a guest bug, return error "run returned 0 without set_output"). WASI: instantiate `wasi_snapshot_preview1` with **no dir preopens**, empty env, stdout/stderr → `io.Discard` (helper name confirmed in Task 3.1; rationale comment cites §2). Memory cap: `WithMemoryLimitPages(MaxMemoryPages)` (confirmed on v1.12.0) plus the bump-allocator cap as an independent floor — comment says both are active. `ponytail:` timeout enforcement is watchdog-`Close` (ceiling = a pathological guest may survive until the runtime closes with it; upgrade = per-call runtime with shared `CompilationCache` if ever observed).

Tests (`plugin/wasm/host_test.go`, ZERO toolchain — fixtures are hand-encoded binaries from a ~60-line `encodeModule` test helper assembling magic+version+type/import/function/export/code sections): (a) happy path — guest calls `gomagpie_log` then `gomagpie_set_output` on bytes it holds in a data section, returns 0 → assert output bytes + captured log; (b) version gate — `gomagpie_api_version` returning `2*10000` → `NewRunner` rejects; (c) socket deny — guest importing the REAL `wasi_snapshot_preview1.sock_send` instantiates fine but calling it returns nonzero errno (no socket FDs are ever preopened — assert the errno, that is the actual sandbox mechanism); (d) env deny — guest calling `environ_sizes_get` then host reads back `(0,0)` from guest memory; (e) filesystem deny — guest calling `path_open` on a data-section path gets nonzero errno and no file appears on disk; (f) pre-canceled ctx → `Transform` errors (no hang risk by construction). Spec §12's "denied host functions truly unreachable" is (c)+(d)+(e).

**Sanity check:** `go test ./plugin/wasm/ -v` — six tests green, `ls` shows no stray files (deny tests must not create any)

### Task 3.7 — Docs, config-surface audit, final matrix (3h)

Goal: a stranger can drive gomagpie from an agent in ten minutes; the full Phase 3 DoD is demonstrated in one pass.

- `config show` includes `serve_transport`/`serve_addr` (+ `exporter_cmd` if Task 3.4 added the key) — redaction N/A (no secrets).
- README: append "MCP server" section — what it is (one paragraph), stdio client-config JSON snippet for Claude Desktop (`{ "mcpServers": { "gomagpie": { "command": "magpie", "args": ["serve"] } } }`), the four tool names in one line each, HTTP mode one-liner, pointer to `magpie serve --help`. Plus a "Custom builds" subsection (one `magpie build --with` example) and a "WASM plugins" subsection (ABI contract pointer to `plugin/wasm/host.go`, TinyGo note from Task 3.6's WASI decision). No more than ~60 lines — README is an index, not a manual.
- `magpie serve --help` documents the stateless/stateful outcome recorded in Task 3.1 (one line, no hedging: state what the flag DOES on the pinned version).
- Final matrix, in order, all green: `go build ./...` → `go test ./...` → `go test ./mcp/ ./plugin/... ./core/ ./build/ -v` (already covered, but run explicitly for the record) → `go vet ./...` → `gofmt -l .` (empty) → `golangci-lint run ./...` → three `CGO_ENABLED=0` cross-builds → manual smoke: `serve` stdio answers `tools/list` (pipe printf JSON-RPC), HTTP mode binds (curl returns 200/400, never 000).

**Sanity check:** the exact command sequence above, pasted into a fresh shell, all green

---

## 4. Deliverables

```
gomagpie/
├── cli/                         # MOVED from cmd/magpie (Task 3.5 step 1); package cli, Execute() int
│   ├── scrape.go                # thinned to config→scrape.Run→marshalOut/writeOut (Task 3.3)
│   ├── serve.go                 # NEW serve --transport stdio|http --addr (Task 3.3)
│   └── build_cmd.go             # NEW build --with --output (Task 3.5)
├── cmd/magpie/
│   └── main.go                  # 10-line shim: os.Exit(cli.Execute()) (Task 3.5)
├── core/
│   ├── pipeline.go              # untouched (Phase 2)
│   ├── registry.go              # NEW RegisterModule/MustRegister/Lookup/ListModules + semver gate (Task 3.2)
│   └── registry_test.go         # gate matrix: skew-accept / major-reject / dup-reject (Task 3.2)
├── scrape/
│   ├── scrape.go                # NEW Run(ctx, Deps, url, Options): fetch→clean→cache→ceiling→extract (Task 3.3)
│   └── scrape_test.go           # moved CLI-equivalence cases (httptest + fake extractor) (Task 3.3)
├── mcp/
│   ├── server.go                # NEW NewServer(Deps): four AddTool registrations (Task 3.3)
│   ├── tools.go                 # NEW In/Out structs + scrape/crawl/extract/selectors handlers (Task 3.3)
│   └── server_test.go           # NEW in-memory-transport tests: 4 tools + run_id + progress (Task 3.3)
├── crawl/
│   └── crawl.go                 # +Progress func(done int) +OnRecord func(map) hooks, nil-safe (Task 3.3)
├── store/
│   └── sqlite.go                # +GetRun(runID) status read (Task 3.3; extend in place)
├── config/
│   └── config.go                # +serve_transport/serve_addr (+exporter_cmd) keys, env, flags-struct (Tasks 3.3/3.4)
├── plugin/
│   ├── exec/
│   │   ├── exporter.go          # NEW stdlib JSONL subprocess exporter (Task 3.4)
│   │   └── exporter_test.go     # NEW helper-process round-trip + failure attribution (Task 3.4)
│   └── wasm/
│       ├── host.go              # NEW wazero Runner: 4-fn host surface, ABI doc, gates (Task 3.6)
│       └── host_test.go         # NEW hand-encoded fixtures: happy/versions/sock/env/fs/ctx (Task 3.6)
├── build/
│   ├── build.go                 # NEW temp-module codegen + go-build wrapper (Task 3.5)
│   └── build_test.go            # NEW main.go goldens + GOPROXY=off hermetic builds (Task 3.5)
├── testdata/
│   └── buildmod/                # NEW fixture module (own go.mod): init registers + marker (Task 3.5)
├── go.mod / go.sum              # +modelcontextprotocol/go-sdk v1.8.x (exact), wazero v1.12.x → direct
├── README.md                    # +MCP server / custom builds / WASM subsections (~60 lines) (Task 3.7)
├── AGENTS.md                    # structure line + gotcha update, one-line edits (Task 3.5)
└── plan/phase-3.md              # this file
```

---

## 5. Exit Criteria

- [ ] `go test ./mcp/ -v` exits 0: `scrape_url` (schema + markdown-only), `crawl_site`, `extract_structured`, `get_cached_selectors` all green over in-memory transports (mcp/server_test.go)
- [ ] `crawl_site` re-invoked with its returned `run_id` reports status with 0 new extractor calls (fake-extractor counts; store/GetRun + CrawlStats)
- [ ] `crawl_site` with `progressToken` delivers ≥1 progress notification on a 3-page origin (ProgressNotificationHandler capture)
- [ ] `cmd`/`cli` scrape e2e tests pass unmodified after the `scrape/` extraction (byte-identical CLI behavior)
- [ ] `magpie serve --transport http --addr 127.0.0.1:8089` binds (curl returns 200/400, never 000); stdio mode answers `tools/list` naming all four tools
- [ ] `go test ./plugin/exec/ -v` exits 0 AND crawl `--exporter-cmd` e2e produces byte-identical `.jsonl` + export files (helper-process, no shell)
- [ ] `GOPROXY=off go test ./build/ -v` exits 0: main.go goldens + hermetic zero-`--with` build (`--help` exits 0) + fixture-module marker proves `init()` registration through a real `magpie build --with`
- [ ] `go test ./plugin/wasm/ -v` exits 0: happy-path round-trip, major-mismatch reject, `sock_send` call returns nonzero errno with no disk side effects, empty environ, `path_open` nonzero errno, pre-canceled ctx errors
- [ ] `go test ./core/ -v` exits 0: semver gate matrix (skew-accept, major-reject, dup-reject, sorted list)
- [ ] `go test ./...` exits 0 with no network/browser/keys in the default suite; `go vet` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean
- [ ] `CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64; `go list -deps` shows zero `CgoFiles`
- [ ] README MCP/custom-builds/WASM subsections exist; `config show` prints the new keys; `serve --help` states the stateful/stateless behavior on the pinned SDK

---

## 6. Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are implementing Phase 3 of `gomagpie` (module `gomagpie`) — MCP server + plugin system. Repo: `/home/domidex/projects/gomagpie`. Source of truth: `spec.md` (§6 plugin interfaces, §7 wazero, §8 MCP, §9 storage, §10 CLI — re-read all five) plus `plan/phase-2.md` (what exists; its execution prompt's "Established" + "Confirmed APIs" sections are still accurate — trust them over re-deriving). Conventions: `AGENTS.md` + `.pi/rules/go.md` + `.pi/rules/testing.md` (read all three before writing code).

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`): static fetch → JS detection → optional go-rod browser → trafilatura → markdown + JSON-LD sidecar → LLM structured extraction → selector cache → crawl pipeline. Pure-Go, zero CGO, `CGO_ENABLED=0` cross-builds for windows/amd64, linux/amd64, darwin/arm64. Phases 1–2 built the pipeline; this phase (spec Milestone 3) exposes it over MCP, adds the module registry, subprocess exporters, custom-binary builds, and the WASM sandbox. Ship MCP first, WASM last. There is no Phase 4.

### Established in Prior Phases
- `cmd/magpie` is `package main`: `main.go` (rootCmd, resolveConfig, fail/exitFor codes 1/2/3/4/5/6/7), `scrape.go` (`runScrape`, `fetchURL`, `selectorApply`, `domainOfURL`, `markdownDoc/extractedDoc`), `shared.go` (`newExtractor` 7-provider switch + `ProviderHelp`, `checkCostCeiling`→exit 6, `fetchBrowser` rod lifecycle, `needsAPIKey`, `uuidNew`, `marshalOut/writeOut`, `extractedOut/markdownOut/usageOut`), `crawl.go` (flags → `crawl.Run`), `cache_cmd.go`, `config_cmd.go`, `extract.go` (stdin/file extract — read it for the `extract_structured` tool), `cmd_test.go` (httptest + file:// e2e, fake providers — MUST pass unmodified after your refactor).
- `crawl.Run(ctx, Options) (Result, error)` with `Options{SeedURL, Schema, MaxPages, MaxDepth, SameHost, FetchWorkers, Rate, Format, Out, RunID, Resume, ResumeID, IgnoreRobots, Provider, Model, MaxCost, DB, Extractor, Propose}`; sentinels `ErrRobotsBlocked`, `ErrCostCeiling`; note `newWriter` maps `Out == ""` to `os.Stdout` — the MCP `crawl_site` handler must pass `os.DevNull`, never `""`.
- `store`: `Open` (single writer, WAL), `BeginRun/FinishRun`, `LogLLMCall`, `RunCost`, `LLMCallCount`, `Get/Put/Delete/ListSelectors`, `Enqueue/Claim/MarkDone/MarkError/CrawlStats`, `ResumeRun/ResetInflight`, `Seen/LoadHashes`, `InsertRecord`; `run_history(run_id,command,started_at,finished_at,pages_ok,pages_err,prompt_tokens,completion_tokens,usd_estimate,status)`.
- `extract`: `Extractor` interface (`Extract(ctx, ExtractInput)(ExtractResult,error)` + `Name()`), `ExtractResult{Record, Provider, Model, Usage, Attempts}`, `LoadSchema(path)` / `ParseSchema(bytes)`, `Schema.Validate`, `NewOpenAI/NewAnthropic` (+`Log` field), codex/zen via `shared.go:newExtractor` (reuse it via injection, never reimplement the switch).
- `fetch`: `NewStaticFetcher`, `Fetch(ctx, FetchRequest{URL})`, `NewRodFetcher` (+`Close`), `ScoreJSRequired` + `NeedsBrowser(score)=score>=2`; `clean.Clean(ctx, RawPage{HTML,URL,FinalURL})` + sidecar harvest; `selector.SchemaHash(sch)`, `selector.NewApplier(doc, sch).Apply(html, sidecar)`.
- `config.Config`: flags > `GOMAGPIE_` env > file > defaults; `APIKey(provider)`, `SetKey`, `Redacted`; extend with new keys following the exact `overlay/overlayEnv/ApplyFlags/Flags` pattern.
- `core/pipeline.go`: `Run`, `FetchTask/FetchedPage/Cleaned/PageResult`, `PipelineConfig`, `BrowserGate` — untouched this phase.
- Tests are hermetic: `httptest` origins, temp-file real SQLite, fake `Extractor` closures, `//go:build browser` for rod. Never break this.

### Data Model Rules (follow exactly)
- MCP tool In/Out are plain structs with `json` + `jsonschema:"description"` tags (`AddTool` infers schemas from them; In must be struct/map, never a bare scalar).
- `mcp.Deps`, `scrape.Deps/Options/Result` are plain structs with func-field injection (`ExtractorFor`, `APIKeyFor`) — `mcp`/`scrape` must never import the CLI package.
- `core.ModuleInfo`, `exec.Exporter`, `wasm.Runner` are plain structs. No ORM, no codegen except the `build` main.go text/template (golden-tested).
- Hot path: one `json.Encoder` per export stream; `CompileModule` once + fresh `Instantiate` per WASM call; ONE goquery parse per page (unchanged).
- Errors: sentinels at the edge (`scrape.ErrMissingKey`→7, `crawl.ErrCostCeiling`→6, both mapped in CLI code only); everywhere else `fmt.Errorf("<pkg>: %w", err)`; MCP handlers return Go errors (SDK surfaces them).
- Deliberate shortcuts get a `ponytail:` comment (argv splitting, no concurrent-crawl registry, progress without totals, fd 1/2 → discard, watchdog-Close CPU bound, allocator memory floor).

### Architecture
Seed/clients → `serve` (stdio `server.Run` / HTTP `NewStreamableHTTPHandler` + `ListenAndServe`) → `mcp/server.go NewServer` (four `AddTool`s) → `mcp/tools.go` handlers → `scrape.Run` (single URL) / `crawl.Run` + `Progress`/`OnRecord` hooks (crawl_site) / extract pkg (extract_structured) / `store.ListSelectors` (get_cached_selectors). `run_id` re-invocation → `store.GetRun` + `CrawlStats`, zero extractor calls. Records → `--exporter-cmd` tee → `plugin/exec` JSONL subprocess (no shell). `magpie build` codegens a temp module (`replace gomagpie => repo root`, blank `--with` imports, `cli.Execute()`) and runs `go build`. `.wasm` → `plugin/wasm.Runner` (compile once, fresh instance per call, 4-fn `gomagpie` host module, WASI zero-preopen link-only, version gate vs `core.CoreAPIVersion`).

### Confirmed Library APIs (verified 2026-09-16 via official README, examples/http, import-go example, pkg.go.dev — re-confirm exact version + the three `go doc` items in Task 3.1)
```go
// modelcontextprotocol/go-sdk v1.8.x (minimum v1.7.0 for spec 2026-07-28; spec's "v1.5.0" note is stale)
server := mcp.NewServer(&mcp.Implementation{Name: "gomagpie", Version: "v1.0.0"}, nil)
type MyIn struct {
    URL string `json:"url" jsonschema:"absolute http(s) or file URL to scrape"`
}
type MyOut struct {
    URL string `json:"url" jsonschema:"echoed URL"`
}
mcp.AddTool(server, &mcp.Tool{Name: "scrape_url", Description: "Fetch, clean and extract one URL"},
    func(ctx context.Context, req *mcp.CallToolRequest, in MyIn) (*mcp.CallToolResult, MyOut, error) {
        return nil, MyOut{URL: in.URL}, nil // nil result + typed Out is the documented pattern
    })
if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil { log.Fatal(err) }
handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server { return server }, opts)
http.ListenAndServe(addr, handler) // opts = &mcp.StreamableHTTPOptions{Stateless: true} IF go doc confirms the field, else nil
// Progress: if token := req.Params.GetProgressToken(); token != nil {
//     _ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(done), Message: "N pages"}) }
// Tests: ct, st := mcp.NewInMemoryTransports(); _, _ = srv.Connect(ctx, st, nil)
//        cs, _ := client.Connect(ctx, ct, nil); res, _ := cs.CallTool(ctx, &mcp.CallToolParams{Name: "x", Arguments: map[string]any{...}})
//        progressToken in tests: &mcp.CallToolParams{Name: "x", Meta: mcp.Meta{"progressToken": "p1"}}

// wazero v1.12.x (already indirect v1.12.0 in go.mod — promote to direct)
r := wazero.NewRuntime(ctx); defer r.Close(ctx)
_, err := r.NewHostModuleBuilder("gomagpie").NewFunctionBuilder().
    WithFunc(func(level, ptr, length uint32) { /* ... */ }).Export("gomagpie_log").Instantiate(ctx)
// WithFunc takes numeric types ONLY — strings cross as (ptr,len) u32 pairs via mod.Memory().Read/Write/Grow.
// mod, err := r.Instantiate(ctx, wasmBytes) (or CompileModule once + InstantiateModule per call)
// out, err := mod.ExportedFunction("run").Call(ctx)
```
Pins: `modelcontextprotocol/go-sdk` EXACT v1.8.x from `go list -m -versions` (floor v1.7.0); `tetratelabs/wazero` v1.12.x. Fallbacks if `go doc` disappoints: nil HTTP opts (stateful), allocator-only memory cap, WASI-less guests with TinyGo deferred.

### Files to Create
`core/registry.go` (~100 lines, imports nothing but stdlib): `CoreAPIVersion="1.0.0"`, `ModuleInfo{ID, APIVersion, Kind string; New func() any}`, seven `Kind*` consts, `RegisterModule` (reject empty/unparsable/dup/major-mismatch — hand-rolled `SplitN` semver parse, no dep), `MustRegister` (panics), `Lookup`, `ListModules` (sorted copy). No stage types (see §2 kind-agnostic decision — document the WHY in a comment). `scrape/scrape.go`: move `runScrape`'s body verbatim into `Run(ctx, Deps, rawURL, Options) (Result, error)`; sentinels; NO cobra, NO output formatting (cmd keeps that). `mcp/server.go` + `mcp/tools.go`: `Deps{DB, ScrapeDeps, DefaultProvider, DefaultModel string, MaxCost float64}`, `NewServer`, four handlers per §8.3 shapes (schema fields are `map[string]any` → `json.Marshal` → `ParseSchema`); `crawl_site` honors `RunID` (status-only path via new `store.GetRun` + `CrawlStats`) and wires `Progress`/`OnRecord`. `crawl/crawl.go`: add nil-safe `Progress func(done int)` + `OnRecord func(map[string]any)` invoked in the sink goroutine ONLY. `store/sqlite.go`: add `GetRun` (one SELECT on run_history; unknown id → error). `cmd`→`cli/` move + `serve.go` + `build_cmd.go` per Tasks 3.3/3.5 (move first if doing 3.5 early — order inside the phase is yours, but MCP before WASM is mandatory). `plugin/exec/exporter.go` (stdlib, `CommandContext` + `StdinPipe` + JSONL + stderr attribution). `build/build.go` (template + `go mod init/edit/get/build` wrapper; `--with` requires `@`; `Replaces` field for tests). `plugin/wasm/host.go` (ABI doc comment + `Runner` + bump allocator + gates + WASI-zero-preopen). `testdata/buildmod/` (own go.mod; init registers + writes marker env path). Tests per task (`registry_test`, `server_test` with in-memory transports, `exporter_test` helper-process, `build_test` golden + `GOPROXY=off`, `host_test` hand-encoded fixtures, `scrape_test` CLI-equivalence). Extend `config.Config` (serve_transport/serve_addr/exporter_cmd + env + Flags). README + AGENTS.md touch-ups in Task 3.7.

### Success Criteria
- All 12 Exit Criteria (Section 5) check out, in order.
- `go test ./... && go vet ./...` exit 0; `gofmt -l .` prints nothing; `golangci-lint run ./...` clean; all three `CGO_ENABLED=0` cross-builds succeed.
- A capable engineer pastes this prompt into a fresh session with no other context and ships the phase with zero follow-up questions.

### Expected File Structure at End
(See Section 4 Deliverables tree — reproduce it exactly: new `cli/` (moved), `scrape/`, `mcp/`, `plugin/exec`, `plugin/wasm`, `build/`, extended `core`/`crawl`/`store`/`config`, `testdata/buildmod/`, pinned `go.mod`.)
---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — Phase 2 deliverables verified on disk (`core/pipeline.go` + `pipeline_test.go` read verbatim; `crawl/crawl.go` `Options`/`Run`/`Result` + all `store`/`selector`/`cmd`/`extract` constructor names taken from `grep` over the actual source, not from the Phase 2 plan); `scrape.go`/`shared.go` read in full for the `scrape/` extraction; no `serve`/`build`/`mcp`/`plugin` code exists yet (confirmed via `ls`)
- [PASS] Every sub-task has a clear, testable completion condition — Tasks 3.1–3.7 each end with a one-liner `Sanity check`
- [PASS] Execution prompt is self-contained: (a) prior-phase facts enumerate exact function/type/file names and behaviors, (b) confirmed MCP + wazero + HTTP-handler API snippets with exact pins, (c) Go Data Model Rules table, (d) per-file guidance for all ~20 files, and (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables — 12 criteria cover registry, all four tools, run_id polling, progress, CLI-equivalence, serve transports, exec exporter + tee, hermetic builds + fixture module, WASM happy/deny matrix, full suite/vet/fmt/lint, cross-builds, docs/config surface
- [PASS] Heavy external dependencies have fake/stub strategies noted — LLM via fake `Extractor` (never keys), MCP via `NewInMemoryTransports` (no sockets), exporter via helper-process re-exec (no shell/`cat`), WASM via hand-encoded fixtures (no TinyGo/wat2wasm toolchain), `build` hermetic test via `GOPROXY=off` + warm module cache; SQLite via real pure-Go temp DB (justified: millisecond-fast, DSN behavior must be real)
- [PASS] New libraries have confirmed usage snippets in the execution prompt — MCP (`NewServer`, generic `AddTool` with `jsonschema` tags, `StdioTransport`/`Run`, `NewStreamableHTTPHandler`, `NewInMemoryTransports`, `GetProgressToken`/`NotifyProgress`, `Meta{"progressToken"}`) from the official README + examples/http + SDK Progress example; wazero (`NewRuntime`, `NewHostModuleBuilder`/`WithFunc`/`Export`/`Instantiate`, `Memory().Read/Write/Grow`, `ExportedFunction.Call`, numeric-only host signatures) from the official import-go example; three remaining load-bearing details (Stateless field, page-limit knob, WASI helper name) are explicit `go doc` confirmations in Task 3.1 with named fallbacks, not hand-waves
