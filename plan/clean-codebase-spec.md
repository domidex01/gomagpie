# Clean-Codebase Plan — gomagpie learns from flyscrape (rev 2)

Rev 2 (2026-09-17): full rewrite of the rev-1 spec (another LLM). Rev 1's diagnosis
was directionally right but its counts were wrong, its budgets were impossible under
its own non-goals, and two of its flagship steps described code that does not exist.
Every number and claim below is verified against the tree. Source comparison:
[`philippta/flyscrape@master`](https://github.com/philippta/flyscrape) (3,221 non-test
LOC, root engine pkg + 15 single-file modules) vs this repo.

Goal: adopt flyscrape's *shape* where it fits, keep every shipped feature, keep the
`scrape.Deps`/`vertical.Fetcher` seams the Wails GUI plan depends on. Smallest diffs
first; working code at every commit; no product deletions disguised as cleanup.

---

## 0. Verified baseline (replaces rev 1's numbers)

| Fact | Value |
|---|---|
| Non-test LOC / test LOC | 13,043 / 12,122 |
| Largest files | `crawl/crawl.go` 774 (one `Run` ≈ 500 lines), `store/sqlite.go` 628 |
| Package sizes | cli 2,036 · crawl 1,945 · clean 1,514 · mcp 1,120 · extract 1,066 · vertical 1,067 · fetch 981 · scrape 791 · store 628 · selector 698 · core 351 · config 332 · plugin 368 · build 135 |
| Import fan-in | extract 27 · clean 20 · store 20 · fetch 19 import sites (hub-and-spoke DTOs — expected, not a smell) |
| Engine count | **One** stage runner (`core/pipeline.go`) + two entry flows. `crawl.Run` *consumes* `core.Run` via fetch/clean/extract closures (`crawl/crawl.go:214–330`). `scrape.Run` is a single-URL flow that legitimately needs no frontier |
| Provider HTTP | **Already shared.** `postJSON` + shared `httpClient` + `runRepairLoop` live in `extract/extractor.go:45–158`. Provider files hold only prompt-shaping and response parsing |
| Rate-limit / browser-gate placement | `HostLimiters` (`crawl/ratelimit.go`) and `core.BrowserGate` sit in the fetch closure *above* the `Fetcher` seam, so they gate **both** static HTTP and rod-browser fetches (`crawl/crawl.go:144,253,309,328–333`). Retry (`FetchWithRetry`) likewise wraps `fetch.FetchResponse`, not `http.RoundTripper` |
| Flyscrape flaws (do NOT copy) | 500 fixed goroutines; queue-full drops URLs; no `context` anywhere; `panic`/`os.Exit` in library code; URL-exact visited set |

---

## 1. Verdicts on flyscrape's patterns

| Flyscrape pattern | Verdict for gomagpie |
|---|---|
| Capability interfaces (`TransportAdapter`, `RequestValidator`, …) — engine type-asserts in a loop | **Adopt as the designated upgrade path** for `core/registry.go` (S2). Not today: exactly one implementation per kind ships; the kind-string registry is documented YAGNI |
| Config self-unmarshal (`json.Unmarshal(cfg, mod)` per module) | Adopt *when* plugin modules take config. Not before |
| Transport chaining (retry/ratelimit/cache/proxy as `RoundTripper`) | **Reject.** Our limiter/retry/gate must also cover rod-browser fetches, which bypass `http.Transport` (verified §0). RoundTrippers would silently un-limit the browser path |
| Root-package engine (`import flyscrape` = whole API) | Adopt when Wails GUI work starts (S4, deferred with trigger) |
| One-file modules, ≤300 LOC | Good discipline; adopt per-file where a file is *incidentally* long. No package-wide budget mandates |
| Errors: per-page `Err`, dumb and local | **We already do this better.** Keep; never regress to log-and-continue silently |

---

## 2. Problems we will actually fix (all verified)

1. **Validation triplication.** The render/format/vertical switch lives in
   `scrape.Run` (trust boundary, correct) *and* is duplicated in `cli/scrape.go`,
   `cli/crawl.go`, `mcp/tools.go`. Adding one option = editing 4 files.
2. **Exit-code mapping scattered.** `fail(2|7|8, …)` decisions are spread across
   CLI command files; there is no single `exitFor(err)` site.
3. **`crawl/crawl.go` Run is a ~500-line function** with an 8-parameter helper
   (`healField`). Two independent reviews flagged it. Split, no behavior change.
4. **`core/registry.go` upgrade path is unnamed.** Its doc says "promote when a
   second consumer appears" but doesn't name the target shape.

---

## 3. Changes (each step: green → `go test ./... && go vet ./... && gofmt -l .` → commit)

### S1 — One validation function, one exit map (≈ −150 LOC, no behavior change)

- Extract the option switches into `scrape.ValidateOptions(o Options) error`
  (exported, in the `scrape` package — validation stays owned by the trust boundary).
- `scrape.Run` calls it first (unchanged behavior). CLI + MCP call it early and map
  the typed errors instead of re-implementing the switches.
- Add `exitFor(err) int` in `cli/root.go` as the ONLY place exit codes (2/7/8) are
  decided. Command files stop calling `fail` with hardcoded codes for these cases.
- Verify: CLI help unchanged; `--render bogus` still exits 2 before any I/O;
  missing-key still exits 7; quality still exits 8.

### S2 — Name the registry upgrade path (1 sentence, 0 LOC)

Append to the `core/registry.go` package doc: "when a second consumer per kind
appears, replace kind strings with flyscrape-style capability interfaces
(`TransportAdapter`, `ResponseReceiver`, …) — interfaces live in core, modules
import core, no cycle."

### S3 — Split `crawl/crawl.go` Run (moves only, no logic change)

- Introduce a `crawlContext` struct (db, limiters, gate, opts, schema, runID) to
  shrink `healField`'s 8-param signature; extract 2–3 named phases from `Run`
  (seed loop / stage wiring / result handling). Pure moves.
- Never mix moves with logic changes in one commit.
- Verify: full suite green; crawl goldens byte-identical; `test -tags browser` suite.

### S4 — DEFERRED: root re-export package (trigger: first Wails GUI commit)

When GUI work starts, add root package `magpie` re-exporting the GUI-facing surface
(`scrape.Deps/Options/Result/Run`, `crawl.Options`, `vertical.Fetcher`) so the Wails
port imports one package. Do NOT do this now — no consumer exists.

---

## 4. Explicitly rejected (so nobody re-proposes them)

| Idea (from rev 1) | Why rejected |
|---|---|
| M2 "collapse providers, shared `callJSON`, −300 LOC" | **Already implemented** (`extractor.go:45–158`). Step delivers zero |
| M6 "one hook-based Engine replaces pipeline+crawl+scrape" | "Three engines" is false — crawl consumes `core.Run`. Flyscrape's six hooks model the *transport path*; our clean/extract/heal stages are CPU-bound post-fetch processing with per-domain state, resume, quality gates. Forcing them through `ResponseReceiver` means a god-`Response` and shared run-scoped state — re-earning phase-2's concurrency lessons for zero user-visible gain |
| M7 "delete plugin/wasm+exec, collapse to one Exporter" | Deletes the shipped `magpie build --with` feature. Product decision, not cleanup |
| M4 "verticals become `Extractor`s; delete `vertical.Fetcher` + `Result.Vertical`" | Type error: verticals are zero-LLM by design (product positioning). The Fetcher seam exists so verticals sub-fetch behind an interface (tests fake it; GUI port stays thin) |
| "backoff/ratelimit/BrowserGate → TransportAdapter chain" | Verified wrong (§0): all three must gate the rod-browser path, which bypasses `http.Transport` |
| ≤4.5k LOC target; "no package >400, no file >250" budgets | Arithmetically impossible while keeping features (clean alone is 1,514 LOC of real logic; cli is 2,036 for 15 commands). Rev 1's own migration steps summed to −2,150 from 13k |
| File-count merges (clean 7→2, vertical 12→1, cli <400) | Cosmetic churn; goldens and tests don't care. Flyscrape's one-file modules work because each module is 50–150 LOC; our units are bigger because they do more |
| Dep deletions: bloom, keyring, go-readability | Bloom is the standard two-tier dedup front over SQLite truth (`frontier.go:68`) — benchmark-first if ever suspicious. Keyring→env-file is a product call. `go-readability` is *indirect* (trafilatura's); undropable |
| JS user-script runtime | Agreed with rev 1: YAGNI. We have YAML schemas + verticals |

---

## 5. Definition of done

- [ ] `go build ./...`, `go test ./...` (381+ RUN lines), `go vet`, `gofmt -l .` empty, `golangci-lint` clean.
- [ ] CGO cross-builds (windows/amd64, linux/amd64, darwin/arm64) pass.
- [ ] `testdata/` goldens byte-identical (clean drift EMPTY, fixtures 15/15).
- [ ] Option switches exist in exactly one function (`scrape.ValidateOptions`); `grep -rn "must be auto|static|browser" cli/ mcp/` finds only the call site.
- [ ] Exit codes 2/7/8 decided in exactly one function (`cli/root.go: exitFor`).
- [ ] `crawl.Run` split into named phases; `healField` takes `*crawlContext`; behavior identical.
- [ ] CLI help text, MCP tool surface, and all seams (`scrape.Deps`, `vertical.Fetcher`) unchanged.

## 6. Non-goals (honest)

- No feature deletions: WASM plugins, exec exporters, verticals, keyring, bloom stay.
- No engine rewrite, no module framework, no `modules/` tree.
- No JS scripting runtime, no TUI (phase-4 is separate), no framework upgrades.
- Visible behavior change: none. Target outcome: ~150 fewer duplicated LOC, one
  validation site, one exit-code site, a 500-line function cut into named phases.
