# Phase D — Testing: Crawl Scope + Sitemap + Security

**Scope:** `crawl/scope.go` (new: `Scope`/`CompileScope`/`Allows`/`SkipExtension`), `crawl/frontier.go` + `crawl/crawl.go` (Scope signature, 5 option fields, scope-filtered warn-and-proceed seeding, `LogFetch`), `crawl/sitemap.go` (budgeted BFS depth≤5, skip+truncate, union robots seeds), `crawl/robots.go` (transport swap), `fetch/ssrf.go` (new: `SSRFOptions`/`ErrPrivateAddress`/`ValidateURL`/hatch) + `fetch/http.go` (`GuardedTransport`, `proxyFunc`, pre-check, redirect validation), `store/sqlite.go` (3 columns + migration + `LogFetch` + `RunInfo`), `scrape/scrape.go` (`LogFetch`), `mcp/tools.go` (5 `CrawlIn` fields + usage counters + `ScrapeIn` doc), `cli/crawl.go` (5 flags + early validation + exit-2 case) + `cli/shared.go` (`scrapeExit` exit-2 case)
**Key Pattern:** Pure tables for scope/glob/SSRF-unit decisions; fake DNS lookup (never real resolvers); hit-counter `httptest` origins proving pre-dial rejection with zero packets served; generated sitemap bodies (never fixtures); temp-dir SQLite including a raw-SQL pre-D schema for the migration test; in-memory MCP server for the new `crawl_site` fields; explicit strictness in every security test (no test depends on the test-binary hatch).
**Dependencies:** stdlib `testing`, `net/http/httptest`, `net/httputil` (in-test reverse proxy only), `compress/gzip` (in-test generation only), `net`, `net/netip`, `sync/atomic`, `os`, `path/filepath`, `strings`, `encoding/json` only — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an operator, I want `--path-prefix/--include/--exclude/--allow-subdomains/--no-sitemap` to bound a crawl, so that docs-only crawls don't burn `--max-pages` on API pages, assets, and foreign hosts | `crawl/scope_test.go` (caps + Allows matrix + ext table, all post-`Canonicalize`) + `TestLinks_*` scope rows + CLI bad-glob exit 2 + `--no-sitemap` zero-fetch + seed-scoping test | only matching links enqueued; exclude wins; `page.pdf`/`font.woff2` never enqueued; bad glob → exit 2 pre-I/O; sitemap hosts hit-count == 0 with `--no-sitemap`; out-of-prefix sitemap URLs dropped before enqueue |
| US-2 | As an operator, I want `map` to survive nested indexes, entities, sloppy robots, dead children, and slow sitemaps, so that one 500 never voids a 10k-URL listing | `crawl/sitemap_test.go` additions (depth-3+ chain, `&amp;` entity, tolerance rows, poisoned-child partial, budget-partial, both gzip shapes) | exact `<loc>` sets; `SITEMAP:`/leading-space/`# comment` seeds all fetched; poisoned child → siblings + `truncated:true`; budget expiry → partial + `truncated:true`, nil error; parent-cancel → error |
| US-3 | As a user, I want private/loopback/metadata targets rejected before dialing (exit 2) and `file://` gated, so that a malicious link or redirect can never make the binary touch the internal network | `fetch/ssrf_test.go` (sentinel + `ValidateURL` table on fake resolver + strict-fetcher pre-dial + redirect abort + file gate) + CLI exit-2 tests + 50 MB bomb cap | `errors.Is(err, ErrPrivateAddress)`; poisoned-server hit-counter == 0; redirect chain aborts naming the hop; `http://127.0.0.1:9/` via CLI → exit 2; bomb yields exactly `50<<20` bytes |
| US-4 | As an operator, I want `fetch_pages/fetch_bytes/fetch_ms` next to LLM tokens on every run row (including pre-D databases), so that cost and volume questions have one answer | `store/sqlite_test.go` (migration + `LogFetch` round-trip + `GetRun` fields) + `scrape`/`crawl` counter tests + MCP usage-fields test | pre-D schema gains 3 zeroed columns on `Open`; one scrape → `fetch_pages==1`, `fetch_bytes==len(body)`; old DBs open clean; `crawl_site` usage contains the 3 counters |
| US-5 | As an operator behind a proxy, I want `GOMAGPIE_PROXY` honored (with `NO_PROXY` bypass) by every fetch including robots, so that egress policy holds without per-command flags | `fetch/proxy_test.go` (hit / bypass / invalid) + robots-via-proxy test | proxied origin arrives with proxy-hit == 1; `NO_PROXY` host → 0; garbage value → loud pre-I/O error; robots fetch honors the same proxy |

---

## 1. Component Mock Strategy

Phase type: **Service/pipeline core + security boundary + schema migration** (Phase-A/B/C precedent, plus a fail-closed transport). Mock strategy in one sentence: **pure tables for every decision function (`Allows`, glob caps, `ValidateURL`); a scripted DNS lookup instead of real resolvers; hit-counter `httptest` origins (including an in-test reverse proxy and a gzip bomb) for transport behavior; temp-dir SQLite with a hand-built pre-D schema for migration; the existing in-memory MCP harness for tool fields; explicit strictness everywhere — the test-binary hatch is load-bearing for the OLD suite only and no new test may depend on it.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `CompileScope` + glob caps | No mock (pure) — tables | `**`-over-4 → error; 1025-char glob → error; uncompilable glob → error; prefix normalized to leading `/`; valid set compiles | US-1 |
| `Scope.Allows` matrix | No mock (pure) — parsed-URL tables, inputs pre-`Canonicalize`d in the test to lock the matching basis | exact-host ok; sub blocked by default, allowed with flag (+`evil-example.com` negative — suffix-without-dot-boundary must NOT match); prefix in/out (+empty-path = `/`); include-any-of; exclude-wins-over-include; `file://` empty-host equality preserved | US-1 |
| `SkipExtension` | No mock — extension table | all ~25 exts skipped (mixed case: `A.PDF`); `page`, `/docs`, `.html`, `feed.xml` kept; query strings ignored (`x.pdf?dl=1` still skipped — ext of path, not URL) | US-1 |
| `Frontier.ExtractLinks(Scope)` | Real frontier + `openCrawlDB` + static HTML (existing `TestLinks_SameHostAndDepth` pattern, updated call sites) | Mixed page (in-scope/out-prefix/excluded/pdf/subdomain/foreign) → only survivors enqueued; depth gate unchanged; deterministic order | US-1 |
| Crawl seeding expansion | `fakeSitemapFetcher` (existing, has status/err support) serving robots+index+urlset | In/out-of-prefix expansion → only in-prefix enqueued; poisoned expansion → seed crawled anyway + stderr warning; `NoSitemap` → zero sitemap fetches (assert via `follows()` log) | US-1, US-2 |
| CLI scope flags | `writeFileSite` (file://) + bad-glob strings + `codeOf` | Bad glob → exit 2 pre-I/O; 5 flags present in help text; `--path-prefix` end-to-end on file site | US-1 |
| `ListSitemapURLs` BFS | `fakeSitemapFetcher` + `gzipBytes` + `locSet` (all existing) + inline-string fixtures | Depth-3 chain union; `&amp;` decoded; tolerance rows (upper/space/comment) all fetched; poisoned child → partial + `truncated:true`; empty+errors → first error naming URL; `TestSitemap_Garbage` UNCHANGED (still passes) | US-2 |
| Sitemap budget | Fake fetcher with sleep-per-child + short ctx (NOT the 25s const — inject cancellation via parent ctx) | Budget-shaped expiry → partial + `truncated:true`, nil error; parent cancel → `ctx.Err()` (distinguish via `WithTimeoutCause` semantics — assert the two paths differ) | US-2 |
| Gzip both shapes | `gzipBytes` raw-sniff (existing) + NEW real `Content-Encoding: gzip` via `httptest` + real `NewStaticFetcher` | Both decode to identical URL sets (proves sniff path AND transparent-decode path) | US-2 |
| `ValidateURL` | `fakeLookup` (§3) — scripted host→IPs, error injection; explicit `SSRFOptions{}` always | Loopback/literal/private/link-local/multicast/unspecified rejected; `localhost` + metadata names rejected (incl. trailing-dot FQDN); public literal + public-DNS ok; DNS-private (rebind shape) rejected; DNS-error propagates; userinfo/empty-host/bad-scheme rejected; `file://` gated by `AllowFile` | US-3 |
| Strict fetcher pre-dial | Real `NewStaticFetcherWithOptions(strict)` (§6) against `httptest` URL + atomic hit counter | Error satisfies `errors.Is(ErrPrivateAddress)` AND server hits == 0 (rejection happened before dial, not after a failed handshake) | US-3 |
| Redirect validation | `httptest` A (public) → 302 → `httptest` B; strict fetcher | Abort with `ErrPrivateAddress` naming the redirect target; B hits == 0 | US-3 |
| `file://` gate | `t.Setenv("GOMAGPIE_ALLOW_FILE", …)` + explicit-options constructors | Unset + production-path → loud error naming the env var; `=1` → serves; explicit `AllowFile:true` serves regardless of env | US-3 |
| CLI exit-2 wiring | `scrapeExit(fmt.Errorf("…: %w", fetch.ErrPrivateAddress), …)` + `runCrawl`-shaped crawl error, `codeOf` | Both map to exit 2; end-to-end `scrape http://127.0.0.1:9/` → exit 2 (127.0.0.1 is unroutable-without-server AND private — deterministic, no listener needed) | US-3 |
| Gzip bomb cap | `httptest` handler streaming `compress/gzip` of 60 MB zeros; real static fetcher | `len(resp.HTML) == 50<<20` exactly; no OOM, no hang (server write-error ignored — client stopped reading) | US-3 |
| Store migration | `openPreDDB` (§3: raw-SQL schema WITHOUT the 3 columns) → `store.Open` same path | `table_info` shows 3 columns, all 0; `LogFetch` then `GetRun` round-trips; old rows (run started pre-migration) readable | US-4 |
| `LogFetch` + `GetRun` | `openTempDB` + `BeginRun` | `LogFetch(id, 1234, 56)` ×2 → `FetchPages==2, FetchBytes==2468, FetchMs==112`; `TestGetRun`/`TestRunRoundTrip` still green (SELECT extension didn't break existing rows) | US-4 |
| `scrape.Run` + crawl `fetchFn` counters | `scrape` package fakes (`openScrapeDB`/`fakeDeps`/`scrapeOrigin`… verify names in-file first) + `file://`/httptest bodies | One markdown-only scrape → `fetch_pages==1`, `fetch_bytes==len(served body)`; mini-crawl (2 pages) → `fetch_pages==2`; telemetry failure can never fail the page (code-review assert — no test forces a DB error mid-fetch) | US-4 |
| MCP `crawl_site` new fields | `dialInMemory` + `agentDeps` + string args (`"allow_subdomains":"true"`, include as JSON-array string) | Coerced + honored (subdomain link followed only when true — small `origin3Pages`-shaped site or fake fetcher); `usage` map contains `fetch_pages/fetch_bytes/fetch_ms`; `TestToolCatalog` still exactly 11 names (fields don't add tools) | US-1, US-4 |
| `proxyFunc` + transport | `newProxyOrigin` (§3: `httputil` reverse proxy + hit counter) + `t.Setenv("GOMAGPIE_PROXY", …)` | Proxied fetch → proxy hits == 1 and body correct; `NO_PROXY` host → 0; garbage value → error containing `GOMAGPIE_PROXY` pre-I/O; `NewChecker` robots fetch honors proxy (robots URL arrives via proxy — same hit counter) | US-5 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | Fake fetchers/lookups, localhost `httptest` only (origins, proxy, bomb, redirect chains), generated sitemap bodies, temp-dir SQLite — no external network/DNS, no browser, no keys | <60s total | Every push; the only default gate |
| Manual smoke (not a test file) | Built binary: `map` on one real `.xml.gz` host; scoped `crawl --max-pages 5` on one real docs site; `GOMAGPIE_PROXY` + `curl -x`-equivalent eyeball; keyless only | minutes | Pre-release: proves depth-5/budget/proxy against the wild, where fixtures can't reach |

No browser tier (D never touches rod paths; `//go:build browser` suite untouched). No golden tier (no golden files in D — sitemap/robots bodies are generated or inline; the one pre-D schema lives in test SQL, not `testdata/`). No integration flag (Go adaptation, Phase-A/B/C precedent): localhost `httptest` IS the hermetic stand-in, including for proxy/bomb/DNS shapes.

---

## 3. Fake / Mock Implementations

One new fake (`fakeLookup`). Everything else is reused or a 10-line in-test helper. Full source for the new fake; pointers for the rest (§5).

### `fakeLookup` — replaces the DNS resolver in `ValidateURL` tests

```go
// fetch/ssrf_test.go (package fetch_test) — the ONLY new fake in Phase D.
type fakeLookup struct {
    mu    sync.Mutex
    hosts map[string][]net.IP   // hostname → scripted answers
    errs  map[string]error       // hostname → scripted failure
    calls []string               // every resolved hostname, in order
}

func (f *fakeLookup) lookup(_ context.Context, host string) ([]net.IP, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.calls = append(f.calls, host)
    if err, ok := f.errs[host]; ok {
        return nil, err
    }
    return f.hosts[host], nil // nil slice = NXDOMAIN shape (no IPs)
}

func publicIP(s string) net.IP  { return net.ParseIP(s) } // "93.184.216.34"
func privateIP(s string) net.IP { return net.ParseIP(s) } // "10.1.2.3", "192.168.0.1"
```

**Matches real call:** `ValidateURL(raw, lookup, opts)` where production passes `nil` (= `net.DefaultResolver.LookupIP`) — the fake has the identical `func(context.Context, string) ([]net.IP, error)` shape. If the implementation names the type `LookupFunc`, use it: `fakeLookup.lookup` must satisfy the assignment `var _ fetch.LookupFunc = (*fakeLookup)(nil).lookup` — method value, compile-locked in the test.

**DNS-rebind row:** `hosts["rebind.example"] = []net.IP{publicIP("93.184.216.34"), privateIP("10.9.9.9")}` → must REJECT (any-private poisons the set). **NXDOMAIN row:** host absent from both maps → empty + nil error → reject (no addresses ≠ public). **Error row:** `errs["down.example"] = errors.New("dns: servfail")` → error propagates, wrapped, never a silent allow.

### In-test helpers (10 lines each, defined at point of use — NOT shared)

```go
// fetch/ssrf_test.go
func hitOrigin(t *testing.T, hits *atomic.Int64, h http.HandlerFunc) *httptest.Server {
    t.Helper()
    s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        hits.Add(1)
        h(w, r)
    }))
    t.Cleanup(s.Close)
    return s
}
// strictFetcher(t) — NewStaticFetcherWithOptions(fetch.SSRFOptions{}) — explicit
// strictness, NEVER the bare constructor (see §6: hatch rule). Fails the file if
// the options constructor doesn't exist — that is intentional (test-driven API, §6).

// fetch/proxy_test.go
func newProxyOrigin(t *testing.T, hits *atomic.Int64, target string) string {
    t.Helper()
    u, err := url.Parse(target)
    if err != nil { t.Fatal(err) }
    p := httputil.NewSingleHostReverseProxy(u) // stdlib, test-only egress stand-in
    s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        hits.Add(1)
        p.ServeHTTP(w, r)
    }))
    t.Cleanup(s.Close)
    return s.URL
}
// bombOrigin(t): handler that gzips 60<<20 zero bytes on the fly (no fixture file,
// ~50 KB on the wire). compress/gzip in-test, matching gzipBytes precedent.

// store/sqlite_test.go
func openPreDDB(t *testing.T) *store.DB {
    t.Helper()
    // Raw-SQL pre-D run_history (all current columns EXCEPT fetch_pages/fetch_bytes/fetch_ms)
    // + BeginRun one row, close the raw handle, then store.Open(same path) — the migration
    // under test runs inside Open. Copy the column list from sqlite.go DDL at time of writing;
    // if DDL gains columns later this helper fails LOUDLY (good — migration coverage must be revisited).
}

// crawl scoping tests (package crawl — internal, reuse openCrawlDB/NewFrontier/NewFilter):
// compileScope(t, ...) tiny wrapper failing on unexpected compile error; scopeHTML() fixture:
// one page linking /docs/a (in), /api/b (out-of-prefix), /x.pdf (ext), sub.ex.com/docs/c,
// other.com/docs/d, /docs/e?x=1&utm_source=t (canonicalization probe).
```

### Reused verbatim (read first, never modify)

- `fakeSitemapFetcher` + `fakeSitemapResp{status, err, body?}` + `follows()` + `gzipBytes` + `locSet` (`crawl/sitemap_test.go:22ff` — validator-confirmed status/err support already covers poisoned-child; DO NOT extend the struct)
- `fakeExtractor` + `count/total`, `mustTestSchema`, `openCrawlDB`, `origin` + `newSiteOrigin(pages, robotsBody)` — the robots-capable httptest site (`crawl/crawl_test.go:40ff,109ff,139ff`); `closedPort` (`:167`); `okResp/errResp` (`:353`); `TestRobots_Sitemaps` seed pattern (`:261`)
- `newFakeOrigin`, `TestStaticRedirectCap` (redirect-cap precedent), `TestStaticGzip`, `TestStaticFileURL` (`fetch/fetch_test.go`); `echoOrigin`, `fetchBody`, `newChallengeOrigin` (`fetch/profiles_test.go`)
- `openTempDB`, `TestTablesExist`, `TestRunRoundTrip`, `TestGetRun` (`store/sqlite_test.go:11ff`)
- `openScrapeDB`, `testSchema`, `fakeDeps`, `scrapeOrigin` (`scrape/scrape_test.go` — VERIFY names in-file before use; Phase-C plan cites them)
- `openMCPDB`, `testMCPServer`, `dialInMemory`, `callTool`, `decodeOut`, `agentDeps`, `llmCalls`, `toolErrText`, `fakeAgentFetcher`, `TestToolCatalog` (`mcp/*_test.go`)
- `resetGlobals`, `captureOutput`, `crawlPageHTML`, `writeFileSite`, `newTestServer`, `codeOf`, `testEnv`, `mustRead` (`cli/*_test.go`)

---

## 4. Test File List

```
gomagpie/
├── fetch/
│   ├── ssrf_test.go         # NEW (package fetch_test): sentinel + ValidateURL table (fakeLookup) + strict pre-dial (hits==0) + redirect abort + file gate + dial-peer check
│   ├── proxy_test.go        # NEW: proxy hit / NO_PROXY bypass / invalid value / robots-via-proxy
│   └── fetch_test.go        # ADD bomb-cap test only (append, touch nothing existing): gzip-bomb → exactly 50<<20 bytes
├── crawl/
│   ├── scope_test.go        # NEW (package crawl): CompileScope caps + Allows matrix + SkipExtension table (all post-Canonicalize)
│   ├── sitemap_test.go      # ADD: depth chain, entity, tolerance rows, poisoned-child partial, budget-vs-cancel, content-encoding gzip shape (TestSitemap_Garbage untouched)
│   └── crawl_test.go        # EDIT call sites (ExtractLinks Scope — required by impl) + ADD: scope-filtered ExtractLinks rows, NoSitemap zero-fetch, seed-expansion scope/warn tests
├── store/
│   └── sqlite_test.go       # ADD: openPreDDB migration test + LogFetch round-trip + GetRun field asserts (existing tests untouched)
├── scrape/
│   └── scrape_test.go       # ADD: LogFetch counter test on markdown-only scrape (existing tests untouched)
├── mcp/
│   └── agent_test.go        # ADD: crawl_site new-field coercion + subdomain honored + usage counters (TestToolCatalog untouched — names only)
├── cli/
│   └── cmd_test.go          # ADD: 5-flag help text + bad-glob exit 2 + scrapeExit/runCrawl sentinel→2 + --no-sitemap e2e (check where crawl CLI tests live — cmd_test.go holds runCrawl tests today)
└── plan/phase-D-tests.md    # this file
```

Every deliverable in `plan/phase-D.md` §4 maps: `crawl/scope.go`→scope_test; `frontier.go`/`crawl.go`→crawl_test rows; `sitemap.go`→sitemap_test; `robots.go`→proxy robots test (its only behavior change); `fetch/ssrf.go`+`http.go`→ssrf_test/proxy_test/bomb; `store/sqlite.go`→sqlite_test; `scrape/scrape.go`→scrape_test row; `mcp/tools.go`→agent_test rows; `cli/crawl.go`+`shared.go`→cmd_test rows. `cli/map.go` needs NO new tests (unchanged — hardened behavior surfaces through sitemap tests + existing map tests).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): per-package test files, hermetic-only suite, no integration-tier flag (Phase-A/B/C precedent). **New files are new; existing files gain appended test funcs only — no existing test is modified** (sole exception: the two `ExtractLinks` call sites in `crawl_test.go`, changed by the implementation itself, with surrounding asserts extended to Scope rows).

Package conventions (follow the file you extend — verified this session): `crawl` tests are **internal** (`package crawl`); `fetch`/`store`/`mcp`/`scrape` tests are **external** (`*_test` packages); `cli` tests are **internal** (`package cli`, manipulate globals via `resetGlobals`).

```go
// fetch/ssrf_test.go — package fetch_test; ADD: fakeLookup (§3 verbatim) + hitOrigin +
// strictFetcher (explicit-options constructor — §6) + publicIP/privateIP one-liners.
// Compile lock: var _ fetch.LookupFunc = (*fakeLookup)(nil).lookup (iff the type exists;
// else the func-shape comment + a note telling the implementer to export it).

// fetch/proxy_test.go — package fetch_test; ADD: newProxyOrigin (§3) + bombOrigin.
// REUSE: hitOrigin from ssrf_test.go (SAME package — no duplication, call it directly).
// Env discipline: every proxy test uses t.Setenv (auto-restore, parallel-safe); no test
// mutates os.Environ without t.Setenv.

// fetch/fetch_test.go — APPEND: TestStaticGzipBomb only. REUSE newFakeOrigin-shape httptest
// (or plain httptest.NewServer — match file convention).

// crawl/scope_test.go — package crawl; ADD: compileScope(t, …) wrapper + scopeHTML() fixture.
// REUSE: Canonicalize (call it in-test on every Allows input — locks the post-canonical basis).

// crawl/sitemap_test.go — package crawl; APPEND depth/entity/tolerance/poisoned/budget/encoding tests.
// REUSE fakeSitemapFetcher/fakeSitemapResp/gzipBytes/locSet/follows AS-IS (validator-confirmed
// sufficient — extending the struct is a plan smell, flag it).

// crawl/crawl_test.go — EDIT 2 ExtractLinks lines (Scope arg) + APPEND scope/no-sitemap/expansion tests.
// REUSE openCrawlDB/NewFrontier/NewFilter/fakeExtractor/newSiteOrigin/closedPort/TestLinks_ pattern.

// store/sqlite_test.go — APPEND: openPreDDB (§3) + TestMigration_AddsFetchColumns +
// TestLogFetch_RoundTrip + TestGetRun_FetchFields. REUSE openTempDB/TestGetRun shape.

// scrape/scrape_test.go — APPEND: TestScrape_LogFetchCounters. REUSE openScrapeDB/fakeDeps-family
// (verify exact names in-file first — Phase-C plan cites openScrapeDB/testSchema/fakeDeps/scrapeOrigin).

// mcp/agent_test.go — APPEND: TestCrawlSite_ScopeFields (+coercion rows), TestCrawlSite_UsageHasFetchCounters.
// REUSE agentDeps/dialInMemory/callTool/decodeOut/toolErrText/llmCalls/fakeAgentFetcher.

// cli/cmd_test.go — APPEND: TestCrawlFlags_HelpText (5 flags), TestCrawl_BadGlobExit2,
// TestScrapeExit_PrivateAddress (sentinel→2 unit), TestScrape_PrivateExit2 (e2e 127.0.0.1),
// TestCrawl_NoSitemapZeroFetch (httptest site + sitemap-hit counter).
// REUSE resetGlobals/captureOutput/codeOf/testEnv/writeFileSite/newTestServer.
```

Scope rationale: everything function-scoped (`t.TempDir()` DBs, per-test `httptest` servers, per-test atomics, `t.Setenv` env) — safe for parallel `go test`. The sole cross-test mutable surface is `os.Environ`, always via `t.Setenv`. No shared fixtures, no ordering dependencies.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Strict tests require an explicit-options constructor | `strictFetcher` uses `NewStaticFetcherWithOptions(SSRFOptions{})` (or equivalent); if it doesn't exist the test file fails to compile — intentional | A strict test built on the bare constructor would silently depend on the test-binary hatch and pass VACUOUSLY in CI while production stays strict; the compile failure is the forcing function (one test-driven API addition, ~5 lines, flagged to the implementer in §8) |
| Hatch has no dedicated test | Covered implicitly by the existing suite (`TestStaticGzip` et al. hit 127.0.0.1 through the bare constructor under `go test`) | A hatch test would pin test-only behavior as contract; the rule is one-directional — old tests keep passing, new security tests use explicit strictness |
| Fake resolver, never real DNS | `fakeLookup` for ALL `ValidateURL` DNS paths; no `example.com` resolution in tests | Real DNS in unit tests is flaky (offline CI, captive portals) and slow; the rebind/NXDOMAIN/servfail shapes are only expressible with a script anyway (hermetic law) |
| Pre-dial proof = server hits == 0 | Every rejection test pairs the error assert with an atomic hit counter | Error-text alone can't distinguish pre-dial rejection from post-connect refusal — the counter proves no packet was served (the actual security property) |
| `errors.Is` in-process, substrings at the edge | Unit/fetcher tests assert `errors.Is(err, fetch.ErrPrivateAddress)`; CLI tests assert `codeOf()==2` + stderr names the host/reason | Sentinel is the contract inside the binary; users see exit codes + messages — test each layer's own contract, never reach through |
| Scope tests canonicalize first | Every `Allows` input goes through `Canonicalize` in-test; one dedicated row feeds raw `?utm_source=t&x=1` vs reordered query | Locks the phase-D decision (match basis = enqueue basis); without it a future canonicalizer tweak silently re-scopes globs |
| Suffix-without-dot is a named negative | `evil-example.com` row in the subdomain table | The `HasSuffix(h, "."+seed)` dot-boundary is THE security-relevant line in scope (cookie-style suffix bugs); it gets its own row, not a footnote |
| No new fixture files | Sitemap/robots/entity/tolerance bodies are inline strings or `locSet`/`gzipBytes`-generated | 10k-URL and depth-chain bodies as files would bloat the repo for zero fidelity gain (Phase-C precedent: generation for scale, fixtures for real shapes) |
| Migration test hand-builds pre-D SQL | `openPreDDB` copies the WITHOUT-columns DDL literally, not by importing old code | Importing old schema is impossible post-change; the literal copy fails loudly if DDL drifts (forcing migration-coverage review — the failure IS the feature) |
| Proxy = stdlib reverse proxy in-test | `httputil.NewSingleHostReverseProxy`, hit counter, `t.Setenv` | No proxy stub to maintain; exercises the real `Transport.Proxy` path including `NO_PROXY`; `t.Setenv` keeps env mutation parallel-safe |
| Bomb asserts EXACT cap bytes | `len(body) == 50<<20`, not `<=` | `<=` would pass if the handler simply sent less; exactness proves the LimitReader truncated the stream (read the const from the implementation? No — hardcode `50<<20` with a comment: if the cap ever changes intentionally, this test SHOULD break) |
| MCP catalog untouched by new fields | New-field tests never assert tool count; `TestToolCatalog` still 11 | Fields ≠ tools — a count assert in the new tests would double-pin and rot on the next tool addition (Phase-C stale-count lesson) |
| Telemetry failure path is review-only | No test forces `LogFetch` to error | Warn-only telemetry failing is unobservable by design; a forced-DB-error test would pin scaffolding, not behavior — happy-path counters + code review suffice |
| Budget-vs-cancel asserted as DIFFERENT outcomes | Two tests, not one: expiry → `(partial, true, nil)`; parent-cancel → error | Collapsing them blesses timeout-drop-by-another-name; the distinction IS the webclaw `discover_within` behavior being ported |

---

## 7. Example Test Case

```go
// fetch/ssrf_test.go
package fetch_test

import (
    "context"
    "errors"
    "net"
    "net/http"
    "strings"
    "sync"
    "sync/atomic"
    "testing"

    "gomagpie/fetch"
)

// TestValidateURL_Table is the phase's load-bearing unit test: every
// rejection reason, decided without a single packet (fakeLookup), plus the
// rebind shape that justifies DNS-checking at all.
func TestValidateURL_Table(t *testing.T) {
    lk := &fakeLookup{
        hosts: map[string][]net.IP{
            "ok.example":     {net.ParseIP("93.184.216.34")},
            "rebind.example": {net.ParseIP("93.184.216.34"), net.ParseIP("10.9.9.9")},
        },
        errs: map[string]error{"down.example": errors.New("dns: servfail")},
    }
    cases := []struct {
        name    string
        url     string
        opts    fetch.SSRFOptions
        wantErr bool
    }{
        {"public literal ok", "http://93.184.216.34/", fetch.SSRFOptions{}, false},
        {"public DNS ok", "http://ok.example/", fetch.SSRFOptions{}, false},
        {"loopback literal", "http://127.0.0.1/", fetch.SSRFOptions{}, true},
        {"loopback decimal-obfuscated", "http://2130706433/", fetch.SSRFOptions{}, true}, // 127.0.0.1 as integer — netip parses, must still reject
        {"private 10/8", "http://10.1.2.3/", fetch.SSRFOptions{}, true},
        {"link-local metadata", "http://169.254.169.254/", fetch.SSRFOptions{}, true},
        {"ipv6 loopback", "http://[::1]/", fetch.SSRFOptions{}, true},
        {"localhost name", "http://localhost:8080/", fetch.SSRFOptions{}, true},
        {"metadata name", "http://metadata.google.internal/", fetch.SSRFOptions{}, true},
        {"metadata trailing dot", "http://metadata.google.internal./", fetch.SSRFOptions{}, true},
        {"dns rebind set", "http://rebind.example/", fetch.SSRFOptions{}, true},
        {"dns nxdomain", "http://absent.example/", fetch.SSRFOptions{}, true},
        {"dns error propagates", "http://down.example/", fetch.SSRFOptions{}, true},
        {"allowPrivate relaxes dns", "http://rebind.example/", fetch.SSRFOptions{AllowPrivate: true}, false},
        {"allowPrivate still blocks literal", "http://10.1.2.3/", fetch.SSRFOptions{AllowPrivate: true}, true}, // literals are never trusted — documents the asymmetry
        {"file gated by default", "file:///etc/hostname", fetch.SSRFOptions{}, true},
        {"file allowed explicitly", "file:///etc/hostname", fetch.SSRFOptions{AllowFile: true}, false},
        {"bad scheme", "ftp://93.184.216.34/", fetch.SSRFOptions{}, true},
        {"empty host", "http:///path", fetch.SSRFOptions{}, true},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            err := fetch.ValidateURL(tc.url, lk.lookup, tc.opts)
            if tc.wantErr && err == nil {
                t.Fatalf("ValidateURL(%q) = nil, want SSRF rejection", tc.url)
            }
            if !tc.wantErr && err != nil {
                t.Fatalf("ValidateURL(%q) = %v, want nil", tc.url, err)
            }
            if tc.wantErr && !errors.Is(err, fetch.ErrPrivateAddress) {
                t.Errorf("ValidateURL(%q) err = %v, want errors.Is(ErrPrivateAddress)", tc.url, err)
            }
        })
    }
    if len(lk.calls) == 0 {
        t.Error("fake lookup never consulted — DNS path untested, suite is vacuous on rebind rows")
    }
}

// TestSSRF_StrictFetcherPreDialRejects is the transport proof: the error must
// arrive with the origin server seeing ZERO hits — rejection before dial,
// not a refused connection after.
func TestSSRF_StrictFetcherPreDialRejects(t *testing.T) {
    var hits atomic.Int64
    srv := hitOrigin(t, &hits, func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("<html><body>pwned</body></html>")) //nolint:errcheck // test server
    })
    f := strictFetcher(t) // explicit SSRFOptions{} — NEVER the bare constructor here
    _, err := f.Fetch(context.Background(), fetch.FetchRequest{URL: srv.URL + "/secret"})
    if !errors.Is(err, fetch.ErrPrivateAddress) {
        t.Fatalf("Fetch(private) = %v, want errors.Is(ErrPrivateAddress)", err)
    }
    if n := hits.Load(); n != 0 {
        t.Fatalf("origin hits = %d, want 0 (rejection must precede dial)", n)
    }
    if !strings.Contains(err.Error(), "127.0.0.1") {
        t.Errorf("error %q should name the blocked address (debuggability)", err)
    }
}
```

Notes for the executor: `strictFetcher` MUST fail to compile until the implementation exposes an explicit-options constructor (§6) — do not "fix" this by falling back to the bare constructor (that would test the hatch, not the guard). The `2130706433` row assumes `netip.ParseAddr` accepts integer-form IPv4 — if Go rejects it, the URL fails at parse/resolve instead: keep the row but relax to "wantErr" only (already is), and record the actual rejection layer in a comment. `lk.calls` non-empty assert is the anti-vacuous lock: a `ValidateURL` that never consults DNS would pass every row except rebind — this line catches that. Companion tests in the same file (same patterns): `TestSSRF_RedirectAbort` (A→302→B, B hits == 0, error names B), `TestSSRF_FileGate` (`t.Setenv` matrix + explicit `AllowFile`), `TestSSRF_SentinelWired` (`scrapeExit`-adjacent? No — that lives in `cli/cmd_test.go`; here assert the sentinel message contains the URL and reason).

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase D of `gomagpie` — crawl scope + sitemap hardening + SSRF/proxy/telemetry (stdlib-only). Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-D.md` (full design AS AMENDED — sentinel exit-2 wiring, warn-and-proceed seeding, scope-filtered expansion, post-Canonicalize glob basis), `plan/phase-D-tests.md` (this suite's contract — §3 fakes, §6 decisions, §7 load-bearing test), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test. Production code may or may not exist yet — write tests against the frozen signatures in phase-D §§2–3 and this plan's §3/§5; if a signature is missing, implement the test to the plan and flag the gap rather than inventing API. Package conventions are load-bearing: `crawl` tests are INTERNAL (`package crawl`), `fetch`/`store`/`mcp`/`scrape` tests are EXTERNAL (`*_test`), `cli` tests are INTERNAL (`package cli`).

### What This Project Is
Go 1.26 CLI web scraper (binary `magpie`, module `gomagpie`): fetch → clean → extract. Phase D adds crawl scoping (`crawl.Scope`: prefix/globs/subdomains/no-sitemap + binary-ext skip), sitemap robustness (budgeted BFS depth≤5, skip+truncate partials, robots-seed union), a fail-closed SSRF guard (`fetch.ErrPrivateAddress` sentinel → CLI exit 2, `file://` env-gated), fetch telemetry (`fetch_pages/bytes/ms` on `run_history` with migration), and `GOMAGPIE_PROXY` single-proxy support. Tests are hermetic: `go test ./...` with no external network/DNS, no browser, no keys. No new test deps. The validator already proved the two failure modes this suite must lock: exit-2-that-ships-as-1 (hence sentinel + `codeOf` tests) and seeding-errors-that-abort-crawls (hence warn-and-proceed tests).

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | scoped crawl (prefix/globs/subdomains/no-sitemap/ext-skip) | scope tables + frontier rows + CLI exit 2 + zero-fetch + seed-scoping | only matches enqueued; exclude wins; bad glob → exit 2; sitemap hits == 0 with flag |
| US-2 | robust map (depth-5/entities/tolerance/partial/budget/gzip×2) | sitemap additions, `TestSitemap_Garbage` untouched | exact sets; poisoned child → partial+truncated; expiry → partial/nil-err; cancel → error |
| US-3 | SSRF pre-dial reject (exit 2) + file gate + bomb cap | §7 verbatim + redirect + file + CLI e2e + exact-cap bomb | `errors.Is` sentinel; origin hits == 0; CLI 127.0.0.1 → exit 2; body == `50<<20` |
| US-4 | fetch telemetry + pre-D migration | migration + round-trip + scrape/crawl counters + MCP usage fields | 3 columns zeroed on open; `fetch_pages==1` per scrape; usage map has counters |
| US-5 | proxy honored incl. robots | hit / bypass / invalid / robots-via-proxy | hits 1/0; garbage errors naming `GOMAGPIE_PROXY` |

### Why Fakes Are Required
- DNS resolution: real resolvers are network, flaky, and can't express rebind/NXDOMAIN/servfail — `fakeLookup` (§3 full source) scripts all three; production passes nil (= default resolver).
- Origins/proxy/redirects/bomb: live hosts banned — `httptest` + hit counters (`hitOrigin`), stdlib reverse proxy (`newProxyOrigin`), on-the-fly gzip bomb; counters prove PRE-dial rejection (error text alone can't).
- Sitemaps at scale: 10k-URL/depth-chain bodies generated (`locSet`/`gzipBytes`, existing) — fixture files would bloat the repo for zero fidelity gain.
- Pre-D database: old code can't be imported — `openPreDDB` hand-builds the WITHOUT-columns schema in SQL; drift fails loudly (the failure is the feature).
- MCP transport: string-coercion of the NEW fields lives in validation-before-unmarshal — tests go through `callTool`/`dialInMemory`, never handlers directly (Phase-C precedent).
- SQLite: NOT faked — real `modernc.org/sqlite` on `t.TempDir()` (precedent); `t.Setenv` for all env mutation (parallel-safe).

### What NOT to Test
- Don't test the test-binary hatch (no dedicated hatch test — old suite passing IS its coverage; new security tests use explicit strictness or they test nothing).
- Don't test live hosts, real DNS, real proxies, or keyed runs — proxy/bomb/DNS shapes are localhost-only; keyed paths don't exist in Phase D at all.
- Don't test go-sdk internals, `encoding/xml` entity mechanics beyond one `&amp;` row, `compress/gzip` beyond the helpers, or `httputil` proxying beyond hit-correctness.
- Don't test Phase E surface (uTLS impersonation, PDF, office docs, REST) — unbuilt; the `GuardedTransport` comment noting E's obligation is review-only.
- Don't extend `fakeSitemapFetcher`'s struct (validator-confirmed sufficient) and don't create a shared `testutil` package — 10-line helpers live at point of use; `hitOrigin` is shared ONLY within `package fetch_test` (same-package call, not a new package).
- Don't commit fixture files for scale/tolerance cases and don't write golden files — D has no goldens; tolerance/entity bodies are inline strings.
- Don't force `LogFetch` to error (warn-only is review-only) and don't assert the 25s budget const by sleeping 25s (inject parent-ctx cancellation for the expiry shape).
- Don't touch existing test funcs — append only (sole exception: the 2 `ExtractLinks` call sites, changed by the implementation, asserts extended to Scope rows).

### Critical: Fake Implementations

Copy verbatim: `fakeLookup` — full source in test-plan §3 (needs `context, net, sync, testing`; compile-lock with `var _ fetch.LookupFunc = (*fakeLookup)(nil).lookup` IFF the type exists, else a func-shape comment telling the implementer to export it). Helpers (write at point of use, §3 sketches): `hitOrigin`, `strictFetcher` (explicit-options constructor — **fails to compile until the implementation adds it; never fall back to the bare constructor**), `newProxyOrigin` (`net/http/httputil`, test-only), `bombOrigin` (gzip 60 MB zeros, ignore server write errors), `openPreDDB` (literal WITHOUT-columns DDL), `compileScope` wrapper + `scopeHTML()` (mixed in/out/ext/subdomain/foreign/utm links).

Reuse verbatim (read first, never modify): `fakeSitemapFetcher`+`fakeSitemapResp`+`follows`+`gzipBytes`+`locSet` (crawl/sitemap_test.go:22ff); `fakeExtractor`+`count/total`, `mustTestSchema`, `openCrawlDB`, `origin`+`newSiteOrigin`, `closedPort`, `TestLinks_SameHostAndDepth` pattern (crawl/crawl_test.go); `newFakeOrigin`, `TestStaticRedirectCap` pattern, `TestStaticGzip`, `TestStaticFileURL` (fetch); `echoOrigin`, `fetchBody` (fetch/profiles_test.go); `openTempDB`, `TestGetRun` shape (store); `openScrapeDB`/`testSchema`/`fakeDeps`/`scrapeOrigin` (scrape — verify names in-file first); `openMCPDB`, `testMCPServer`, `dialInMemory`, `callTool`, `decodeOut`, `agentDeps`, `llmCalls`, `toolErrText`, `fakeAgentFetcher`, `TestToolCatalog` (mcp); `resetGlobals`, `captureOutput`, `crawlPageHTML`, `writeFileSite`, `newTestServer`, `codeOf`, `testEnv`, `mustRead` (cli).

### Test Files to Create

```
fetch/ssrf_test.go       # NEW (~10: §7 verbatim ×2 + redirect + file-gate + sentinel-message + AllowPrivate-asymmetry row lives in the table)
fetch/proxy_test.go       # NEW (~4: hit / bypass / invalid / robots-via-proxy)
fetch/fetch_test.go       # APPEND TestStaticGzipBomb (exact-cap assert + hardcoded 50<<20 w/ comment)
crawl/scope_test.go       # NEW (~8: caps ×3 + Allows matrix ×12 + ext table + canonical-basis row + evil-suffix negative)
crawl/sitemap_test.go     # APPEND (~6: depth chain + entity + tolerance ×3 + poisoned partial + budget-vs-cancel ×2 + content-encoding shape)
crawl/crawl_test.go       # EDIT 2 lines + APPEND (~5: scoped-ExtractLinks + NoSitemap zero-fetch + expansion scope-filter + expansion warn-and-proceed)
store/sqlite_test.go      # APPEND (~3: migration + LogFetch round-trip + GetRun fields)
scrape/scrape_test.go     # APPEND (~1: LogFetch counters on markdown-only scrape)
mcp/agent_test.go         # APPEND (~3: scope-field coercion+honored + usage counters; catalog untouched)
cli/cmd_test.go           # APPEND (~5: help text + bad-glob exit 2 + sentinel unit + 127.0.0.1 e2e + no-sitemap e2e)
```

### Per-File Coverage Guidance

#### fetch/ssrf_test.go
§7 verbatim first (table + pre-dial). Then `TestSSRF_RedirectAbort`: A serves 302→B (both `hitOrigin`-counted), strict fetch of A → `errors.Is(ErrPrivateAddress)`, B hits == 0, error names B's host. `TestSSRF_FileGate`: matrix over `t.Setenv("GOMAGPIE_ALLOW_FILE", "")` / `"1"` / `"bogus"`(→ treated as unset? DECIDE with implementation: truthy-only-`"1"` vs non-empty — test whichever the implementation documents, and say which in a comment) × bare vs explicit-options constructor. `TestSSRF_DialPeerCheck`: close the loop on TOCTOU — hardest hermetic row: reach the dial wrapper with a hostname whose fake… (dial uses REAL resolution — can't fake per-conn without a custom dialer). Honest scope: assert the peer-check CODE PATH via a direct unit on the exported-or-internal check func if the implementation factors one (`isPublicAddr(conn.RemoteAddr())`-shaped); if inlined in the closure, cover by review + note it here as the ONE untested line with its justification. Do not build a fake DNS server (stdlib has none; third-party DNS stubs are a new dep — banned).

#### fetch/proxy_test.go
`TestProxy_Hit`: origin + `newProxyOrigin` + `t.Setenv("GOMAGPIE_PROXY", proxy.URL)` → body correct AND proxy hits == 1. `TestProxy_NoProxyBypass`: `t.Setenv("NO_PROXY", originHost)` → hits == 0, body correct (parse host WITHOUT port for the expectation). `TestProxy_InvalidValue`: `t.Setenv("GOMAGPIE_PROXY", "://bogus")` → fetch errors containing `GOMAGPIE_PROXY` before any dial (origin hits == 0). `TestProxy_RobotsViaProxy`: `NewChecker`-driven `Allowed()` against an `httptest` site with robots while `GOMAGPIE_PROXY` set → proxy hits ≥ 1 (proves the shared transport, not just the fetcher). Env hygiene: `t.Setenv` everywhere; save/restore nothing by hand.

#### crawl/scope_test.go (+ crawl_test.go additions)
`TestCompileScope_Caps`: `**`-×5 → err; 1025-char → err; `"[unclosed"` → err; prefix `"docs"` → normalized `"/docs"` (assert via a passing Allows probe, not internals). `TestAllows_Matrix`: ~12 rows incl. `evil-example.com` negative, empty-path seed (`http://ex.com` vs `/docs/a` — empty path must behave as `/`), `file:///a` vs `file:///b` same-host-equal, utm/reordered-query canonical row (feed RAW urls, canonicalize in-test). `TestSkipExtension_Table`: every listed ext lower+upper, `x.pdf?dl=1`, negatives (`/docs`, `/feed.xml`, extensionless). Frontier rows (`crawl_test.go`): `scopeHTML()` through `ExtractLinks` with strict scope → exact survivor set in deterministic order; depth-gate row preserved. `TestCrawl_NoSitemapZeroFetch`: `newSiteOrigin` with robots advertising a sitemap + `NoSitemap:true` mini-crawl → sitemap-path hits == 0. `TestCrawl_SeedExpansionScoped`: fake-fetcher expansion mixing prefixes → only in-prefix claimed (assert via `CrawlStats` pending or frontier claim). `TestCrawl_SeedExpansionWarnProceed`: poisoned expansion fake → crawl completes seed-only, `PagesOK ≥ 1`, stderr contains `continuing seed-only` (capture via existing stderr helper or `captureOutput`).

#### crawl/sitemap_test.go additions
`TestSitemap_IndexChainDepth3`: index→index→urlset union (proves recursion, not just one level). `TestSitemap_EntityDecoding`: `<loc>https://x/?a=1&amp;b=2</loc>` → decoded `&` in output. `TestSitemap_RobotsTolerance`: three robots variants (upper/leading-space/trailing-comment) → all seeds fetched (if grobotstxt already covers all three, the fallback-union path is STILL asserted by a direct unit on the fallback scanner if exported, else by review-note — same honesty rule as the dial peer-check). `TestSitemap_PoisonedChildPartial`: index with 1 good + 1 500 child → good locs + `truncated:true`, nil error. `TestSitemap_BudgetVsCancel`: cancel-parent → error (not partial); short-deadline-parent on a sleepy child → partial + truncated + nil error. `TestSitemap_ContentEncodingGzip`: real `httptest` + static fetcher serving `Content-Encoding: gzip` → same set as raw-sniff row.

#### store/sqlite_test.go + scrape + mcp + cli additions
Migration: `openPreDDB` → `store.Open` same path → `table_info` has 3 cols all-0 → `LogFetch` → `GetRun` matches; plus `TestRunRoundTrip`-adjacent existing tests still green (run the file). Scrape: markdown-only `scrape.Run` over served body → `GetRun(runID).FetchPages==1`, `FetchBytes==len(body)` (exact — deterministic fixture), `FetchMs>=0`. MCP: `crawl_site` with `"allow_subdomains":"true"` (string!) + include/exclude JSON-array strings → coerced, honored on a fake-fetched mini-site; `usage` contains `fetch_pages` (any int ≥0 — presence + type, not value); catalog still 11. CLI: help `Contains` per flag ×5; `crawl --include '[unclosed'` → exit 2 WITHOUT any fetch (assert via closed-port seed: error must be the glob error, not a dial error); sentinel unit on `scrapeExit` + e2e 127.0.0.1 → exit 2; `--no-sitemap` e2e with sitemap-hit counter == 0.

### Data Model Notes (Go)
- `errors.Is(err, fetch.ErrPrivateAddress)` in-process; `codeOf(err)==2` + stderr-substring at the CLI edge — never assert sentinel across the CLI boundary (fail() flattens to message).
- `map[string]any` tool outputs: `, ok` asserts + `t.Fatalf` on shape mismatch (a shape change fails LOUD, never panics).
- `In`-struct flex round-trips for new fields: reuse the `TestIn_RoundTrip`/`TestFlex_CoercionUnits` patterns (mcp/agent_test.go:267ff) — add rows, not new harnesses.
- `t.Setenv` for ALL env mutation (proxy/file tests) — parallel-safe by construction; never touch `os.Environ` directly.
- Exit codes via `codeOf`; output via `captureOutput`; DBs via `t.TempDir()` openers; exactly-one intoboolean: `hits.Load()` reads happen AFTER the fetch returns (no sleep-polling, no flakes).
- Hardcoded `50<<20` in the bomb test WITH the drift comment (§6) — magic numbers are banned except when the test's job is pinning the magic number.

### Success Criteria
- `go test ./...` exits 0; RUN count strictly exceeds the pre-phase baseline (`tee /tmp/phaseD-baseline.log` BEFORE writing — non-vacuous rule, testing.md; Phase-C close was 374 RUN lines, record actual)
- `git status --porcelain testdata/` empty throughout (D adds NO fixtures — a new file under testdata/ fails this criterion)
- `grep -rn "test.v\|isTestBinary" --include="*_test.go" fetch/ crawl/ store/ scrape/ mcp/ cli/` returns nothing (no new test depends on the hatch — the one exception the suite must prove absent)
- Every deliverable from `plan/phase-D.md` §4 maps to §4 above (no orphan deliverable, no orphan test file)
- Full gates: `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass

### Expected File Structure at End
(Same tree as Test File List §4 — reproduce exactly: 3 new test files, 7 appended test files, zero fixture files, zero helper packages.)
---

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -v 2>&1 | tee /tmp/phaseD-baseline.log; grep -c '^=== RUN' /tmp/phaseD-baseline.log  # expect 374 (Phase-C close); record actual

# Fast hermetic suite (every push — localhost httptest only, no external network/DNS/keys/browser)
go test ./...

# New-test count vs baseline (must strictly exceed; ~40 new funcs incl. table subtests expected)
go test ./... -v 2>&1 | tee /tmp/phaseD-tests.log; grep -c '^=== RUN' /tmp/phaseD-tests.log

# Focused per task (mirrors phase-D sanity checks)
go test ./crawl/ -run 'TestCompileScope|TestAllows_|TestSkipExtension' -v          # D.1 pure
go test ./crawl/ -run 'TestLinks_|TestCrawl_NoSitemap|TestCrawl_SeedExpansion' -v  # D.1 frontier + seeding
go test ./crawl/ -run 'TestSitemap_' -v                                           # D.2
go test ./fetch/ -run 'TestValidateURL|TestSSRF_' -v                              # D.3
go test ./store/ -run 'TestMigration|TestLogFetch|TestGetRun' -v                  # D.4
go test ./fetch/ -run 'TestProxy_|TestStaticGzipBomb' -v                          # D.5 + bomb
go test ./mcp/ -run 'TestToolCatalog|TestCrawlSite_' -v; go test ./cli/ -run 'TestCrawl|TestScrapeExit|TestScrape_Private' -v  # surface

# Fixture-discipline + hatch-discipline (US-4/US-3: what must NOT appear)
git status --porcelain testdata/                                                  # must print nothing (D adds no fixtures)
grep -rn "test\.v\|isTestBinary" --include="*_test.go" fetch/ crawl/ store/ scrape/ mcp/ cli/ || echo NO-HATCH-DEPENDENCE

# Stale-count sweep (D adds fields, not tools — catalog stays 11)
go test ./mcp/ -run TestToolCatalog -v

# Full gate (mirrors phase-D exit criterion)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service/pipeline + security boundary + migration; fake lookup + hit-counter origins + generated bodies + temp SQLite + in-memory MCP (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to D1 (scope/seeding) / D2 (sitemap) / D3 (SSRF/bomb) / D4 (telemetry) / D5 (proxy)
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 19 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all of phase-D §4 (`cli/map.go` correctly needs none — unchanged file, stated not silent; `scrape.Run` signature unchanged, behavior row present)
- [x] Every external/heavy dependency has a fake or mock equivalent — live hosts/DNS/proxy → httptest + `fakeLookup` + in-test reverse proxy; pre-D schema → hand-built SQL; browser/keyring untouched by this phase (extract/ LLM paths need no fakes — D is LLM-free)
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — fakes + `file://` + localhost `httptest` only; injected-DNS rule stated in §6; the ONE honest exception (dial peer-check closure) is review-only with its justification in §8, not a hidden network call
- [x] Integration tests are gated behind a CLI flag (not run by default) — N/A adapted (Go repo, Phase-A/B/C precedent): no integration tier exists; hermetic-only suite + explicitly-not-a-test-file manual smoke (§2); stated at the top of §5 per skill rule
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure reusing existing per-package conventions; stated at the top of §5 per skill rule
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — adapted: §8 pastes seam/helper contracts with full `fakeLookup` source in §3 + §7's verbatim test; same adaptation Phase-C's test plan used
- [x] Run commands section is present — §9 with baseline/focused/discipline-sweeps/full-gate/cross commands
