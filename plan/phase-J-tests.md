# Phase J — Testing: Competitive Parity (injection strip, XHR capture, AutoThrottle, sitemap-only, CDP)

**Scope:** `clean/injection.go` + hook in `clean/clean.go` (J.1), `fetch/xhr.go` + additive `FetchRequest.CaptureXHR`/`FetchResponse.XHR` + implied additive `scrape.Result.XHR` (J.2), `crawl/ratelimit.go` `SetAuto`/`Report` + wiring inside the `do` closure (J.3), `crawl` `Options.SitemapOnly` + `ErrSitemapOnlyEmpty` + link-enqueue guard (J.4), `fetch/rod.go` `CDP` field + `ValidateOptions` scheme check (J.5), CLI/MCP param plumbing (J.2/J.3/J.4/J.5)
**Key Pattern:** **No new fakes — every tier reuses an existing oracle.** J.1 rides `clean`'s own harness (`cleanFile`-style direct `clean.Clean` calls + the `golden`/`-update` machinery, with a strict no-blind-`-update` protocol); J.2's contract is pinned at the CLI doc-builder with a *constructed* `scrape.Result{XHR: …}` (the `TestScrape_ScreenshotDoc` precedent — no browser, no fetch fake), its mechanics at pure helper units, and its reality in one `//go:build browser` test modeled on `rod_smoke_test.go`; J.3 is a pure `Limit()`-asserted table PLUS one timing-gap tripwire that fails if `Report` is wired outside the retry closure (the PR #11 dead-wiring class this plan explicitly fears); J.4 reuses `newSiteOrigin` + the robots-declared-sitemap serving pattern from `TestSitemap_RobotsSeeds` and pins `ErrSitemapOnlyEmpty`→exit 3 at three layers; J.5's no-launch proof is default-suite hermetic (a refused loopback dial, no browser involved).
**Dependencies:** stdlib `testing`, `context`, `encoding/json`, `errors`, `fmt`, `net/http/httptest`, `os`, `path/filepath`, `strings`, `sync`, `time`, `regexp` only — plus in-repo helpers: `cleanFile`/`golden` (clean/clean_test.go:34/15), `newSiteOrigin`/`itemPage`/`openCrawlDB`/`countingDo` (crawl/crawl_test.go:140/472/110/364), `fakeSitemapFetcher` (crawl/sitemap_test.go), `fakeAgentFetcher`/`dialInMemory`/`callTool`/`agentDeps` (mcp/agent_test.go), `resetGlobals`/`captureOutput`/`codeOf`/`rootCmd` (cli). No new test deps. **`TestToolCatalog` stays 12 — no new MCP tools, only params; do not touch it.**

**Deviations from plan/phase-J.md (sanctioned, discovered while verifying seams):**
1. **MCP round-trips append to `mcp/agent_test.go`, not `mcp/server_test.go`** — the plan says "following the existing `lang` test," and that test (with `dialInMemory`/`callTool`/`agentDeps` and the flex-shadow round-trips) lives in agent_test.go. server_test.go has no param-round-trip precedent.
2. **`scrape.Result` gains additive `XHR []fetch.XHRCapture` (`json:",omitempty"`)** — implied by "CLI envelope gains `xhr`": `markdownDoc(res)` takes a `scrape.Result`, so the captures must ride it. The envelope pin therefore needs no fetcher fake at all (see US-2).
3. `crawl/ratelimit_test.go` and `crawl/sitemap_only_test.go` don't exist yet — they are plan deliverables (unlike phase I's phantom files) and coherent clusters; create them as named, `package crawl`, reusing `crawl_test.go`'s unexported helpers freely.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an LLM/agent user, I want hidden prompt-injection text (`display:none` etc.) stripped from every cleaned output by default, so that scraped pages can't hijack my agents | `clean/injection_test.go`: six `MAGPIE_HIDDEN_<n>` vectors absent from `Clean`'s markdown; `display:flex` + `aria-hidden` controls survive; visible paragraph intact | every vector marker: `strings.Contains(md, marker) == false`; every control: `== true`; `go test ./clean/...` green with **zero** pre-existing golden changes (spike doc names any exception) |
| US-2 | As a user reverse-engineering a site's JSON API, I want `--capture-xhr` to return the XHR/fetch bodies the page loaded, so that I skip the devtools work | `fetch/xhr_test.go` units (patterns, 64 KiB truncation, base64 fallback, eviction→empty-body metadata); `cli/scrape_test.go` `TestScrape_XHREnvelope` (constructed `Result{XHR}`); `TestCaptureXHRLive` (browser tag) | bad regexp → exit 2; `Truncated:true` past 64 KiB; undecodable base64 keeps raw; evicted id → `Body:""` not error; envelope has `xhr` array only when non-empty; live test: exactly 1 capture, `Body` unmarshals to `{"ok":true}` |
| US-3 | As a polite crawler, I want `--auto-throttle` to back off ×2 on 429/503 and decay toward my configured rate on success, so that block signals pace *future* requests | `crawl/ratelimit_test.go` `TestHostLimiters_Auto*` (pure `Limit()` table) + `TestAutoThrottle_WiredInsideRetry` (timing-gap tripwire) | two 429s → `Limit()` halves (delay ×2); `Retry-After: 10s` → exactly that delay; five 200s → monotonic decay, never below floor; cap 60s; auto-off → `Report` is a no-op; wired test: gap between do-calls ≥ 2× base delay (fails at ~1× if Report is post-retry) |
| US-4 | As a sitemap-driven user, I want `crawl --sitemap-only` to fetch exactly the sitemap URLs and nothing else, so that the frontier is auditable | `crawl/sitemap_only_test.go`: sitemap lists 3 URLs, pages link a 4th — `o.count("/off") == 0`; empty sitemap → `errors.Is(err, crawl.ErrSitemapOnlyEmpty)`; `exitfor_test.go` arm → 3; conflict `--sitemap-only --no-sitemap` → 2 | frontier == exactly the 3 listed URLs; seed not in sitemap → never fetched; typed error surfaces as exit **3** (not the default 1); contradiction exits **2** pre-I/O |
| US-5 | As a farm/CI operator, I want `--cdp-url` to drive a remote browser without ever launching/downloading a local one, so that containers stay slim | `fetch/rod_cdp_test.go` `TestRod_CDPNoLocalLaunch` (unreachable `ws://127.0.0.1:1/x`); `scrape/validate_test.go` scheme rows; CLI env-fallback row | error names `connect remote browser` + **redacted** URL, returns in <2s (no download); `ftp://`/garbage scheme → exit 2 pre-I/O; `MAGPIE_CDP_URL` used when flag empty, flag wins when set |

---

## 1. Component Mock Strategy

Phase type: **integration + pure logic + one browser-tagged tier** (G/H successor; same tiering). Mock strategy in one sentence: **J.1 runs the real `clean.Clean` on a new fixture against the existing golden machinery; J.2 splits into pure helper units (internal `package fetch` test), a constructed-`Result` envelope pin (screenshotDoc precedent — zero fakes), and one rod-tagged live row; J.3 is pure `Limit()` math plus a real-timing wiring tripwire through `fetchWithRetry` + `countingDo`; J.4 runs the real crawl pipeline over `newSiteOrigin` with a robots-declared sitemap; J.5 is a loopback-refused dial plus validation-table rows — and every CLI/MCP param rides existing plumbing tests.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| StripHidden vectors | `clean/injection_test.go` (NEW, `package clean` — needs nothing unexported but sits beside `cleanFile`): build the six-vector page in-test (same content as `testdata/clean/injection.html` fixture — or load it via `cleanFile`-style read) and call `clean.Clean` directly | `MAGPIE_HIDDEN_1..6` (display:none, visibility:hidden, font-size:0, opacity:0, `[hidden]` attr, HTML comment) all absent from `Markdown`; visible paragraph + `Widget` h1 present; **assert on `CleanedPage.Markdown` AND `CleanedPage.HTML`** (strip runs pre-conversion — both surfaces must be clean, that's the choke-point contract) | US-1 |
| StripHidden precision controls | same file | `<span style="display:flex">` and `<span aria-hidden="true">icon</span>` content SURVIVES into markdown (the aria-hidden-out-of-scope pin); a `display : none` (space) and `DISPLAY:NONE` (case) variant are stripped (regexp robustness); `opacity:0.5` survives (the `(?:\.0+)?` boundary) | US-1 |
| Golden drift protocol | existing `TestCleanArticle`/`TestCleanProduct` etc. run **untouched**; spike doc (in the J.1 commit message) inventories any fixture whose golden changes and why it contains hidden text | `go test ./clean/...` green WITHOUT `-update`; `git status --porcelain testdata/` shows ONLY the new `injection.html`; any golden regen is a named, justified diff in the spike doc — never silent | US-1 |
| XHR pattern compile | `fetch/xhr_test.go` (NEW, `package fetch` — internal, so unexported helpers are testable; `actions_test.go` is external only because `ParseActions` is exported) | valid patterns compile; `"[bad"` → error from `ValidateXHRPatterns` naming the pattern; empty slice → nil compiled set, no error | US-2 |
| XHR capture mechanics | `fetch/xhr_test.go` — **requires the drain logic shaped as pure functions** (design-for-test: `truncBody(s string) (string, bool)` and `buildCapture(pending, rawBody string, b64 bool) XHRCapture` or equivalent); no rod, no browser | 64 KiB+1 body → truncated at 64 KiB with `Truncated:true`; exactly-64KiB → not truncated; `Base64Encoded=true` + valid b64 → decoded; invalid b64 → raw kept (fallback, no error); eviction sentinel (`-32000`) → `XHRCapture{URL, Status, MIMEType}` with empty `Body`, `Truncated:false`, nil error propagated; 51st capture dropped (cap 50) | US-2 |
| `capture-xhr` + static rejection; CDP scheme | `scrape/validate_test.go` APPEND (established `TestValidateOptions_Actions`/`_LangControlChars` pattern, `errors.As(*OptionsError)`) | `CaptureXHR:["/api"] + Render:"static"` → OptionsError (mirror screenshot wording); `CDP:"ftp://x"` and `CDP:"not a url"` → OptionsError; `CDP:"ws://…"`, `wss://…`, `http(s)://…` pass; `CDP:""` passes; bad XHR regexp surfaces from ValidateOptions (via `fetch.ValidateXHRPatterns`) pre-I/O | US-2, US-5 |
| XHR envelope pin | `cli/scrape_test.go` APPEND — `TestScrape_XHREnvelope`: construct `scrape.Result{URL:…, XHR: []fetch.XHRCapture{{URL:"https://x/api", Status:200, MIMEType:"application/json", Body:`{"ok":true}`}}}` and call the doc builder directly (screenshotDoc precedent :400 — the envelope function is the contract, no fetcher fake needed); second row with `XHR:nil` | envelope JSON has `xhr` array with url/status/mime/body intact; `XHR:nil` result → **no `xhr` key at all** (omitempty, additiveness: existing envelopes byte-identical); markdown/page-format interaction unchanged | US-2 |
| XHR live capture | `fetch/xhr_test.go` (browser-tagged section or `xhr_browser_test.go`, `//go:build browser`, modeled on `rod_smoke_test.go` incl. both skip styles: `launcher.LookPath()` miss AND launch/connect-error skip) | httptest origin: `/` serves a page whose script `fetch('/api/data')`es; `/api/data` serves `{"ok":true}`; `Fetch` with `CaptureXHR:["/api/"]` → exactly 1 capture, URL ends `/api/data`, status 200, body unmarshals to `{"ok":true}`; a non-matching XHR (e.g. `/other`) is not captured; `CaptureXHR` request field on the STATIC fetcher path → ignored (no error, `XHR nil`) | US-2 |
| CDP no-launch | `fetch/rod_cdp_test.go` (NEW, default suite — a refused loopback dial is hermetic; no Chrome needed, that's the point) | `RodFetcher{CDP:"ws://127.0.0.1:1/x"}` fetch → error within <2s containing `connect remote browser`; error text contains NO userinfo when CDP has `ws://user:pass@127.0.0.1:1/x` (redaction — `RedactProxy` spirit, fetch/proxy.go:189); `Close()` on the failed fetcher doesn't panic (launcher never started) | US-5 |
| AutoThrottle math | `crawl/ratelimit_test.go` (NEW, `package crawl`) — pure table over `NewHostLimiters(1, 3)` + `SetAuto()` + `Report`, asserted via `Limit(host)` (the existing test helper, ratelimit.go:57) | base 1/s → `Limit()==1`; two `Report(host,429,0)` → `Limit()==0.25` (delay 1s→2s→4s); `Report(host,429,10*time.Second)` → `Limit()==0.1` exactly; 2xx after that decays ×¾ per report, monotonic, never below floor (SetFloor'd host decays toward the crawl-delay, not past it); 31 block reports cap at `Limit()==1/60`; `Report` on a non-auto limiter (no SetAuto) leaves `Limit()` untouched (no-op pin); 503 and 500 both back off; explicit Retry-After capped at 5m | US-3 |
| **AutoThrottle wiring (dead-wiring tripwire)** | `crawl/crawl_test.go` APPEND — `TestAutoThrottle_WiredInsideRetry`: `newSiteOrigin` mux that 429s the FIRST hit per path then 200s (per-path hit counter like `TestCrawl_QualityCountedNotCached`); `Options{AutoThrottle:true, Rate:2, …}` run; measure gap between first and second request timestamps | gap ≥ 900ms (base delay 500ms ×2 after the 429). **If `Report` is wired after `FetchWithRetry` returns, backoff.go consumes the 429 and the gap stays ~500ms → test fails.** This is the only >1s test in the suite (~2–3s total); mark it with a comment explaining why the sleep is load-bearing | US-3 |
| Sitemap-only frontier | `crawl/sitemap_only_test.go` (NEW, `package crawl`) — origin serving robots.txt declaring `sitemap.xml`, sitemap listing `/a`,`/b`,`/c`, pages linking `/off` (fixture style of `TestSitemap_RobotsSeeds` but served over `newSiteOrigin`'s mux so `o.count` works) | after `Run{SitemapOnly:true}`: `o.count(/a|/b|/c) == 1` each, `o.count("/") == 0` (seed not listed → not fetched), `o.count("/off") == 0` (link-following off), `PagesOK == 3` | US-4 |
| Sitemap-only empty + errors | same file | sitemap lists nothing → `Run` error with `errors.Is(err, ErrSitemapOnlyEmpty)` true; expansion error (robots points at 404 sitemap) in SitemapOnly mode → fatal error (NOT warn-and-proceed); same expansion error with SitemapOnly=false → still warn-and-proceed (existing behavior intact, run succeeds on seed) | US-4 |
| Sitemap-only exits | `cli/exitfor_test.go` APPEND (typed-error table rows) + one `runCrawl` row | `exitCode(errors.Is` arm): wrapped `ErrSitemapOnlyEmpty` → **3**; `runCrawl` with `SitemapOnly+NoSitemap` → `codeOf(err)==2`; `--sitemap-only` alone against the httptest site → exit 0 | US-4 |
| CDP env fallback + flags | `cli/scrape_test.go` APPEND (`testEnv` sets `MAGPIE_CDP_URL`; direct option plumbing) | flag set → Options.CDP == flag value (env ignored); flag empty + env set → CDP == env value; both empty → `""`; `--cdp-url ftp://x` → exit 2 pre-I/O (zero fetches — assert via a closed-port target never dialed, or ValidateOptions row covers it) | US-5 |
| MCP params | `mcp/agent_test.go` APPEND (`dialInMemory`/`callTool`/`agentDeps`; **not server_test.go** — see deviation 1) | `scrape_url` `capture_xhr:["/api"]` + `cdp_url` round-trip into `scrape.Options` (canned `fakeAgentFetcher` response with `XHR` set → `ScrapeOut.xhr` visible to the agent); `capture_xhr`+static → tool error (options error text crosses the boundary); `crawl_site` `sitemap_only`/`auto_throttle` `*FlexBool` round-trips (true/false/absent); `sitemap_only`+`no_sitemap` → tool error; **`TestToolCatalog` still passes untouched (12 tools)** | US-2, US-4, US-5 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit/Integration (default `go test ./...`) | Loopback httptest (`newSiteOrigin`, CDP refused-dial to `127.0.0.1:1`), real `clean.Clean` on in-repo fixtures, pure `HostLimiters`/XHR-helper tables, constructed `scrape.Result` — **no external network, no DNS, no browser** | <10s added (+ ~2s for the one timing tripwire) | Every push; the only default gate |
| Browser (`go test -tags browser ./fetch/`) | Real rod: local launch (managed download or LookPath Chrome), httptest origin with a `fetch()`ing page | ~5–10s | Manual/pre-release + browser-capable CI job; joins `rod_smoke_test.go`/`actions_browser_test.go` in the gated family |
| Manual (not a test file) | Built binary: a real SPA for `--capture-xhr`, a real docs site for `--sitemap-only`, a real remote-browser endpoint for `--cdp-url`, a real hidden-text page for the strip | minutes | Pre-release eyeball; fixtures prove contract shape, live sites prove reality — never a CI claim |

Fixture rule: this phase adds exactly ONE fixture — `testdata/clean/injection.html` (J.1 spike). `git status --porcelain testdata/` may show ONLY that path; any golden regen must be named in the spike doc.

---

## 3. Fake / Mock Implementations

**No new fakes.** Every oracle already exists and is reused verbatim:

- `clean.Clean` itself (J.1's contract is the real choke point — faking clean would prove nothing)
- `countingDo` (crawl/crawl_test.go:364) — scripted per-attempt responses for retry tests; the timing tripwire needs a timestamping variant (3 lines: append `time.Now()` per call — shown in §7)
- `fakeAgentFetcher` (mcp/agent_test.go) — canned `FetchResponse` (now carrying `XHR`) for MCP round-trips
- `newSiteOrigin`/`fakeSitemapFetcher` (crawl) — origin + sitemap bodies
- Constructed `scrape.Result` literals (cli) — the envelope oracle, per `TestScrape_ScreenshotDoc`

The only new in-test material worth full source — the timestamping `do` for the wiring tripwire (goes in `crawl_test.go` next to `countingDo`):

```go
// timingDo records each attempt's wall time, then serves the script
// (countingDo semantics: last entry repeats). Used by the AutoThrottle
// wiring tripwire ONLY — real sleeps, never parallel with other tests.
func timingDo(times *[]time.Time, script []struct {
	resp *fetch.FetchResponse
	err  error
}) func() (*fetch.FetchResponse, error) {
	return func() (*fetch.FetchResponse, error) {
		*times = append(*times, time.Now())
		idx := len(*times) - 1
		if idx >= len(script) {
			idx = len(script) - 1
		}
		return script[idx].resp, script[idx].err
	}
}
```

And the canned capture for envelope/MCP rows:

```go
cannedXHR := []fetch.XHRCapture{{
	URL: "https://x/api/data", Status: 200,
	MIMEType: "application/json", Body: `{"ok":true}`,
}}
```

---

## 4. Test File List

```
magpie/
├── clean/
│   ├── injection.go                    # DELIVERABLE (impl): StripHidden
│   ├── clean.go                        # DELIVERABLE (impl): hook post-scope pre-conversion
│   └── injection_test.go               # NEW: 6 vectors absent (Markdown AND HTML), flex/aria-hidden
│                                       #   controls survive, case/space regexp robustness, opacity:0.5 survives
├── testdata/clean/
│   └── injection.html                  # NEW FIXTURE (the only one): 6 markers + visible control + survivors
├── fetch/
│   ├── rod.go                          # DELIVERABLE (impl): CDP field + remote branch
│   ├── xhr.go                          # DELIVERABLE (impl): XHRCapture, patterns, drain (pure helpers!)
│   ├── fetcher.go                      # DELIVERABLE (impl): CaptureXHR/XHR additive fields
│   ├── xhr_test.go                     # NEW: pattern compile, truncation/base64/eviction/cap units
│   │                                   #   (package fetch) + browser-tagged TestCaptureXHRLive
│   └── rod_cdp_test.go                 # NEW: TestRod_CDPNoLocalLaunch + redaction (default suite)
├── crawl/
│   ├── ratelimit.go                    # DELIVERABLE (impl): SetAuto/Report
│   ├── crawl.go                        # DELIVERABLE (impl): SitemapOnly, Report wiring, ErrSitemapOnlyEmpty
│   ├── ratelimit_test.go               # NEW: Auto backoff/Retry-After/decay/cap/no-op table (Limit()-based)
│   ├── sitemap_only_test.go            # NEW: exact frontier, seed-exclusion, empty→typed err, expansion-fatal
│   └── crawl_test.go                   # APPEND: TestAutoThrottle_WiredInsideRetry (timingDo tripwire)
├── scrape/
│   ├── scrape.go                       # DELIVERABLE (impl): ValidateOptions rows + Result.XHR additive field
│   └── validate_test.go                # APPEND: capture-xhr+static, CDP schemes, bad XHR regexp
├── cli/
│   ├── scrape.go / crawl.go            # DELIVERABLE (impl): flags, env fallback, envelope xhr block
│   ├── scrape_test.go                  # APPEND: TestScrape_XHREnvelope (constructed Result), CDP env/fallback rows
│   └── exitfor_test.go                 # APPEND: ErrSitemapOnlyEmpty → 3 arm; conflict → 2
├── mcp/
│   ├── tools.go                        # DELIVERABLE (impl): capture_xhr/cdp_url/sitemap_only/auto_throttle params
│   └── agent_test.go                   # APPEND: param round-trips + rejections (NOT server_test.go — deviation 1)
├── spec.md / README.md                 # DELIVERABLE (docs) — gate: grep counts, no tests
└── testdata/                           # +injection.html ONLY — porcelain gate
```

Existing tests that must stay green **untouched**: all clean goldens (`TestCleanArticle`/`Product`/… — spike-justified exceptions only), `TestClassify_*`/`TestRetry_*` (backoff semantics unchanged), all crawl pipeline/resume/quality tests, `TestValidateOptions` base table, `TestToolCatalog` (12), screenshot envelope rows, exporter e2e.

---

## 5. Test Helper Structure (Go — no `conftest.py`)

| Helper | Home | Used for | New? |
|--------|------|----------|------|
| `cleanFile(t, name)` / `golden(t, …)` | clean/clean_test.go:34/15 | load fixture → `clean.Clean`; golden compare with `-update` (NEVER run blindly this phase) | reuse |
| `newSiteOrigin` / `o.count(p)` | crawl/crawl_test.go:140 | sitemap-only origin + frontier exactness | reuse |
| `itemPage` / `openCrawlDB` / `sevenPages` | crawl/crawl_test.go | sitemap-only pages + run scaffolding | reuse |
| `countingDo` | crawl/crawl_test.go:364 | retry-classification rows (unchanged behavior proof) | reuse |
| `timingDo` | crawl/crawl_test.go (new, §3) | AutoThrottle wiring tripwire timestamps | **new, ~15 lines** |
| `fakeSitemapFetcher` | crawl/sitemap_test.go | unit-level sitemap expansion rows if needed (pipeline rows use the real origin) | reuse |
| `fakeAgentFetcher` + `dialInMemory`/`callTool`/`agentDeps` | mcp/agent_test.go | MCP param round-trips with canned `XHR` | reuse |
| `resetGlobals`/`captureOutput`/`codeOf`/`rootCmd`/`testEnv` | cli/*_test.go | CLI rows, env fallback | reuse |
| in-test vector page builder | clean/injection_test.go | six-vector HTML as a Go literal (fixture and literal share content; literal keeps the test readable at the assertion site) | **new const** |

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Pin the strip on BOTH `Markdown` and `HTML` | injection test asserts both `CleanedPage` surfaces are marker-free | J.1's whole premise is the choke point: `CleanedPage.HTML` feeds sidecars/verticals, `Markdown` feeds LLM/watch/CLI. Stripping only one surface would reopen the hole through the other |
| Spike doc gates golden drift | `go test ./clean/...` must pass WITHOUT `-update`; any regen is named+justified in the J.1 commit message | A blind `-update` would launder a behavior change into a golden. The verified expectation is ZERO drift (no existing fixture has hidden text) — anything else is a finding, not an update |
| Envelope pinned at the doc builder, not through a fetch fake | construct `scrape.Result{XHR: canned}`, call the builder (screenshotDoc precedent) | The envelope function IS the output contract; a fake fetcher would add a moving part without adding an assertion. The fetch side is covered by units + the tagged live test |
| XHR drain logic must be pure helpers | demand `truncBody`/`buildCapture`-shaped functions in `xhr.go` so truncation/base64/eviction are table-testable without rod | Otherwise those branches are only reachable browser-tagged — i.e., untested in CI. Design-for-test costs nothing here (the functions are trivially extractable) |
| AutoThrottle math via `Limit()`, wiring via wall-clock | pure table asserts `Limit(host)` after `Report`; ONE timing test asserts the do-call gap grew | `Report` applying via `SetLimit` eagerly is what makes `Limit()` a valid oracle (the plan's own design). But `Limit()` can't see WHERE Report was called from — only a real gap measurement catches the post-retry dead-wiring (the PR #11 class: unit tests green, feature dead) |
| One slow test, quarantined by comment | the tripwire runs ~2–3s real time (Rate:2 → 500ms base, ≥900ms asserted gap) | Timing assertions under 100ms flake; this is the single place wall-clock is the assertion. Comment explains why the sleep is load-bearing so nobody "optimizes" it into a flake |
| `ErrSitemapOnlyEmpty` pinned at 3 layers | `errors.Is` at Run level → `exitfor_test.go` arm → CLI `codeOf==3` row | The plan's own warning: a bare error falls to exit 1, and exit codes are documented contract. Each layer fails differently (unit red, table red, e2e red) — pin all three, they're one line each |
| CDP no-launch is default-suite | refused loopback dial (`ws://127.0.0.1:1/x`), assert error identity + <2s | No browser is needed to prove NO browser launches — that's the point of the test. Tagging it `browser` would let it silently stop running |
| Comment-node strip needs its own row | HTML comments are invisible in markdown — assert on `CleanedPage.HTML` (comment gone) not `Markdown` | `Find("comment")` is a no-op in goquery (verified in the plan); the manual walk's correctness is only observable on the HTML surface |
| MCP round-trips in agent_test.go | deviation 1 | Follow the seam that exists, not the one the plan guessed at |

---

## 7. Example Test Case

The dead-wiring tripwire — the phase's most subtle pin, in full (append to `crawl/crawl_test.go`):

```go
func TestAutoThrottle_WiredInsideRetry(t *testing.T) {
	// Origin 429s the first hit on /x, then 200s. backoff.go retries the 429
	// ~immediately (jittered backoff ≪ throttle delay), so the gap between
	// attempt 1 and attempt 2 measures ONLY the base limiter delay. If Report
	// were wired AFTER FetchWithRetry (the dead-wiring trap), it would observe
	// only the final 200 and the gap would never grow. Load-bearing real time:
	// base delay 500ms (Rate:2), post-Report ≥1s — asserted ≥900ms with slack.
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`<html><body><p>ok</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	db := openCrawlDB(t)

	o := newSiteOrigin(t, map[string]string{"/x": itemPage()}, "") // unused; see note
	_ = o
	out := filepath.Join(t.TempDir(), "r.jsonl")
	start := time.Now()
	res, err := Run(context.Background(), Options{
		SeedURL: srv.URL + "/x", Schema: mustTestSchema(t),
		MaxPages: 1, MaxDepth: 0, SameHost: true,
		FetchWorkers: 1, Rate: 2, Format: "jsonl", Out: out,
		DB: db, Extractor: &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}},
		AutoThrottle: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	// One page, one retry: minimum healthy path ≈ base(500ms) + backoff retry
	// + throttled second attempt (≥1s after the 429 report). If the report is
	// dead-wired, elapsed collapses to ≲600ms.
	if elapsed < 900*time.Millisecond {
		t.Errorf("run took %v; AutoThrottle report looks dead-wired (want ≥900ms)", elapsed)
	}
	if res.PagesOK != 1 {
		t.Errorf("pages_ok = %d, want 1", res.PagesOK)
	}
}
```

(If per-attempt timestamps prove more robust than total elapsed across CI machines, switch to `timingDo` from §3 wired through the same Options path and assert `times[1].Sub(times[0]) >= 900*time.Millisecond` — same property, sharper oracle. The `newSiteOrigin` line above exists only if the fetch seam needs an origin alongside the raw mux; drop it when the direct-mux version compiles.)

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write these tests (alongside the phase-J implementation):

---
You are writing the tests for **Phase J of magpie** — Competitive Parity: hidden-text stripping (J.1), `--capture-xhr` (J.2), `--auto-throttle` (J.3), `crawl --sitemap-only` (J.4), `--cdp-url` (J.5). magpie is a Go CLI web scraper (module `magpie`, Go 1.26+, CGO-free) at `/home/domidex/projects/magpie`. Read `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md`, `plan/phase-J.md`, and `plan/phase-J-tests.md` first. Default suite is hermetic (loopback httptest only); browser tests live behind `//go:build browser`; no new deps; rod stays sealed in `fetch/`.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | Hidden injection text stripped everywhere by default | `clean/injection_test.go` + golden-drift protocol | 6 vectors absent from Markdown AND HTML; `display:flex`/`aria-hidden` survive; zero unexplained golden changes |
| US-2 | `--capture-xhr` returns page-loaded JSON bodies | xhr units + `TestScrape_XHREnvelope` + tagged live test | patterns validated exit 2; 64KiB truncation; base64 fallback; eviction→empty body; envelope `xhr` only when non-empty; live capture parses |
| US-3 | `--auto-throttle` backs off ×2, decays ≥ floor | `TestHostLimiters_Auto*` + `TestAutoThrottle_WiredInsideRetry` | Limit() math exact; off = no-op; real-run gap ≥900ms proves in-closure wiring |
| US-4 | `--sitemap-only` frontier is exactly the sitemap | `crawl/sitemap_only_test.go` + exitfor + CLI rows | 3 listed fetched, seed and off-sitemap never; empty → `ErrSitemapOnlyEmpty` → exit 3; conflict → exit 2 |
| US-5 | `--cdp-url` never launches local Chrome | `TestRod_CDPNoLocalLaunch` + validate/env rows | fast refused-dial error, redacted, no launcher; schemes validated; env fallback flag>env |

### Why There Are No New Fakes
Every oracle exists: `clean.Clean` is its own contract (J.1), constructed `scrape.Result` literals pin the CLI envelope (screenshotDoc precedent, `cli/scrape_test.go:400`), `countingDo` scripts retry responses, `fakeAgentFetcher` serves canned XHR to MCP, `newSiteOrigin`+robots-serve the sitemap-only site. The only new helpers: a ~15-line `timingDo` (crawl_test.go, full source in the tests plan §3) and the in-test vector-page literal. Do not invent a fetch fake for the envelope row — there is nothing behind it to fake.

### What NOT to Test
- **rod internals**: EachEvent/GetResponseBody behavior is rod's; we pin OUR subscribe→drain→assign flow (tagged live test) and our pure helpers (units). Don't fake CDP.
- **CSS computed styles / white-on-white / off-screen**: J.1's documented ceiling (ponytail). Inline-style regexps + `hidden` attr + comments only. `aria-hidden` must SURVIVE (out of scope by design) — that's a precision pin, not a gap.
- **sitemap XML parsing**: `crawl/sitemap_test.go` already owns `ListSitemapURLs` (index chains, gzip, caps, entities). Sitemap-only tests cover the SEED/GUARD delta, not the parser.
- **latency-based AutoThrottle**: deliberately out (plan Decision 3 ponytail). Status-driven only.
- **LLM anything**: this phase has zero extractor surface. No fakeProvider rows.
- **`TestToolCatalog`**: still 12 tools. Params only. Don't edit it.

### Critical: The Harness You Must Reuse

```go
// crawl/crawl_test.go (package crawl):
func openCrawlDB(t *testing.T) *store.DB          // temp-dir store, t.Cleanup close
func newSiteOrigin(t, pages map[string]string, robotsBody string) *origin
                                                  // robots.txt served when robotsBody != ""; o.count(path) = hits
func itemPage(linkPaths ...string) string         // static quality-passing page
var crawlTruth = map[string]any{"price": 12.99, "title": "Widget", "ean": "4001234567890"}
func countingDo(calls *int, script []struct{ resp *fetch.FetchResponse; err error }) func() (*fetch.FetchResponse, error)
```

```go
// clean/clean_test.go (package clean — mirror of cleanFile for your new fixture):
func cleanFile(t *testing.T, htmlName string) clean.CleanedPage // reads ../testdata/clean/<name>.html
// For in-test literals: clean.Clean(t.Context(), clean.RawPage{HTML: []byte(html), FinalURL: "https://example.com/x"})
```

```go
// cli/scrape_test.go — the envelope oracle (screenshotDoc precedent, :400):
// construct the result, call the doc builder, unmarshal, assert keys.
doc, err := markdownDoc(scrape.Result{URL: "https://x", XHR: cannedXHR})
```

```go
// mcp/agent_test.go — round-trips (NOT server_test.go):
cs := dialInMemory(t, magpiemcp.NewServer(agentDeps(db, &fakeExtractor{}, nil, fetch)), nil)
out := decodeOut(t, callTool(t, cs, "scrape_url", map[string]any{"url": …, "capture_xhr": []string{"/api"}}, ""))
```

New test material only: `timingDo` (plan §3, full source) and the injection fixture/literal:

```go
// testdata/clean/injection.html content (also as a Go literal in injection_test.go):
// visible <p> + <h1>Widget</h1>, then MAGPIE_HIDDEN_1..6 each inside:
//   1 <div style="display:none">, 2 <span style="visibility:hidden">,
//   3 <p style="font-size:0">, 4 <div style="opacity:0">,
//   5 <div hidden>, 6 <!-- MAGPIE_HIDDEN_6 -->
// plus survivors: <span style="display:flex">FLEX</span>,
//   <span aria-hidden="true">icon</span>, <span style="opacity:0.5">HALF</span>
```

### Test Files to Create / Edit
- **NEW `clean/injection_test.go`**: vectors absent from `Markdown` AND `CleanedPage.HTML`; controls survive; `display : none`-with-space and `DISPLAY:NONE` stripped; `opacity:0.5` survives; comment gone from HTML surface.
- **NEW `fetch/xhr_test.go`** (`package fetch`): `ValidateXHRPatterns` table (valid compile, `"[bad"` error, empty→nil); truncation at exactly/over 64 KiB; base64 decode + invalid-b64 fallback; eviction sentinel → metadata-only capture, nil error; 50-cap. Browser-tagged `TestCaptureXHRLive`: httptest page `fetch('/api/data')` → 1 capture with `{"ok":true}` body; skip on LookPath miss OR launch/connect error (rod_smoke pattern).
- **NEW `fetch/rod_cdp_test.go`**: unreachable `ws://127.0.0.1:1/x` → error contains `connect remote browser`, <2s, no userinfo in message (test the `ws://user:pass@…` redaction); `Close()` after failed connect doesn't panic.
- **NEW `crawl/ratelimit_test.go`**: `TestHostLimiters_Auto*` — the full math table via `Limit()` (backoff ×2 per block, Retry-After exact + 5m cap, decay ×¾ monotonic ≥ floor, 60s delay cap, 503/500 both count, auto-off no-op, SetFloor interplay).
- **NEW `crawl/sitemap_only_test.go`**: exact frontier (3 listed, seed-excluded-when-unlisted, `/off` never), empty sitemap → `errors.Is ErrSitemapOnlyEmpty`, expansion error fatal only in sitemap-only mode, warn-and-proceed intact otherwise.
- **APPEND `crawl/crawl_test.go`**: `timingDo` + `TestAutoThrottle_WiredInsideRetry` (full source in plan §7 — the ≥900ms gap assertion with the load-bearing-sleep comment).
- **APPEND `scrape/validate_test.go`**: `capture-xhr`+static OptionsError (screenshot wording); CDP scheme table (ws/wss/http/https pass; ftp/garbage fail); bad XHR regexp pre-I/O.
- **APPEND `cli/scrape_test.go`**: `TestScrape_XHREnvelope` (constructed Result; nil XHR → no `xhr` key); CDP env rows (flag>env, env-only, neither).
- **APPEND `cli/exitfor_test.go`**: `ErrSitemapOnlyEmpty` wrapped → 3; crawl conflict predicate → 2.
- **APPEND `mcp/agent_test.go`**: scrape_url `capture_xhr`/`cdp_url` round-trips + `capture_xhr`+static tool error; crawl_site `sitemap_only`/`auto_throttle` FlexBool round-trips + conflict error; canned XHR visible in `ScrapeOut`.

### Data Model Notes (Go)
- `XHRCapture{URL string `json:"url"`, Status int `json:"status"`, MIMEType string `json:"mime,omitempty"`, Body string `json:"body,omitempty"`, Truncated bool `json:"truncated,omitempty"`}` — assert presence/absence of `mime`/`body`/`truncated` keys in envelope JSON per omitempty, not just values.
- `FetchResponse.XHR` / `FetchRequest.CaptureXHR` / `scrape.Result.XHR` are additive-omitempty: existing envelopes and goldens must not gain a key. `XHR:nil` ⇒ no `xhr` key anywhere.
- `ErrSitemapOnlyEmpty` must survive wrapping: assert with `errors.Is`, never string-match.
- AutoThrottle delay math is integer (`delay*3/4`) — assert via `rate.Limit` with `math.Abs(got-want) < 1e-9`, not exact float equality on derived fractions.

### Success Criteria
- `go test ./clean/ ./fetch/ ./crawl/ ./scrape/ ./cli/ ./mcp/ -count=1` green with all new tests
- `go test ./...` green; `go test -tags browser ./fetch/` green (or skipped where no Chrome)
- `git status --porcelain testdata/` shows ONLY `testdata/clean/injection.html`
- Zero edits to existing test bodies; `TestToolCatalog` untouched
- `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...` clean

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -count=1 && git status --porcelain testdata/

# Fast hermetic suite (every push)
go test ./... -count=1

# Per-task focus (mirrors the J.x sanity checks)
go test ./clean/ -run Injection -v -count=1                # J.1 vectors + controls
go test ./fetch/ -run 'XHRPattern|TestRod_CDP' -v -count=1 # J.2 units + J.5 no-launch
go test ./crawl/ -run 'TestHostLimiters_Auto|SitemapOnly' -v -count=1  # J.3 + J.4
go test ./crawl/ -run WiredInsideRetry -v -count=1         # J.3 tripwire (~3s, real time)
go test ./cli/ -run 'XHREnvelope|ExitCode' -v -count=1     # envelope + exit arms
go test ./mcp/ -run 'TestScrape|TestCrawl' -v -count=1     # MCP round-trips

# Browser tier (Chrome present; manual / CI-with-browser only)
go test -tags browser ./fetch/ -run 'TestCaptureXHRLive|TestRodSmoke' -v

# Fixture-drift gate (must show ONLY testdata/clean/injection.html)
git status --porcelain testdata/

# Race on touched packages
go test -race ./clean/ ./fetch/ ./crawl/ -count=1

# Full gate (mirrors phase-J exit criteria)
go test ./... -count=1 && go test -tags browser ./... && go vet ./... \
  && gofmt -l . && golangci-lint run ./... \
  && test "$(git status --porcelain testdata/ | grep -v 'testdata/clean/injection.html')" = "" \
  && echo ALL GREEN
```
