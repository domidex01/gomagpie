# Phase H — Testing: Actions DSL, Verticals Breadth, Watch, Crawl Status, Locale

**Scope:** `fetch/actions.go` (new: `Action`/`ParseActions`/rod `fetchWithActions`/`ScreenshotActions`), `fetch/http.go` (`FetchRequest.Lang` + Accept-Language merge), `fetch/rod.go` (Fetch/ScreenshotPage delegate refactor), `scrape/scrape.go` (`Options.Actions`/`.Lang`, ValidateOptions checks, browser dispatch), `scrape/watch.go` (new: `CheckForChange`/`WatchResult`/scoped-AllowPrivate webhook), `store/snapshots.go` (new: `snapshots` DDL/`PutSnapshot`/`LatestSnapshot`), `vertical/social.go` (reddit `.json`-first comment trees), `vertical/stackoverflow.go` + `vertical/trustpilot.go` + `vertical/og.go` (new), `vertical/registries.go` (+dockerhub, +huggingface), `cli/scrape.go` (`--action`/`--actions`/`--lang`), `cli/watch.go` (new), `cli/crawl.go` (`--status`), `mcp/tools.go` (`actions`/`lang` + screenshot-verb rejection), `testdata/vertical/*` fixtures
**Key Pattern:** **No new fakes for transports — the existing injection seams ARE the mocks.** `ParseActions` is a pure exported function (table-test it); verticals run against `fakeVerticalFetcher` (serves fixture bytes by URL substring AND records request order — exactly what the sanctioned reddit order-pin rewrite asserts); watch runs through `scrape.Deps` with a ~25-line mutable-body `watchFetcher` plus an httptest webhook sink, so the entire watch contract is hermetic; lang headers are witnessed at an echo origin; rod-only behavior (click/load-more, mid-flow screenshot bytes, rod extra-header) is fenced in `-tags browser`. The four H.7 gate tests — webhook scope under `GOMAGPIE_STRICT_SSRF=1`, lang control-chars, og never-auto-fires, MCP screenshot rejection — are all default-suite, milliseconds, no flags.
**Dependencies:** stdlib `testing`, `net/http/httptest`, `encoding/json`, `errors`, `strings`, `sync`, `sync/atomic`, `os`, `path/filepath`, `time`, `context` only — plus in-repo test helpers (`fakeVerticalFetcher`, `openScrapeDB`/`fakeDeps`/`fakeExtractor`, `resetGlobals`/`captureOutput`/`codeOf`/`rootCmd`, `dialInMemory`/`callTool`, `echoOrigin`). No test frameworks, no new test deps. **`TestToolCatalog` stays 12 — no MCP tool is added this phase; do NOT edit it.**

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As a user scraping a JS-gated page, I want `--action 'click #consent' --action 'wait-for .row:nth-child(30)'`, so that rows the static page never served land in my markdown | `fetch/actions_test.go` parser table; `scrape/scrape_test.go` validation rows; `scrape/actions_browser_test.go` (browser tag) load-more e2e; `mcp/agent_test.go` round-trip + screenshot rejection | every verb parses with rest-of-line args; errors name verb + line; `wait 99999` and `actions`+`static` → exit 2 pre-I/O; browser e2e markdown contains row 30 (static run does not); MCP `screenshot` verb → typed CLI-only error, zero browser launches |
| US-2 | As a zero-LLM user, I want typed extractors for reddit threads (with comment trees), stackoverflow, trustpilot, dockerhub, huggingface, and explicit `og`, so that structured records cost 0 tokens | `vertical/social_test.go` (rewritten order-pin + tree/caps), `stackoverflow_test.go`, `trustpilot_test.go`, `registries_test.go` appends, `og_test.go` | reddit `.json`-first = exactly 1 request (order recorded), fallback = `.json`→HTML (2 requests, that order), tree depth ≤ 10 / total ≤ 200; each extractor returns its fixture's fields; `MatchURL` misses og (OptIn holds); no-JSON-LD trustpilot → loud error |
| US-3 | As a competitor-monitor, I want `magpie watch` to store snapshots, print a word-diff and fire my webhook on change, so that repricing is a cron line | `store/snapshots_test.go` round-trip + same-second inserts; `scrape/watch_test.go` baseline→change sequence, webhook JSON at an httptest sink, failure-not-fatal, zero-LLM; `TestWatch_WebhookScope` (GATE 1) | first check `changed=false` + 1 snapshot row; after mutation `changed=true` + diff non-empty + sink got `{url,changed,old_hash,new_hash,diff}`; webhook 500 → check still succeeds, status says failed; loopback webhook delivered while loopback scrape target (real fetcher, `GOMAGPIE_STRICT_SSRF=1`) rejected with sink untouched; `--every 5s` → exit 2 |
| US-4 | As an operator, I want `magpie crawl --status <run_id>`, so that I check progress without leaving the terminal | `cli` status rows over a seeded temp store | unknown run_id → exit **4** + stderr names the id (handler-local `fail`, no exitFor change); known run_id → exit 0, stdout shows pending/inflight/done/errors matching a `CrawlStats` seed |
| US-5 | As a locale-sensitive user, I want `--lang fr-CA,fr;q=0.9` to set Accept-Language, so that localized pages come back localized | `fetch/profiles_test.go` echo rows (bare + with profile); `scrape/validate_test.go` control-char row (GATE 2); browser-tag rod extra-header row | header == the flag value with AND without `--header-profile chrome` (override, not append); empty lang keeps profile default; `$'en\r\nX-Evil: 1'` → `OptionsError`, zero bytes on the wire; rod path delivers the lang header (asserted alone — rod drops Profile headers, pre-existing) |

---

## 1. Component Mock Strategy

Phase type: **integration + pure logic** (G's successor; same tiering rationale). Mock strategy in one sentence: **nothing gets a new transport fake — `ParseActions` is table-tested pure, verticals run on the fixture-serving `fakeVerticalFetcher` (whose request-order log is the order-pin oracle), watch runs on `scrape.Deps` with the mutable `watchFetcher` (§3) and an httptest webhook sink, lang is witnessed at `echoOrigin`, and everything only rod can see lives in `-tags browser`.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `ParseActions` | External table (`fetch/actions_test.go`, `package fetch_test` — function is exported) | 7 verbs parse: `click sel`; `type sel text with spaces` (Sel+Text split on FIRST space only); `scroll 500\|top\|bottom`; `wait 500`; `wait-for sel`; `screenshot path with spaces`; `eval-js expr with spaces` — field meanings per verb pinned once; `#` comments + blank lines skipped; empty input → `[]Action{}, nil`; every error contains the **1-based line number** AND the offending verb: unknown verb, empty text/path/expr, non-numeric wait, `wait 30001` (cap named), bad scroll arg | US-1 |
| Rod actions executor (click/load-more) | `-tags browser`: `scrape/actions_browser_test.go` — httptest origin whose script appends 20 rows on `#load-more` click; drive through **`scrape.Run`** (the exported seam — `fetchWithActions` is unexported by design; see §5 note) | actions `[click #load-more, wait-for .row:nth-child(30)]` → markdown contains "row 30"; same origin WITHOUT actions → row 30 absent (the before/after pair proves causality); `Actions` + `Render:"static"` never reaches the browser (validation row covers it hermetically) | US-1 |
| Screenshot action mid-flow | `-tags browser`: `fetch` browser-tag rows via `fetch.ScreenshotActions(ctx, url, 0, 0, acts)` with `acts=[screenshot <tmp>/x.png]` | file exists, starts `\x89PNG`; a second action after `screenshot` still executes (capture is mid-flow, not terminal); nil-acts path == `ScreenshotPage` (delegation smoke) | US-1 |
| Actions validation (scrape) | `scrape/validate_test.go` + `scrape_test.go` APPEND — `errors.As(*OptionsError)` | `Actions=["click #x"]` + `Render:"static"` → OptionsError naming both; malformed DSL line surfaces from **ValidateOptions** (pre-I/O) as OptionsError with verb + line, not from the rod path; `Actions` + auto → passes validation | US-1 |
| MCP screenshot rejection (GATE 4) | `mcp/agent_test.go` APPEND — `dialInMemory`/`callTool`, hermetic | `scrape_url` with `actions:["screenshot /tmp/x"]` → tool error contains `CLI-only` (and `page_format screenshot` hint); **zero browser launches** (pre-flight: error text proves the boundary check fired — a rod launch attempt in the sandbox fails differently); `actions` + `lang` round-trip via the flex-shadow `TestIn_RoundTrip` APPEND; `TestToolCatalog` still 12 (untouched) | US-1 |
| Reddit `.json`-first + fallback order | `vertical/social_test.go` — `fakeVerticalFetcher` (request-order log is the oracle); **EDIT `TestRedditExtract_FallbackJSON` (:99) — the one sanctioned rewrite** | `.json` success: `len(order)==1` AND `order[0]` ends `.json` (record = post + `comments` from fixture); `.json` failure (404 body): `order == [jsonURL, htmlURL]` in that order, record = old HTML summary shape (title/score, no `comments` key); subreddit path: untouched existing tests stay green | US-2 |
| Reddit comment tree + caps | `social_test.go` APPEND — fixture `testdata/vertical/reddit-thread.json` (happy path) + **programmatically generated** shapes for caps (in-test `json.Marshal`, no fixture bloat) | happy path: nested `replies` → `comments[].replies` with author/score/body; 15-deep chain → depth truncates at 10; 250 flat comments → exactly 200 kept; `more`-object children ignored without error | US-2 |
| stackoverflow | NEW `vertical/stackoverflow_test.go` — `fakeVerticalFetcher` substring keys for both API URLs (question + answers); fixture `stackoverflow.json` per call | 2 requests in order (question, then answers); record has title/score/tags/body/owner + `answers[]` with `is_accepted`; match table: `/questions/{id}`, `/questions/{id}/slug` match; `/questions` bare, `/users/...` miss; non-2xx from fake → error naming status (fetchBytes contract) | US-2 |
| trustpilot | NEW `vertical/trustpilot_test.go` — fixture `trustpilot.html` (embedded `ld+json`) via fakeVerticalFetcher | business name, aggregateRating, `reviews[]` (author/ratingValue/datePublished/reviewBody) from the JSON-LD; page with NO `ld+json` → **error** (loud — never an empty map); match: `/review/{domain}` yes, `/about/...` no | US-2 |
| dockerhub + huggingface | `registries_test.go` APPEND — fixture JSONs via fakeVerticalFetcher | dockerhub: `/v2/repositories/{o}/{r}` URL asserted, star_count/pull_count mapped; huggingface: models path → `pipeline_tag` present, `datasets/` path → dataset record without it; match tables for both host shapes | US-2 |
| og generic (GATE 3) | NEW `vertical/og_test.go` — fixture `og.html` via fakeVerticalFetcher + pure match rows | extracts og:title/description/image/url/type, twitter:*, `<title>`, meta description, canonical, first `<h1>`; **`TestOG_NeverAutoFires`: `vertical.MatchURL("https://example.com/anything")` → false AND `Lookup("og").OptIn == true`** (same-commit regression guard); explicit `Lookup("og")` + Extract works on the fixture | US-2 |
| Snapshots store | NEW `store/snapshots_test.go` (`package store_test`, `store.Open(t.TempDir())` pattern) | PutSnapshot→LatestSnapshot round-trip (url/hash/markdown/checked_at); latest = max checked_at across 3 inserts; **two PutSnapshot calls back-to-back (same wall-clock second) both persist** — RFC3339Nano pins the PK; different URLs don't see each other; empty DB → `(Snapshot{}, false, nil)` | US-3 |
| `CheckForChange` happy path | `scrape/watch_test.go` (`package scrape` — reuses `openScrapeDB`/`fakeDeps`) + `watchFetcher` (§3) + `webhookSink` (§7) | check 1: `Changed=false`, `Diff=""`, 1 snapshot row, no webhook POST; flip body → check 2: `Changed=true`, `OldHash≠NewHash`, `Diff` non-empty, sink got exactly one POST with JSON `{url,changed:true,old_hash,new_hash,diff}`; snapshot rows == check count (2) | US-3 |
| Webhook failure semantics | `watch_test.go` — sink returns 500 | check still succeeds (nil error), snapshot stored, `WebhookStatus` contains `failed`; sink down (closed port) → same: result intact, status failed | US-3 |
| Watch is zero-LLM | `watch_test.go` — `fakeDeps` with counting `fakeExtractor`; pass `Options{Schema: …}` anyway | `fakeExtractor.total()==0` after the check (CheckForChange clears Schema — markdown-only is enforced, not hoped for) | US-3 |
| **`TestWatch_WebhookScope` (GATE 1)** | `watch_test.go` — full source in §7; two subtests | (a) loopback webhook sink **receives** the POST (webhook client's AllowPrivate works); (b) under `t.Setenv("GOMAGPIE_STRICT_SSRF","1")` (serial — no t.Parallel), `Deps.Fetcher = fetch.NewStaticFetcher()` (constructed AFTER Setenv) + loopback target → check errors, sink POSTs == 0: the scoped AllowPrivate did NOT leak into the scrape path | US-3 |
| watch CLI | `cli` appends — `resetGlobals`/`captureOutput`/`codeOf`/`rootCmd()`; exits + one `--once` e2e against a local origin (default suite: loopback allowed in tests) | `--every` missing → exit 2; `--every 5s` → exit 2 (floor named); `--every 30m --once` against an httptest origin → exit 0, first run prints nothing (no-change silence, diff-command precedent), second run after origin flip prints the diff; `--webhook` with the sink → delivery observed at exit-level too; help lists `--every/--once/--webhook` | US-3 |
| crawl `--status` | `cli` appends — seed temp store via `store.Open` + `BeginRun`/`Enqueue`/`MarkDone`/`MarkError`, then run through `rootCmd()` | unknown run_id → exit **4**, stderr contains the id (and NOT a new sentinel — `grep exitfor_test.go` untouched); seeded run (2 done, 1 error, 1 inflight, 3 pending) → exit 0, stdout shows all four counts; help row present | US-4 |
| Lang static path | `fetch/profiles_test.go` APPEND — `echoOrigin` (existing) | `Lang:"fr-CA,fr;q=0.9"` no profile → echo shows exactly that header; same + `Profile:"chrome"` → still fr-CA (override beats profile's en-US); `Lang:""` + chrome → en-US (profile default intact — existing rows prove the default side already) | US-5 |
| Lang validation (GATE 2) | `scrape/validate_test.go` APPEND | `Lang:"en\r\nX-Evil: 1"` → OptionsError (message names control chars); `Lang:"en"` passes; `Lang:"fr-CA,fr;q=0.9"` passes (BCP47-realistic value never rejected) | US-5 |
| Lang rod path | `-tags browser`: fetch browser-tag rows — echo origin + rod via `scrape.Run` browser render | `Accept-Language: fr-CA…` arrives at the origin through the REAL browser; assert ONLY the lang header (rod drops Profile headers today — pre-existing, don't pin that behavior here) | US-5 |
| Fetch delegation contract | existing `fetch/rod_smoke_test.go` (browser tag) — **runs UNTOUCHED** | suite-level proof: all existing rod rows pass byte-identical after the `Fetch = fetchWithActions(nil)` refactor; no new test asserts delegation internals (the smoke IS the contract) | US-1 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (default `go test ./...`) | Loopback `httptest` (echo, load-more origins, webhook sinks), `fakeVerticalFetcher` + `testdata/vertical/` fixture reads, `watchFetcher` (in-process), temp-dir SQLite, in-test sleeps ≤100ms — **no external network, no DNS, no browser** | <60s added | Every push; the only default gate |
| Browser (`go test -tags browser ./fetch/ ./scrape/`) | Real rod (click/type/eval-js, mid-flow screenshot bytes, rod extra-header), local origins only | ~15s | Manual/pre-release + network-capable CI job; joins `rod_smoke_test.go` in the gated family |
| Manual (not a test file) | Built binary: real reddit/trustpilot/stackoverflow/dockerhub/huggingface pages, a real webhook receiver (ntnfy/HA), a genuinely JS-gated site | minutes | Pre-release eyeball: fixtures prove contract shape; live sites prove the extractors still match reality — never a CI claim |

Fixture note (same rule as G): `testdata/vertical/` additions are **plan deliverables** — exactly that dir may appear in `git status --porcelain testdata/`; `testdata/clean` drift stays empty.

---

## 3. Fake / Mock Implementations

**No new transport fakes.** The seams already exist and are reused verbatim: `fakeVerticalFetcher` (vertical/vertical_test.go — substring-keyed bodies **plus the `order` request log**, which is what makes the reddit order-pin rewrite assertable), `openScrapeDB`/`fakeDeps`/`fakeExtractor` (scrape/scrape_test.go:73/96), `echoOrigin` (fetch/profiles_test.go:50), `resetGlobals`/`captureOutput`/`codeOf`/`rootCmd` (cli), `dialInMemory`/`callTool` (mcp/agent_test.go). The one load-bearing NEW helper is the mutable fetcher watch tests need — a fake `scrape.Deps.Fetcher` whose body flips between checks:

### `watchFetcher` — the only new helper worth full source (scrape/watch_test.go)

```go
// watchFetcher is a fake vertical.Fetcher for CheckForChange: a mutable body
// with a hit counter. Flip body between checks to drive the
// baseline → changed sequence without a second origin or any HTTP.
type watchFetcher struct {
	mu   sync.Mutex
	body string
	hits int
	err  error // when non-nil, Fetch fails (webhook-failure-path rows don't need it; SSRF rows use the real fetcher instead)
}

func (w *watchFetcher) Fetch(_ context.Context, req fetch.FetchRequest) (*fetch.FetchResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hits++
	if w.err != nil {
		return nil, w.err
	}
	return &fetch.FetchResponse{URL: req.URL, FinalURL: req.URL, StatusCode: 200,
		HTML: []byte(w.body), Headers: http.Header{}}, nil
}

func (w *watchFetcher) set(body string) { w.mu.Lock(); w.body = body; w.mu.Unlock() }
func (w *watchFetcher) CanHandle(fetch.FetchRequest) bool { return true }
func (w *watchFetcher) Close() error                      { return nil }
```

**Matches real call:** `CheckForChange` routes through `scrape.Run`, which fetches via `Deps.Fetcher` — the same seam production wires to `fetch.NewStaticFetcher()`. SSRF-scope rows deliberately do NOT use this fake (a fake would bypass the guard and prove nothing): they inject the real `fetch.NewStaticFetcher()` under `GOMAGPIE_STRICT_SSRF=1` (§7).

`webhookSink` (~15 lines, shown in situ in §7): an `httptest.Server` that records each POST's body into a mutex-guarded slice with an atomic count and a configurable status code (200 for delivery rows, 500 for the failure row).

---

## 4. Test File List

```
gomagpie/
├── fetch/
│   ├── actions.go                      # DELIVERABLE (impl)
│   ├── actions_test.go                 # NEW (package fetch_test): ParseActions table — 7 verbs, rest-of-line
│   │                                   #   splits, comments/blanks, line-numbered errors, wait cap, empty input
│   ├── actions_browser_test.go         # NEW (//go:build browser): ScreenshotActions — PNG magic via screenshot-action,
│   │                                   #   post-screenshot action still runs, nil-acts == ScreenshotPage; rod lang extra-header row
│   ├── profiles_test.go                # APPEND: Accept-Language echo rows (bare lang, lang×chrome override, default intact)
│   └── rod_smoke_test.go               # UNTOUCHED — green run after the delegation refactor IS the contract test
├── scrape/
│   ├── scrape.go / watch.go            # DELIVERABLES (impl)
│   ├── validate_test.go                # APPEND: actions×static OptionsError, malformed-line pre-I/O row,
│   │                                   #   TestValidateOptions_LangControlChars (GATE 2), lang pass rows
│   ├── scrape_test.go                  # APPEND: actions validation wiring (OptionsError type via errors.As),
│   │                                   #   screenshot page-format + actions validation OK row
│   ├── watch_test.go                   # NEW (package scrape): watchFetcher-driven baseline→change, webhook JSON at sink,
│   │                                   #   failure-not-fatal, zero-LLM pin, TestWatch_WebhookScope (GATE 1, §7 verbatim)
│   └── actions_browser_test.go         # NEW (//go:build browser): load-more origin e2e through scrape.Run —
│                                       #   actions markdown contains row 30, actionless run does not
├── store/
│   └── snapshots_test.go               # NEW (package store_test): round-trip, latest-wins, same-second double insert
│                                       #   (RFC3339Nano pin), per-URL isolation, empty-DB shape
├── vertical/
│   ├── social_test.go                  # EDIT TestRedditExtract_FallbackJSON (:99 — the one sanctioned rewrite:
│   │                                   #   .json-first = 1 request; fallback = .json→HTML order) + APPEND comment-tree
│   │                                   #   happy path (fixture) + caps (generated 15-deep chain, 250 flat)
│   ├── stackoverflow_test.go           # NEW: fixture extraction, 2-request order, match table, non-2xx error
│   ├── trustpilot_test.go              # NEW: JSON-LD extraction, no-JSON-LD → loud error, match table
│   ├── registries_test.go              # APPEND: dockerhub + huggingface extraction + API-URL asserts + match tables
│   └── og_test.go                      # NEW: og.html extraction table + TestOG_NeverAutoFires (GATE 3: MatchURL false,
│                                       #   OptIn true) + explicit Lookup/Extract row
├── cli/
│   ├── cmd_test.go                     # APPEND: --action inline rows, --actions file (tmp), --lang flag validation,
│   │                                   #   watch --every floor/--once e2e, crawl --status unknown→4 / seeded→counts
│   └── scrape_test.go                  # APPEND: --lang control-char → exit 2 pre-I/O (zero dials, closed-port origin)
├── mcp/
│   └── agent_test.go                   # APPEND: TestMCP_ScreenshotActionRejected (GATE 4), TestIn_RoundTrip rows for
│                                       #   actions+lang; TestToolCatalog stays 12 — DO NOT EDIT
├── testdata/vertical/                  # DELIVERABLES: reddit-thread.json, stackoverflow.json (per-call shapes),
│                                       #   trustpilot.html, dockerhub.json, huggingface.json, og.html — dated comment each
└── plan/phase-H-tests.md               # this file
```

Every deliverable in `plan/phase-H.md` §5 maps: `fetch/actions.go`→actions_test + browser rows + scrape validation; `fetch/http.go`→profiles_test echo rows; `fetch/rod.go`→rod_smoke untouched-green; `scrape/scrape.go`→validate_test + scrape_test; `scrape/watch.go`→watch_test; `store/snapshots.go`→snapshots_test; `vertical/*`→their four test files; `cli/scrape.go`/`cli/watch.go`/`cli/crawl.go`→cmd_test+scrape_test appends; `mcp/tools.go`→agent_test; `testdata/vertical/`→fixture-loading tests (a missing fixture file is a test failure, not a skip); `README.md`/`spec.md`→docs, no tests.

---

## 5. Test Helper Structure (Go — no `conftest.py`)

Per Phase A–G precedent: per-package test files, hermetic default suite, no integration flag (the `-tags browser` build tag IS the gate). Package choices this phase, each justified in its file header:

- `fetch/actions_test.go` — `package fetch_test` (ParseActions is exported; external keeps the table honest about the public API).
- `scrape/watch_test.go` — `package scrape` (internal): reuses `openScrapeDB`/`fakeDeps`/`fakeExtractor` unexported helpers, and `webhookSink`/`watchFetcher` live here.
- `scrape/actions_browser_test.go` — `package scrape_test` (external, browser tag): drives the exported `scrape.Run` seam only.
- `store/snapshots_test.go` — `package store_test` (matches sqlite_test.go).
- Everything else appends to existing files in their existing packages.

**Flagged edits — the legitimate categories (everything else is append-only or untouched):**

1. `vertical/social_test.go:99` — `TestRedditExtract_FallbackJSON`: REWRITTEN to pin the new order contract (`.json`-first success = exactly 1 request via the fake's `order` log; `.json`-failure fallback = `.json` request then HTML request). Sanctioned verbatim by phase-H.md §4 H.2 — "the ONLY pre-existing assertion this phase may touch." Every other reddit assertion stays untouched.
2. `mcp/agent_test.go` — `TestIn_RoundTrip` APPEND rows for `actions`/`lang` (Phase C pattern; an input-field addition, same legitimacy class). **`TestToolCatalog` is NOT edited — 12 tools before and after.**
3. `scrape/validate_test.go` — APPEND-only rows (actions×static, wait-cap surface, lang control-chars); the existing page-format/browser union rows are unchanged — **H adds no enum values**, so no union-message edits (contrast with G's flagged items 3–4).
4. `fetch/profiles_test.go` — APPEND echo rows; no existing row changes.

Anything ELSE failing to compile against new signatures is a plan gap — flag it, don't refactor silently.

Routing clarification to phase-H.md §5 (testability-driven, no production change): the load-more e2e lives in `scrape/actions_browser_test.go` and drives `scrape.Run`, because the rod executor `fetchWithActions` is unexported **by design** (no exported test surface on `RodFetcher`; `Fetch` takes no actions). `fetch/actions_browser_test.go` covers only the exported `ScreenshotActions` + the rod lang extra-header. If the implementation instead exports a rod actions entry, move the e2e down a level and say so in the PR description.

Scope rationale: everything function-scoped — per-test `httptest`, `t.TempDir()` stores and fixture files, `t.Setenv` for `GOMAGPIE_STRICT_SSRF` (the ONLY env this phase touches in tests; watch needs no new env vars), per-test atomics. No sleeps >100ms anywhere in the default suite: the watch "loop" is tested through `CheckForChange` directly; the CLI ticker is exercised only via `--once` e2e and flag validation. Signal handling (Ctrl-C) is a manual-tier claim — do not fake SIGINT in CI.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Reddit order proven by the fake's request log, not by error text | `fakeVerticalFetcher.order` == `[…json]` (success) / `[…json, …html]` (fallback) — length AND suffix asserts | The whole point of the rewrite is request ORDER; counters-over-error-text is the Phase D lesson, and the order log makes the two-contract pin mechanical |
| Caps tested with generated payloads, not fixtures | In-test `json.Marshal` of a 15-deep chain and 250 flat comments | Depth/total caps are boundary arithmetic — a fixture can only show one point; generated shapes sweep the exact boundaries (10/11, 200/201) without bloating testdata |
| `TestOG_NeverAutoFires` in the same file as extraction | `MatchURL("https://example.com/anything") == false` + `OptIn == true` next to the extraction table | The one silent-contract-breaker of the phase (every scrape growing a Record); same-commit pin per phase-H §2, and the match row is the only thing that can rot if someone "fixes" the matcher to be helpful |
| Webhook scope proven with the REAL fetcher, not the fake | Subtest (b) injects `fetch.NewStaticFetcher()` under `GOMAGPIE_STRICT_SSRF=1`; a `watchFetcher` would bypass SSRF entirely | The gate's claim is "AllowPrivate did not leak into the scrape path" — only the real guard can witness that; the fake would make the test pass vacuously, which is the exact trap the external validator flagged in the first plan draft |
| `STRICT_SSRF` fetcher constructed AFTER `t.Setenv` | Setenv first, then `fetch.NewStaticFetcher()` in the subtest body | `isTestBinary()` reads the env at options construction; constructing before Setenv freezes the relaxed mode and the rejection assert fails backwards |
| Same-second double-insert is an explicit store row | Two `PutSnapshot` calls back-to-back; assert both persist and Latest returns the second | RFC3339 second-precision + the PK is a guaranteed CI flake (validator item 4); the test pins Nano as the contract so a "tidy up the timestamp format" refactor fails here, not in a nightly |
| Watch loop tested at `CheckForChange`, CLI at exits + `--once` | No ticker sleeps; Ctrl-C is manual-tier | The loop's only logic is check→report, which CheckForChange owns; a real-ticker test buys flaky wall-clock assertions and proves nothing the `--once` e2e doesn't |
| Screenshot rejection asserted by error text + no-launch, not by browser absence | MCP `callTool` returns the typed CLI-only error; rod never launches (sandbox launch failure has different text) | The boundary check lives in `handleScrape` pre-flight — the error text IS the contract; a hermetic test that depends on rod failing would invert into a browser test |
| Lang override proven against chrome (not default) | chrome profile's `en-US` vs flag's `fr-CA` — the echo must show fr-CA | Overriding the already-fr default profile would prove nothing (same value by coincidence); chrome's distinct en-US makes the override unambiguous |
| `--once` e2e allowed on loopback in the default suite | Real `scrape.Run` against httptest origin, temp CacheDB | The relaxed test-binary SSRF mode makes loopback scrape targets work in tests (fetch/ssrf.go:138) — this is the one place we RIDE that behavior instead of fighting it, and it buys a real end-to-end watch run hermetically |
| Baseline RUN count, then strictly more | `tee /tmp/phaseH-baseline.log` BEFORE writing (testing.md non-vacuous rule) | One sanctioned edit exists (reddit order-pin); the RUN delta proves the rewrite shrank nothing and appends landed |

---

## 7. Example Test Case

```go
// scrape/watch_test.go — GATE 1. Package scrape: reuses openScrapeDB/fakeDeps.
// The scope proof is the subtlest test of the phase: under plain `go test` the
// default fetcher relaxes AllowPrivate (fetch/ssrf.go:138), so the rejection
// half MUST pin GOMAGPIE_STRICT_SSRF=1 or it fails backwards — the loopback
// target would pass the guard and the test would "fail" for the opposite
// reason it expects (external-validator lesson, phase-H §2).

package scrape

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gomagpie/fetch"
)

// webhookSink records POST bodies; status codes start at 200, flippable to 500.
type webhookSink struct {
	mu     sync.Mutex
	bodies []string
	srv    *httptest.Server
	status *int32 // atomic status code the handler answers with
}

func newWebhookSink(t *testing.T) *webhookSink {
	t.Helper()
	ws := &webhookSink{}
	ws.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck // test sink
		ws.mu.Lock()
		ws.bodies = append(ws.bodies, string(b))
		ws.mu.Unlock()
		w.WriteHeader(int(atomic.LoadInt32(ws.status)))
	}))
	t.Cleanup(ws.srv.Close)
	return ws
}

func (ws *webhookSink) posts() []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]string(nil), ws.bodies...)
}

// TestWatch_WebhookScope — H.7 gate test 1. Two proofs in one name:
// (a) the webhook client's AllowPrivate reaches a loopback sink;
// (b) the scrape path keeps the default guard even inside the SAME
// CheckForChange that carries the AllowPrivate webhook client.
func TestWatch_WebhookScope(t *testing.T) {
	t.Run("webhook reaches loopback sink", func(t *testing.T) {
		db := openScrapeDB(t)
		wf := &watchFetcher{body: "<p>price is 10</p>"}
		sink := newWebhookSink(t)
		deps := Deps{DB: db, Fetcher: wf}
		res, err := CheckForChange(context.Background(), deps,
			"https://shop.example.com/p/1", Options{Webhook: sink.srv.URL})
		if err != nil {
			t.Fatalf("baseline check: %v", err)
		}
		if res.Changed {
			t.Fatal("baseline must not report change")
		}
		if n := len(sink.posts()); n != 0 {
			t.Fatalf("no-change check must not fire the webhook, got %d POSTs", n)
		}

		wf.set("<p>price is 20</p>") // the reprice
		res, err = CheckForChange(context.Background(), deps,
			"https://shop.example.com/p/1", Options{Webhook: sink.srv.URL})
		if err != nil {
			t.Fatalf("change check: %v", err)
		}
		if !res.Changed || res.Diff == "" || res.OldHash == res.NewHash {
			t.Fatalf("change not reported: changed=%v diff=%q", res.Changed, res.Diff)
		}
		posts := sink.posts()
		if len(posts) != 1 {
			t.Fatalf("want exactly 1 webhook POST, got %d", len(posts))
		}
		var payload struct {
			URL     string `json:"url"`
			Changed bool   `json:"changed"`
			OldHash string `json:"old_hash"`
			NewHash string `json:"new_hash"`
			Diff    string `json:"diff"`
		}
		if err := json.Unmarshal([]byte(posts[0]), &payload); err != nil {
			t.Fatalf("webhook payload not the contracted JSON: %v", err)
		}
		if payload.URL != res.URL || !payload.Changed ||
			payload.OldHash != res.OldHash || payload.NewHash != res.NewHash {
			t.Errorf("payload mismatch: %+v vs result %+v", payload, res)
		}
		if res.WebhookStatus != "sent" {
			t.Errorf("WebhookStatus = %q, want sent", res.WebhookStatus)
		}
	})

	t.Run("scrape path keeps default guard while webhook stays loopback-capable", func(t *testing.T) {
		t.Setenv("GOMAGPIE_STRICT_SSRF", "1") // serial test: no t.Parallel anywhere above
		db := openScrapeDB(t)
		sink := newWebhookSink(t)

		// The REAL fetcher — constructed AFTER Setenv so isTestBinary() sees
		// the strict pin. A watchFetcher here would bypass the guard and
		// make this subtest vacuous.
		deps := Deps{DB: db, Fetcher: fetch.NewStaticFetcher()}
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<p>hi</p>")) //nolint:errcheck // test server
		}))
		defer origin.Close()

		_, err := CheckForChange(context.Background(), deps, origin.URL, Options{Webhook: sink.srv.URL})
		if err == nil {
			t.Fatal("loopback scrape target must be rejected under STRICT_SSRF — AllowPrivate leaked into the scrape path")
		}
		if !strings.Contains(err.Error(), origin.URL) && !strings.Contains(err.Error(), "private") {
			t.Errorf("rejection should name the guard or the URL, got: %v", err)
		}
		if n := len(sink.posts()); n != 0 {
			t.Errorf("failed scrape must not fire the webhook, got %d POSTs", n)
		}
	})
}
```

Notes for the executor: `openScrapeDB` and `Deps` are the existing in-package helpers (`scrape/scrape_test.go:73/96`) — reuse, don't restate. `atomic` needs importing for `webhookSink.status` (declared `int32` + `atomic.StoreInt32` in `newWebhookSink` — 3 lines left as an exercise to keep the listing tight). The failure row (`TestWatch_WebhookFailureNotFatal`) reuses the sink with `atomic.StoreInt32(ws.status, 500)` and asserts `err == nil`, snapshot stored, `strings.Contains(res.WebhookStatus, "failed")`. The zero-LLM row passes `Options{Schema: <any>}` and asserts `fakeExtractor.total() == 0`.

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase H of `gomagpie` — rod actions DSL, vertical extractor breadth, `magpie watch`, `crawl --status`, and `--lang`. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-H.md` (design — §2 failure modes and §4 per-task rules are test contracts), `plan/phase-H-tests.md` (this suite's contract — §3 helper, §5 flagged edits, §6 decisions, §7 gate source), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test. Production code may or may not exist yet — write tests against the frozen names in phase-H §§3–5 and this plan §1; if a name diverges, rename the TEST to the implementation ONLY when the behavior matches the plan, else flag the gap. Package conventions (state each in its header): `fetch/actions_test.go` is `package fetch_test` (ParseActions is exported); `scrape/watch_test.go` is `package scrape` (internal — reuses `openScrapeDB`/`fakeDeps`); `scrape/actions_browser_test.go` is `package scrape_test` with `//go:build browser`; `store/snapshots_test.go` is `package store_test` (matches sqlite_test.go).

### What This Project Is
Go 1.26 CLI web scraper (`magpie`, module `gomagpie`): fetch → clean → extract. Phase H adds: a line-grammar actions DSL executed by rod before capture (`click|type|scroll|wait|wait-for|screenshot|eval-js`, rest-of-line args, wait cap 30000ms, `screenshot` verb CLI-only at the MCP boundary); five vertical extractor additions (reddit `.json`-first comment trees with depth-10/total-200 caps, stackoverflow, trustpilot, dockerhub, huggingface) plus OptIn `og`; a `watch` change-monitor (snapshots in SQLite with RFC3339Nano `checked_at`, word-diff, single-attempt webhook whose client is scoped-AllowPrivate); `crawl --status` CLI parity; and `--lang` (Accept-Language override + control-char rejection). Tests are hermetic by default: no external network, no DNS, no browser in `go test ./...`, no sleep >100ms.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | actions DSL: click through JS gates before capture | parser table + validation rows + browser e2e + MCP rejection | verbs parse; errors name verb+line; `wait 99999` and actions×static → exit 2; row 30 in markdown only with actions; MCP `screenshot` → CLI-only error, no browser launch |
| US-2 | zero-LLM extractors: reddit trees, SO, trustpilot, dockerhub, HF, og | order-pin rewrite + 4 new extractor files + og gate | `.json`-first = 1 request, fallback = `.json`→HTML; caps 10/200 hold; fixtures extract; `MatchURL(og)` false + OptIn true; no-JSON-LD trustpilot errors loudly |
| US-3 | watch: snapshots, diff, webhook, cron-friendly | snapshots store tests + watch_test sequence + GATE 1 | baseline `changed=false`; mutation → diff + exactly 1 POST `{url,changed,old_hash,new_hash,diff}`; sink 500 → check still succeeds; loopback webhook delivered AND loopback scrape target rejected under `GOMAGPIE_STRICT_SSRF=1` with sink untouched; `--every 5s` → exit 2 |
| US-4 | crawl `--status` | cli rows over seeded store | unknown → exit 4 + id in stderr (handler-local, no exitFor change); seeded → pending/inflight/done/errors in stdout |
| US-5 | `--lang` locale | echo rows + control-char GATE + rod browser row | header == flag value bare and over chrome; `\r\n` payload → OptionsError pre-I/O; rod path delivers lang header (asserted alone) |

### Why There Are No New Fakes
- The injection seams already exist: `scrape.Deps.Fetcher` (watch), `vertical.Fetcher` + `fakeVerticalFetcher` (verticals — its `order` log is the request-order oracle), `echoOrigin` (lang), `dialInMemory`/`callTool` (MCP). The only new helper is `watchFetcher` (§3 verbatim) — a mutable-body fake for the baseline→change sequence — plus the ~15-line `webhookSink`.
- SSRF-scope rows deliberately use the REAL `fetch.NewStaticFetcher()` under `t.Setenv("GOMAGPIE_STRICT_SSRF","1")` (constructed AFTER Setenv; serial — no `t.Parallel` in the file): a fake fetcher would bypass the guard and make the gate vacuous. Under plain `go test` the guard relaxes AllowPrivate (fetch/ssrf.go:138) — that relaxation is also why the `--once` CLI e2e may scrape a loopback origin hermetically.

### What NOT to Test
- Don't test go-rod interaction internals — the load-more markdown delta and the PNG magic bytes ARE the proofs; pixel/geometry assertions are banned; `rod_smoke_test.go` stays untouched (its green run IS the delegation contract).
- Don't test live reddit/stackoverflow/trustpilot/dockerhub/huggingface — fixtures with dated comments are the contract; live-shape drift is a manual-tier claim.
- Don't fake SIGINT/Ctrl-C — signal handling is a manual-tier row; the loop is tested through `CheckForChange` + `--once`.
- Don't test README/spec prose; don't add a shared testutil package (helpers live at point of use).
- Don't edit `TestToolCatalog` (still 12 — no tool added) or any existing test outside the §5 flagged list (exactly one: `TestRedditExtract_FallbackJSON`).
- Don't add env vars — watch/actions/lang need none; `GOMAGPIE_STRICT_SSRF` is the only env any test touches.
- Don't grow `testdata/` beyond `testdata/vertical/` additions; `testdata/clean` drift must stay empty.

### Critical: The One Load-Bearing Helper
`watchFetcher` — full source in this plan §3; copy verbatim into `scrape/watch_test.go`. Then copy §7's `TestWatch_WebhookScope` (including `webhookSink`) verbatim — it is the H.7 gate artifact; the STRICT_SSRF subtest ordering (Setenv before fetcher construction) is load-bearing, as is the absence of `t.Parallel`.

### Test Files to Create / Edit

```
fetch/actions_test.go          # NEW (~1 table + ~6 rows): ParseActions — 7 verbs, rest-of-line splits,
                               #   comments/blanks, line-numbered errors (unknown verb/empty args/non-numeric
                               #   wait/wait 30001/bad scroll), empty input → empty,nil
fetch/actions_browser_test.go  # NEW (browser tag, ~3): ScreenshotActions PNG magic, post-screenshot action runs,
                               #   nil-acts == ScreenshotPage; rod lang extra-header (assert lang header ONLY)
fetch/profiles_test.go         # APPEND (~2): Accept-Language echo — bare lang; lang×chrome override; (default
                               #   side already covered by existing rows)
scrape/validate_test.go        # APPEND (~4): actions×static OptionsError; malformed DSL line pre-I/O;
                               #   TestValidateOptions_LangControlChars (GATE 2); lang pass rows
scrape/scrape_test.go          # APPEND (~2): actions validation wiring via errors.As; screenshot format + actions OK
scrape/watch_test.go           # NEW (~7): watchFetcher baseline→change (sink JSON contract), failure-not-fatal
                               #   (500 + closed port), zero-LLM pin, TestWatch_WebhookScope (GATE 1, §7 verbatim)
scrape/actions_browser_test.go # NEW (browser tag, ~1): load-more origin — actions markdown has row 30, actionless doesn't
store/snapshots_test.go        # NEW (~5): round-trip, latest-wins over 3 inserts, same-second double insert,
                               #   per-URL isolation, empty-DB shape
vertical/social_test.go        # EDIT TestRedditExtract_FallbackJSON (flagged — new order contract) + APPEND (~3):
                               #   comment-tree fixture, depth-10 cap (15-chain generated), total-200 cap (250 generated)
vertical/stackoverflow_test.go # NEW (~3): fixture extraction, 2-request order, match table, non-2xx error
vertical/trustpilot_test.go    # NEW (~3): JSON-LD extraction table, no-JSON-LD → error, match table
vertical/registries_test.go    # APPEND (~4): dockerhub + huggingface extraction, API-URL asserts, match tables
vertical/og_test.go            # NEW (~3): og.html extraction table, TestOG_NeverAutoFires (GATE 3), explicit extract
cli/cmd_test.go                # APPEND (~7): --action inline (bogus verb → 2, zero dials via closed-port origin),
                               #   --actions tmp-file valid rows, watch --every missing/5s → 2, watch --once e2e
                               #   (loopback origin, temp CacheDB, silence then diff), crawl --status unknown→4
                               #   / seeded→4 counts + help rows
cli/scrape_test.go             # APPEND (~1): --lang control-char → exit 2 pre-I/O (zero dials)
mcp/agent_test.go              # APPEND (~3): TestMCP_ScreenshotActionRejected (GATE 4); TestIn_RoundTrip rows
                               #   for actions + lang; scrape_url union-error text if pinned anywhere
```

### Per-File Coverage Guidance (beyond the table)
- **actions_test.go**: `type sel multi word text` splits on the FIRST space only (`Sel="sel"`, `Text="multi word text"`); assert `Action` field-by-field per verb (Verb/Sel/Text/Ms meanings pinned once); every error row asserts BOTH the line number substring AND the verb substring.
- **watch_test.go**: snapshot count assertion after each check (rows == checks); the change payload asserts all five JSON keys, not just `changed`; `WebhookStatus` values pinned exactly: `""` (no webhook/no change), `"sent"`, prefix `"failed"`.
- **social_test.go**: build the deep chain and flat-250 shapes with a tiny `redditChild(author string, replies []any)` recursive helper; the caps asserts are `maxDepth ≤ 10` and `len(comments) == 200` — walk the returned map, don't trust the fixture.
- **stackoverflow_test.go**: fake keys `"api.stackexchange.com/2.3/questions/123?"` and `"/questions/123/answers"`; assert `order` has question before answers; `is_accepted` maps from the fixture's `score`/`accepted_answer_id` pairing however the implementation defines it — assert what phase-H §4 H.2 freezes (accepted flag present and true for exactly one answer).
- **cmd_test.go crawl --status seeding**: `db.BeginRun(id, "crawl")` + `db.Enqueue(id, []string{x1..x7}, 0)` + `MarkDone`×2 + `MarkError`×1 — leave one inflight by claiming it (`db.Claim`) and three pending untouched; stdout asserts the four counts, not formatting.
- **Anti-vacuous rule**: every "must not reach X" claim pairs an error/exit assert with a counter (sink POSTs == 0, fakeExtractor.total() == 0, fetcher hits frozen) — error text alone never proves sequencing.

### Data Model Notes (Go)
- `errors.Is`/`errors.As` in-process (`*scrape.OptionsError`); `codeOf` + stderr at the CLI edge; `CheckForChange` returns `WatchResult` (fields asserted directly, it's a plain struct).
- All env via `t.Setenv` (`GOMAGPIE_STRICT_SSRF` only); serial tests where Setenv is used.
- Fixture files carry a one-line `// recorded 2026-09-19 from <source> (shape fixture — not a live capture)` comment; a missing fixture file fails the test (deliverable, not optional).
- Timestamps in snapshots tests: never `time.Sleep` to cross a second — RFC3339Nano makes same-second inserts VALID; assert ordering via returned `checked_at` values.

### Success Criteria
- `go test ./...` exits 0; RUN count strictly exceeds the pre-phase baseline (`tee /tmp/phaseH-baseline.log` FIRST; record the post-H number)
- `go test -tags browser ./fetch/ ./scrape/ -run 'Actions|Screenshot|Lang' -v` passes manually with Chrome present
- `git status --porcelain testdata/` shows ONLY `testdata/vertical/` additions; `testdata/clean` drift empty
- `git diff --stat` on test files touches `vertical/social_test.go` existing funcs ONLY at `TestRedditExtract_FallbackJSON`; `mcp/agent_test.go` `TestToolCatalog` byte-identical
- No default-suite test sleeps >100ms, launches a browser, or dials beyond 127.0.0.1
- Full gates green: `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, `go test -race ./fetch/ ./scrape/ ./store/ ./vertical/ ./cli/`

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -v 2>&1 | tee /tmp/phaseH-baseline.log; grep -c '^=== RUN' /tmp/phaseH-baseline.log

# Fast hermetic suite (every push — loopback only, no browser, no DNS)
go test ./...

# New-test count vs baseline
go test ./... -v 2>&1 | tee /tmp/phaseH-tests.log; grep -c '^=== RUN' /tmp/phaseH-tests.log

# Focused per task (mirrors phase-H sanity checks)
go test ./fetch/ -run 'TestParseActions' -v                                          # H.1 parser table
go test ./scrape/ -run 'TestValidateOptions|TestScrape_Actions' -v                   # H.1 validation rows
go test ./mcp/ -run 'TestMCP_ScreenshotActionRejected|TestIn_RoundTrip' -v           # H.1 GATE 4 + round-trips
go test ./vertical/ -run 'TestReddit' -v                                             # H.2 order-pin + trees/caps
go test ./vertical/ -run 'TestStackOverflow|TestTrustpilot|TestDockerHub|TestHuggingFace|TestOG' -v  # H.2 new extractors
go test ./store/ -run 'TestSnapshots' -v                                             # H.3 store rows
go test ./scrape/ -run 'TestWatch' -v                                                # H.3 watch + GATE 1
go test ./cli/ -run 'TestWatch|TestCrawlStatus|TestScrape_Lang|TestAction' -v        # H.3–H.5 CLI surfaces
go test ./fetch/ -run 'TestLang|TestAcceptLanguage' -v                               # H.5 echo rows
go test ./scrape/ -run 'TestValidateOptions_LangControlChars' -v                     # H.5 GATE 2
go test ./vertical/ -run 'TestOG_NeverAutoFires' -v                                  # H.2 GATE 3

# All four H.7 gates in one line (the go/no-go readout)
go test ./scrape/ -run 'TestWatch_WebhookScope|TestValidateOptions_LangControlChars' -v && \
go test ./vertical/ -run 'TestOG_NeverAutoFires' -v && \
go test ./mcp/ -run 'TestMCP_ScreenshotActionRejected' -v && echo GATES-GREEN

# Browser-gated (Chrome present; manual / CI-with-browser only)
go test -tags browser ./fetch/ ./scrape/ -run 'Actions|Screenshot|Lang' -v

# Race on touched packages (testing.md)
go test -race ./fetch/ ./scrape/ ./store/ ./vertical/ ./cli/

# Discipline checks
git status --porcelain testdata/                              # ONLY testdata/vertical/ may appear
git status --porcelain testdata/clean                         # must print nothing
git diff --name-only HEAD -- 'mcp/agent_test.go'              # TestToolCatalog untouched (verify via git diff content)

# Full gate (mirrors phase-H exit criteria)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
```
