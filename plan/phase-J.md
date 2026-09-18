# Phase J — Competitive Parity (agent-safety, XHR capture, AutoThrottle, sitemap-only, remote browser)

**Duration:** 2 days (~16h)
**Depends on:** master (Phase H merged, PR #13). Independent of Phase I (corpus mode) — either order.
**Blocks:** nothing hard; `--cdp-url` + XHR capture feed the future Wails-GUI and scraping-farm stories.
**Risk Level:** MEDIUM — four of five features are purely additive (new flags, additive-omitempty
fields); the one default-behavior change (hidden-text strip in `clean.Clean`) is contained by a
Task J.1 spike that measures what survives before anything is wired.
**Stack:** go

> Note (AGENTS.md): `run-phase` expects `stack: python|nextjs|react|typescript` and will
> hard-block on this file. Execute manually via the execution prompt below.

**Source:** Scrapling comparison 2026-09-28 — five "ideas worth stealing", re-scoped against our
codebase. One shrank on inspection: sitemap *seeding already exists* (`crawl/crawl.go` `seed()`
expands robots-declared sitemaps, capped, `--no-sitemap` to skip), so idea #4 is only the
`--sitemap-only` delta.

---

## Objective

Close the five concrete gaps Scrapling exposed, in our idiom (flags validated at the exit-2
boundary, additive-omitempty output fields, one choke point per behavior):

1. **Prompt-injection stripping** — hidden text (`display:none` etc.) must not reach LLM/agent
   consumers. Strip at the `clean.Clean` choke point so every consumer (CLI, MCP, crawl, watch,
   extract) is covered by one change, like Scrapling's MCP sanitization but deeper in the pipe.
2. **`--capture-xhr`** — capture XHR/fetch response bodies the page loads, so users get a site's
   JSON API without reverse-engineering it (our §1.2 Next/Nuxt embedded-JSON thesis).
3. **`--auto-throttle`** — adaptive per-host pacing: back off ×2 on 429/503, decay on success.
4. **`crawl --sitemap-only`** — frontier = sitemap URLs only, no link-following.
5. **`--cdp-url` / `MAGPIE_CDP_URL`** — drive a remote/already-running browser instead of
   launching one (scraping farms, containers, remote browsers-as-a-service).

## What Success Looks Like

1. A fixture page whose markdown text hides "IGNORE ALL PREVIOUS INSTRUCTIONS" inside
   `<div style="display:none">` cleans to markdown **without** that string, on master's public
   API with no new flags: `magpie extract --content-type html < fixture.html | grep -c "IGNORE ALL"`
   prints `0` (and `1` on a `--page-format raw` escape hatch path).
2. `magpie scrape <local-test-page> --capture-xhr /api/ --page-format json` exits 0 and the
   envelope has an `xhr` array whose elements carry `url`, `status`, `mime`, `body` where
   `body` parses as the JSON the test page's `fetch()` produced.
3. `magpie crawl <seed> --auto-throttle --max-pages 5` against a local server that returns 429
   twice then 200s: the second request starts later than the first (limiter delay grew), and the
   per-host limit returns to ≥ base rate after successes (`crawl/ratelimit_test.go` asserts
   `Limit()` monotonicity — no live network needed).
4. `magpie crawl <seed-with-sitemap> --sitemap-only --max-pages 10` enqueues exactly the sitemap
   URLs (scope-filtered) and never follows links off them; `--sitemap-only --no-sitemap` exits 2.
5. `MAGPIE_CDP_URL=ws://127.0.0.1:9/blackhole magpie scrape <url> --render browser` fails fast
   with a connect error and **never attempts a local browser download/launch** (grep the error).
6. All gates green: `go test ./...`, `go test -tags browser ./...` (new browser tests included),
   `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean; existing JSONL/markdown
   goldens byte-identical except fixtures deliberately containing hidden text (Task J.1 spike
   inventories exactly which and why).

---

## Architecture / Key Design Decisions

```
                 ┌─ J.1 StripHidden ─────────────────────────────┐
 raw HTML ─► Clean(scope ─► injection strip ─► trafilatura ─► md)│─► CLI / MCP / crawl / watch
                 └──────────── one choke point ─────────────────┘

 scrape --capture-xhr ─► RodFetcher: EachEvent(NetworkResponseReceived)
                         ─► collect {RequestID,url,status,mime} (XHR/Fetch only)
                         ─► after settle, GetResponseBody per id ─► FetchResponse.XHR
                         ─► additive `xhr` array in CLI envelope + MCP ScrapeOut

 crawl fetchPage ─► limiters.Wait(host) ─► FetchWithRetry(do() ─ per attempt ─► Report(host,code,retryAfter))
                    (J.3: ×2 backoff on 429/503, decay on 2xx — Report fires INSIDE the
                     retry closure; backoff.go's internal retries would hide 429/503s)

 crawl --sitemap-only ─► seed() = sitemap expansion ONLY ─► link-enqueue guard skips

 RodFetcher.CDP ─► ensureBrowser(): remote ControlURL+Connect, launcher never runs
```

### Decisions

1. **Strip hidden text in `clean`, default-on, no new flag.** Hidden text is invisible by
   definition — emitting it is a fidelity bug, not a preference. One choke point covers every
   consumer (the Scrapling lesson: they strip only in MCP; our `clean` layer is upstream of MCP,
   LLM extract, corpus, and watch). Escape hatch is the existing `--page-format raw` (bypasses
   clean entirely) — no new knob. Task J.1's spike on master decides how much survives
   trafilatura today; either way the fixture test pins the behavior so drift = loud test, not a
   silent leak. `aria-hidden` is **out of scope v1** (icon fonts / legit decorative markup make
   it low-precision); high-precision vectors only.
2. **`XHR []XHRCapture` is additive on `FetchResponse`, `CaptureXHR []string` additive on
   `FetchRequest`** (Phase G `CleanedPage.HTML` pattern): static fetchers ignore the request
   field (CLI/MCP reject `capture-xhr` + `render=static` at the exit-2 boundary, exactly like
   `screenshot`), static path leaves the response field nil, no existing output shape drifts.
   Patterns are Go regexps validated at the boundary; capture restricted to resource types
   XHR/Fetch; caps 50 captures × 64 KiB body (`Truncated: true` past cap — fail-visible in data,
   never an error). Bodies drained **after** the settle, before `page.Close()` — CDP evicts
   buffers on close, and fetching inside the event callback would stall the event loop.
3. **AutoThrottle is opt-in (`--auto-throttle`)** — Scrapy ships it off by default too; turning
   polite-crawl pacing into a moving target unannounced would surprise every existing user and
   golden test. Implementation is per-host adaptive *delay* inside `HostLimiters`:
   `Report(host, code, retryAfter)` doubles delay on 429/503 (cap 60s; an explicit `Retry-After`
   header sets the delay directly), multiplies by 0.75 on 2xx (decay toward the configured
   floor), floor = `max(configured rps, crawl-delay)` (existing `SetFloor` stays authoritative —
   auto never goes faster than robots asks). `Report` applies the new delay EAGERLY via
   `SetLimit` under the existing mutex, so `Limit()` reflects it for tests and the `Wait` call
   site (`crawl/crawl.go:423`) is untouched. Complementary, not duplicate, to `FetchWithRetry`
   (`crawl/backoff.go`), which retries the SAME request 4× with jittered backoff + Retry-After —
   AutoThrottle paces FUTURE requests to the host. Wire `Report` per attempt INSIDE the `do`
   closure (`crawl.go:438`), never after `FetchWithRetry` returns: the retry loop consumes
   intermediate 429/503s, so a post-hoc Report sees mostly final 200s → backoff is dead code
   while unit tests stay green (the PR#11 dead-wiring trap).
4. **`--sitemap-only` reuses `seed()`'s expansion, adds a guard at link-enqueue.** Sitemap URLs
   enter at depth 0 alongside the seed (`frontier.Add(seeds, 0)` — depth is moot here since
   link-following is off), `scopeFilterExpansion` and `maxPages` caps apply unchanged; the only
   new behavior is skipping discovered-link enqueue when the flag is set, and exit 2 on
   the contradictory `--sitemap-only --no-sitemap` pair. Zero expanded URLs → typed
   `crawl.ErrSitemapOnlyEmpty` + an `errors.Is` arm in `exitCode` → exit 3 (the
   `ErrRobotsBlocked`→5 pattern, cli/root.go:113) — a bare error falls off exitFor's default
   (exit 1), and exit codes are documented contract (J.6). Never a silent 0-page success.
5. **CDP URL lives on the struct, not the interface.** `RodFetcher` gains a `CDP string` field;
   `ensureBrowser` skips `launcher.New()` entirely when set (which also means **no local Chromium
   download** — CI/farm win). `Fetcher` interface unchanged; rod stays sealed in `fetch/`
   (AGENTS rule). Flag `--cdp-url` on `scrape` + MCP `scrape_url` `cdp_url` param + env
   `MAGPIE_CDP_URL` (flag > env, MAGPIE_PROXY pattern). Scheme must be `ws://`, `wss://`, or
   `http(s)://` — rejected pre-I/O at the options boundary.
6. **No new dependencies anywhere.** go-rod, x/time/rate, goquery: all already vendored.
7. **rod.go is touched by both J.2 and J.5 — implement sequentially, J.5 first** (it's 15 lines
   and shrinks J.2's diff context).

---

## Tasks

### Task J.5 — Remote browser via CDP URL (1.5h) — *do first, smallest*

**Depends on:** nothing.

In `fetch/rod.go`: add field `CDP string` to `RodFetcher`; in `ensureBrowser`, branch before
`launcher.New()`:

```go
// Confirmed pattern — already in our own code (rod.go:55); remote is the
// same two calls with an operator-supplied URL. No local launch/download.
if r.CDP != "" {
    r.browser = rod.New().ControlURL(r.CDP)
    if err := r.browser.Connect(); err != nil {
        return fmt.Errorf("fetch: connect remote browser %s: %w", r.CDP, err)
    }
    return nil
}
```

Plumb `Options.CDP` (string) through `scrape.Options` → the browser-construction helper in
`cli/scrape.go` (find the `fetch.NewRodFetcher()` call sites — `fetchBrowser` and the screenshot
path) so every per-call fetcher carries it; env fallback `MAGPIE_CDP_URL` when flag empty.
Validate at the options boundary (`ValidateOptions`): non-empty must parse with scheme
ws/wss/http(s) → else exit 2. MCP: `cdp_url` string param on `scrape_url` (mirror `lang`).
Flag: `--cdp-url` on `scrape`.

**Tests:** unit — options validation (bad scheme → exit 2); `TestRod_CDPNoLocalLaunch` — CDP set
to an unreachable `ws://127.0.0.1:1/x` errors at Connect *without* invoking the launcher
(assert no download/launch side effect by error identity + speed). Redact the URL in errors that
could carry credentials (`ws://user:pass@` — reuse the proxy redaction helper's spirit).

**Sanity check:** `MAGPIE_CDP_URL=ws://127.0.0.1:1/x go test ./fetch/ -run TestRod_CDP -v`

### Task J.1 — Prompt-injection stripping (4h incl. spike)

**Depends on:** nothing.

**Step 1 — spike (≤1h, go/no-go inside the task):** build `testdata/clean/injection.html` with
the vector set — `display:none`, `visibility:hidden`, `font-size:0`, `opacity:0`, `[hidden]`
attribute, and an HTML comment, each wrapping the marker string `MAGPIE_HIDDEN_<n>` — plus a
visible paragraph. Run through master's `clean.Clean` today and inventory which markers survive
into markdown. **Either outcome proceeds**: survivors get stripped by J.1's pass; already-dropped
vectors just get pinned (trafilatura behavior is not a contract — a silent upstream change would
re-open the hole, and the fixture test turns that into a red test).

**Step 2 — `clean/injection.go`:** `StripHidden(doc *goquery.Document)` removing elements whose
inline `style` matches the vector set (`display\s*:\s*none`, `visibility\s*:\s*hidden`,
`font-size\s*:\s*0`, `opacity\s*:\s*0(?:\.0+)?\s*(;|$)`) or that carry the `hidden` attribute,
plus HTML comment nodes. Match on lowercased style text with a precompiled regexp — no CSS
parsing, no new dep. Hook in `clean/clean.go` `Clean()` after the scope pass, before the
trafilatura/markdown conversion. Never fail the page on a strip error: on any panic-adjacent
weirdness, keep the original node (ponytail: selector-level strip, not a CSS engine — computed
styles / white-on-white / off-screen positioning are the known ceiling; the upgrade path is a
browser-computed-style pass, only if a real case appears).

**Tests:** `clean/injection_test.go` — each vector asserted absent from markdown, visible content
asserted present; a legit `<span style="display:flex">` and an `aria-hidden` icon span asserted
*surviving* (precision pins). Golden check: run `go test ./clean/...` against existing goldens —
only fixtures that genuinely contain hidden text may change, and the spike doc names them.
Spike must also inventory second-order effects: StripHidden runs before conversion, so
`Classify` scores the stripped HTML (clean.go:90) and `CleanedPage.HTML` is the stripped
document — quality-shift and HTML-sidecar deltas belong in the spike doc and spec §10.6.

**Sanity check:** `go test ./clean/ -run Injection -v`

### Task J.2 — XHR capture (5h)

**Depends on:** J.5 (shared rod.go context).

**`fetch/xhr.go`:**

```go
// XHRCapture is one captured XHR/fetch response (rod-only; additive on FetchResponse).
type XHRCapture struct {
    URL       string `json:"url"`
    Status    int    `json:"status"`
    MIMEType  string `json:"mime,omitempty"`
    Body      string `json:"body,omitempty"`
    Truncated bool   `json:"truncated,omitempty"`
}
```

- `FetchRequest` gains `CaptureXHR []string` (comment: rod-only; static fetchers ignore — CLI
  rejects the combination earlier). `FetchResponse` gains `XHR []XHRCapture` (additive-omitempty).
- Compile patterns to `regexp` at the boundary — `scrape.ValidateOptions` (scrape/scrape.go:114,
  the same function that validates actions/lang; CLI, MCP, and batch all reach it via
  `scrape.Run`, so MCP is covered for free; `cli/shared.go` has no validation role): bad regexp
  → exit 2. The compile helper itself lives in `fetch/` (`fetch.ValidateXHRPatterns`) — regexps
  are a fetch concern.
- In `FetchWithActions`, when len(CaptureXHR) > 0: subscribe **before** navigation —

```go
// Confirmed API (go-rod docs + issue #466: page.GetResource cannot see XHR;
// passive network events are the route — hijack would perturb the page):
wait := page.EachEvent(func(e *proto.NetworkResponseReceived) {
    if e.Type != proto.NetworkResourceTypeXHR && e.Type != proto.NetworkResourceTypeFetch {
        return
    }
    if !matchesAny(e.Response.URL, patterns) || len(caps) >= maxCaptures {
        return
    }
    caps = append(caps, xhrPending{e.RequestID, e.Response.URL, int(e.Response.Status), e.Response.MIMEType})
})
defer wait() // never blocks long; bodies drained below, not in the callback
```

  (`wait()` is the callback-removal func `EachEvent` returns; the callback only *records IDs*
  — draining inside it would stall rod's event loop.)
- After actions + settle, **before** `page.Close()`: for each pending capture call
  `proto.NetworkGetResponseBody{RequestID: p.id}.Call(page)` → `{Body, Base64Encoded}`; on CDP
  eviction error (`-32000 No resource…`) record metadata with empty body (fail-visible in data,
  never fatal); `Base64Encoded` → `base64.StdEncoding.DecodeString`, decode failure keeps raw.
  Truncate at 64 KiB with `Truncated: true`; hard cap 50 captures.
- Assign `resp.XHR = …` on the `FetchResponse` before return.

**Envelope + MCP:** CLI scrape envelope gains `xhr` additive field (rendered only when non-empty;
follow the Phase-G screenshot lesson — ONE cohesive block in the output ladder, no scattered
cases). `page_format json` unchanged in shape + `xhr` rides along. MCP `scrape_url` gains
`capture_xhr` string-array param; `ScrapeOut` gains additive `xhr`. `capture_xhr` +
`render=static` → exit 2 / MCP options error (same wording as screenshot). Crawl does **not**
get capture (v1: scrape-only — crawl corpus/records have no place to put bodies; YAGNI).

**Tests:** unit — pattern compile/validation, truncation math, base64 decode fallback, envelope
wiring (`TestScrape_XHREnvelope` with a fake fetcher returning canned `XHR` — no browser needed,
this is the contract pin). Browser-tagged (`//go:build browser`) live test: httptest page whose
script `fetch()`es `/api/data` (serving `{"ok":true}`), assert one capture with parsed-JSON body.
`rod_smoke_test.go` already establishes the local-launch pattern for tagged tests.

**Sanity check:** `go test ./fetch/ -run XHRPattern -v && go test -tags browser ./fetch/ -run TestCaptureXHRLive -v`

### Task J.3 — AutoThrottle (3h)

**Depends on:** nothing (touches `crawl/ratelimit.go`, lifts the Retry-After parse from
`crawl/backoff.go:70`, and wires inside the `do` closure at `crawl/crawl.go:438`).

Extend `HostLimiters` (no signature change to `NewHostLimiters` — add `SetAuto()` called once
from `newCrawlContext` when `opts.AutoThrottle`):

```go
// Report feeds the adaptive delay: backoff on block signals, decay on success.
// delay >= floor always (floor = max(configured 1/rps, SetFloor crawl-delay)).
func (h *HostLimiters) Report(host string, code int, retryAfter time.Duration)
```

- Per-host `delay time.Duration` map (init `1/rps`), under the existing mutex.
- `code == 429 || code/100 == 5` → `delay *= 2`, cap 60s; if `retryAfter > 0`, `delay = retryAfter`
  (explicit server instruction wins over our guess, cap 5m).
- `code` 2xx → `delay = max(floor, delay * 3 / 4)` (integer math; decay only ever approaches the
  floor from above, never below it).
- `Report` applies the delay eagerly via `lim.SetLimit(rate.Every(delay))` under the existing
  mutex (that's what makes `Limit()` a valid test assertion); the `Wait` call site
  (`crawl.go:423`) stays untouched. `SetFloor` continues to lower the floor as today (auto
  delay starts from the floored rate). Reuse `classify`'s Retry-After parsing (backoff.go:70) —
  lift it into a shared helper, don't write a parallel `retryAfterFrom`.
- Wire: INSIDE the `do` closure handed to `FetchWithRetry` (crawl.go:438) — per attempt, where
  the response is in hand — call `c.limiters.Report(host, r.StatusCode, retryAfter)` only when
  `opts.AutoThrottle`. Wiring after `FetchWithRetry` returns is the dead-wiring trap: backoff.go
  already retries 429/503 (4 tries, jittered, Retry-After honored per attempt via
  `backoff.RetryAfter`), so intermediate 429/503s never surface at the call site — a post-hoc
  Report sees mostly final 200s → decay-only backoff, unit tests green. The mechanisms are
  complementary (retry = same request; AutoThrottle = future requests to the host) — document,
  don't deduplicate. CLI flag `--auto-throttle`; MCP `crawl_site` gains `auto_throttle`
  (`*FlexBool`, house CrawlIn pattern). Latency-based tuning (Scrapy's latency-target variant)
  is deliberately out: status-driven backoff is 90% of the value with none of the tuning knobs
  (ponytail).

**Tests:** pure unit — `TestHostLimiters_AutoBackoff`: two 429s double the effective delay
(assert via `Limit()`), a `Retry-After: 10s` sets it exactly, five 200s decay monotonically and
never below the floor, cap holds at 60s, auto-off = `Report` is a no-op.

**Sanity check:** `go test ./crawl/ -run TestHostLimiters_Auto -v`

### Task J.4 — `crawl --sitemap-only` (1.5h)

**Depends on:** nothing.

- `crawl.Options` gains `SitemapOnly bool`. CLI flag `--sitemap-only`; MCP `crawl_site` gains
  `sitemap_only *FlexBool` (house CrawlIn pattern). Validation: `SitemapOnly && NoSitemap` →
  exit 2 (one crawl-package predicate, called from both the CLI and MCP edges).
- `seed()` (`crawl/crawl.go:223`): when `SitemapOnly`, `seeds = scopeFilterExpansion(...expanded)`
  **only** (seed URL itself excluded unless the sitemap lists it); zero expanded URLs → return
  typed `ErrSitemapOnlyEmpty` wrapped as `crawl: sitemap-only: no URLs found`, plus an
  `errors.Is` arm in `exitCode` (cli/root.go) mapping it to 3 — a bare error falls through to
  exitFor's default (1), and exit codes are documented contract. Never a 0-page success.
  Existing `maxPages` cap and warn-and-proceed-on-expansion-error semantics unchanged — except
  expansion error in sitemap-only mode **is** fatal (there is no "seed-only" fallback: the seed
  URL was explicitly excluded from the contract).
- Link-following guard: skip the `frontier.ExtractLinks` call in the finish handler
  (crawl.go:492 — its only call site) when `SitemapOnly`.

**Tests:** `crawl` httptest site with a sitemap listing 3 URLs, pages that link to a 4th
non-sitemap URL — assert frontier = exactly the 3, 4th never fetched; empty sitemap → error;
`--sitemap-only --no-sitemap` → exit 2 (exitfor_test.go pattern).

**Sanity check:** `go test ./crawl/ -run SitemapOnly -v`

### Task J.6 — Docs + spec sync (1h)

**Depends on:** J.1–J.5.

- `spec.md`: new `### 10.6 Competitive-parity delta (Phase J)` subsection after §10.5 (same
  register as §10.4/§10.5): the five features, flag names, caps (50×64KiB XHR, 60s throttle cap),
  the hidden-text strip contract and its `--page-format raw` escape hatch, CDP env/flag
  precedence.
- `README.md`: feature bullets for `--capture-xhr`, `--auto-throttle`, `--sitemap-only`,
  `--cdp-url`, and a security note on injection stripping.
- Exit codes: no new codes needed (2 = options errors, 3 = no pages, 4 = partial — all reused).

**Sanity check:** `grep -c "capture-xhr" README.md spec.md` ≥ 1 each.

---

## Deliverables

```
fetch/
├── rod.go               # +CDP field, remote branch in ensureBrowser (J.5)
├── xhr.go               # NEW: XHRCapture, pattern match, drain logic (J.2)
├── fetcher.go           # +CaptureXHR on FetchRequest, +XHR on FetchResponse (additive)
└── xhr_test.go          # pattern/truncation/envelope units + tagged live capture test
clean/
├── injection.go         # NEW: StripHidden goquery pass (J.1)
├── clean.go             # hook StripHidden post-scope, pre-conversion
└── injection_test.go    # vector + precision pins
crawl/
├── ratelimit.go         # +Report/SetAuto adaptive delay (J.3)
├── crawl.go             # +SitemapOnly seed/guard, Report wiring, AutoThrottle option
├── ratelimit_test.go    # auto backoff/decay/floor units
└── sitemap_only_test.go # frontier-exactness, empty-sitemap error
scrape/
└── scrape.go            # ValidateOptions: XHR regexps, CDP scheme, capture-xhr+static (one boundary)
cli/
├── scrape.go            # --capture-xhr, --cdp-url flags + envelope xhr block (ONE place)
└── crawl.go             # --sitemap-only, --auto-throttle flags
mcp/
├── tools.go             # scrape_url: capture_xhr, cdp_url; crawl_site: sitemap_only, auto_throttle
└── server_test.go       # param round-trips
testdata/clean/injection.html   # spike fixture (vectors + visible control)
README.md / spec.md             # §10.6 + feature bullets
```

---

## Exit Criteria

- [ ] `go test ./clean/ -run Injection -v` passes: all hidden vectors absent from markdown,
      `display:flex` and `aria-hidden` controls survive (J.1)
- [ ] `go test ./fetch/ -run XHRPattern -v` passes; `go test -tags browser ./fetch/ -run
      TestCaptureXHRLive -v` captures the test page's `/api` JSON body (J.2)
- [ ] `go test ./crawl/ -run TestHostLimiters_Auto -v` passes: backoff ×2, Retry-After exact,
      decay ≥ floor, 60s cap, auto-off no-op (J.3)
- [ ] `go test ./crawl/ -run SitemapOnly -v` passes: exact frontier, empty sitemap → typed
      ErrSitemapOnlyEmpty (exitCode arm → exit 3), contradictory-flags exit 2 (J.4)
- [ ] `MAGPIE_CDP_URL=ws://127.0.0.1:1/x go test ./fetch/ -run TestRod_CDP -v` fails at connect
      with no launch/download attempt; bad scheme → exit 2 (J.5)
- [ ] MCP round-trip tests for `capture_xhr`, `cdp_url`, `sitemap_only`, `auto_throttle` pass;
      envelope/goldens byte-identical except spike-inventoried hidden-text fixtures (J.2/J.5)
- [ ] All gates: `go test ./...` + `go test -tags browser ./...` green, `go vet ./...` clean,
      `gofmt -l .` empty, `golangci-lint run ./...` clean
- [ ] `spec.md` §10.6 + README bullets present (J.6)

---

## Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---

You are building **Phase J of magpie** — Competitive Parity (5 features stolen from a Scrapling
comparison). magpie is a Go CLI web scraper (fetch → clean → extract) at
`/home/domidex/projects/magpie`; module `magpie`, Go 1.26+, zero CGO, single static binary.
Source of truth: `spec.md` (read §10.4–10.5 for the house style of flag/delta documentation).
Read `AGENTS.md` first: no new dependencies, no rod imports outside `fetch/`, browser tests
behind `//go:build browser`, all tests hermetic in the default suite.

### Established in prior phases (master, Phase H merged)

- Pipeline: `clean.Clean(ctx, RawPage)` is the single choke point — scope pass (goquery,
  `clean/scope.go`) → trafilatura main-content → html-to-markdown. Quality gate + challenge
  detection live in `clean/quality.go`.
- `fetch/rod.go`: `RodFetcher` lazily launches via `launcher.New()` in `ensureBrowser()`;
  `Fetch` delegates to `FetchWithActions(ctx, req, nil)` (one code path — keep it that way).
  Phase H added the actions DSL there. `Screenshot`/`ScreenshotActions` are rod-only extras.
- Additive-output convention (Phase G): new response fields are additive + `omitempty`; existing
  goldens must not drift. Static fetchers leave rod-only response fields nil; the CLI/MCP reject
  rod-only options + `render=static` at the exit-2 boundary (precedent: `page-format screenshot`).
- Options are validated pre-I/O in ONE boundary: `scrape.ValidateOptions` (scrape/scrape.go:114;
  CLI, MCP, and batch all reach it via `scrape.Run` — see how `--lang`/`--action` and the
  screenshot+static rejection live there). MCP params mirror flags in `mcp/tools.go` (see
  `lang`, `actions`). Exit codes: 2 = usage/options, 3 = all pages failed, 4 = partial; crawl
  typed errors map via `errors.Is` arms in `exitCode` (cli/root.go:93: robots→5, ceiling→6,
  scope/SSRF→2).
- `crawl/ratelimit.go`: `HostLimiters` — per-host `x/time/rate` bucket, `Wait`, `SetFloor`
  (crawl-delay, never raises), `Limit` (test helper). Wired at `crawl/crawl.go:199` (construct)
  and `:423` (Wait); `:418` applies crawl-delay floors. Fetches go through `FetchWithRetry`
  (`crawl/backoff.go`: 4 tries, jittered exponential backoff, 429/503 + Retry-After classified
  per attempt by `classify`) — AutoThrottle's Report must wire INSIDE that retry closure.
- `crawl/crawl.go seed()` already expands the seed through robots-declared sitemaps via
  `crawl.ListSitemapURLs` (capped at maxPages, scope-filtered by `scopeFilterExpansion`,
  warn-and-proceed on expansion error, `--no-sitemap` skips).
- `x-magpie` is the schema-key namespace; env prefix `MAGPIE_`; redaction rule: proxy/credential
  material never surfaces in errors/logs.

### Confirmed library APIs (verified 2026-09-28, do not re-research)

```go
// go-rod (already vendored) — passive XHR capture. page.GetResource CANNOT see XHR
// (go-rod issue #466); network events are the route (hijack would perturb the page).
wait := page.EachEvent(func(e *proto.NetworkResponseReceived) {
    // e.Type: proto.NetworkResourceTypeXHR | proto.NetworkResourceTypeFetch
    // e.RequestID, e.Response.URL, e.Response.Status, e.Response.MIMEType
}) // returns a wait func; invoke the callback-lightly, drain bodies AFTER settle
resp, err := proto.NetworkGetResponseBody{RequestID: id}.Call(page)
// resp.Body string, resp.Base64Encoded bool. CDP evicts buffers on page close:
// drain before page.Close(). "-32000 No resource with given URL found" = evicted
// → record metadata with empty body, never fail the fetch.

// go-rod remote browser — same calls as our local path (rod.go:55), no launcher:
r.browser = rod.New().ControlURL(r.CDP) // r.CDP = ws:// | wss:// | http(s)://
err := r.browser.Connect()

// goquery (already vendored) — strip pass operates on the scoped document:
doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
doc.Find("[style]").Each(...) // match inline styles by lowercased regexp, Remove() hits
// COMMENTS: selectors match ELEMENT nodes only — Find("comment") is a no-op (verified:
// 0 matches). Walk doc.Nodes[0] descendants and remove html.CommentNode manually.
```

### Data model rules (follow exactly)

- Plain structs, no interfaces with one implementation, no new packages beyond the files listed.
- `XHRCapture`: plain struct with json tags (additive, `omitempty` on optional fields).
- No new config-file keys — flags + env only (`MAGPIE_CDP_URL`), flag wins over env.
- Additive response/request fields only; never reorder or rename existing JSON envelope keys.

### Your goal

Implement, in this order: **J.5** (CDP URL, ~15 lines + plumbing) → **J.1** (hidden-text strip +
spike fixture) → **J.2** (XHR capture) → **J.3** (AutoThrottle) → **J.4** (sitemap-only) →
**J.6** (spec §10.6 + README). J.5 before J.2 because both edit `fetch/rod.go`.

### Per-file guidance

- **`fetch/rod.go`**: add `CDP string` field; `ensureBrowser` branches to
  `rod.New().ControlURL(r.CDP).Connect()` when set, skipping `launcher.New()` entirely. Redact
  userinfo in the wrapped error (reuse the redaction spirit from `fetch/proxy.go`).
- **`fetch/fetcher.go`**: `FetchRequest.CaptureXHR []string` (comment: rod-only, static ignores);
  `FetchResponse.XHR []XHRCapture` (additive-omitempty).
- **`fetch/xhr.go`** (new): `XHRCapture{URL, Status, MIMEType, Body, Truncated}`; pattern-compile
  helper returning error for bad regexps (boundary calls it); capture collector that subscribes
  `EachEvent(NetworkResponseReceived)` before navigation, records only XHR/Fetch types matching
  any pattern, caps at 50; `drain()` that runs after settle before page close, calling
  `NetworkGetResponseBody`, base64-decoding when flagged, truncating bodies at 64 KiB with
  `Truncated: true`, and tolerating CDP eviction errors as empty-body metadata. `FetchWithActions`
  calls subscribe/drain around its existing navigate+actions+settle flow only when
  `len(req.CaptureXHR) > 0`.
- **`clean/injection.go`** (new): `StripHidden(doc *goquery.Document)` — remove elements with
  `hidden` attr or inline style matching (lowercased, precompiled regexps):
  `display\s*:\s*none`, `visibility\s*:\s*hidden`, `font-size\s*:\s*0`, `opacity\s*:\s*0(\.0+)?\s*(;|$)`;
  plus comment nodes. `aria-hidden` and computed styles are OUT (precision; ponytail-comment the
  ceiling). **`clean/clean.go`**: call it after the scope pass, before conversion; strip errors
  never fail the page.
- **`testdata/clean/injection.html`** (new): six `MAGPIE_HIDDEN_<n>` markers in the six vectors +
  visible control paragraph + a `display:flex` span and an `aria-hidden` icon span that must
  survive. Write a spike note (commit message or comment) inventorying which vectors master's
  clean already dropped.
- **`crawl/ratelimit.go`**: `SetAuto()` once-flag + per-host `delay` map; `Report(host, code,
  retryAfter)` — 429/5xx: `delay = min(delay*2, 60s)`; explicit `retryAfter > 0` wins (cap 5m);
  2xx: `delay = max(floor, delay*3/4)`; `Report` applies the delay EAGERLY via
  `lim.SetLimit(rate.Every(delay))` under the existing mutex (so `Limit()` is testable; the
  `Wait` site stays untouched); auto-off ⇒ `Report` no-op. `SetFloor` remains authoritative for
  the floor. Lift `classify`'s Retry-After parse (backoff.go:70) into a shared helper — no
  parallel parser.
- **`crawl/crawl.go`**: `Options.AutoThrottle` + `Options.SitemapOnly`; `SetAuto()` call in
  `newCrawlContext`; `Report` call INSIDE the `do` closure passed to `FetchWithRetry`
  (crawl.go:438) — per attempt, never after it returns (backoff.go consumes intermediate
  429/503s; post-hoc wiring = dead backoff with green tests); seed(): SitemapOnly ⇒ seeds =
  expanded-only, zero expanded ⇒ typed `ErrSitemapOnlyEmpty` + `exitCode` arm → exit 3,
  expansion error fatal; skip the `frontier.ExtractLinks` call (crawl.go:492, only call site)
  when SitemapOnly.
- **`scrape/scrape.go`** (`ValidateOptions`, :114 — the one boundary CLI/MCP/batch share via
  `scrape.Run`; `cli/shared.go` has no validation role): XHR patterns compile (via
  `fetch.ValidateXHRPatterns`), CDP scheme ∈ {ws,wss,http,https}, `capture-xhr + render=static`
  → OptionsError exit 2 (mirror the screenshot wording).
- **`cli/scrape.go` / `cli/crawl.go`**: flags `--capture-xhr` (StringSlice, repeatable),
  `--cdp-url`, `--sitemap-only`, `--auto-throttle`; env fallback `MAGPIE_CDP_URL`;
  `sitemap-only + no-sitemap` → crawl-side predicate exit 2. The scrape envelope gains `xhr` in
  ONE cohesive block (Phase-G lesson: never scatter cases through the output ladder). Passing
  CDP/CaptureXHR down: `fetchBrowser` (scrape.go:420) is at 4 positional args — switch it to
  take `scrape.Options` (the direction the Phase-H review already took with `fetchURL`).
- **`mcp/tools.go`**: `scrape_url` params `capture_xhr []string`, `cdp_url string`;
  `crawl_site` params `sitemap_only *FlexBool`, `auto_throttle *FlexBool` (house CrawlIn
  pattern); scrape-option rejections come free via `scrape.Run` → `ValidateOptions`; the
  sitemap-only/no-sitemap conflict needs the crawl-side predicate called here too;
  `ScrapeOut` gains additive `xhr`.
- **`spec.md`**: `### 10.6 Competitive-parity delta (Phase J)` — same register as §10.4/10.5
  (flag names, precedence, caps, strip contract + `--page-format raw` escape hatch). **`README.md`**:
  five feature bullets.

### Tests (hermetic unless //go:build browser)

- `clean/injection_test.go`: all six vectors absent from markdown; `display:flex` +
  `aria-hidden` survive; visible paragraph intact.
- `fetch/xhr_test.go`: pattern compile errors; truncation at 64KiB; base64 fallback; eviction →
  empty-body metadata. `cli/scrape_test.go`: `TestScrape_XHREnvelope` with a fake fetcher
  returning canned `XHR` (the output-shape pin — no browser).
- `fetch/rod_cdp_test.go`: `TestRod_CDPNoLocalLaunch` — unreachable ws:// endpoint errors at
  Connect with no launcher activity.
- `crawl/ratelimit_test.go`: `TestHostLimiters_Auto*` — backoff ×2, Retry-After exact, decay ≥
  floor, 60s cap, off = no-op (assert via `Limit()`).
- `crawl/sitemap_only_test.go`: httptest site (sitemap + off-sitemap link) — exact frontier;
  empty sitemap → error; flag conflict → exit 2 (follow `cli/exitfor_test.go` pattern).
- Browser-tagged live test for end-to-end capture (httptest page `fetch()`ing `/api/data`) —
  model it on `fetch/rod_smoke_test.go`.
- MCP param round-trips in `mcp/server_test.go` following the existing `lang` test.

### Hard rules

- No new dependencies. No CGO. No rod imports outside `fetch/`.
- Default suite stays hermetic; browser tests behind `//go:build browser`.
- Every flag validated pre-I/O; bad option = exit 2 before any network/file I/O.
- Credentials (CDP URL userinfo) never appear in errors/logs.
- Gates before done: `go build ./... && go vet ./... && gofmt -l . && golangci-lint run ./... &&
  go test ./... && go test -tags browser ./...` all clean.

### Success criteria

1. Hidden-text fixture: markers absent from cleaned markdown, controls survive.
2. `--capture-xhr` envelope carries the live test's JSON body; static+flag rejected.
3. AutoThrottle unit math exact; `--auto-throttle` wires through crawl; off = no-op.
4. `--sitemap-only` frontier exact; empty-sitemap loud failure; conflicts exit 2.
5. CDP set ⇒ zero launcher activity, fast connect error, redacted; scheme validated.
6. All gates green; goldens byte-identical except inventoried hidden-text fixtures.

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — master @ 1e24a6f (Phase H, PR #13
  merged) verified via git log; every touched seam read in this planning session (`fetch/rod.go`
  79 lines, `crawl/ratelimit.go` 68 lines, `crawl/crawl.go` seed/link paths, `clean/quality.go`,
  `cli/scrape.go` flag plumbing, `mcp/tools.go` param pattern).
- [PASS] Every sub-task has a clear, testable completion condition — each of J.1–J.6 names its
  test files, the exact property asserted, and a one-line sanity check command.
- [PASS] Execution prompt is self-contained: (a) prior-phase facts inline (choke point, additive
  convention, exit codes, HostLimiters wiring lines, FetchWithRetry seam), (b) confirmed API
  snippets (rod NetworkResponseReceived/GetResponseBody with the eviction caveat, remote
  ControlURL, goquery element selectors + the comment-node manual-walk caveat — Find("comment")
  is a no-op) — verified against the vendored deps this session, no re-research needed,
  (c) Data Model Rules section (plain structs, additive-omitempty, flags+env only), (d) per-file
  guidance for every deliverable, (e) six observable success criteria.
- [PASS] Exit criteria map 1:1 to deliverables — every new file/test/flag has a matching
  criterion; docs criterion covers README+spec; gates criterion covers the whole diff.
- [PASS] Heavy external dependency strategy — the only real dependency is Chrome (rod): contract
  pins use a fake fetcher (TestScrape_XHREnvelope) in the hermetic suite; the live capture test is
  `//go:build browser`-tagged following rod_smoke_test.go; no LLM, no network in default suite.
- [PASS] New libraries: none (go-rod/x-time-rate/goquery all vendored). The one new *usage
  pattern* (rod network-event capture) carries a confirmed snippet + failure-mode notes
  ("-32000" eviction, drain-before-close, no body fetch inside the callback).
