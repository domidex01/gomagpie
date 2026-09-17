# Phase D — Crawl Scope + Sitemap + Security (stdlib)

**Duration:** Days 1–4 (~26 hours)
**Depends on:** Phase C (`crawl.ListSitemapURLs` + caps, `map` command, MCP `crawl_site`, `StringList`/`FlexBool` coercion, `Fetcher` seam conventions)
**Blocks:** Phase E (dep-gated TLS/PDF work is independent, but E's transport swap must preserve D3's SSRF dial guard + D5's proxy func — note it in E)
**Risk Level:** MEDIUM — touches the fetch security boundary (default-deny SSRF), the crawl frontier signature, and a SQLite schema migration, but everything is additive + stdlib-only and the hermetic suite stays green via a test-binary escape hatch. 6 sections (failure section omitted per MEDIUM risk).
**Stack:** go

---

## 1. Objective + What Success Looks Like

Close the crawl/sitemap/security gaps from `plan/webclaw-gap-spec.md` §5-Phase D without adding a dependency: glob + subdomain + prefix scoping on the crawl frontier (with binary-extension skip), a verified-then-hardened sitemap lister (recursion depth ≥5, entity decoding, partial-on-budget, `Sitemap:` tolerance) that also feeds crawl seeding, an SSRF guard enforced before dialing (loopback/link-local/metadata rejected, DNS re-checked, redirects re-validated, `file://` gated), fetch-side telemetry persisted next to LLM usage, and single-proxy configuration via `GOMAGPIE_PROXY`.

1. `magpie crawl <seed> --include '**/docs/**' --exclude '**/api/**' --allow-subdomains` enqueues only matching links; `--path-prefix /docs` drops everything outside the prefix; `page.pdf`, `font.woff2`, `clip.mp4` are never enqueued (frontier unit tests prove each).
2. `magpie crawl <seed> --no-sitemap` performs zero sitemap fetches; without the flag, seed expansion goes through `ListSitemapURLs` (capped at `--max-pages`), not raw sitemap-XML URLs as crawl pages.
3. `magpie map <site>` against fixtures returns nested-index URLs at depth 5, decodes `&amp;` entities, tolerates `SITEMAP:` / leading-space / `# comment` robots lines, and returns partial results with `truncated:true` when a child sitemap 500s or the 25s budget expires (no timeout-drop).
4. `magpie scrape http://169.254.169.254/`, `.../localhost...`, or any private/loopback/link-local target exits 2 pre-dial with a message naming the reason (wired via the `fetch.ErrPrivateAddress` sentinel → exit-2 cases in `scrapeExit`/`runCrawl`, D.3); an HTTP redirect chain landing on a private host aborts at the redirect; `file:///x` works under `go test` and with `GOMAGPIE_ALLOW_FILE=1`, and is rejected otherwise.
5. After any crawl/scrape, the run row carries `fetch_pages/fetch_bytes/fetch_ms` alongside `prompt_tokens` (old DB files gain the columns automatically on open; `crawl_site` status usage includes the new counters).
6. `GOMAGPIE_PROXY=http://127.0.0.1:PORT magpie scrape <url>` arrives at the origin via the proxy (asserted server-side); `NO_PROXY` hosts bypass it; garbage `GOMAGPIE_PROXY` fails loudly pre-I/O.
7. `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, and 3-way `CGO_ENABLED=0` cross-builds all pass; a gzip bomb (50 KB raw → 60 MB decoded) is capped at exactly 50 MB.

---

## 2. Architecture / Key Design Decisions

```
CLI (crawl --path-prefix/--include/--exclude/--allow-subdomains/--no-sitemap)
MCP (crawl_site: same fields, StringList/*FlexBool — no coerce.go change)
  → crawl.CompileScope (pure validation, exit 2) → crawl.Scope
  → Frontier.ExtractLinks(html, page, depth, max, Scope)  [signature change, 3 call sites]
  → qualify: host rule → prefix → include → exclude-wins → ext skip-list

fetch.NewStaticFetcher() ─┬─ SSRF: ValidateURL pre-dial + CheckRedirect re-validate
                          ├─ DialContext peer-IP check (post-connect, pre-handshake)
                          ├─ proxy: GOMAGPIE_PROXY > ProxyFromEnvironment (+NO_PROXY)
                          └─ 50 MB LimitReader on the decoded stream (bomb guard, proven by test)
crawl.NewChecker() ── shares fetch.GuardedTransport (proxy + dial guard, one constructor)

crawl.ListSitemapURLs ── BFS depth ≤5, visited set, ≤100 child fetches, ≤10k URLs,
  25s internal budget → partial + truncated:true; child errors skip (truncated),
  hard error only when zero URLs AND ≥1 error; robots seeds = grobotstxt ∪ fallback scan
crawl.Run seeding ── ListSitemapURLs(staticFetcher, seed) capped at maxPages ── NoSitemap skips

store.Open ── DDL + table_info migration (ADD COLUMN when missing)
  LogFetch(runID, bytes, ms) ← scrape.Run + crawl fetchFn (warn-only, never fails the page)
```

### Data model strategy (stack: go)

| Layer | Type | Why |
|---|---|---|
| CLI/MCP boundary | Plain structs (`crawl.Options` += 5 fields, `CrawlIn` += 5 fields with existing `StringList`/`*FlexBool`) | Same pattern as Phase C; go-sdk infers schemas, no validation framework |
| Internal scope | `crawl.Scope` struct (bool flags + compiled `[]*regexp.Regexp`, never raw strings past `CompileScope`) | Compile once per run, match per link — no per-link regexp.Compile |
| Fetch security | `fetch.SSRFOptions{AllowPrivate, AllowFile bool}` + pure `ValidateURL(raw, lookup, opts)` | Unit-testable without network; strict by default, relaxed only under `go test` |
| Telemetry | 3 new `run_history` columns (`fetch_pages/fetch_bytes/fetch_ms`, `DEFAULT 0`) + `RunInfo` fields | Same row as LLM usage — one `GetRun` answers both cost questions |

**Rules for this phase (follow exactly):**
- **One guard, one place:** scope checks live in `crawl/scope.go` (`Scope.Allows`); SSRF lives in `fetch/ssrf.go` (`ValidateURL` + guarded transport). No handler re-implements either. `ExtractLinks` signature change (3 call sites: `crawl.go:330`, two test lines) is the shared-function fix — no wrapper preserving the old shape.
- **Validate pre-I/O, fail loud:** bad globs, bad site/seed URLs, SSRF rejections of user-supplied URLs, and garbage `GOMAGPIE_PROXY` all fail before any fetch (CLI exit 2). Mid-crawl scope/SSRF drops are silent non-enqueues, never page errors (they were never fetched).
- **Telemetry never fails the page:** `LogFetch` errors warn to stderr (same pattern as `MarkDone`/`FinishRun` warnings), never abort fetch/crawl.
- **Tests stay hermetic:** `httptest` + fake `Fetcher` + direct `ValidateURL` unit tests. Strict-mode SSRF tests construct the guarded fetcher explicitly and assert rejection *pre-dial* (no packets leave the box). The existing suite keeps passing because the guard auto-relaxes private-net + `file://` checks inside test binaries only (see D3 notes) — production binaries are always strict.
- **No new dependency, no new interface.** `net/netip`, `regexp`, `net/http`, `context.WithTimeoutCause` are stdlib. `golang.org/x/net/publicsuffix` is already used (cookiejar) — no addition.

### Per-item design notes

- **D1 (scope):** `Scope{SameHost, AllowSubdomains bool, PathPrefix string, Include, Exclude []*regexp.Regexp}`. `CompileScope(sameHost, allowSub bool, prefix string, include, exclude []string) (Scope, error)` enforces webclaw's caps (each glob ≤1024 chars, ≤4 `**` per glob, must compile) and normalizes the prefix to start with `/`. `Allows(seed, cand *url.URL) bool`: (1) host rule — `SameHost` off → any host; on → exact match OR (`AllowSubdomains` AND `cand.Host == seed.Host` suffix with dot boundary, case-insensitive); (2) `PathPrefix != ""` → `strings.HasPrefix(cand.EscapedPath(), prefix)`; (3) `Include` non-empty → must match ≥1 (matched against the canonicalized absolute URL — the same post-`Canonicalize` string that gets enqueued, so utm-stripped and query-reordered links match predictably); (4) `Exclude` match → drop (exclude wins). `SkipExtension(path)` checks a `map[string]bool` of ~25 binary extensions (pdf/png/jpg/jpeg/gif/webp/svg/ico/avif/mp4/webm/mp3/avi/mov/zip/gz/tgz/rar/7z/exe/dmg/msi/woff/woff2/ttf/eot/otf) on the lowercased final path extension — applied in `ExtractLinks` before canonicalize/add. `file://` links: host rule compares empty hosts as equal (today's `EqualFold(abs.Host, base.Host)` already treats two empty hosts as same-host — preserve that). `SameHost` stays in `crawl.Options` (default true) for compat; CLI keeps `--same-host` and adds the five new flags with the same flag type `scrape`'s `--include/--exclude` uses.
- **D2 (sitemap):** Rewrite `ListSitemapURLs` internals as BFS: queue of `(sitemapURL, depth)`, `visited` set (indexes can cycle), depth ≤5, child-fetch count ≤100 (existing `maxSitemapChildren`), URL count ≤10k (existing `maxSitemapURLs`), internal budget 25s via `context.WithTimeoutCause(ctx, 25*time.Second, errSitemapBudget)` — on budget expiry return collected URLs with `truncated:true, nil` error; parent-cancel (`ctx.Err() != nil` with other cause) returns the parent error. Per-sitemap fetch/parse failures: skip + `truncated=true` (never fatal); hard error only when the final URL set is empty AND at least one error occurred (return the first, which preserves today's "missing /sitemap.xml → error naming the URL" behavior for the single-seed case). `encoding/xml` already entity-decodes `<loc>` — prove with a fixture, don't add code. Robots seeds: `robotSeeds` returns union of `grobotstxt.Sitemaps` + a ~15-line stdlib fallback scanner (per line: strip `#` comment, trim space, `EqualFold(field, "sitemap:")` → value), because `grobotstxt`'s tolerance for case/comments/leading-space must be *verified, not assumed* — write the probe test first, keep the fallback regardless (it's the belt to grobotstxt's suspenders). gzip: current 0x1f8b sniff stays; add the second fixture shape (real `Content-Encoding: gzip` through `httptest` + static fetcher, which arrives pre-decoded) to prove both paths. Crawl seeding: replace the `checker.Sitemaps` append in `crawl.Run` with `ListSitemapURLs(ctx, staticFetcher, seedURL)` capped at `maxPages`, then filtered through the run's compiled `Scope` before enqueue — sitemaps list the whole site, so without this `--path-prefix /docs` would burn the `--max-pages` budget on out-of-scope URLs. Skipped when `NoSitemap` or non-HTTP seed; build `staticFetcher` before seeding (reorder, ~5 lines). Seeding expansion NEVER fails the crawl: any expansion error warns to stderr (`crawl: sitemap expansion: <err>; continuing seed-only`) and proceeds with just the seed — a site with no robots.txt and no `/sitemap.xml` (fallback 404) crawls exactly as today. Accepted cost: one extra robots.txt fetch per crawl (Checker cache is separate) — negligible.
- **D3 (SSRF):** `fetch/ssrf.go`: `ValidateURL(raw string, lookup LookupFunc, o SSRFOptions) error` (`type LookupFunc func(context.Context, string) ([]net.IP, error)`, nil = `net.DefaultResolver.LookupIP`) — (0) sentinel: every rejection wraps `fetch.ErrPrivateAddress` (`errors.Is`-able; CLI maps it to exit 2 in `scrapeExit` + `runCrawl`, D.3); (1) parse; scheme must be `http`/`https` (`file` iff `o.AllowFile`, else loud error naming `GOMAGPIE_ALLOW_FILE`); (2) reject empty host, userinfo, and non-default-port tricks? No — ports are fine, only hosts matter; (3) hostname blocklist (case-insensitive, exact-or-dot-suffix): `localhost`, `*.localhost`, `metadata.google.internal`, `metadata.google.internal.` (trailing-dot FQDN form), `instance-data` (AWS legacy)? Keep the list short and documented: `localhost`, `metadata.google.internal`, `metadata.google.internal.` — IP checks below already catch 169.254.169.254 in all literal forms `netip` parses; (4) IP literal via `netip.ParseAddr(host-after-trim-brackets)`: reject unless `IsGlobalUnicast() && !IsPrivate()` (the standard combo — covers loopback, link-local incl. cloud metadata, multicast, unspecified, private); (5) DNS: `LookupIP` all returned IPs through the same predicate (skip when `AllowPrivate`). `net.Resolver` with `PreferGo` default is fine. Enforce at three points in the guarded transport (single constructor `fetch.GuardedTransport() *http.Transport`): `DialContext` wrapper (resolve → validate → dial → verify `conn.RemoteAddr` IP → close+error if non-public — the post-connect check closes the DNS-rebind TOCTOU window for the connected socket; no HTTP is sent before it), `CheckRedirect` extension (validate every redirect target; violation aborts the chain with the SSRF error), and `do()` pre-check (`ValidateURL` before the first `client.Do`). `file://` scheme: `NewStaticFetcher` sets `AllowFile = GOMAGPIE_ALLOW_FILE=="1" || isTestBinary()` (`flag.Lookup("test.v") != nil`); same env/test rule drives `AllowPrivate` **for the private-net checks only** — wait, no: relaxing private-net checks under `go test` is what keeps the 100+ existing `httptest` (127.0.0.1) call sites green with zero churn. `ValidateURL` unit tests and strict-mode fetcher tests pass explicit `SSRFOptions{}` and assert rejection of `httptest.Server.URL` pre-dial. Document the hatch honestly: test binaries never ship; production is always strict. `crawl.NewChecker` switches its internal client to `fetch.GuardedTransport()` (proxy + SSRF for robots fetches, zero signature change). go-rod escalation: out of scope — escalation only runs on content already fetched through the guarded client, so a blocked URL never reaches the browser; note it in code comment, don't build browser SSRF.
- **D4 (telemetry):** Migration-safe columns: DDL gains the three columns AND `Open()` runs `PRAGMA table_info(run_history)` → `ALTER TABLE … ADD COLUMN …` for any missing (pre-D databases). `LogFetch(runID string, nBytes, ms int64) error`: single `UPDATE run_history SET fetch_pages=fetch_pages+1, fetch_bytes=…+?, fetch_ms=…+?`. Call sites: `scrape.Run` (around its fetch — time it, `len(resp.HTML)`, warn-only) and crawl `fetchFn` (around `FetchWithRetry`, same). `RunInfo` + `GetRun` SELECT gain the fields; `mcp.runUsage` includes `fetch_pages/fetch_bytes/fetch_ms`. `FinishRun` signature unchanged (pages counters already flow).
- **D5 (proxy):** `fetch/http.go`: `proxyFunc` — `GOMAGPIE_PROXY` set → parse (error → loud `fetch: bad GOMAGPIE_PROXY %q`, surfaced pre-I/O) → honor a minimal `NO_PROXY` (split comma, trim, match exact-or-dot-suffix + `*` → all, loopback always direct? No — keep it dumb and documented: exact/suffix match only; loopback goes through the proxy if listed nowhere, same as curl without noproxy) → else `http.ProxyFromEnvironment(req)`. `Transport.Proxy = proxyFunc`. `Checker` inherits it via `GuardedTransport`. Tests: `httptest` origin + `httptest` proxy (assert `X-Forwarded` custom header or proxy-side hit counter), `NO_PROXY` bypass, invalid value error. Config surface: none (env-only, like `HTTP_PROXY`) — but document in README/CLI long-help that `GOMAGPIE_PROXY` wins over standard env. Pool rotation explicitly deferred (needs per-host pinning design — spec says so).

---

## 3. Tasks

### Task D.0 — Scaffolding: scope skeleton + SSRF probe tests (2h)

Create `crawl/scope.go` with the `Scope` struct, `SkipExtension` + ext map, and `CompileScope` validating caps but *stub* `Allows` (host-exact only, mirroring today's behavior). Create `fetch/ssrf.go` with `SSRFOptions`, `ValidateURL`, and the hostname blocklist. Write failing-first probe tests: glob caps (`**`-over-limit, 1025-char), ext skip table, `ValidateURL` table (loopback/literal/private/DNS-name/cloud-metadata/ok-public), and the grobotstxt tolerance probe (`SITEMAP:` uppercase, leading spaces, trailing `# comment` → does `grobotstxt.Sitemaps` see all three? record the answer in a comment).

**Sanity check:** `go test ./crawl/ ./fetch/ -run 'TestScope_|TestSSRF_|TestRobotsSitemapTolerance' -v` — new tests fail on `Allows` extras, pass on validation.

### Task D.1 — Scope filters end-to-end (5h)

**Depends on:** D.0. Implement `Scope.Allows` (host → prefix → include → exclude-wins) + `SkipExtension` wiring in `ExtractLinks`; change `ExtractLinks` signature to take `Scope` and update the 3 call sites (`crawl.go:330` builds the scope once via `CompileScope` at `Run` start — compile error returns pre-I/O; `crawl_test.go:334,337` pass explicit scopes). `crawl.Options` += `PathPrefix string, Include, Exclude []string, AllowSubdomains, NoSitemap bool`. CLI `crawl` += `--path-prefix/--include/--exclude/--allow-subdomains/--no-sitemap` (same flag type as `scrape`'s include/exclude); invalid globs → `fail(2, …)` via an early `CompileScope` in `runCrawl` (same pure function `Run` uses — validate once at the edge, not twice). MCP `CrawlIn` += `PathPrefix string, Include/Exclude StringList, AllowSubdomains/NoSitemap *FlexBool` (no `coerce.go` change — `widenToolInput` already widens string/bool/array; add a test asserting the new fields coerce `"true"`/`"a,b"`-as-string).

```go
// Confirmed pattern (stdlib only — glob→regexp, anchored):
func compileGlob(g string) (*regexp.Regexp, error) {
	if len(g) > 1024 { return nil, fmt.Errorf("crawl: glob exceeds 1024 chars: %q", g) }
	if strings.Count(g, "**") > 4 { return nil, fmt.Errorf("crawl: glob %q: at most 4 **", g) }
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); {
		switch {
		case strings.HasPrefix(g[i:], "**"): b.WriteString(".*"); i += 2
		case g[i] == '*': b.WriteString("[^/]*"); i++
		case g[i] == '?': b.WriteString("[^/]"); i++
		default: b.WriteString(regexp.QuoteMeta(string(g[i]))); i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String()) //nolint:wrapcheck // caller adds context
}
```

Subdomain check: `h == seedH || (allowSub && strings.HasSuffix(h, "."+seedH))` after `strings.ToLower` + trim trailing dot. Prefix: normalize to leading `/` at compile; match `cand.EscapedPath()` (empty path = `/`).

**Sanity check:** frontier unit test — seed `http://ex.com/docs/`, page linking `/docs/a`, `/api/b`, `http://sub.ex.com/docs/c`, `http://ex.com/x.pdf` → with prefix+subdomains on, only the first and third enqueue.

### Task D.2 — Sitemap hardening + seed expansion (5h)

**Depends on:** D.0 (fallback scanner decision recorded). Rewrite `ListSitemapURLs` as budgeted BFS (depth ≤5, visited set, existing 100/10k caps, 25s `WithTimeoutCause` budget → partial + `truncated:true`; child errors skip+truncate; empty-result-with-errors → first error). `robotSeeds` = union(grobotstxt, fallback scan). Keep `fetchBody`'s gzip sniff; add the `Content-Encoding: gzip` fixture through the real static fetcher + an `&amp;`-entity fixture. No child-hard-error assertions exist today (only `TestSitemap_Garbage`, which survives unchanged under the empty+errors rule) — add new partial-result tests instead of hunting for ones to update. Crawl seeding: expand via `ListSitemapURLs` capped at `maxPages`, scope-filtered (D.1 `Scope`), warn-and-proceed seed-only on ANY expansion error (never fail the crawl); skip when `NoSitemap`/non-HTTP; move `staticFetcher` construction above seeding.

```go
// Confirmed pattern (stdlib, go1.21+):
const sitemapBudget = 25 * time.Second
var errSitemapBudget = errors.New("crawl: map: sitemap budget exceeded")
bctx, cancel := context.WithTimeoutCause(ctx, sitemapBudget, errSitemapBudget)
defer cancel()
// ... BFS loop; on loop exit check:
// if len(urls) == 0 && firstErr != nil → return nil, false, firstErr
// budget expiry surfaces as context.DeadlineExceeded with Cause()==errSitemapBudget
//   → return urls, true, nil ; parent cancel (other cause) → return nil,false,ctx.Err()
```

**Sanity check:** nested-index fixture (index→index→urlset, depth 3) returns all `<loc>`s; poisoned-child fixture returns siblings with `truncated:true`; `magpie map` on both prints accordingly. Seed-scoping test: expansion mixing in/out-of-prefix URLs with `--path-prefix` enqueues only in-prefix; expansion-error test: crawl proceeds seed-only with a stderr warning.

### Task D.3 — SSRF guard (6h)

**Depends on:** D.0. Implement `ValidateURL` (scheme → blocklist → literal-IP → DNS) + `GuardedTransport()` (dial wrapper with post-connect peer check, redirect re-validation, `Proxy: proxyFunc` placeholder wiring for D5) + `do()` pre-check in `fetch/http.go` + `NewChecker` transport swap + `AllowFile`/`AllowPrivate` resolution (`GOMAGPIE_ALLOW_FILE` / test-binary detection). Sentinel + exit codes: `var ErrPrivateAddress = errors.New("fetch: non-public address")` in `fetch/ssrf.go`, every SSRF rejection wraps it (`%w`); `scrapeExit` (cli/shared.go:97) and the `runCrawl` switch (cli/crawl.go:209) each gain `case errors.Is(err, fetch.ErrPrivateAddress): return fail(2, ...)` so objective §1.4's exit 2 actually ships. Strict-mode tests: table test on `ValidateURL` with explicit `SSRFOptions{}` (incl. `httptest.URL` rejected, `example.com` resolving public accepted — hermetic: use a fake resolver? `LookupIP` on `example.com` needs DNS… hermetic rule! Inject resolver: `ValidateURL` takes a `lookup func(ctx, host) ([]net.IP, error)` param defaulting to `net.DefaultResolver.LookupIP`, tests inject fakes returning public/private IPs. No network in the suite, DNS-rebind shape tested via fake returning private-after-public.). Fetcher-level test: strict `StaticFetcher` against `httptest` URL asserts error containing `ssrf`/`private` and zero bytes served (server hit-counter stays 0 — proves pre-dial rejection). Redirect test: `httptest` → 302 → second `httptest` URL asserts abort naming the redirect. `file://` test: unset-env production-path constructor rejects; `t.Setenv(GOMAGPIE_ALLOW_FILE,1)` + fresh fetcher accepts.

```go
// Confirmed pattern (stdlib net/netip):
func isPublicIP(ip netip.Addr) bool { return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() }
// hostname: strings.TrimSuffix(strings.ToLower(h), "."); block exact "localhost",
// "metadata.google.internal" or suffix ".localhost"/".internal"? keep to the two documented names.
// literal: netip.ParseAddr(strings.Trim(host, "[]")) → isPublicIP or reject naming the IP.
// DNS: addrs, err := lookup(ctx, host); every addr → netip.AddrFromSlice → isPublicIP.
// DialContext wrapper: conn, err := base.DialContext(...); if na, ok := addrFromConn(conn); !isPublicIP → conn.Close + error.
```

**Sanity check:** `go test ./fetch/ -run TestSSRF -v` green; `go run ./cmd/magpie scrape http://127.0.0.1:9/` exits 2; `GOMAGPIE_ALLOW_FILE= go run ./cmd/magpie scrape file:///etc/hostname` exits non-zero naming the flag.

### Task D.4 — Fetch telemetry (3h)

**Depends on:** nothing (parallelizable with D.1–D.3). DDL + `table_info` migration in `store.Open`, `LogFetch`, `RunInfo`/`GetRun` extension, `scrape.Run` + crawl `fetchFn` instrumentation (warn-only), `mcp.runUsage` extension. Migration test: create a pre-D DB (DDL without the columns — build via raw SQL in the test), `Open` it, assert columns exist with 0s and `LogFetch`/`GetRun` round-trip. Counter test: one `scrape.Run` over `file://` + one mini-crawl assert `fetch_pages ≥ 1`, `fetch_bytes == len(html serving?)` (≥1, exact on deterministic fixture), `fetch_ms ≥ 0`.

```go
// Confirmed pattern (modernc sqlite — PRAGMA table_info works):
rows, _ := db.Query(`PRAGMA table_info(run_history)`) // collect names
for _, col := range []string{"fetch_pages INTEGER NOT NULL DEFAULT 0", ...} {
    if !has[name] { db.Exec(`ALTER TABLE run_history ADD COLUMN ` + col) }
}
func (d *DB) LogFetch(runID string, nBytes, ms int64) error {
	_, err := d.db.Exec(`UPDATE run_history SET fetch_pages=fetch_pages+1, fetch_bytes=fetch_bytes+?, fetch_ms=fetch_ms+? WHERE run_id=?`, nBytes, ms, runID)
	...
}
```

**Sanity check:** `go test ./store/ -run 'TestFetchCounters|TestMigrationAddsFetchColumns' -v` green.

### Task D.5 — Single-proxy env (2h)

**Depends on:** D.3 (shared `GuardedTransport`). Implement `proxyFunc` in `fetch/http.go` (`GOMAGPIE_PROXY` > `ProxyFromEnvironment`, minimal `NO_PROXY` suffix match), wire `Transport.Proxy`, document precedence in `map`/`scrape`/`crawl` long-help + README proxy paragraph. Tests: proxy-hit counter, bypass, invalid value.

```go
// Confirmed pattern (stdlib):
func proxyFunc(req *http.Request) (*url.URL, error) {
	if v := strings.TrimSpace(os.Getenv("GOMAGPIE_PROXY")); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("fetch: bad GOMAGPIE_PROXY %q: want http(s)://host:port", v)
		}
		if noProxyMatch(req.URL.Hostname()) { return nil, nil }
		return u, nil
	}
	return http.ProxyFromEnvironment(req)
}
```

**Sanity check:** `GOMAGPIE_PROXY=http://127.0.0.1:$P magpie scrape http://example.com/` hits the proxy (counter 1); `NO_PROXY=example.com` → counter 0.

### Task D.6 — Bomb-cap proof + docs + full gates (3h)

Gzip-bomb test (raw ~50 KB → 60 MB decoded, assert `len(HTML) == 50<<20` exactly), `testdata/` fixtures as needed (sitemap depth/entity/robots-tolerance). Update README (scope flags, `map` behavior, SSRF + `GOMAGPIE_ALLOW_FILE`, `GOMAGPIE_PROXY` precedence, new `run_history` columns), `cli` help-text docs test for the 5 new crawl flags + new `CrawlIn` fields in the MCP catalog test if it pins inputs. Run the full gates: `go test ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds. Note for Phase E in code: comment on `GuardedTransport` that a uTLS swap must preserve the dial guard + proxy func.

---

## 4. Deliverables

```
plan/phase-D.md              # this file
crawl/scope.go               # Scope, CompileScope (+caps), Allows, SkipExtension + ext map
crawl/crawl.go               # (edit) Options += 5 fields; scope built once; scope-filtered sitemap seed expansion (warn-and-proceed); fetchFn LogFetch
crawl/frontier.go            # (edit) ExtractLinks takes Scope; ext skip; scope check per link
crawl/sitemap.go             # (edit) budgeted BFS depth≤5, visited set, skip+truncate, union robots seeds
crawl/robots.go              # (edit) Checker uses fetch.GuardedTransport
fetch/ssrf.go                # SSRFOptions, ErrPrivateAddress sentinel, ValidateURL (injectable lookup), blocklist, isTestBinary
fetch/http.go                # (edit) GuardedTransport, do() pre-check, redirect validation, proxyFunc, bomb-cap comment
store/sqlite.go              # (edit) 3 columns + table_info migration, LogFetch, RunInfo/GetRun extension
scrape/scrape.go             # (edit) fetch timing + LogFetch (warn-only)
mcp/tools.go                 # (edit) CrawlIn += 5 fields; runUsage += fetch counters; ScrapeIn URL doc notes file:// env-gating
cli/shared.go                # (edit) scrapeExit: ErrPrivateAddress → exit 2 (wires objective §1.4)
cli/crawl.go                 # (edit) 5 new flags + early CompileScope (exit 2) + NoSitemap threading + ErrPrivateAddress → exit 2 case
crawl/scope_test.go          # glob caps, Allows matrix (host/sub/prefix/include/exclude-wins), ext table
crawl/sitemap_test.go        # (add) depth-5, entity, tolerance, poisoned-child, budget-partial, encoding shapes (no existing assertions change)
crawl/crawl_test.go          # (edit) ExtractLinks call sites; NoSitemap test; seed-expansion test; seed-scoping + warn-and-proceed tests
fetch/ssrf_test.go           # ValidateURL table (fake resolver), pre-dial reject, redirect abort, file gate
fetch/proxy_test.go          # proxy hit/bypass/invalid
fetch/fetch_test.go          # (edit/add) gzip-bomb cap assertion
store/sqlite_test.go         # (edit/add) migration + LogFetch round-trip
mcp/*_test.go                # (edit/add) new CrawlIn fields coerce; usage includes fetch counters
cli/*_test.go                # (edit/add) flag help text; bad-glob exit 2
```

`mcp/coerce.go` needs **no** change (verify in D.1 test). `cli/map.go` needs **no** change (hardening is behind `ListSitemapURLs`). `scrape.Run` signature unchanged.

---

## 5. Exit Criteria

- [ ] Frontier scope matrix green: subdomain on/off, glob include/exclude (exclude wins), prefix, ext skip, bad glob → exit 2 (D.1)
- [ ] `magpie crawl --no-sitemap` makes zero sitemap fetches; default seed expansion lists page URLs (not sitemap XMLs), capped at `--max-pages`, scope-filtered, and warn-and-proceed seed-only on expansion errors (D.2)
- [ ] Sitemap depth-5 + entity + tolerance + poisoned-child-partial + budget-partial fixtures pass; both gzip shapes (raw sniff + content-encoding) pass (D.2)
- [ ] SSRF table green (fake resolver incl. DNS-rebind shape); strict fetcher rejects `httptest` URL with 0 server hits; redirect-to-private aborts; `file://` gated by env/test-binary; user-supplied private URLs exit 2 via `scrapeExit`/`runCrawl` sentinel cases (D.3)
- [ ] Gzip bomb capped at exactly 50 MB; `LogFetch` round-trips; pre-D DB files migrate on open (D.4/D.6)
- [ ] Proxy hit/bypass/invalid tests green; `Checker` robots fetches honor `GOMAGPIE_PROXY` (D.5)
- [ ] `CrawlIn` new fields coerce stringy inputs; `runUsage` exposes fetch counters; crawl help text lists 5 new flags (D.1/D.4/D.6)
- [ ] `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds pass (D.6)

---

## 6. Execution Prompt

---
You are implementing Phase D (crawl + sitemap + security, stdlib-only) of gomagpie (`magpie`, module `gomagpie`, Go 1.26).

### What this project is

Go CLI web scraper: fetch → clean → extract. Single static binary (`CGO_ENABLED=0`, 3-way cross-builds windows/amd64 + linux/amd64 + darwin/arm64), pure-Go, no CGO ever. Source of truth for this phase: `plan/webclaw-gap-spec.md` §5-Phase D. Rules: `AGENTS.md` + `.pi/rules/go.md` + `.pi/rules/testing.md`. Lazy senior dev: reuse helpers, stdlib first, no new dependency without asking (none needed — `net/netip`, `regexp`, `context.WithTimeoutCause` are stdlib), no new abstraction for one caller. Hermetic tests only (`httptest`, fakes, injected DNS lookup) — no live network in the default suite. Note: `scrape/` imports `crawl/`, so `crawl` must never import `scrape` — `Scope` lives in `crawl/scope.go`.

### Established in prior phases (reuse all of it)

- `crawl.Run(ctx, Options) (Result, error)` (crawl/crawl.go): SQLite frontier, pump (Claim batches ≤32, cap `maxPages`), `staticFetcher` via `fetch.NewStaticFetcher()`, robots `Checker` gates, `FetchWithRetry` taxonomy, `frontier.ExtractLinks(html, finalURL, depth, maxDepth, sameHost bool)` at :330, quality-error page path, `db.BeginRun/FinishRun/CrawlStats`. Seeding today enqueues `checker.Sitemaps` raw sitemap-XML URLs as pages (D.2 replaces this with expansion). `maxDepth<=0→3`, `<0` = seed-only.
- `crawl.ListSitemapURLs(ctx, Fetcher, site)` (crawl/sitemap.go): robots seeds via `Checker`-independent `robotSeeds` (Fetcher seam + `grobotstxt.Sitemaps`, fallback `/sitemap.xml`), 0x1f8b gzip sniff, ONE index level, ≤100 children / ≤10k URLs / `truncated` flag, hard errors naming the URL. `magpie map` (cli/map.go) and MCP `map` are thin wrappers — no changes needed there.
- `fetch.StaticFetcher` (fetch/http.go): `http.Transport{Proxy: http.ProxyFromEnvironment, …}` + `file://` transport registered globally + 10-redirect cap + 50 MB `LimitReader` on the **decoded** stream + challenge-warmup retry + profiles/cookies. `FetchRequest{URL, Timeout, Profile, Cookies}`, `FetchResponse{URL, FinalURL, StatusCode, HTML, Headers}`. `crawl.NewChecker` (crawl/robots.go) has its OWN `http.Client{Timeout:10s}` (D.3/D.5 unify via `GuardedTransport`).
- `store.DB` (store/sqlite.go): `run_history(run_id, command, started_at, finished_at, pages_ok, pages_err, prompt_tokens, completion_tokens, usd_estimate, status)`, `SetMaxOpenConns(1)`, `BeginRun/FinishRun/LogLLMCall/RunCost/GetRun→RunInfo`. DDL is `CREATE TABLE IF NOT EXISTS` — D.4 adds a `table_info` migration for existing DBs.
- `scrape.Run(ctx, Deps, url, Options) Result` (scrape/scrape.go): per-call `BeginRun("scrape")`/`FinishRun`, `d.Fetcher` nil → static, validation pre-I/O (exit-2 style errors). D.4 adds warn-only `LogFetch` around its fetch.
- MCP (mcp/tools.go, coerce.go, server.go): 11 tools; `CrawlIn{URL, MaxPages FlexInt, MaxDepth FlexInt, SameHost *FlexBool, Schema FlexMap, RunID}`; `widenToolInput` already widens string/bool/array/object; `runUsage` builds LLM usage from `RunInfo`. New fields reuse `StringList`/`*FlexBool` — no coerce.go change.
- CLI (cli/crawl.go): flags `--schema/--format/--out/--resume/--max-pages/--max-depth/--concurrency/--same-host/--rate/--ignore-robots/--provider/--model/--exporter-cmd`; `fail(code,…)` + exit codes (2 usage, 3 empty, 4 partial, 5 robots, 6 cost, 7 key, 8 quality). `file://` seeds used across `cli/*_test.go`, `fetch/fetch_test.go:115` — the D.3 test-binary hatch keeps them green.
- Gates: `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds.

### Data model rules (follow exactly)

- Boundary structs only gain plain fields (`crawl.Options`, `CrawlIn`, `RunInfo`) — same json/jsonschema tag style, `*FlexBool`/`StringList` for MCP scalars/lists.
- `crawl.Scope` holds COMPILED `[]*regexp.Regexp` (never raw glob strings past `CompileScope`); `fetch.SSRFOptions{AllowPrivate, AllowFile bool}` is a pure value struct.
- Telemetry is columns, not a new table: `fetch_pages/fetch_bytes/fetch_ms` on `run_history`, `DEFAULT 0`, migrated via `table_info`.

### Architecture (from §2 above — implement as drawn)

`crawl/scope.go` (Scope+CompileScope+Allows+SkipExtension) → `ExtractLinks` takes `Scope` (fix the 3 call sites, no compat wrapper) → CLI validates globs early with the same `CompileScope` (`fail(2)`). `fetch/ssrf.go` (`ValidateURL` with injectable `lookup`, blocklist, `isTestBinary`) + `fetch.GuardedTransport()` (dial peer-check + redirect validation + `proxyFunc`) consumed by `StaticFetcher` and `Checker`. `ListSitemapURLs` becomes budgeted BFS (depth 5, visited, skip+truncate, empty+errors→first error, union robots seeds). Crawl seeding expands via `ListSitemapURLs` capped at `maxPages`, scope-filtered, warn-and-proceed seed-only on error (never fails the crawl; skip if `NoSitemap`/non-HTTP). `store` migration + `LogFetch` (warn-only call sites in `scrape.Run` + crawl `fetchFn`). `GOMAGPIE_PROXY` wins over standard env with minimal `NO_PROXY`.

### Confirmed API snippets (stdlib — no research needed)

```go
// Glob → anchored regexp (crawl/scope.go):
var b strings.Builder; b.WriteString("^")
for i := 0; i < len(g); {
	switch {
	case strings.HasPrefix(g[i:], "**"): b.WriteString(".*"); i += 2
	case g[i] == '*': b.WriteString("[^/]*"); i++
	case g[i] == '?': b.WriteString("[^/]"); i++
	default: b.WriteString(regexp.QuoteMeta(string(g[i]))); i++
	}
}
b.WriteString("$"); re, err := regexp.Compile(b.String())
// caps before compiling: len(g) ≤ 1024, strings.Count(g, "**") ≤ 4.

// SSRF IP predicate (fetch/ssrf.go):
func isPublicIP(ip netip.Addr) bool { return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() }
// literal: netip.ParseAddr(strings.Trim(host, "[]")); DNS: lookup(ctx, host) → every IP must satisfy it.
// test hatch: func isTestBinary() bool { return flag.Lookup("test.v") != nil }

// Budgeted sitemap (crawl/sitemap.go):
bctx, cancel := context.WithTimeoutCause(ctx, 25*time.Second, errSitemapBudget)
defer cancel()
// budget expiry (Cause()==errSitemapBudget) → return urls, true, nil
// parent cancel (other cause / ctx.Err()!=nil) → return nil, false, ctx.Err()

// Proxy (fetch/http.go):
// GOMAGPIE_PROXY set → must parse as http(s)://host:port else loud error; NO_PROXY = comma
// exact-or-dot-suffix match → direct; else standard http.ProxyFromEnvironment(req).

// Migration (store/sqlite.go): PRAGMA table_info(run_history) → ADD COLUMN for each missing of
// fetch_pages/fetch_bytes/fetch_ms INTEGER NOT NULL DEFAULT 0.
```

### Files to create/edit (per-file guidance)

- `crawl/scope.go` (new): `Scope`, `CompileScope` (caps+prefix normalize+compile), `Allows(seed, cand *url.URL) bool` (host→prefix→include→exclude-wins), `SkipExtension` + ext map. Pure, no I/O, no imports beyond stdlib.
- `crawl/frontier.go` + `crawl/crawl.go`: signature change + 5 new `Options` fields + scope built once in `Run` + scope-filtered seed expansion (warn-and-proceed on stderr, never an error return) + `fetchFn` timing/`LogFetch`. Globs match the post-`Canonicalize` URL string. Keep `SameHost` default true end-to-end (CLI default, MCP nil→true).
- `crawl/sitemap.go`: budgeted BFS rewrite per §2; keep exported signature + 100/10k consts; `robotSeeds` union fallback.
- `crawl/robots.go`: swap client transport to `fetch.GuardedTransport()` only.
- `fetch/ssrf.go` (new): options, `ErrPrivateAddress` sentinel (every rejection wraps it), `ValidateURL(raw string, lookup ..., o SSRFOptions) error` (nil lookup = default resolver), blocklist, hatch.
- `fetch/http.go`: `GuardedTransport()`, `proxyFunc` + `noProxyMatch`, `do()` pre-check, `CheckRedirect` validation, bomb-cap comment. `NewStaticFetcher` resolves Allow flags from env/test-binary.
- `store/sqlite.go`: columns + migration + `LogFetch` + `RunInfo`/`GetRun`.
- `scrape/scrape.go`: time fetch, warn-only `LogFetch`.
- `mcp/tools.go`: 5 `CrawlIn` fields + `runUsage` counters + `ScrapeIn` URL doc touch-up (file:// env-gated). `cli/crawl.go`: 5 flags + early validation + threading + sentinel→exit-2 case. `cli/shared.go`: `scrapeExit` sentinel→exit-2 case. README + help texts.
- Tests per §4; update the 2 `ExtractLinks` test call sites (comment citing this plan). No child-hard-error sitemap assertions exist — add new partial tests.

### Success criteria

§5 exit criteria, all boxes checked. Post-phase, `magpie crawl` is scoped (prefix/globs/subdomains/no-sitemap), `magpie map` is robust (depth-5/budget/partial/tolerant), every fetch is SSRF-guarded + proxy-aware, and every run row accounts fetch bytes/ms next to LLM tokens — all with zero new dependencies.

### Expected file structure at end

See §4 Deliverables tree.

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available (crawl.Run/Options/ExtractLinks call sites, ListSitemapURLs+caps, StaticFetcher transport, run_history DDL, scrape.Run run-ID plumbing, CrawlIn/coercion types, CLI flag conventions — all verified by direct file reads this session)
- [PASS] Every sub-task has a clear, testable completion condition (each task ends with a Sanity check one-liner)
- [PASS] Execution prompt is self-contained: includes (a) what prior phases established, (b) confirmed API snippets (glob→regexp, netip predicate, WithTimeoutCause, proxy, table_info — all stdlib, no web research needed), (c) a Data Model Rules section, (d) per-file guidance, and (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables (one criterion per scope/sitemap/SSRF/telemetry/proxy item + gates)
- [PASS] Heavy external dependency strategy noted (no new deps; DNS hermetic via injected lookup; gzip-bomb/proxy/redirect tests via httptest; LLM-free throughout)
- [PASS] New libraries: none — net/netip, regexp, net/http, context cause APIs are stdlib (go 1.26); confirmed via go.mod read. No `web_search` required.
