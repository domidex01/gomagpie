# Phase 3 — Testing: MCP server + plugin system

**Scope:** `core/registry.go`, `scrape/scrape.go` (extracted from `cmd`), `mcp/server.go` + `mcp/tools.go`, `crawl/crawl.go` (`Progress`/`OnRecord` hooks), `store/sqlite.go` (`GetRun`), `config/config.go` (serve/exporter keys), `cli/` (moved tree + `serve.go` + `build_cmd.go`), `plugin/exec/exporter.go`, `build/build.go` + `testdata/buildmod/`, `plugin/wasm/host.go`
**Key Pattern:** Fake the LLM with the Phase 2 counting `fakeExtractor`, fake MCP clients with `NewInMemoryTransports` (no sockets), fake subprocesses with the Go helper-process re-exec trick (no shell), fake WASM guests with a hand-encoded `encodeModule` fixture builder (no toolchain); real pure-Go SQLite on `t.TempDir()`; hermetic `GOPROXY=off` builds only.
**Dependencies:** stdlib `testing`, `net/http/httptest`, `os`, `os/exec`, `encoding/json`, `sync`, `sync/atomic` + the two new pinned deps (`modelcontextprotocol/go-sdk`, `tetratelabs/wazero`) — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an agent developer, I want all four MCP tools callable over in-memory transports with no network/keys, so that agents can drive extraction deterministically | `TestMCP_FourTools` in `mcp` (fake `Extractor` + temp DB, `CallTool` per tool) | all four return typed `Out` + exit 0; `go test ./mcp/ -v` exits 0 |
| US-2 | As an agent developer, I want `crawl_site` re-invoked with `run_id` to report status with 0 new extractor calls, with ≥1 progress notification on multi-page crawls, so that long crawls are pollable and observable | `TestCrawlSite_RunIDZeroCalls` (fake-extractor call count delta) + `TestCrawlSite_Progress` (`ProgressNotificationHandler` capture on 3-page origin) + stdout-clean guard | call-count delta == 0 AND notifications ≥ 1 AND captured stdout empty |
| US-3 | As a pipeline operator, I want `--exporter-cmd` to tee byte-identical JSONL to a subprocess that fails loudly with attribution, so that records reach downstream systems without silent loss | `TestExport_JSONLRoundTrip` + `TestExport_FailureAttribution` in `plugin/exec` + crawl `--exporter-cmd` e2e in `cli` | export file bytes == `.jsonl` bytes; failure error names program + quotes stderr |
| US-4 | As a distributor, I want `magpie build --with <module>` to emit a working static binary whose third-party `init()` registration provably executed, behind the semver gate, so that custom builds are reproducible | `TestBuild_Hermetic` + `TestBuild_FixtureMarker` in `build` (GOPROXY=off, marker-file protocol) + `TestRegistry_Gate` in `core` | `--help` exits 0; marker file exists; skew-accept / major-reject / dup-reject |
| US-5 | As a host operator, I want untrusted `.wasm` confined to 4 host functions with sockets/env/filesystem denied and version-gated, so that third-party transforms can't exfiltrate | `TestWASM_*` in `plugin/wasm` (happy/versions/sock/env/fs/ctx via `encodeModule` fixtures) | 6 subtests green; `sock_send` returns nonzero errno; `environ_sizes_get` → (0,0); `path_open` nonzero errno; no stray files on disk |

---

## 1. Component Mock Strategy

Phase type: **Service** (MCP server over the Phase 1–2 pipeline + three plugin surfaces: subprocess, compile-time modules, WASM sandbox). Mock strategy in one sentence: **fake the LLM with the Phase 2 call-logging `fakeExtractor`, fake MCP peers with `NewInMemoryTransports`, fake subprocesses with helper-process re-exec, fake WASM guests with hand-encoded binaries, build hermetically with `GOPROXY=off`, and use the real pure-Go SQLite driver on temp files — no network, no browser, no keys, no toolchain in the default suite.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `core/registry.go` gate | No mock (pure in-memory map + hand-rolled semver parse) | Same-major-minor-skew accepted; different-major rejected; duplicate ID rejected; empty ID/version + unparsable semver rejected; `Lookup` miss → false; `ListModules` sorted + defensive copy | US-4 |
| `scrape/scrape.go` Run | Reuse `fakeExtractor` + `httptest` origin (same harness as Phase 1/2 `cmd` scrape tests); `ExtractorFor`/`APIKeyFor` func fields injected | Schema + markdown-only paths; `ErrMissingKey` when `APIKeyFor` empty; ceiling reuses `crawl.ErrCostCeiling`; existing `cmd` scrape e2e passes unmodified (byte-identical CLI) | US-1 |
| `mcp` four tools | `mcp.NewInMemoryTransports` client/server pair + fake `Extractor` + temp-file DB; `crawl_site` against 3-page httptest origin | `scrape_url` (schema + markdown-only), `extract_structured` (html→clean + markdown-direct), `get_cached_selectors` (domain + schema_hash filter), `crawl_site` returns `{run_id, pages_crawled, records}` | US-1 |
| `crawl_site` run_id + progress + stdout | Same in-memory pair; `RunID` re-invocation after a completed crawl; `CallToolParams{Meta: mcp.Meta{"progressToken": "p1"}}` + `ProgressNotificationHandler`; `os.Pipe` capture around the call | Status fields match `GetRun` + `CrawlStats`; fake-extractor total unchanged; ≥1 notification with `Message: "N pages"`; captured stdout is empty (stdio-framing guard) | US-2 |
| `crawl` hooks + `store.GetRun` | `Progress`/`OnRecord` as counting closures on a small httptest crawl; `GetRun` against temp DB after `BeginRun/FinishRun` | Hook called once per done/errored page; nil hooks never panic (existing crawl suite unmodified); unknown run_id → loud error; returned row matches `pages_ok/pages_err/tokens/cost/status` | US-2 |
| `plugin/exec` exporter | Helper-process re-exec (`GO_WANT_HELPER_PROCESS=1`, reads stdin, appends to `$MAGPIE_TEST_OUT`); failure variant exits 3 with stderr text | Line-delimited JSON equality record-for-record; error quotes stderr AND names `Cmd[0]`; spawn failure (bogus binary) errors at `Start`; `cmd.Wait` nonzero → loud error | US-3 |
| crawl `--exporter-cmd` tee | Same helper binary via `file://` site e2e in `cli` | `.jsonl` file bytes == export file bytes; exporter failure → stderr warning + exit 4 path, records still in `--out`/SQLite | US-3 |
| `cli/` move + `serve` + `build_cmd` flags | No mock — moved cobra tree runs in-process; `serve` http branch binds `127.0.0.1:0` (ephemeral port) + manual curl smoke | Full existing `cmd` suite passes with package-name-only changes; `serve --help` states sync-crawl + stateful/stateless behavior; serve-transport/addr + exporter_cmd keys round-trip through `config show` | US-1, US-3 |
| `build/build.go` + `testdata/buildmod` | Hermetic: `GOPROXY=off GOFLAGS=-mod=mod GOSUMDB=off`, repo `go.sum` copied into temp module, warm module cache only; fixture module pulled via `Replaces` field (no CLI surface) | `main.go` goldens (0 and 2 `--with`); zero-`--with` build → `<bin> --help` exits 0; fixture build → running `<bin> --help` creates `$MAGPIE_BUILD_TEST_MARKER` (init executed); bare `--with` without `@` → loud error | US-4 |
| `plugin/wasm` host | `encodeModule` test helper assembles magic+version+type/import/function/export/code sections by hand — no TinyGo, no wat2wasm; real wazero runtime, `io.Discard` sinks, zero-preopen WASI | Happy path output bytes + captured log; `api_version=2*10000` → `NewRunner` rejects; missing export → "not a gomagpie plugin"; `sock_send` call → nonzero errno; `environ_sizes_get` → (0,0); `path_open` → nonzero errno + no disk file; pre-canceled ctx → error (no hang) | US-5 |
| `config` new keys | No mock — table test over `overlay/overlayEnv/ApplyFlags` chain | `serve_transport`/`serve_addr`/`exporter_cmd` resolve flags > `GOMAGPIE_` env > file > defaults; `config show` prints them; no secrets so redaction N/A | US-1, US-3 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | In-memory MCP transports, `fakeExtractor`, helper-process re-exec, `encodeModule` wasm fixtures, `GOPROXY=off` hermetic builds, temp-dir SQLite, httptest origins — no network, no browser, no keys, no toolchain | <120s total (hermetic `go build` ×2 inside `build` tests dominates; everything else ms-scale) | Every push; the only default gate |
| Browser | No new browser tests — existing `//go:build browser` smoke stays as-is; `scrape.Render: "browser"` path in MCP is covered only via `ExtractorFor` injection, never a real rod launch | n/a | Separate CI job (unchanged) |
| E2E (manual, not a test file) | Built binary: stdio `tools/list` pipe, HTTP bind curl (200/400 never 000), non-hermetic `magpie build --with <real module>` (the ONLY network path, documented) | minutes | Pre-release sanity (Task 3.7 matrix) |

No live-provider tier by design (same as Phase 2): real keys must never appear in any test. No `--with-*` pytest-style flag exists — the Go adaptation is build tags (`browser`) + the hermetic-by-construction default suite.

---

## 3. Fake / Mock Implementations

Four fakes/patterns. Reuse — don't reinvent: `fakeExtractor` (copy the Phase 2 `selector`/`crawl` version verbatim into `mcp` tests), `openTempDB` (store), `newFakeProvider` + `GOMAGPIE_BASE_URL` + `file://` harness (`cli`), `newSiteOrigin` 3-page chain (`crawl`), `SelectorDoc` remarshal (`selector`).

### Counting `fakeExtractor` — replaces `extract.Extractor` (mcp + scrape tests)

```go
// mcp/server_test.go (copy per package — Phase 2 rule-of-three: no shared testutil package)
type fakeExtractor struct {
    mu     sync.Mutex
    calls  int
    script map[string]any // record returned for every call
    err    error          // when non-nil, every call fails
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
    if err := ctx.Err(); err != nil {
        return extract.ExtractResult{}, err
    }
    f.mu.Lock()
    defer f.mu.Unlock()
    f.calls++
    if f.err != nil {
        return extract.ExtractResult{}, f.err
    }
    raw, _ := json.Marshal(f.script)
    return extract.ExtractResult{Record: f.script, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) total() int {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.calls
}
```

**Matches real call:** production MCP handlers obtain the extractor via `scrape.Deps.ExtractorFor(provider, key, model, sch, runID)` and call `ex.Extract(ctx, extract.ExtractInput{...})` on the `extract.Extractor` interface (same interface the OpenAI/Anthropic/codex/zen adapters implement). Tests inject a `Deps` whose `ExtractorFor` ignores its args and returns `*fakeExtractor`; `total()` proves the run_id status path makes 0 calls (US-2). NOTE: `ExtractorFor` must return the SAME `*fakeExtractor` across calls in a test, or counts reset — close over one instance.

### In-memory MCP pair — replaces the stdio/HTTP transport (mcp tests)

```go
// mcp/server_test.go
func dialInMemory(t *testing.T, srv *mcp.Server) *client.Client {
    t.Helper()
    ctx := context.Background()
    ct, st := mcp.NewInMemoryTransports()
    _, err := srv.Connect(ctx, st, nil)
    if err != nil {
        t.Fatalf("server Connect: %v", err)
    }
    cs, err := client.Connect(ctx, ct, nil)
    if err != nil {
        t.Fatalf("client Connect: %v", err)
    }
    t.Cleanup(func() { cs.Close(); srv.Close() })
    return cs
}

func callTool(t *testing.T, cs *client.Client, name string, args map[string]any, progressToken string) *mcp.CallToolResult {
    t.Helper()
    params := &mcp.CallToolParams{Name: name, Arguments: args}
    if progressToken != "" {
        params.Meta = mcp.Meta{"progressToken": progressToken} // confirmed SDK Progress-example pattern
    }
    res, err := cs.CallTool(context.Background(), params)
    if err != nil {
        t.Fatalf("CallTool(%s): %v", name, err)
    }
    return res
}
```

**Matches real call:** production clients connect over stdio (`server.Run(ctx, &mcp.StdioTransport{})`) or Streamable HTTP; the SDK routes both through the same tool registry, so in-memory transports exercise the identical handler code with zero sockets. Progress capture: register a `ProgressNotificationHandler` on the client before `callTool(..., "p1")` and collect into a mutex-guarded slice.

### Helper-process exporter — replaces `/bin/cat` and any real downstream (exec + cli tests)

```go
// plugin/exec/exporter_test.go
func TestHelperExporterProcess(t *testing.T) {
    if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
        return
    }
    // Runs INSIDE the re-exec child: copy stdin to $MAGPIE_TEST_OUT, optionally fail.
    out := os.Getenv("MAGPIE_TEST_OUT")
    if os.Getenv("MAGPIE_TEST_FAIL") == "1" {
        fmt.Fprintln(os.Stderr, "boom: downstream exploded")
        os.Exit(3)
    }
    data, _ := io.ReadAll(os.Stdin)
    os.WriteFile(out, data, 0o600)
    os.Exit(0)
}

func helperExporter(t *testing.T, out string, fail bool) []string {
    t.Helper()
    t.Setenv("MAGPIE_TEST_OUT", out)
    t.Setenv("MAGPIE_TEST_FAIL", map[bool]string{true: "1", false: ""}[fail])
    return []string{os.Args[0], "-test.run=TestHelperExporterProcess"}
}
// NOTE: the child needs GO_WANT_HELPER_PROCESS=1 in ITS env — set it in Export's cmd.Env
// inside the test via t.Setenv BEFORE calling Export (inherited), or pass explicit Env.
```

**Matches real call:** production runs `exec.CommandContext(ctx, argv[0], argv[1:]...)` with `StdinPipe` + JSONL encoder. The re-exec trick is hermetic and Windows-safe (no `cat`, no shell, no quoting — mirrors the `strings.Fields` argv contract). Assert line-delimited JSON equality by decoding both files line-by-line, not byte-compare (encoder whitespace is an implementation detail).

### `encodeModule` — replaces the guest toolchain (wasm tests)

```go
// plugin/wasm/host_test.go (~60 lines): minimal wasm binary assembler.
// Sections: magic+version, type, import, function, export, code (+ data for byte-holding guests).
// Helpers: u32lebs, vec([]byte), section(id, body), funcType(params, results),
// importFunc(mod, name, typeidx), exportFunc(name, funcidx), codeBody(locals, instrs...).
// Instruction bytes needed: i32.const (0x41), call (0x10), end (0x0b), drop (0x1a),
// plus WASI calls: environ_sizes_get (fd-relative import idx), path_open, sock_send.
// Guest memory access: (memory 1) + data segments at fixed offsets (e.g. 0x1000).
```

**Matches real call:** production `NewRunner(ctx, wasmBytes)` compiles arbitrary guest bytes with `CompileModule`; the fixtures are the smallest binaries that import the exact host/WASI functions under test and export `gomagpie_api_version` + `run`. Each deny test imports the REAL function name (`wasi_snapshot_preview1.sock_send`, not a made-up `sock_open` — which doesn't exist in snapshot-preview1 and would pass vacuously) and asserts the call's nonzero errno. Happy-path guest holds output bytes in a data section and calls `gomagpie_set_output(ptr, len)`.

### Hermetic build harness — replaces the networked `go get` path (build tests)

```go
// build/build_test.go
func TestMain(m *testing.M) {
    // Gate: skip hermetic build tests when the module cache is cold?
    // NO — fail loud instead. GOPROXY=off with a warm cache is a CI precondition
    // (same as the phase plan); a skip would let the flagship test pass vacuously.
    os.Exit(m.Run())
}

func copyGoSum(t *testing.T, repoRoot, dir string) {
    t.Helper()
    sum, err := os.ReadFile(filepath.Join(repoRoot, "go.sum"))
    if err != nil {
        t.Fatalf("read go.sum: %v", err)
    }
    if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o600); err != nil {
        t.Fatalf("write go.sum: %v", err)
    }
}
// Build env: GOPROXY=off GOFLAGS=-mod=mod GOSUMDB=off GOCACHE=<t.TempDir()> —
// hermetic but isolated (never pollute the shared build cache from tests).
```

**Matches real call:** production `build.Build` shells `go mod init/edit/get/build` in a temp module; the zero-`--with` test path never hits the network (only `replace gomagpie => repo root` + local cache). The fixture-module test adds `Replaces: {"buildmod": <abs testdata/buildmod>}` — the test-only struct field, 3 lines, no CLI surface.

---

## 4. Test File List

```
gomagpie/
├── core/
│   └── registry_test.go      # NEW: gate matrix — skew-accept / major-reject / dup-reject / empty+unparsable / lookup-miss / sorted-list
├── scrape/
│   └── scrape_test.go        # NEW: Run schema + markdown-only, ErrMissingKey, ceiling reuse; existing cmd e2e stays in cli/ as the byte-identical proof
├── mcp/
│   └── server_test.go        # NEW: 4 tools over in-memory transports + run_id zero-calls + progress ≥1 + stdout-clean guard
├── crawl/
│   └── crawl_test.go         # EXTEND: Progress/OnRecord counting on small origin; nil-hook safety (existing suite must pass untouched)
├── store/
│   └── sqlite_test.go        # EXTEND in place: GetRun round-trip (BeginRun/FinishRun → GetRun field match) + unknown-id error
├── config/
│   └── config_test.go        # EXTEND in place: serve_transport/serve_addr/exporter_cmd overlay chain (flag > env > file > default) + config-show presence
├── cli/ (moved cmd/magpie)
│   ├── cmd_test.go           # MOVED: must pass with package-name-only changes (byte-identical CLI proof)
│   └── exporter_e2e_test.go  # NEW: crawl --exporter-cmd via helper-process → byte-identical .jsonl + export; failure → exit 4 + stderr warning
├── plugin/exec/
│   └── exporter_test.go      # NEW: helper-process round-trip + failure attribution + spawn-failure
├── build/
│   └── build_test.go         # NEW: main.go goldens (0 + 2 --with) + GOPROXY=off zero---with build + fixture marker + bare---with rejection
├── testdata/buildmod/
│   ├── go.mod                # module buildmod (excluded from ./... by its own go.mod)
│   └── buildmod.go           # init(): MustRegister + os.AppendFile($MAGPIE_BUILD_TEST_MARKER)
├── plugin/wasm/
│   └── host_test.go          # NEW: encodeModule fixtures — happy / version-gate / sock-deny / env-deny / fs-deny / canceled-ctx
└── plan/phase-3-tests.md     # this file
```

Every deliverable in `plan/phase-3.md` §4 has a test file: `cli/` move → `cmd_test.go` moved-unmodified; `serve.go` → `server_test.go` + `serve --help` assertion + Task 3.7 manual smoke (not a Go test); `scrape/` → `scrape_test.go`; `mcp/` → `server_test.go`; crawl hooks → `crawl_test.go` additions; `store.GetRun` → `sqlite_test.go` additions; `config` keys → `config_test.go` additions; `plugin/exec` → `exporter_test.go` + `exporter_e2e_test.go`; `build/` → `build_test.go`; `testdata/buildmod` → exercised by `build_test.go`; `plugin/wasm` → `host_test.go`; `go.mod` pins → Task 3.1 `go doc` confirmations + `go list -deps` CGO check + cross-build gate (§9), not a Go test (same as Phases 1–2); README/AGENTS.md → grep assertions in `TestDocs_Phase3Surface` (see §5) + manual review.

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): fixtures are per-package test helpers, hermetic gating is "default suite never touches network/browser/keys/toolchain" (no flag needed — everything new is hermetic by construction), and the only non-hermetic path (`build --with` a REMOTE module) is manual smoke, not a Go test. One new shared convention: `t.Setenv` for ALL env manipulation (parallel-safe, auto-restored).

```go
// core/registry_test.go — NEW, self-contained (table-driven, no helpers needed)
// cases: {"1.0.1 vs core 1.0.0": accept}, {"1.9.0": accept}, {"2.0.0": reject},
//        {"0.9.9": reject}, {"": reject}, {"not-semver": reject}, {duplicate: reject}

// scrape/scrape_test.go — NEW (fakeExtractor + httptest origin, mirror Phase 1/2 cmd harness)
func openScrapeDB(t *testing.T) *store.DB          // temp-file DB (copy openTempDB pattern)
func fakeScrapeDeps(t *testing.T, fx *fakeExtractor) scrape.Deps // ExtractorFor returns fx; APIKeyFor returns "test-key"

// mcp/server_test.go — NEW (dialInMemory + callTool per §3; fakeExtractor; temp DB)
func testMCPServer(t *testing.T, fx *fakeExtractor) *mcp.Server // Deps{DB: tmp, ScrapeDeps: fake, DefaultProvider: "fake", ...}
func seedSelectors(t *testing.T, db *store.DB, domain string)  // Put one SelectorDoc for get_cached_selectors

// crawl/crawl_test.go — ADD (reuse newSiteOrigin, openCrawlDB)
func TestHooks_ProgressAndOnRecord(t *testing.T) // counting closures; assert calls == pages done+errored
func TestHooks_NilSafe(t *testing.T)             // Run without hooks on erroring origin — must not panic

// store/sqlite_test.go — ADD (reuse openTempDB)
func TestGetRun(t *testing.T)          // BeginRun → FinishRun(3,1,"complete") → GetRun: assert all six columns
func TestGetRun_Unknown(t *testing.T)  // unknown id → non-nil error containing the id

// config/config_test.go — ADD (reuse existing overlay harness)
func TestServeKeys_OverlayChain(t *testing.T) // flag > GOMAGPIE_SERVE_TRANSPORT env > file > default

// cli/exporter_e2e_test.go — NEW (helper-process mirror of §3 + file:// site)
func TestHelperExporterProcess(t *testing.T) // GO_WANT_HELPER_PROCESS guard (same shape as exec's)

// plugin/exec/exporter_test.go — NEW (helper-process per §3)

// build/build_test.go — NEW (copyGoSum + golden compare + GOPROXY=off env per §3)
// golden files: testdata/build_main_zero.txtar? NO — inline want strings (two small mains, files add indirection)

// plugin/wasm/host_test.go — NEW (encodeModule + section helpers per §3; table of deny cases)

// cli/docs_test.go — NEW, tiny: TestDocs_Phase3Surface
// asserts README names the 4 tools + "magpie serve" + "magpie build --with" + "gomagpie_api_version",
// and AGENTS.md no longer contains "No plugins/WASM until core stable".
// Rationale: Task 3.7 docs are an Exit Criterion — a 20-line grep test keeps them from rotting.
```

Scope rationale: everything is function-scoped (`t.TempDir()`, per-test servers/fakes/runtimes) — no shared state, no `TestMain` (except the documented non-skip decision in `build`), safe for `go test -race`. Ephemeral ports only (`127.0.0.1:0`); the fixed `:8089` from the phase plan is manual-smoke-only, never asserted in a test (parallel CI + occupied ports = flake).

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Fake the `Extractor` interface, not MCP transports or provider HTTP, for tool tests | One counting `fakeExtractor` shared via `ExtractorFor` closure; in-memory transports are the REAL SDK path, not a mock | This phase's risk is handler wiring (schema map→ParseSchema, RunID branch, Out: DevNull) — the SDK's own framing is already tested upstream; counts prove US-2 deterministically |
| Real SQLite + real SDK + real wazero runtime in tests | Temp-file DB; `NewInMemoryTransports`; `wazero.NewRuntime` per test | The SQL (`GetRun` SELECT), the generic `AddTool` schema inference, and the WASI errno behavior are exactly what must be real — faking any of them hides the bug class this phase introduces |
| Hand-encoded wasm over any guest toolchain | `encodeModule` helper (~60 lines), no TinyGo/wat2wasm | Toolchains are absent on CI/WSL and version-fragile; byte-assembled fixtures are deterministic and pin the exact import names that make deny tests non-vacuous |
| Deny tests assert ERRNO on real calls, never instantiation failure | `sock_send`/`path_open`/`environ_sizes_get` invoked by the guest; assert nonzero errno / (0,0) | WASI instantiates fine by design (link-only sandbox) — asserting instantiation failure would test a mechanism that doesn't exist and pass vacuously |
| Helper-process over `cat`/shell for exec tests | `TestHelperExporterProcess` re-exec pattern | `cat` doesn't exist on Windows CI; shell quoting contradicts the `strings.Fields` contract; re-exec is hermetic and asserts the exact argv path production uses |
| Hermetic `GOPROXY=off` builds fail loud, never skip | No cold-cache skip in `build_test.go` | A skip makes the flagship reproducibility proof pass vacuously on exactly the machines where it matters |
| Golden-test the codegen'd `main.go` as inline strings | Two small `want` strings in `build_test.go` | The template is ~10 lines; fixture files add indirection for zero benefit (unlike HTML/JSON goldens in clean/extract) |
| Moved `cmd` tests must pass UNMODIFIED (package name only) | No new assertions added to moved files; new CLI surface tested in new files | The move's contract is byte-identical behavior — editing moved tests to fit the refactor destroys the proof |
| Progress asserted as ≥1, not exact count | `len(notifications) >= 1` on a 3-page origin | Exact per-page counts couple the test to sink-goroutine scheduling; the contract is "observable", not "N notifications" |
| Stdout-clean guard around `crawl_site` | `os.Pipe` capture, assert empty | `Out: ""` → stdout corruption of the stdio JSON-RPC framing is the single most catastrophic wiring bug available; one 10-line guard kills it forever |
| Cross-compile + vet + gofmt + golangci-lint are CI commands, not Go tests | §9 gate lines (same as Phases 1–2) | A Go test cannot change GOOS; the Task 3.1 sanity-check line is the test |
| Duplicate the ~30-line `fakeExtractor` per package | Copy into `mcp` (and `scrape` if needed), don't create `testutil` | Same rule-of-three call as Phases 1–2: three small copies don't justify a shared package |
| Docs get a grep test, not just review | `TestDocs_Phase3Surface` (20 lines) | README/AGENTS touch-ups are an Exit Criterion; untested docs rot within one refactor |

---

## 7. Example Test Case

```go
// mcp/server_test.go
package mcp_test

import (
    "context"
    "encoding/json"
    "os"
    "sync"
    "testing"

    "gomagpie/extract"
    "gomagpie/mcp"
    "gomagpie/store"
)

func TestCrawlSite_RunIDZeroCalls(t *testing.T) {
    fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
    db := openMCPDB(t)
    cs := dialInMemory(t, testMCPServer(t, db, fx))

    // First call: real crawl against a 3-page httptest origin.
    res := callTool(t, cs, "crawl_site", map[string]any{
        "url": origin3Pages(t), "max_pages": 10,
    }, "")
    var out map[string]any
    if err := json.Unmarshal(res.StructuredContent, &out); err != nil {
        t.Fatalf("decode crawl_site out: %v", err)
    }
    runID, _ := out["run_id"].(string)
    if runID == "" {
        t.Fatal("crawl_site out has no run_id")
    }
    before := fx.total()
    if before == 0 {
        t.Fatal("first crawl made 0 extractor calls — fixture isn't exercising the LLM path")
    }

    // Second call: status-only re-invocation. Must not touch the extractor.
    res2 := callTool(t, cs, "crawl_site", map[string]any{"run_id": runID}, "")
    var out2 map[string]any
    if err := json.Unmarshal(res2.StructuredContent, &out2); err != nil {
        t.Fatalf("decode status out: %v", err)
    }
    if out2["run_id"] != runID {
        t.Errorf("status run_id = %v, want %q", out2["run_id"], runID)
    }
    if got := fx.total() - before; got != 0 {
        t.Errorf("status re-invoke made %d extractor calls, want 0", got)
    }
    if out2["status"] != "complete" {
        t.Errorf("status = %v, want complete", out2["status"])
    }
}

func TestCrawlSite_Progress(t *testing.T) {
    fx := &fakeExtractor{script: map[string]any{"title": "Widget"}}
    db := openMCPDB(t)
    srv := testMCPServer(t, db, fx)

    ctx := context.Background()
    ct, st := mcp.NewInMemoryTransports()
    if _, err := srv.Connect(ctx, st, nil); err != nil {
        t.Fatalf("server Connect: %v", err)
    }
    cs, err := client.Connect(ctx, ct, nil)
    if err != nil {
        t.Fatalf("client Connect: %v", err)
    }
    t.Cleanup(func() { cs.Close(); srv.Close() })

    var mu sync.Mutex
    var notes int
    cs.ProgressNotificationHandler(func(ctx context.Context, p *mcp.ProgressNotificationParams) {
        mu.Lock()
        defer mu.Unlock()
        notes++
        if p.Message == "" {
            t.Error("progress notification with empty message")
        }
    })

    // Stdout must stay clean: crawl_site writing to os.Stdout would corrupt
    // the stdio transport's JSON-RPC framing in production.
    r, w, _ := os.Pipe()
    old := os.Stdout
    os.Stdout = w
    defer func() { os.Stdout = old }()

    callTool(t, cs, "crawl_site", map[string]any{
        "url": origin3Pages(t), "max_pages": 10,
    }, "p1")

    w.Close()
    os.Stdout = old
    if data, _ := io.ReadAll(r); len(data) != 0 {
        t.Errorf("crawl_site wrote %d bytes to stdout, want 0 (Out must be os.DevNull)", len(data))
    }

    mu.Lock()
    defer mu.Unlock()
    if notes < 1 {
        t.Error("0 progress notifications on a 3-page crawl, want >= 1")
    }
}

func TestRegistry_Gate(t *testing.T) {
    // Table-driven semver gate (core package — representative pure-logic shape):
    // {"1.0.1", true}, {"1.9.0", true}, {"2.0.0", false}, {"0.9.9", false},
    // {"", false}, {"not-semver", false}, plus duplicate-ID and sorted-list cases.
    _ = extract.ExtractInput{} // placeholder: keeps imports honest in this sketch
}
```

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase 3 of `gomagpie` — MCP server + plugin system. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-3.md` (the implementation plan — all 7 tasks, confirmed MCP/wazero API snippets, exact pins), `plan/phase-2-tests.md` (established fake patterns to reuse: `fakeExtractor`, `newSiteOrigin`, `openTempDB`/`openCrawlDB`, `GOMAGPIE_BASE_URL` + `file://` harness), `spec.md` §§6–8 (behavioral source of truth), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test.

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`, module `gomagpie`): Phases 1–2 built the single-URL fetch→clean→extract pipeline plus selector cache and bounded-concurrency crawling. Phase 3 (spec Milestone 3) exposes it over MCP (`magpie serve`, four tools), adds the compile-time module registry with semver gate, subprocess JSONL exporters, `magpie build --with` custom binaries, and the wazero WASM sandbox. Pure-Go, zero CGO. Tests are hermetic: `go test ./...` must pass with no network, no browser, no API keys, no guest toolchain.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | All four MCP tools callable with no network/keys | TestMCP_FourTools in mcp | all four green; `go test ./mcp/ -v` exits 0 |
| US-2 | run_id re-invoke = status with 0 new extractor calls; ≥1 progress notification; stdout stays clean | TestCrawlSite_RunIDZeroCalls + TestCrawlSite_Progress | call delta == 0 AND notes ≥ 1 AND stdout empty |
| US-3 | --exporter-cmd tees byte-identical JSONL; failures loud + attributed | TestExport_JSONLRoundTrip + TestExport_FailureAttribution + cli e2e | bytes equal; error names program + quotes stderr |
| US-4 | build --with emits working binary; init() registration proven; semver gate holds | TestBuild_Hermetic + TestBuild_FixtureMarker + TestRegistry_Gate | --help exits 0; marker exists; skew-accept/major-reject/dup-reject |
| US-5 | WASM confined to 4 host fns; sock/env/fs denied; version-gated | TestWASM_* (6 subtests) | green; sock_send nonzero errno; environ (0,0); path_open nonzero errno; no stray files |

### Why Fakes Are Required
- LLM extraction: real calls need keys/billing/network — and US-2 must prove ZERO calls on the status path. Fake with the counting `fakeExtractor` (§Critical); assert `total()` deltas, never timing.
- MCP peers: real stdio/HTTP needs sockets and framing. Fake with `mcp.NewInMemoryTransports` (§Critical) — exercises the identical handler code with zero sockets.
- Subprocess exporters: `cat`/shell don't exist on Windows CI and shell quoting contradicts the `strings.Fields` contract. Fake with helper-process re-exec (§Critical).
- WASM guests: TinyGo/wat2wasm toolchains are absent on CI/WSL and version-fragile. Fake with hand-encoded `encodeModule` fixtures (§Critical) that import the REAL deny-target names.
- Remote modules: `go get module@version` needs network. Never tested in Go — hermetic `GOPROXY=off` + warm cache only; remote `--with` is manual smoke (Task 3.7).
- Chrome: absent on CI/WSL — no new browser tests; `Render: "browser"` in MCP covered only via `ExtractorFor` injection.
- SQLite is NOT faked: `modernc.org/sqlite` is pure Go and millisecond-fast — test the real driver on `t.TempDir()` files (`GetRun`'s SELECT must be real).
- OS keyring is NOT touched (same as Phases 1–2).

### What NOT to Test
- Don't test the MCP SDK's framing, schema inference, or HTTP handling — test OUR four handlers (In/Out shapes, RunID branch, DevNull wiring, schema_hash filter).
- Don't test wazero's compilation, memory management, or WASI implementation — test OUR host surface (4 functions), OUR gates (version, allocator cap), OUR sandbox config (zero preopens, discard sinks).
- Don't test `go build`/`go mod` themselves — golden-test OUR template, hermetic-test OUR wrapper's happy path + loud failures.
- Don't test Cobra flag parsing or `net/http` mechanics beyond our policies (transport switch, ephemeral-port bind in tests).
- Don't test live providers, real websites, real Chrome, remote modules, or guest toolchains in the default suite.
- Don't create a shared `testutil` package — copy `fakeExtractor` (~30 lines) into `mcp` (and `scrape` if needed); copy the helper-process guard into `cli` (Phases 1–2 rule-of-three).
- Don't test json-format RAM buffering limits, robots `Expires`, canonicalization, or backoff internals — locked by Phase 1–2 suites, untouched this phase.
- Don't build any Phase 4 scaffolding — there is no Phase 4.

### Critical: Fake Implementations

Copy these verbatim into the owning test files (`fakeExtractor` full copy in `mcp/server_test.go`; in-memory pair + `callTool` in `mcp/server_test.go`; helper-process guard in BOTH `plugin/exec/exporter_test.go` and `cli/exporter_e2e_test.go`; `encodeModule` in `plugin/wasm/host_test.go`; hermetic env in `build/build_test.go`):

```go
type fakeExtractor struct {
    mu     sync.Mutex
    calls  int
    script map[string]any
    err    error
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
    if err := ctx.Err(); err != nil {
        return extract.ExtractResult{}, err
    }
    f.mu.Lock()
    defer f.mu.Unlock()
    f.calls++
    if f.err != nil {
        return extract.ExtractResult{}, f.err
    }
    raw, _ := json.Marshal(f.script)
    return extract.ExtractResult{Record: f.script, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) total() int {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.calls
}
```

```go
func dialInMemory(t *testing.T, srv *mcp.Server) *client.Client {
    t.Helper()
    ctx := context.Background()
    ct, st := mcp.NewInMemoryTransports()
    _, err := srv.Connect(ctx, st, nil)
    if err != nil {
        t.Fatalf("server Connect: %v", err)
    }
    cs, err := client.Connect(ctx, ct, nil)
    if err != nil {
        t.Fatalf("client Connect: %v", err)
    }
    t.Cleanup(func() { cs.Close(); srv.Close() })
    return cs
}

func callTool(t *testing.T, cs *client.Client, name string, args map[string]any, progressToken string) *mcp.CallToolResult {
    t.Helper()
    params := &mcp.CallToolParams{Name: name, Arguments: args}
    if progressToken != "" {
        params.Meta = mcp.Meta{"progressToken": progressToken}
    }
    res, err := cs.CallTool(context.Background(), params)
    if err != nil {
        t.Fatalf("CallTool(%s): %v", name, err)
    }
    return res
}
```

```go
func TestHelperExporterProcess(t *testing.T) {
    if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
        return
    }
    out := os.Getenv("MAGPIE_TEST_OUT")
    if os.Getenv("MAGPIE_TEST_FAIL") == "1" {
        fmt.Fprintln(os.Stderr, "boom: downstream exploded")
        os.Exit(3)
    }
    data, _ := io.ReadAll(os.Stdin)
    os.WriteFile(out, data, 0o600)
    os.Exit(0)
}
```

WASM `encodeModule` (~60 lines assembling magic+version+type/import/function/export/code sections) and the hermetic build env (`GOPROXY=off GOFLAGS=-mod=mod GOSUMDB=off GOCACHE=<t.TempDir()>` + `copyGoSum` from §3) are specified in `plan/phase-3-tests.md` §3 — follow them exactly. Deny tests MUST import the real `wasi_snapshot_preview1.sock_send` (there is no `sock_open` in snapshot-preview1 — importing it passes vacuously).

### Test Files to Create

```
core/registry_test.go       # NEW: gate matrix (skew-accept/major-reject/dup-reject/empty+unparsable/miss/sorted)
scrape/scrape_test.go       # NEW: Run schema + markdown-only, ErrMissingKey, ceiling reuse
mcp/server_test.go          # NEW: 4 tools + run_id zero-calls + progress + stdout-clean (§7 example)
crawl/crawl_test.go         # ADD: TestHooks_ProgressAndOnRecord + TestHooks_NilSafe (suite untouched otherwise)
store/sqlite_test.go        # ADD: TestGetRun + TestGetRun_Unknown (reuse openTempDB)
config/config_test.go       # ADD: TestServeKeys_OverlayChain + config-show presence
cli/cmd_test.go             # MOVED: package-name-only changes, must pass unmodified
cli/exporter_e2e_test.go    # NEW: --exporter-cmd byte-identical tee + failure→exit-4 path
plugin/exec/exporter_test.go# NEW: round-trip + failure attribution + spawn-failure
build/build_test.go         # NEW: main.go goldens + GOPROXY=off zero---with + fixture marker + bare---with reject
testdata/buildmod/          # NEW fixture module (own go.mod): init registers + writes $MAGPIE_BUILD_TEST_MARKER
plugin/wasm/host_test.go    # NEW: happy / version-gate / sock-deny / env-deny / fs-deny / canceled-ctx
cli/docs_test.go            # NEW: TestDocs_Phase3Surface (README 4 tools + serve + build--with + api_version; AGENTS gotcha gone)
```

### Per-File Coverage Guidance

#### core/registry_test.go
Table-driven gate: accept `1.0.1`/`1.9.0` vs core `1.0.0`; reject `2.0.0`/`0.9.9`/`""`/`"not-semver"`/empty ID; duplicate `RegisterModule` → error; `MustRegister` duplicate → panics (assert with recover); `Lookup` miss → false; `ListModules` sorted by ID + mutating the result doesn't affect the registry (defensive copy). No concurrency test (mutex is trivially held per-call).

#### scrape/scrape_test.go
`Run` with nil schema → markdown-only result, 0 extractor calls when content needs no LLM (static httptest page, cache warm) else exactly 1; with schema → 1 call, record matches script; `APIKeyFor` returning "" with a schema → `errors.Is(err, ErrMissingKey)`; `MaxCost: 0` with schema → `errors.Is(err, crawl.ErrCostCeiling)` (reuse, don't redeclare); `UseCache: false` bypasses `store` read (assert via `LLMCallCount` delta vs cached run). Do NOT retest output formatting (`marshalOut` stays in cli, covered by moved cmd_test.go).

#### mcp/server_test.go
Per §7 example plus: `scrape_url` markdown-only (no schema arg) + with-schema (inline `{"type":"object",...}` map); `extract_structured` with `content_type: "html"` (runs clean first) and `"markdown"` (direct); `get_cached_selectors` seeded via `store.Put` → assert domain/fields/null-rates; unknown `run_id` → tool error containing the id (not a panic). `StructuredContent` decoded into `map[string]any` — assert `run_id`/`pages_crawled`/`records`/`status` keys exist with the right Go types (float64 for numbers).

#### crawl/crawl_test.go (additions only — do not modify existing tests)
`TestHooks_ProgressAndOnRecord`: 5-page origin, counting `Progress` + collecting `OnRecord`; assert progress calls == done+errored pages, records collected == pages_ok. `TestHooks_NilSafe`: same origin with hooks nil (zero value) — asserts no panic and identical `Result` (the whole existing suite already covers nil-hooks implicitly; this test names the contract).

#### store/sqlite_test.go (additions only)
`TestGetRun`: `BeginRun(id, "crawl")` → `LogLLMCall` ×2 → `FinishRun(id, 3, 1, "complete")` → `GetRun(id)`: assert status/pages_ok/pages_err/prompt_tokens/usd_estimate all match. `TestGetRun_Unknown`: unknown id → error containing the id string. NEVER hold `*sql.Rows` across calls (single-conn discipline).

#### config/config_test.go (additions only)
`TestServeKeys_OverlayChain`: defaults (`stdio`, `:8080`-or-plan-default) → file sets `serve_addr` → `GOMAGPIE_SERVE_TRANSPORT=http` overrides → `--serve-addr` flag wins. Same single-key spot-check for `exporter_cmd` (default "" → env → flag). Assert `config show` output contains all three key names.

#### cli/cmd_test.go (moved — modify NOTHING except the package clause)
If anything fails after the move, the bug is in the moved non-test code (import cycle, unexported symbol), not the test — fix the source, never "adapt" the test. This file is the byte-identical-CLI proof.

#### cli/exporter_e2e_test.go
`file://` 2-page site + `--exporter-cmd "<helper argv>"` → exit 0; decode both `.jsonl` and export file line-by-line → deep-equal record sets. Failure variant (`MAGPIE_TEST_FAIL=1`) → exit 4, stderr contains `WARNING` + program name, `.jsonl` still complete (tee never blocks the primary sink). `strings.Fields` ceiling: one test with a space-free path only — no quoting tests (documented ponytail).

#### plugin/exec/exporter_test.go
`TestExport_JSONLRoundTrip`: 3 records (incl. one with unicode + nested map) through helper → decode export file → deep-equal. `TestExport_FailureAttribution`: fail-variant → error contains binary name AND `boom: downstream exploded`. `TestExport_SpawnFailure`: `Cmd: ["magpie-definitely-missing-binary-xyz"]` → error at Start mentioning the name. `TestExport_CanceledCtx`: pre-canceled ctx → error, child reaped (no zombie — assert `cmd.ProcessState` via the returned error path, no sleep).

#### build/build_test.go
`TestBuild_MainGolden_Zero/Two`: render template → exact string match (inline `want`, ~10 lines each). `TestBuild_Hermetic`: `GOPROXY=off` env + `copyGoSum` → `Build(ctx, {Output: tmp/bin})` → run `<bin> --help` → exit 0. `TestBuild_FixtureMarker`: `Build(ctx, {With: ["buildmod@v0.0.0"], Replaces: {"buildmod": <abs testdata/buildmod>}, ...})` → run `<bin> --help` with `MAGPIE_BUILD_TEST_MARKER=tmp/marker` → marker file exists with ≥1 line. `TestBuild_BareWithRejected`: `With: ["./localmod"]` (no `@`) → error containing "@". All builds use `GOCACHE=<t.TempDir()>` isolation.

#### plugin/wasm/host_test.go
Six subtests via `encodeModule`: (a) happy — data-section bytes → `set_output` → assert output + captured log line/level; (b) version — `api_version` returns `2*10000` → `NewRunner` error contains "version"; missing export → error contains "not a gomagpie plugin"; (c) sock — guest calls REAL `sock_send` → nonzero errno returned to guest (assert via guest writing errno to memory + host readback, or `run` returning the errno); (d) env — `environ_sizes_get` → guest-visible (0,0); (e) fs — `path_open` on a data-section path → nonzero errno AND `os.Stat` on the path fails (no file created); (f) ctx — pre-canceled ctx → `Transform` returns error immediately. Table the three deny cases (c/d/e) where the harness allows; keep happy/version/ctx standalone (different assertions). After the suite, `filepath.Glob` the temp dir for unexpected files (deny tests must not create any).

#### cli/docs_test.go
`TestDocs_Phase3Surface`: read `README.md` → contains `scrape_url`, `crawl_site`, `extract_structured`, `get_cached_selectors`, `magpie serve`, `magpie build --with`, `gomagpie_api_version`; read `AGENTS.md` → does NOT contain `No plugins/WASM until core stable`. `strings.Contains` only — never exact-match prose.

### Data Model Notes
- Plain structs with json tags: assert fields directly (`out["run_id"]`, `stats` tuples), never via reflection.
- MCP `StructuredContent` numbers decode as `float64` — compare `out["pages_crawled"].(float64) == 3`, not `== int(3)`.
- `CrawlStats` order (pending/inflight/done/errors): assert the full 4-tuple every time.
- Exit codes asserted via the cobra error-to-code mapping helper, not subprocesses (except built binaries in `build` tests).
- Stderr assertions: `strings.Contains`, never exact equality (log prefixes evolve).
- JSONL equality: decode line-by-line + deep-equal, never raw byte-compare (encoder whitespace is an impl detail) — EXCEPT the cli tee test, which asserts byte-identity between the two files (same encoder, same stream: bytes MUST match).

### Success Criteria
- `go test ./...` exits 0 with >0 tests in `mcp` AND `plugin/exec` AND `plugin/wasm` AND `build` AND `core` (non-vacuous: `go test ./... -v 2>&1 | grep -c '^=== RUN'` grows by ≥30 vs Phase 2)
- `go test -race ./mcp/ ./plugin/... ./core/` exits 0 (new hooks + fakes must be race-clean)
- `go test ./...` passes with NO network, NO browser, NO keys, NO toolchain (verify: `env -u GOPROXY` default + `GOPROXY=off` build tests; unset all `GOMAGPIE_*` except test overrides)
- `go vet ./...` exits 0; `gofmt -l .` prints nothing
- Every deliverable from `plan/phase-3.md` §4 has at least one test file (see §4 list above)

### Expected File Structure at End
(Same tree as Test File List §4 — `core/registry_test.go` + `scrape/scrape_test.go` + `mcp/server_test.go` + `plugin/exec/exporter_test.go` + `build/build_test.go` + `plugin/wasm/host_test.go` + `cli/exporter_e2e_test.go` + `cli/docs_test.go` new; `crawl_test.go` + `sqlite_test.go` + `config_test.go` extended; `cmd_test.go` moved unmodified; `testdata/buildmod/` fixture. No `testutil` package, no extra harnesses.)
---

---

## 9. Run Commands

```bash
# Fast hermetic suite (every push — no network, no browser, no keys, no toolchain)
go test ./...

# Verbose with test counts (non-vacuous check: must grow by >=30 RUN lines vs Phase 2)
go test ./... -v 2>&1 | tee /tmp/phase3-tests.log; grep -c '^=== RUN' /tmp/phase3-tests.log

# Focused: the five proofs that carry the phase
go test ./core/ -run 'TestRegistry' -v
go test ./mcp/ -v
go test ./plugin/exec/ -v
go test ./plugin/wasm/ -v
go test ./build/ -v

# Deltas on touched packages (hooks, GetRun, config keys, moved CLI)
go test ./crawl/ ./store/ ./config/ ./cli/ ./scrape/ -v

# Race detector over the new concurrency + fakes (must be clean)
go test -race ./mcp/ ./plugin/... ./core/ ./crawl/ -v

# Full gate (mirrors Exit Criteria)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./... | grep . && echo CGO-LEAK || echo CGO-CLEAN
# golangci-lint run ./...  # CI or local install (same standing as Phase 2)
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service; `fakeExtractor` + in-memory MCP transports + helper-process + `encodeModule` wasm fixtures + `GOPROXY=off` hermetic builds + real temp-file SQLite (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to Milestone-3 behaviors (agent-driven tools, pollable crawls, exporter tee, custom builds, WASM sandbox)
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 11 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all §4 deliverables (`cli/` move→moved `cmd_test.go`, `scrape/`→`scrape_test.go`, `mcp/`→`server_test.go`, hooks→`crawl_test.go`, `GetRun`→`sqlite_test.go`, config keys→`config_test.go`, exec→`exporter_test.go`+`exporter_e2e_test.go`, `build/`→`build_test.go`, `testdata/buildmod` exercised by it, wasm→`host_test.go`, `go.mod` pins via `go doc`/CGO/cross-build commands, README/AGENTS→`docs_test.go`)
- [x] Every external/heavy dependency has a fake or mock equivalent — LLM→`fakeExtractor`, MCP peers→in-memory transports (real SDK path), subprocess→helper-process re-exec, WASM guests→`encodeModule` fixtures (real wazero runtime), remote modules→hermetic builds only (remote path is manual smoke), SQLite→real pure-Go temp DB (justified, §6), Chrome→no new tests
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — `GOMAGPIE_BASE_URL` override + `file://` + httptest + in-memory transports only; "What NOT to Test" bans live providers/remote modules/toolchains
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation (same as Phases 1–2): no live tier exists by design; non-hermetic paths are manual Task 3.7 smoke (fixed `:8089`, remote `--with`), never Go tests
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure with scope rationale, same as `phase-1-tests.md`/`phase-2-tests.md` §5
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — full `fakeExtractor` + `dialInMemory`/`callTool` + helper-process guard pasted verbatim in §8 (`encodeModule`/hermetic env by exact spec reference)
- [x] Run commands section is present — §9 with fast/focused/race/delta/full-gate commands
