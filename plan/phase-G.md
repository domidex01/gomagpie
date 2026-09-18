# Phase G — P0 parity gaps (proxy pool, typed challenges, TLS breadth, output formats, search)

**Duration:** Days 1–5 (~33 hours)
**Depends on:** Phase E (uTLS clients + `ValidBrowser`/`resolveBrowser`, browser dial), Phase A (`--page-format`, quality gate exit 8, `clean.Render`), Phase C (MCP surface, `scrape.Prompt`), Phase F (`fetch.GuardedTransport()` consumers pattern via crawl/robots.go)
**Blocks:** P1 items from competitive-analysis-2026-09-18.md (actions DSL wants screenshot output; watch wants diff + run_history; locale knobs want header profiles from G.3)
**Risk Level:** HIGH — G.1 rewires the egress trust seam (`dialPeerAllowed`'s `proxied` flag currently keys on `os.Getenv("GOMAGPIE_PROXY")`; the pool must preserve "operator-chosen egress is trusted, everything else is peer-checked" exactly, and a wrong flag either breaks Tor-on-loopback or silently drops the SSRF peer-IP backstop). G.2 changes fetch→clean control flow. Everything else is additive. Mitigation: a table-driven security matrix test is the phase gate.
**Stack:** go *(per AGENTS.md: `run-phase` hard-blocks on `stack: go` — execute manually via the execution prompt in §7)*
**New libraries:** none. `impersonate-http` v0.4.0 already carries all needed profiles + a socks5/http `ProxyDialer`; SOCKS5 in the stock transport is stdlib (`http.Transport.Proxy` accepts `socks5`/`socks5h` URLs on Go 1.26); search backends are plain HTTP JSON. No dependency ask.

---

## 1. Objective + What Success Looks Like

Close the five P0 parity gaps from `plan/competitive-analysis-2026-09-18.md` §P0: (1) BYO proxy pool with rotation, failover, and sticky sessions; (2) typed bot-challenge detection so a 200 challenge page errors loudly with a vendor name instead of flowing into cleaning as garbage; (3) browser profile breadth (safari, edge, ios, chrome_android) — the library already has them; (4) `html`/`raw`/`screenshot` page formats; (5) a `magpie search` command with six BYOK/no-key SERP backends that can scrape top-N hits through the normal pipeline. All local-first/BYOK — no hosted anything.

1. `GOMAGPIE_PROXY_FILE` with 3 mixed entries (`http://`, `socks5://127.0.0.1:9050` Tor, `host:port:user:pass` vendor-paste form) + `GOMAGPIE_PROXY=http://bad-url` unset: fetching through an httptest proxy succeeds, a second request uses the next entry (round-robin), and the Tor-style loopback entry is accepted without tripping the SSRF peer check (it is operator-chosen egress). Malformed pool file → exit 2 pre-I/O naming the line number.
2. With one pool entry dead (connection refused): the fetch transparently fails over to the next entry, the dead one is skipped for 60s, and `run_history.proxy` records the redacted `host:port` that actually served — never credentials.
3. `{{session}}` in a proxy URL resolves to the same token for the same target host within a run (sticky sessions for rotating-gateway vendors) and a different token per host.
4. A minimal Cloudflare-challenge fixture served over httptest: `magpie scrape` (render auto) no longer emits garbage markdown — it reports `challenge: cloudflare` (typed), warms up + retries, then escalates to rod if configured; a **rich** article that merely says "Just a moment" in its body still cleans normally (size gate holds).
5. `--browser safari|edge|ios|chrome_android` all accepted by scrape/crawl/batch/map; each resolves to the correct uTLS `ClientHelloID`; `random` picks among all 6 profiles; unknown names still exit 2.
6. `magpie scrape URL --page-format html` emits cleaned (scope-applied) HTML; `--page-format raw` emits the decoded response body untouched; `--page-format screenshot --out shot.png` writes a PNG (full-page via rod, `--viewport 1280x800` honored); without `--out`, JSON carries base64 in `content`.
7. `magpie search "golang scraping" --provider brave --limit 5 --scrape-top 2` hits the Brave API with `GOMAGPIE_BRAVE_API_KEY`, prints 5 JSONL hits ranked by position, and the first 2 records embed full scrape results. `--provider searxng` with `GOMAGPIE_SEARXNG_URL` works with **no key**. Missing key → the extract-style missing-key hint and exit code (see `cli/exitfor_test.go`; expected 7). MCP gains the `search` tool (#12).
8. `go test ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...`, and 3-way `CGO_ENABLED=0` cross-builds pass; no live network in the default suite; `testdata/clean` golden drift empty.

**Good:** "`GOMAGPIE_PROXY_FILE=testdata/proxy-pool.txt magpie scrape URL` serves via entry 2 after entry 1 refuses connections, and `run_history.proxy` shows `127.0.0.1:8888` (redacted)"
**Bad:** "Proxies work and challenges are detected better"

---

## 2. What Failure Looks Like (and what to do)

- **Security matrix test red** (peer check skipped for a non-proxied dial, or a loopback *target* dialable because pool code touches `AllowPrivate`) → stop, revert `fetch` to the env-single proxy, ship G.2–G.6 without the pool, re-plan the pool. Never weaken `dialPeerAllowed` semantics to make a test green.
- **`http.Transport` rejects `socks5h`** (stdlib scheme coverage) → normalize `socks5h` → `socks5` at pool parse (stdlib's SOCKS5 dialer sends the hostname to the proxy anyway); never silently downgrade — log the normalization once at parse.
- **Challenge detector misfires on rich pages** → the size gate (`clean.WordCount < clean.ThinPageWords` AND `< 15KB`) is the contract; fix the signature list, never the gate. Every challenge fixture stays under the gate; the "rich article mentioning Just a moment" fixture stays over it.
- **`CleanedPage.HTML` regresses markdown goldens** → field is additive `omitempty`; if `testdata/clean` drifts non-empty, the serialization touched scoring — back it out and serialize in `Render` instead.
- **Screenshot tests flake in CI** (rod launch) → gate them `//go:build browser` per AGENTS.md; keep flag/validation/JSON-shape coverage in the default suite.
- **Search provider shape drift** (Brave/Serper change JSON) → fixtures in `testdata/search/*.json` are the contract; regenerate from provider docs with a dated comment, never from a live call inside tests.

---

## 3. Architecture / Key Design Decisions

```
             GOMAGPIE_PROXY (single, unchanged) ─┐
flag --proxy-file ──► GOMAGPIE_PROXY_FILE ────────┤
                                                  ▼
                                       fetch/proxy.go pool (parse-once singleton)
                                       round-robin | sticky-host | {{session}}
                                       60s cooldown on dial/CONNECT error
                                                  │
             proxyForHost(hostname) ◄─────────────┘   (same per-request seam as today)
              │                    │
   stock transport Proxy   browserDial → impersonate.ProxyDialer
              └──────────┬─────────┘
                    proxiedForHost() ──► dialPeerAllowed(peer, proxied)   ← THE seam
magpie search "q" ─► provider registry (6 backends, GuardedTransport client)
                       └► hits[] ─► --scrape-top N ─► scrape.Batch ─► JSONL
fetch response ─► DetectChallenge(body, headers, status) ─► ChallengeError{Vendor}
                       └► warmup+retry (exists) ─► still challenge → typed failure
```

### Data Model Rules (Go — per repo ethos: plain data, no single-impl interfaces)

| Construct | Shape | Why |
|---|---|---|
| Proxy pool | concrete struct `pool` + `sync.Mutex` (entries, rr counter, cooldowns, session map) | one implementation, no interface; state is inherently mutable |
| Pool file entry | `poolEntry{u *url.URL, raw string}` | raw kept for redaction and `{{session}}` re-substitution |
| Challenge result | `ChallengeError` (error impl: `Vendor string`, `StatusCode int`) | callers `errors.As` it; message crosses MCP boundary — text is API |
| Search providers | `map[string]SearchProvider`, `type SearchProvider func(ctx, *http.Client, string, int) ([]SearchHit, error)` | data registry like `fetch.HeaderProfiles`, not a plugin system |
| Search hit | `SearchHit{Position int, Title, URL, Snippet string}` | plain struct, JSONL-record embedded |
| Page formats | extend existing `switch`es (`clean.Render`, `scrape.ValidateOptions`) | one switch site each — the established pattern |

**Other critical rules for this phase:**
- Redaction is table stakes (webclaw got burned): `RedactProxy(u) → "host:port"`, userinfo never appears in any error, log, or record. A test greps pool error paths for the password.
- Fail loudly pre-I/O: bad pool line → exit 2 with line number; unknown browser/format/provider → `OptionsError` (exit 2) — never a silent fallback.
- `proxiedForHost` derives from `proxyForHost` (non-nil result ⇒ proxied) — one source of truth for the trust decision.
- Search HTTP calls use `fetch.GuardedTransport()` (crawl/robots.go precedent) so SSRF + proxy pool are inherited for free; no new transport code.
- No live network in the default suite: challenge fixtures are synthetic blobs; search fixtures are recorded JSON in `testdata/search/`; browser-gated tests carry `//go:build browser`.

---

## 4. Tasks

### Task G.1 — Proxy pool, rotation, failover, redaction (8h)

One new file, `fetch/proxy.go`, plus rewiring the existing per-request seam (this is why the refactor is cheap: static, robots, and uTLS all funnel through `proxyForHost`).

```go
// fetch/proxy.go — parse-once pool, consulted by the existing proxyForHost.
// Sources: GOMAGPIE_PROXY_FILE wins over GOMAGPIE_PROXY (single = 1-entry pool).
// Line forms: URL (http|https|socks5|socks5h) OR host:port:user:pass (vendor paste).
// '#' comments, blank lines skipped. Strategy: GOMAGPIE_PROXY_STRATEGY=round-robin|sticky-host.
// {{session}} in a URL → per-host stable 8-hex token (map[host]token under pool mutex).
```

- Parse: `parsePool(content []byte) ([]poolEntry, error)` — pure function, table-tested; line numbers in errors; `host:port:user:pass` → `http://user:pass@host:port`; malformed → error naming the line. `socks5h` normalization decided here (stdlib check first).
- Selection: `pool.get(hostname)` — round-robin (atomic counter) or sticky-host (FNV-1a hash of hostname mod alive entries). Dead entries (cooldown `time.Now().Add(-cooldown)` fresh) skipped; all dead → error listing **redacted** endpoints.
- Failover: `pool.reportFailure(entry)` sets 60s cooldown. A retry loop (≤ min(3, len(pool)) attempts) wraps the single-attempt `do()` in `fetch/http.go` — dial/CONNECT/timeout errors only; HTTP 4xx/5xx is a page outcome, not a proxy failure.
- **The seam:** `guardedDialFunc`'s `proxied := os.Getenv("GOMAGPIE_PROXY") != ""` becomes `proxied := proxiedForHost(host)` where `proxiedForHost(h)` = `proxyForHost(h)` returns non-nil. Tor (`socks5://127.0.0.1:9050`) must pass; a loopback *target* without a proxy must still be rejected. Stock-transport scheme validation in `proxyForHost` widens from `http|https` to `http|https|socks5|socks5h` (stdlib supports both since Go 1.20 — verify with one httptest/loopback SOCKS test; the uTLS path already accepts all four via `impersonate.ProxyDialer`).
- NO_PROXY carries over untouched: a bypassed host makes `proxyForHost` return nil → `proxiedForHost` false → the peer check applies (safe default, inherited for free — pool entries honor NO_PROXY by construction).
- Surfacing: `run_history` gains `proxy TEXT DEFAULT ''` via the existing `migrateRunHistory` column list (`store/sqlite.go`); the retry loop records `RedactProxy(chosen)` on success. Scrape result does **not** gain the field (run records are the contract).
- Wiring: `--proxy-file` becomes a root persistent flag; `cli/root.go` `PersistentPreRunE` sets `GOMAGPIE_PROXY_FILE` from it (flag > env, one line). `ponytail:` env is the real config surface; the flag is sugar so GUI/docs don't need env plumbing.
- Tests: parse table (forms, comments, line numbers, `{{session}}`, socks5h); selection strategies; cooldown skip + all-dead error (redaction asserted); **security matrix** (Task G.7 gate). The existing `fetch/proxy_test.go` (GOMAGPIE_PROXY hit, NO_PROXY bypass, bad-value-loud) must keep passing — extend that file; prefix new pool test names `TestPool*` to avoid collisions.

**Sanity check:** `GOMAGPIE_PROXY_FILE=/tmp/pool.txt go test ./fetch/ -run TestProxyPool -v`

---

### Task G.2 — Typed challenge detection → `ChallengeError` (4h)

New `fetch/challenge.go`; `fetch/profiles.go`'s `IsChallengePage` becomes the size-gate helper it already is.

```go
type ChallengeError struct{ Vendor string; StatusCode int }
func (e *ChallengeError) Error() string // "fetch: bot challenge (cloudflare) on <url> [status 403]"

// DetectChallenge returns vendor or "". Header signature is authoritative (no size gate);
// body signatures are size-gated via the existing IsChallengePage gate.
func DetectChallenge(body []byte, headers http.Header, status int) string
```

- Signatures (first match wins), ported from webclaw's public set per the analysis:
  `cf-mitigated` header non-empty → `cloudflare`; `_cf_chl_opt`, `challenge-platform/h/b/orchestrate/`, `/h/g/orchestrate/` → `cloudflare`; `challenges.cloudflare.com/turnstile` → `turnstile`; `datadome` + captcha/geo markers → `datadome`; `awswaf` → `awswaf`; `h-captcha` + block/verify text → `hcaptcha`. All body checks run only under the thin-page gate.
- Integration: the static fetch path checks `DetectChallenge` on every response. Challenge + existing conditions → current warmup-homepage + retry flow fires (http.go:184 — today gated on Profile/Browser being set; now also on `DetectChallenge != ""`). Retry still challenge → return `*ChallengeError`.
- Scrape: `render=auto` + `errors.As ChallengeError` → one rod escalation attempt (`fetchBrowser` path exists), re-check; still challenge → fail with the typed message. Crawl: page error records vendor string in the existing page-error path (Phase A hook).
- `clean/quality.go` stays untouched — it remains the final backstop; detection runs before clean sees the body, which is the entire point (no more garbage markdown or false quality-blocks on 200 challenge pages).
- Tests: one minimal fixture blob per vendor (synthetic, under the gate) + the negative fixture (rich page mentioning "Just a moment" → not a challenge, still cleans). No challenge fixtures exist under `testdata/` today (only article/product/spa-shell in `testdata/clean`) — create `testdata/challenge/{cloudflare,turnstile,datadome,awswaf,hcaptcha}.html` fresh, plus the rich-negative fixture.

**Sanity check:** `go test ./fetch/ -run TestDetectChallenge -v`

---

### Task G.3 — Browser/TLS profile breadth (2h)

The library already ships them — verified in `impersonate-http@v0.4.0/profile.go`: `Profiles = {"chrome", "chrome_android", "firefox", "safari", "edge", "ios"}` with `HelloSafari_Auto`/`HelloEdge_Auto`/`HelloIOS_Auto`/etc.

- `fetch/utls.go`: `ValidBrowser` and `resolveBrowser` accept `safari|edge|ios|chrome_android`; `randomBrowser` picks uniformly from all 6 (keep `sync.OnceValue` — one fingerprint per process).
- `fetch/profiles.go`: add `safari`/`edge`/`ios`/`chrome_android` header profiles (copy the header sets straight from the library's `profile.go` — they are the correct UA/client-hint bundles) so the cleartext fallback matches the fingerprint.
- Update the union everywhere it's spelled out: `scrape.ValidateOptions` message, MCP `jsonschema` strings (`mcp/tools.go`, `mcp/agent.go`), CLI `--browser` help, README. The `*_test.go` files that pin the accepted set get the new names.
- Note in README: profiles govern the static fetch + header escalation only; rod launches real Chrome regardless (browser render ≠ profile).
- Tests: extend `fetch/utls_test.go`/`utls_internal_test.go` — each new name maps to the right `ClientHelloID`; unknown still errors.

**Sanity check:** `go test ./fetch/ ./scrape/ -run 'Browser' -v`

---

### Task G.4 — Page formats: `html`, `raw`, `screenshot` (7h)

- **html:** add `HTML string \`json:"html,omitempty"\`` to `clean.CleanedPage`, populated after scoping (the cleaned document serialized) — additive; markdown goldens must not drift. `clean.Render` gains `case "html"`. PDFs: HTML empty → `Render` returns the markdown inside a minimal `<article>` wrapper (one line; document it).
- **raw:** `scrape` handles before `Render`: `Result.Content` = decoded response body, pipeline otherwise unchanged (quality gate still classifies — G.2 makes that meaningful). Not a `clean.Render` case.
- **screenshot:** also scrape-side (not page text). rod: `page.Screenshot(true)` full-page. `--viewport "1280x800"` flag sets the page viewport before capture. Capability shape (settled — no executor decision needed): the `Fetcher` interface stays untouched (StaticFetcher can't screenshot); add a `Screenshot` method to `RodFetcher` plus a package-level `fetch.ScreenshotPage(ctx, url, viewport)` helper that news+closes a throwaway RodFetcher — same per-call browser pattern as scrape's existing `fetchBrowser`. `ponytail:` fresh browser per screenshot is the known ceiling; client reuse is the upgrade path if MCP load ever demands. `--out` writes PNG bytes; absence → `Result.Content` = base64 PNG. Validation: `screenshot` + explicit `render=static` → `OptionsError` (exit 2); `screenshot` + auto/empty render → takes the browser path.
- Union update in `scrape.ValidateOptions` (`markdown|llm|text|json|html|raw|screenshot`), CLI help, MCP jsonschema strings + the flex-shadow round-trips in `mcp/agent_test.go` (`TestIn_RoundTrip`, Phase C pattern). Crawl accepts `html|raw` per page; `screenshot` in crawl → exit 2 (one page at a time).
- Tests: `Render` table gains html (+PDF wrapper); scrape-level: raw body equality vs fake fetcher, screenshot behind `//go:build browser` + validation/JSON-shape tests in the default suite; coerce round-trip for the new enum values.

**Sanity check:** `go test ./clean/ ./scrape/ -run 'Render|PageFormat' -v`

---

### Task G.5 — `magpie search` + MCP `search` tool (9h)

New `scrape/search.go` (sits beside batch/brand/diff in the command family; no new package).

```go
type SearchHit struct{ Position int; Title, URL, Snippet string }
type SearchProvider func(ctx context.Context, c *http.Client, query string, limit int) ([]SearchHit, error)
var searchProviders = map[string]SearchProvider{...} // data registry, like HeaderProfiles
```

- Client: `&http.Client{Transport: fetch.GuardedTransport(), Timeout: 15*time.Second}` — inherits SSRF guard **and** the G.1 pool for free (crawl/robots.go precedent). Endpoints are package-level vars so tests override with httptest URLs.
- Backends (all P0 per the analysis): **brave** `GET api.search.brave.com/res/v1/web/search?q=&count=` + `X-Subscription-Token`; **serper** `POST google.serper.dev/search` `{q,num}` + `X-API-KEY`; **serpapi** `GET serpapi.com/search?engine=google&q=&num=&api_key=`; **searxng** `GET {GOMAGPIE_SEARXNG_URL}/search?q=&format=json` (no key — the zero-key path); **exa** `POST api.exa.ai/search` `{query,numResults}` + `x-api-key`; **duckduckgo** `GET html.duckduckgo.com/html/?q=` with a small anchor parse (`result__a`) — zero-key fallback. Each backend ~30 lines + one recorded fixture.
- Keys: `cfg.APIKey(provider)` as-is (generic derivation: `GOMAGPIE_BRAVE_API_KEY`, `GOMAGPIE_SERPER_API_KEY`, `GOMAGPIE_SERPAPI_API_KEY`, `GOMAGPIE_EXA_API_KEY`, flag override, keyring). Missing → error naming the provider through `keyHint` → extract's missing-key exit (pin in `exitfor_test.go`; expected 7).
- CLI `cli/search.go`: `magpie search "query" [--provider P] [--limit N=10] [--scrape-top N=0] [--out path]`. `--scrape-top N>0`: first N hit URLs through `scrape.Batch` (existing bounded concurrency + Deps). Output: JSONL, one record per hit — `{position, title, url, snippet}` + optional `"page": {…}` batch record. `--format json` not needed; JSONL is the contract (matches batch precedent).
- MCP `search` tool (#12) in `mcp/tools.go`: `{query, provider?, limit?, scrape_top?}` → same path; missing key surfaces the same typed error.
- Tests: per-provider fixture servers from `testdata/search/*.json` (recorded shapes, dated comment); hit-mapping table per provider; scrape-top wiring with the fake fetcher; no live network.

**Sanity check:** `go test ./scrape/ -run TestSearch -v && go run ./cmd/magpie search --help`

---

### Task G.6 — Docs, spec delta, smoke (2h)

- README: new env vars (`GOMAGPIE_PROXY_FILE`, `GOMAGPIE_PROXY_STRATEGY`, `GOMAGPIE_SEARXNG_URL`, search keys), `--browser` value list, `--page-format` list, `search` command, Tor caveat ("privacy path, not an unblocking path — exits are widely blocked"), credential-redaction note.
- `spec.md`: short §"Egress & challenges" delta (pool semantics, `ChallengeError`, formats, search) — spec stays source of truth.
- `magpie` binary smoke (script, not a test): pool file round-robin over two httptest proxies; challenge fixture scrape prints the typed vendor; search against the searxng fixture server.

---

### Task G.7 — Gate: security matrix + full checks (1h)

The phase gate (HIGH risk). One table-driven test, `fetch/proxy_security_test.go`:

| proxy source | dial target | expectation |
|---|---|---|
| none | loopback target | rejected by peer check |
| env single | loopback target (as proxy egress) | allowed (operator-chosen) |
| pool http entry | loopback target via proxy | allowed |
| pool Tor `socks5://127.0.0.1:9050` | public target | allowed (proxy peer is loopback — trusted egress) |
| pool Tor | loopback *target* | rejected (proxy ≠ license to hit private targets: `ValidateURL` still governs) |
| pool entry with creds | any | credentials never in any error/log string (grep assertion) |

Then: `go test ./... && go vet ./... && gofmt -l . && golangci-lint run ./...` + 3-way `CGO_ENABLED=0` cross-builds + `testdata/clean` drift empty.

---

## 5. Deliverables

```
gomagpie/
├── fetch/
│   ├── proxy.go               # pool: parse, strategies, cooldown, {{session}}, RedactProxy
│   ├── proxy_security_test.go # gate matrix: proxied-flag × source × target
│   ├── challenge.go           # ChallengeError + DetectChallenge signatures
│   ├── http.go                # retry loop, proxiedForHost seam, scheme widening
│   ├── utls.go                # 6-profile ValidBrowser/resolveBrowser, random over all
│   ├── rod.go                 # NEW capability: RodFetcher.Screenshot + fetch.ScreenshotPage helper
│   ├── proxy_test.go          # existing env/NO_PROXY/bad-value tests, extended for the pool
│   └── profiles.go            # header profiles safari/edge/ios/chrome_android
├── clean/
│   ├── clean.go               # CleanedPage.HTML (scoped, omitempty)
│   └── llm.go                 # Render: html case + PDF <article> fallback
├── scrape/
│   ├── scrape.go              # PageFormat union, raw/screenshot handling, ChallengeError escalation
│   └── search.go              # SearchHit, provider registry, 6 backends, scrape-top
├── store/sqlite.go            # run_history.proxy column (migration list)
├── cli/
│   ├── root.go                # --proxy-file persistent flag
│   └── search.go              # magpie search command
├── mcp/tools.go               # search tool (#12); jsonschema string updates
├── testdata/
│   ├── challenge/*.html       # 5 vendor fixtures + rich-negative fixture
│   └── search/*.json          # recorded provider responses (dated)
└── README.md, spec.md         # env vars, values, exits, Tor caveat
```

---

## 6. Exit Criteria

- [ ] `parsePool` table test passes: URL/host:port:user:pass forms, `#` comments, line-numbered errors, `{{session}}`, socks5h normalization
- [ ] Round-robin + sticky-host selection unit tests pass; failover test shows dead entry skipped 60s and all-dead error listing redacted endpoints only
- [ ] Security matrix (`fetch/proxy_security_test.go`) all 6 rows green — **go/no-go gate:** if any row requires weakening `dialPeerAllowed`/`ValidateURL` semantics → stop: revert the pool (keep env-single), ship G.2–G.6, re-plan the pool with an explicit design pass
- [ ] `run_history.proxy` column migrated (old DBs upgrade in place) and populated with redacted host:port after a pooled fetch
- [ ] `go test ./fetch/ -run TestDetectChallenge` passes: 5 vendor fixtures classify, rich-negative fixture does not; challenge scrape emits typed vendor, not garbage markdown
- [ ] `--browser safari|edge|ios|chrome_android` accepted everywhere (`ValidBrowser` set test green); `random` spans 6 profiles; unknown exits 2
- [ ] `--page-format html|raw` outputs match expected bytes via fake fetcher; `screenshot` browser-gated test writes a PNG; `screenshot`+`render=static` exits 2
- [ ] `magpie search` fixture tests pass for all 6 providers; `--scrape-top 2` embeds page records; missing key hits `keyHint` with the pinned exit
- [ ] MCP `search` tool callable; all jsonschema/coerce round-trip tests green
- [ ] `go test ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...`, 3-way cross-builds pass; `testdata/clean` drift empty; no live-network tests in the default suite

---

## 7. Execution Prompt

Copy everything between the `---` lines into a fresh pi session:

---
You are building Phase G of gomagpie (`magpie`) — P0 parity gaps: proxy pool, typed bot-challenge detection, TLS profile breadth, html/raw/screenshot page formats, and a BYOK `search` command.

### What This Project Is
Go 1.26 CLI web scraper (fetch → clean → extract), module `gomagpie`, binary `magpie`. Pure-Go single static binary, zero CGO. Source of truth: `spec.md`; competitive rationale: `plan/competitive-analysis-2026-09-18.md` §P0. Local-first/BYOK positioning — no hosted dependencies. Read `AGENTS.md` and `.pi/rules/go.md`, `.pi/rules/testing.md` first. House ethos: lazy senior dev — fewest files, no single-implementation interfaces, data registries over plugin systems, fail loudly at trust boundaries.

### Established in Prior Phases (verified at HEAD)
- Fetch seam: everything outbound funnels through `fetch/http.go` `proxyForHost(hostname)` (reads `GOMAGPIE_PROXY` per request, validates `http|https` only, honors `NO_PROXY`). The stock transport's `Proxy: proxyFunc` and the uTLS `browserDial` (which calls `impersonate.ProxyDialer`, supporting `socks5|socks5h|http|https`) both use it.
- SSRF: `guardedDialFunc` does dial + post-connect peer-IP check; the check is **skipped when `proxied`** — currently `proxied := os.Getenv("GOMAGPIE_PROXY") != ""` in `fetch/http.go` `guardedDialFunc`. Semantics: operator-chosen egress is trusted (may be loopback, e.g. Tor); non-proxied dials must be public peers. `ValidateURL` governs targets independently. Never weaken either.
- TLS: `fetch/utls.go` `ValidBrowser`/`resolveBrowser` accept `""|chrome|firefox|random`; `random` resolved once per process via `sync.OnceValue`. Library `github.com/North-web-dev/impersonate-http v0.4.0` `Profiles` map already contains: `chrome, chrome_android, firefox, safari, edge, ios` (ClientHelloIDs `HelloChrome_Auto`, `HelloFirefox_Auto`, `HelloSafari_Auto`, `HelloEdge_Auto`, `HelloIOS_Auto`). Header sets to copy for `fetch/profiles.go HeaderProfiles` are in the library's `profile.go`.
- Challenge today: `fetch/profiles.go` `IsChallengePage(body, status)` — size-gated (`clean.WordCount < clean.ThinPageWords` AND `< 15KB`), status check via `clean.ChallengeStatuses`, markers via `clean.HasChallengeMarkers` (single vocabulary in `clean/quality.go` `ChallengeMarkers`). Warmup+retry at `fetch/http.go` ~line 184, currently gated on `req.Profile == "" && req.Browser == ""` || not-challenge. `clean/quality.go` Classify remains the final backstop — do not touch it.
- Page formats: `clean/llm.go` `Render(p CleanedPage, format)` handles `""|markdown|llm|text|json`; `scrape.ValidateOptions` (one switch, shared by CLI/batch/MCP) validates the union and must stay the single validation site; `mcp/tools.go`/`mcp/agent.go` carry `jsonschema` strings; `mcp/agent_test.go` (`TestIn_RoundTrip`) holds the flex-shadow round-trip tests to extend. `CleanedPage` (clean/clean.go) currently has `Markdown, StructuredData, Title, FinalURL, Metadata, Quality` — no HTML field.
- Rod: `fetch/rod.go` `RodFetcher` lazy-launches and is a bare navigate→`page.HTML()` fetch — **no screenshot or viewport code exists yet**; G.4 adds it (`Screenshot` method on `RodFetcher` + `fetch.ScreenshotPage` helper; go-rod API: `page.SetViewport`, `page.Screenshot(true)` = full-page). The `Fetcher` interface (`fetch/fetcher.go`: Fetch/CanHandle/Close) is NOT extended — screenshot is a rod-only capability, scrape calls the helper. Browser-gated tests carry `//go:build browser` (AGENTS.md: no live network/browser in default suite).
- Keys: `config.APIKey(provider)` resolves flag > `GOMAGPIE_<UPPER_PROVIDER>_API_KEY` > `GOMAGPIE_API_KEY` > keyring (`gomagpie` service). CLI maps missing-key errors through `keyHint`; exit codes pinned in `cli/exitfor_test.go` (options=2, quality=8, missing key per that test file).
- Guarded outbound HTTP precedent: `crawl/robots.go` builds `&http.Client{Transport: fetch.GuardedTransport()}` — search providers do the same to inherit SSRF + proxy pool.
- `run_history` migrations: `store/sqlite.go` `migrateRunHistory` ALTER-list pattern — add `proxy TEXT NOT NULL DEFAULT ''` there.
- `scrape.Batch` exists with bounded concurrency and JSONL output shape — reuse for `search --scrape-top`.
- Golden fixtures: `testdata/clean` with `-update` flag; drift must stay empty.

### Your Goal
Implement tasks G.1–G.7 from `plan/phase-G.md` (read it first — it has the exact signatures, signature lists, backend endpoints, and the security matrix).

### Data Model Rules (follow exactly)
- Concrete structs + mutex for the pool; no interface (one implementation).
- `ChallengeError` struct implementing `error` (`Vendor`, `StatusCode`); callers use `errors.As`.
- Search providers: `map[string]SearchProvider` where `type SearchProvider func(ctx context.Context, c *http.Client, query string, limit int) ([]SearchHit, error)` — data registry, not a plugin system.
- Extend existing `switch` sites (`clean.Render`, `scrape.ValidateOptions`); never add a second validator.
- `CleanedPage.HTML` additive with `json:"html,omitempty"` — markdown goldens must not drift.

### Hard Rules
- No new dependencies. No CGO. Never import go-rod outside `fetch/`.
- Credentials (proxy userinfo) never appear in errors, logs, or records — `RedactProxy` everywhere; grep-test it.
- `proxiedForHost` = `proxyForHost(host)` returns non-nil; `guardedDialFunc` uses it instead of the raw env read. All six matrix rows in G.7 must pass without touching `dialPeerAllowed`'s trust logic.
- Malformed pool file / unknown browser / unknown format / unknown search provider / screenshot+static → `OptionsError`-style exit 2 pre-I/O, message names the offending value.
- No live network in the default suite: synthetic challenge fixtures, recorded `testdata/search/*.json`, `//go:build browser` for rod tests.
- HTTP 4xx/5xx is a page outcome, not a proxy failure — failover only on dial/CONNECT/timeout errors, ≤ min(3, len(pool)) attempts.

### Files to Create / Touch
Per `plan/phase-G.md` §5 Deliverables tree and §4 task notes (parse forms, signature list, endpoint table, exit-code expectations are all specified there). Implementation order G.1 → G.2 → G.3 → G.4 → G.5 → G.6 → G.7; G.2–G.4 are independent if you parallelize mentally, but land the security matrix (G.7) only after G.1.

### Success Criteria
- All exit criteria in `plan/phase-G.md` §6 check green, including the G.7 go/no-go gate.
- `go test ./... && go vet ./... && gofmt -l . && golangci-lint run ./...` clean; 3-way `CGO_ENABLED=0` cross-builds (linux/amd64, linux/arm64, darwin/arm64) pass; `testdata/clean` drift empty.

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — execution prompt cites verified HEAD facts: `proxyForHost` seam (`fetch/http.go`), `guardedDialFunc` proxied flag, `impersonate-http` v0.4.0 `Profiles` map (read in module cache), `IsChallengePage` gate, `clean.Render` switch, `config.APIKey` derivation, `migrateRunHistory` ALTER list, `scrape.Batch`, `fetch.GuardedTransport()` consumer precedent (`crawl/robots.go`)
- [PASS] Every sub-task has a clear, testable completion condition — each task ends in a sanity-check command; §6 criteria are observable values (exit codes, file bytes, matrix rows)
- [PASS] Execution prompt is self-contained: (a) prior-phase facts inline, (b) confirmed API snippets (profile names, ProxyDialer schemes, Render cases), (c) Data Model Rules section, (d) per-file guidance, (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables — every file in §5 is exercised by at least one §6 bullet; the G.7 matrix covers the one high-risk deliverable
- [PASS] Heavy external dependencies have stub strategies — rod behind `//go:build browser`, search providers behind recorded fixtures + httptest, challenge fixtures synthetic (no live network in default suite, per AGENTS.md)
- [PASS] New libraries — none; the only library surface (impersonate-http profiles, ProxyDialer) was verified by reading `~/go/pkg/mod/github.com/!north-web-dev/impersonate-http@v0.4.0/{profile.go,proxy.go}`, and stdlib socks5 support is flagged for a one-test verification rather than assumed
