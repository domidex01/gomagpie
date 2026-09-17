# Phase B — Testing: Zero-LLM Vertical Extractors

**Scope:** `vertical/` (new: registry, github, registries, social, arxiv, youtube, commerce), `scrape/` (dispatch + `runVertical` + `Deps.Fetcher` seam), `cli/` (`--vertical` flag), `clean/` (islands hook — behavior lock, no new goldens), `testdata/vertical/` (15 hand-authored fixtures), `fetch` (UA header flow for crates.io)
**Key Pattern:** Fake `Fetcher` (substring→bytes map, zero network) for all extractor units; real `StaticFetcher` against one recording `httptest.Server` for the single UA-flow test; panic-on-call `ExtractorFor` for the zero-LLM proof; pure `Match` tables on URL strings (no fetch at all); existing `fakeExtractor`/`openScrapeDB`/`codeOf`/`file://` helpers reused, one additive `Deps.Fetcher` seam required (nil = today's behavior).
**Dependencies:** stdlib `testing`, `net/http/httptest`, `net/url`, `encoding/json`, `math`, `reflect`, `strings`, `os`, `path/filepath` only — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an agent dev, I want registry/discussion/video/commerce URLs to return typed records with zero LLM calls and no API key, so that structured data is free | `vertical/*_test.go` per-extractor tables (`reflect.DeepEqual` on exact maps) + `TestRun_VerticalAutoHit_ZeroLLM` in `scrape/vertical_test.go` (panic-on-call `ExtractorFor`, `fx.total()==0`) | All 10 extractors DeepEqual-pass on fixtures; dispatch hit returns `Record` + `Vertical` name with 0 extractor calls and no key configured |
| US-2 | As a user, I want permissive matchers (`shopify_product`, `ecommerce_product`) to never hijack generic URLs, so that `auto` is safe to leave on | `TestOptInNeverSteals` in `vertical/vertical_test.go` (`MatchURL` on generic blog + github URL + bare shop homepage) + explicit `Lookup`+`Match` positive | `MatchURL` returns `ok==false` on all 3 generic URLs; explicit opt-in path still matches a real product URL |
| US-3 | As a user, I want vertical failures to be loud (mismatch = exit 2, extractor errors propagate, never silent LLM fallback), so that outages are visible | `TestRun_VerticalExplicitMismatch` + `TestRun_VerticalErrorPropagates` (scrape) + `TestScrape_VerticalFlags` (cli, `codeOf`) | `errors.Is(err, vertical.ErrURLMismatch)`; CLI exit 2 with `does not handle`; extractor HTTP-500 surfaces as hard error, `fx.total()==0` (no fallback attempt) |
| US-4 | As a user, I want thin SPA-shell pages rescued with island text while rich/scoped pages stay byte-identical, so that recall improves without regressions | `clean/islands_test.go` (thin-appends / scoped-unchanged / rich-unchanged / player-prepend / nil-cases) + `git status --porcelain testdata/clean/` gate | Thin+unscoped gains words; scoped and rich outputs byte-identical; `testdata/clean/` diff EMPTY (see §6: the B5 regen is expected to be a no-op) |
| US-5 | As a downstream consumer, I want vertical records to tolerate unknown API fields and keep numbers as numbers, so that upstream API drift never breaks parsing | Every fixture carries an unknown-field blob (`"future_field_xyz"`) + `DeepEqual` want-maps using `float64` for all numbers | All tables pass with blobs present; a `string`-typed assertion on any numeric field would fail (types locked by DeepEqual, not eyeballed) |

---

## 1. Component Mock Strategy

Phase type: **Service/pipeline with a pure-logic core** (Phase-1/A precedent). Mock strategy in one sentence: **one new fake (`fakeVerticalFetcher`, substring→bytes, in `vertical` tests only) plus an additive `scrape.Deps.Fetcher` seam for dispatch tests; everything else reuses existing helpers (`fakeExtractor`, `openScrapeDB`, `scrapeOrigin`, `codeOf`, `cleanFile`, `goldenDir`, `file://` CLI pattern); the single real-network-shaped test (crates.io UA) uses a recording `httptest.Server` with a real `StaticFetcher`.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| Registry (`List/Lookup/MatchURL`, sentinels) | No mock (pure) — URL strings in, `Info`/bool out | Exact 10-name set (`github_repo, pypi, npm, crates_io, reddit, hackernews, arxiv, youtube, shopify_product, ecommerce_product`); unknown `Lookup` → false; `MatchURL` skips both `OptIn` extractors | US-1, US-2 |
| Per-extractor `Match` | No mock (pure) — positive/negative URL tables | github 4 kinds incl. `/pull/`, `/releases/tag/`; lib.rs → crates false; `youtu.be`/`shorts`/`embed` → youtube true, bare `youtube.com/` false; `sh.reddit.com` false; arxiv `/pdf/` true | US-1 |
| JSON extractors (github, pypi, npm, crates) | `fakeVerticalFetcher` serving `testdata/vertical/*.json`; `DeepEqual` on full want-map | Exact B0 field sets (§8); requested downstream URL equals the expected API URL (assert from fetcher request log); non-2xx → hard error naming status | US-1, US-5 |
| reddit (HTML + `.json` fallback) | `fakeVerticalFetcher`: HTML hit; fallback case serves error-then-JSON; request log proves the 2-request shape | Post fields; subreddit `kind`; fallback fires ONLY for comment permalinks on HTML failure (post-HTML-failure on non-permalink → hard error, no retry) | US-1 |
| arxiv (XML) | `fakeVerticalFetcher` serving `arxiv.xml` (2 entries) | Versioned-ID strip in request URL (`id_list=2401.12345`, no `v2`) with full ID kept in output `url`; multi-author join; picks FIRST entry only | US-1 |
| youtube (blob scan + oEmbed) | `fakeVerticalFetcher`: full-blob HTML; fallback case blob-less HTML then oEmbed JSON | 4 URL shapes → same ID; `strconv` garbage (`viewCount:"abc"`) → 0, no error; oEmbed path yields title/author with empty description + 0 views (documented partial) | US-1 |
| commerce (shopify + ecommerce) | `fakeVerticalFetcher` (`.js` / page HTML via `clean.HarvestSidecar` reuse) | Price cents→float with `1e-9` tolerance; `currency` key ABSENT from shopify map (omit-don't-guess locked); no-Product page → error containing `no product data`, never empty map | US-1, US-2 |
| `fetchJSON` UA flow (crates.io policy) | Recording `httptest.Server` + REAL `fetch.NewStaticFetcher()` (sole real-client test) + compile-time `var _ vertical.Fetcher = (*fetch.StaticFetcher)(nil)` | Server-observed `User-Agent` == `magpie/1.0 (+https://github.com/you/gomagpie)` (value from `fetch/http.go:25` — read, don't hardcode blindly) | US-1 |
| `scrape.Run` dispatch | `Deps.Fetcher` seam (§3, REQUIRED) + panic-on-call `ExtractorFor` + `openScrapeDB` + `testSchema` | Hit: `Record` DeepEqual + `Vertical` name + `fx.total()==0` + `FromCache==false`; miss (auto, generic URL): normal markdown/LLM path intact (`fx.total()==1` with schema+key); mismatch: `errors.Is(ErrURLMismatch)`; extractor 500: hard error, `fx.total()==0` | US-1, US-3 |
| Selector-cache isolation on vertical hit | Existing `PutSelectors`/`GetSelectors` (`store/sqlite.go:263ff`; seed pattern `scrape_test.go:178`, not-found pattern `:334`) | Pre-seeded `(github.com, hash)` row is IGNORED on hit (vertical `Record` returned, proving no read); post-hit `GetSelectors` still not-found for the vertical run (proving no write) | US-1 |
| `cli --vertical` | Existing `codeOf` + `testEnv` + `file://` fixtures (no new helpers); unknown-name case does zero I/O | `--vertical bogus` → exit 2 pre-fetch; `--vertical reddit file://article.html` → exit 2 with `does not handle` (mismatch AFTER local fetch — hermetic); `--vertical` help text mentions default-off | US-3 |
| `clean` islands + player | No mock (pure) — synthetic thin HTML + hand-built sidecar JSON + existing `cleanFile(t,"article")` as the rich negative | Thin+unscoped appends (`WordCount` grows, dedupe holds, ≤4000 chars); same input with `Scope{Include:["article"]}` byte-identical; article golden untouched; `PlayerDetails` on non-YT HTML → nil; `PlayerResponseHTML` table (full blob / noise-only / truncated braces → false) | US-4 |
| Golden drift gate | No Go test — `git status --porcelain testdata/clean/` must print nothing (§9) | Proves the islands hook fires nowhere in the existing corpus (spa-shell.html carries zero island markers — verified 2026-09-17, so B5's "regen" is a no-op by construction) | US-4 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | `fakeVerticalFetcher`, httptest recording server (localhost only), `testdata/` files, temp-dir SQLite — no external network, no browser, no keys | <60s total | Every push; the only default gate |
| Golden regen (`go test ./clean/ -update`) | Same as unit; expected to write NOTHING new (empty-diff gate, §6) | seconds | After any `Clean`-touching change — followed by `git status` check, never blind-commit |
| Manual smoke (not a test file) | Built binary + local replay stub (httptest or `file://`); real-host URLs only by hand, never in tests | minutes | Pre-release: `magpie scrape --vertical auto` against a local GitHub-API replay with no key set; `--vertical reddit <github URL>` → exit 2 |

No browser tier (verticals never escalate to rod; existing `//go:build browser` smoke untouched). No live-provider tier: the zero-LLM proof is a panic-on-call fake, and CLI-level auto-hit on real hosts is explicitly NOT tested (see "What NOT to Test" — hermetic law).

---

## 3. Fake / Mock Implementations

One new fake only. Everything else is reused verbatim (file:line pointers in §5).

### `fakeVerticalFetcher` — replaces `*fetch.StaticFetcher` in `vertical` + `scrape` tests

```go
// vertical/vertical_test.go (package vertical_test)
type fakeVerticalFetcher struct {
    mu      sync.Mutex
    bodies  map[string]fakeResp // substring match on request URL
    order   []string            // every requested URL, in order
    headers http.Header         // headers of the LAST request (UA/cookie asserts)
}

type fakeResp struct {
    status int    // 0 = 200
    body   []byte // fixture bytes
    err    error  // non-nil = transport failure (reddit-fallback path)
}

func (f *fakeVerticalFetcher) Fetch(_ context.Context, req fetch.FetchRequest) (*fetch.FetchResponse, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.order = append(f.order, req.URL)
    for sub, r := range f.bodies {
        if strings.Contains(req.URL, sub) {
            if r.err != nil {
                return nil, r.err
            }
            st := r.status
            if st == 0 {
                st = 200
            }
            return &fetch.FetchResponse{URL: req.URL, FinalURL: req.URL, StatusCode: st, HTML: r.body}, nil
        }
    }
    return nil, fmt.Errorf("fake fetcher: unexpected URL %s (order=%v)", req.URL, f.order)
}

func verticalFixture(t *testing.T, name string) []byte {
    t.Helper()
    raw, err := os.ReadFile(filepath.Join("..", "testdata", "vertical", name))
    if err != nil {
        t.Fatal(err)
    }
    return raw
}
```

**Matches real use:** `Fetch(ctx, fetch.FetchRequest{URL: "https://api.github.com/repos/o/r"})` → `*fetch.FetchResponse` with `HTML` = fixture bytes — the exact shape `*fetch.StaticFetcher.Fetch` returns. Substring keys are downstream-host fragments (`"api.github.com"`, `"old.reddit.com"`, `"export.arxiv.org"`, `"youtu"`, `".js"`); tests additionally assert `f.order` equals the expected request sequence (proves reddit-fallback 2-request shape and no redundant fetches). For `scrape` dispatch tests the same struct is reused — it lives in `vertical_test` so `scrape` tests CANNOT import it (Go: no cross-package test imports); §5 prescribes a 15-line copy in `scrape/vertical_test.go` (rule-of-three: 2 call sites, copy beats a shared harness — Phase-A precedent).

### REQUIRED testability seam — `scrape.Deps.Fetcher` (production change, additive)

Hermetic dispatch tests are UNBUILDABLE without this: `scrape.Run` constructs its own `StaticFetcher`, and registry `Match` requires real-host URLs (`github.com`), so a hit would dial the live internet. Prescribe exactly:

```go
// scrape/scrape.go — ADD one field, default nil:
type Deps struct {
    ...
    Fetcher vertical.Fetcher // nil = NewStaticFetcher() (production default; tests inject the fake)
}
```

`Run` uses `d.Fetcher` when non-nil for BOTH the raw-URL fetch and `runVertical` (the fake serves both — see §7). `fetchBrowser` path unchanged. This is the phase's only production-for-testability delta: one interface field reusing the existing `vertical.Fetcher` (no new interface), consistent with `Deps`' existing `ExtractorFor`/`APIKeyFor` injection pattern.

### Panic-on-call extractor (zero-LLM proof) — inline per test, no helper

```go
deps := scrape.Deps{
    DB: db,
    Fetcher: fake,
    ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
        t.Error("ExtractorFor called on vertical hit — zero-LLM contract violated")
        return nil, errors.New("must not be called")
    },
    APIKeyFor: func(string) string { return "" }, // no key: proves keylessness
}
```

`t.Error` (not panic) so the failure message names the contract; the returned error is unreachable on a correct hit.

---

## 4. Test File List

```
gomagpie/
├── vertical/
│   ├── vertical_test.go     # NEW: TestList (exact 10-name set) + TestLookup + TestMatchURLDispatch (strict-only incl. OptIn skip) + TestOptInNeverSteals + fetchJSON table (2xx/non-2xx/bad-JSON) + UA-flow test (real StaticFetcher × recording httptest) + fakeVerticalFetcher + verticalFixture
│   ├── github_test.go       # NEW: Match 4-kind table (repo/issue/pull/releases-tag + negatives) + Extract repo/issue DeepEqual + unknown-blob tolerance + API-URL assert from request log
│   ├── registries_test.go   # NEW: pypi/npm/crates DeepEqual (B0 field sets) + name-from-path vectors (trailing slash, /project/ prefix) + lib.rs/blog negatives
│   ├── social_test.go       # NEW: reddit post/subreddit/fallback-only-on-permalink/fallback-exhausted + HN story/comment DeepEqual + Match negatives (sh.reddit.com, /r/ without path)
│   ├── arxiv_test.go        # NEW: version-strip + first-entry-only + multi-author join + non-arxiv negatives; note: encoding/xml strictness — malformed fixture row asserts hard error
│   ├── youtube_test.go      # NEW: 4-shape ID table + full-blob DeepEqual + strconv-garbage→0 + oEmbed-fallback partial + no-blob-no-oembed error
│   └── commerce_test.go     # NEW: shopify DeepEqual (currency ABSENT — assert with _, present := m["currency"]) + price-tolerance + handle treatment + ecommerce Product-pick + no-Product error + explicit opt-in Match positives
├── scrape/
│   └── vertical_test.go     # NEW: 15-line fake copy + auto-hit zero-LLM + auto-miss fallthrough (markdown AND schema paths) + explicit mismatch + unknown name + extractor-error propagation + cache no-read/no-write + Vertical=="" bit-identical (Rendered/Markdown vs baseline run)
├── cli/
│   └── scrape_test.go       # ADD: unknown --vertical → exit 2 with zero I/O (assert BEFORE any server var is touched) + mismatch via file:// → exit 2 + "does not handle" + help-text grep for default-off (cheap string-contains on Long/flag usage)
├── clean/
│   └── islands_test.go      # NEW: IslandText table (array/single/deep-nest/dedupe/<40-drop/CSS-noise/4000-cap) + PlayerResponseHTML table + PlayerDetails nil-cases + Clean-integration (thin-appends/scoped-unchanged/rich-unchanged/YT-prepend)
├── testdata/
│   └── vertical/            # 15 NEW hand-authored fixtures, EACH with a "future_field_xyz" unknown blob (see §6 table for per-file content contract)
└── plan/phase-B-tests.md    # this file
```

Every deliverable in `plan/phase-B.md` §4 maps: 7 `vertical/*.go` → 7 test files; `clean/islands.go` + hook → `islands_test.go` + empty-diff gate; `scrape/scrape.go` → `scrape/vertical_test.go`; `cli/scrape.go` → additions; `testdata/vertical/` → produced alongside the tables; `spa-shell.md` → covered by the empty-diff gate (no regen expected). Phase C surface (`vertical` command, MCP tools) has NO test file here — out of scope by plan.

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): per-package helpers + the existing `-update` flag; hermetic-only suite, no integration-tier flag (Phase-A precedent). **Additions only — no existing test file is modified except `cli/scrape_test.go` gaining 3 funcs.**

```go
// vertical/vertical_test.go — NEW package vertical_test (external: Match/Extract/List are all exported)
import ("context" "encoding/json" "errors" "fmt" "net/http" "net/http/httptest" "net/url" "os" "path/filepath" "reflect" "strings" "sync" "testing"
    "gomagpie/fetch" "gomagpie/vertical")
// fakeVerticalFetcher + verticalFixture: full source in §3, copy verbatim.
// mustURL(t, s): url.Parse or Fatal. wantMaps as Go literals with float64 numbers.

// scrape/vertical_test.go — NEW, package scrape_test
// 15-line fakeVerticalFetcher COPY (same shape, bodies keyed on "github.com"/"api.github.com"/"example.com").
// REUSE verbatim: fakeExtractor, openScrapeDB, testSchema, fakeDeps (for miss-path tests), scrapeHTML, scrapeOrigin.
// Seed pattern: db.PutSelectors(domain, selector.SchemaHash(sch), string(doc), 3) (scrape_test.go:178).
// Not-found pattern: db.GetSelectors(host, hash) → (!ok) (scrape_test.go:334).

// clean/islands_test.go — NEW, package clean_test
// REUSE: cleanFile(t, "article") (rich negative), t.Context() idiom.
// thinShell(scripts ...string): builds minimal trafilatura-thin HTML with given <script> blobs — 10 lines, local to this file.
// sidecarOf(t, v any): json.Marshal grounds IslandText inputs without fixture files for unit rows.

// cli/scrape_test.go — ADD 3 funcs; REUSE: codeOf, testEnv, mustAbs, runScrape, scrapeOptions (Vertical field added by B4).
```

Scope rationale: everything function-scoped (per-test fakes, `t.TempDir()` DBs, per-test servers) — safe for parallel `go test`. The sole shared mutable artifact is `testdata/`, written only under `-update`, which this phase expects to be a no-op.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| `Deps.Fetcher` seam is REQUIRED, not optional | One additive interface field, nil-default; §3 prescribes the exact shape | Without it, any dispatch hit dials live `github.com` — the zero-LLM test would be either networked (banned) or nonexistent (worse). Follows the file's own `ExtractorFor`/`APIKeyFor` precedent |
| `DeepEqual` on full maps, not spot asserts | Every extractor table compares the ENTIRE returned map to a literal (numbers as `float64`) | Spot asserts pass a record that leaks extra keys or coerces numbers to strings — the exact-map contract IS US-1/US-5; `encoding/json`→`map[string]any` always yields `float64`, so literals must too (document the gotcha in §8) |
| Unknown-blob-per-fixture is mandatory, not garnish | Each of the 15 fixtures carries `"future_field_xyz": {...}` (shape varies: object/array/scalar across files) + contract table below | Upstream APIs add fields constantly; the blob proves pick-don't-parse. A fixture without one fails review even if tests pass |
| Opt-in guarantee tested at `MatchURL`, not `Match` | `TestOptInNeverSteals` calls the DISPATCH function on 3 generic URLs | Testing `Match` alone would pass while `MatchURL` still auto-fires permissives — the bug lives in the dispatch skip, so the test must too |
| Zero-LLM proven by `t.Error`-inside-`ExtractorFor`, not by usage counters alone | Fake that reports if constructed | `llm_calls==0` in SQLite is necessary but not sufficient (a run could skip recording); construction-attempt is the direct observable. Both asserted (belt + suspenders, 2 lines) |
| Cache isolation needs BOTH no-read and no-write asserts | Pre-seed then prove ignored; post-hit prove absent | No-write-only would miss a run that READS a stale cached row and returns it instead of the vertical record (wrong data, green test) |
| B5's "regen spa-shell.md" re-scoped to an empty-diff gate | `git status --porcelain testdata/clean/` must print NOTHING; §7 includes `TestClean_SpaShellUntouchedByIslands` | Verified 2026-09-17: `spa-shell.html` has zero island markers and 7-word output — the hook cannot fire (no sidecar). Regenerating anyway would churn a golden for zero behavior change; the honest gate is proving no drift |
| Shopify `currency` asserted ABSENT (`_, ok := m["currency"]; ok → Error`) | Negative key assertion, not omission-by-silence | The `.js` payload has no currency; a future "helpful" default (e.g. `"USD"`) would be a lie — the test locks the omission (phase-B §B3 decision) |
| Price float compared with tolerance, everything else exact | `math.Abs(got-19.99) > 1e-9 → Error` for the cents→units conversion only | `1999/100` is not exact in binary; DeepEqual on the full map would flake — isolate the one inexact field, keep the rest exact |
| HN comments = top-level `len(children)` only | Fixture has nested grandchildren; want-map pins the TOP-LEVEL count | Locks the phase-B "no recursion" decision so a future recursive count fails loudly instead of drifting silently |
| CLI auto-hit on real hosts is NOT tested | Stated in §8 What-NOT-to-test; scrape-level seam test + manual smoke cover it | Would require live `github.com` — banned. The seam test exercises identical code (`runVertical`); CLI adds only flag parsing, tested via unknown/mismatch cases |
| Copy the 15-line fake into `scrape` tests; no shared harness | Duplication of a trivial struct, Phase-A precedent (3× `fakeExtractor`) | An exported `verticaltest` helper package is a new public API surface for 2 call sites — rule of three says copy |
| arXiv malformed-XML row asserts hard error | One table row feeds truncated Atom | `encoding/xml` is strict; proves the fail-loud rule for the only non-JSON transport instead of assuming it |

### Fixture content contract (each file: minimal real shape + unknown blob)

| Fixture | Must contain | Unknown blob |
|---|---|---|
| `github-repo.json` | `full_name, description, stargazers_count, forks_count, language, license{name}, open_issues_count` | `"future_field_xyz": {"nested": [1,2]}` top-level |
| `github-issue.json` | `title, user{login}, state, comments, body` (+ repo URL context from request path) | `"reactions": {"+1": 99, "rocket": "soon"}` |
| `pypi.json` | `info{name, version, summary, author, requires_python, home_page}` | `"vulnerabilities": [{"id": "x"}]` |
| `npm.json` | `name, description, dist-tags{latest}, versions{<latest>{version, description}}` | `"future_field_xyz": [1, "two"]` |
| `crates.json` | `crate{name, description, downloads, newest_version}` | `"meta": {"ttl_hours": 1}` |
| `reddit-post.html` | old.reddit-shaped: `a.title`, `a.author`, `div.score`, `div.usertext-body`, comments link with count | HTML comment `<!-- future: new sidebar node -->` + extra `div.ad-slot` node (must be ignored) |
| `reddit-post.json` | `[{data:{children:[{data:{title,author,score,selftext,num_comments}}]}}]` | `"sponsored": true` inside child data |
| `reddit-subreddit.html` | subscribers count node + description node | extra `div.widget` node |
| `hn-story.json` | Algolia shape: `id, title, url, points, author, text:null, children[2 with nested child]` | `"highlightResult": {...}` |
| `hn-comment.json` | Same shape with `parent_id` set, `title:null`, `text` non-empty | `"story_id": 123` |
| `arxiv.xml` | 2 `<entry>`: multi-author first entry, versioned id `2401.12345v2` | `<arxiv:comment>` + custom `<future:tag>` elements |
| `youtube-watch.html` | 2 noise `<script>` + `ytInitialPlayerResponse = {...full videoDetails...}` | Extra `playerAds`, `cards` subtrees inside the blob |
| `youtube-oembed.json` | `title, author_name, html` | `"future_field_xyz": "scalar"` |
| `shopify-product.json` | `title, vendor, body_html, product_type, price: 1999 (cents, number), images[{src}×2]` | `"variants": [{"id": 1}]` |
| `product-jsonld.html` | JSON-LD `Product` block + a `WebSite` block (must be skipped) + thin body text | `WebSite` block doubles as the unknown-shape negative |

---

## 7. Example Test Case

```go
// scrape/vertical_test.go
package scrape_test

import (
    "context"
    "errors"
    "reflect"
    "strings"
    "testing"

    "gomagpie/extract"
    "gomagpie/scrape"
    "gomagpie/selector"
    "gomagpie/vertical"
)

// TestRun_VerticalAutoHit_ZeroLLM is the phase's load-bearing test: a
// github URL with Vertical:"auto" must return the vertical record through
// the fake fetcher while the LLM constructor reports any call as failure.
// It exercises the Deps.Fetcher seam (§3), the real registry Match
// (github.com — no network, Extract is faked), and cache isolation.
func TestRun_VerticalAutoHit_ZeroLLM(t *testing.T) {
    db := openScrapeDB(t)
    sch := testSchema(t)
    // Seed a cache row the hit MUST ignore (no-read proof): if dispatch
    // consulted the cache, we'd get the seeded Widget, not the repo map.
    if err := db.PutSelectors("github.com", selector.SchemaHash(sch), `{"fields":{}}`, 3); err != nil {
        t.Fatalf("PutSelectors: %v", err)
    }
    fake := &fakeVerticalFetcher{bodies: map[string]fakeResp{
        "github.com/o/r":   {body: []byte(`<html><head><title>o/r</title></head><body><p>` + strings.Repeat("filler ", 60) + `</p></body></html>`)},
        "api.github.com/":  {body: verticalFixtureBytes(t, "github-repo.json")},
    }}
    deps := scrape.Deps{
        DB:      db,
        Fetcher: fake, // §3 seam: raw-URL fetch AND api.github.com served locally
        ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
            t.Error("ExtractorFor called on vertical hit — zero-LLM contract violated")
            return nil, errors.New("must not be called")
        },
        APIKeyFor: func(string) string { return "" }, // no key configured — hit must not need one
    }
    res, err := scrape.Run(context.Background(), deps, "https://github.com/o/r", scrape.Options{
        Render: "static", Schema: sch, UseCache: true, Vertical: "auto",
    })
    if err != nil {
        t.Fatalf("Run: %v", err)
    }
    if res.Vertical != "github_repo" {
        t.Errorf("Vertical = %q, want github_repo", res.Vertical)
    }
    want := map[string]any{
        "kind": "repo", "full_name": "o/r", "description": "demo",
        "stars": float64(42), "forks": float64(7), "language": "Go",
        "license": "mit", "open_issues": float64(3), "url": "https://github.com/o/r",
    }
    if !reflect.DeepEqual(res.Record, want) {
        t.Errorf("Record mismatch:\n got %#v\nwant %#v", res.Record, want)
    }
    if res.FromCache {
        t.Error("FromCache = true on vertical hit — vertical output must never ride the selector cache")
    }
    // No-write proof: the vertical run must not leave a cache row behind.
    // (The seeded row above is under the same key — so instead assert the
    // ROW IS STILL the seed: proves neither overwrite nor delete.)
    got, ok, err := db.GetSelectors("github.com", selector.SchemaHash(sch))
    if err != nil || !ok || got != `{"fields":{}}` {
        t.Errorf("seeded cache row disturbed by vertical hit: got %q ok=%v err=%v", got, ok, err)
    }
    // Request-log proof: exactly the raw fetch + ONE api call, nothing else.
    if len(fake.order) != 2 || !strings.Contains(fake.order[1], "api.github.com/repos/o/r") {
        t.Errorf("request order = %v, want [raw, api.github.com/repos/o/r]", fake.order)
    }
}
```

Notes for the executor: `verticalFixtureBytes` is `scrape`-sideaccess to `../testdata/vertical/` (same `os.ReadFile` one-liner as `verticalFixture` — copy it, don't import test helpers across packages); the seeded-row-identity trick replaces a plain not-found assert because the seed doubles as the no-read proof (two properties, one row). The `want` literal must match B0 field names EXACTLY — cross-check against `plan/phase-B.md` B0 before running. `float64` on every number (US-5 gotcha: `json.Unmarshal` never yields `int`).

Companion tests in the same file (table shape, not full source): `TestRun_VerticalAutoMiss` (generic `https://example.com/x` + schema + key → `fx.total()==1`, `Vertical==""`), `TestRun_VerticalMissMarkdown` (no schema → markdown path, `Record==nil`), `TestRun_VerticalExplicitMismatch` (`Vertical:"reddit"` × github URL via fake → `errors.Is(err, vertical.ErrURLMismatch)`), `TestRun_VerticalUnknownName` (`Vertical:"tumblr"` → error naming the name, zero fetcher requests), `TestRun_VerticalErrorPropagates` (fake serves 500 on api host → hard error containing `500`, `fx.total()==0`), `TestRun_VerticalEmptyIsBitIdentical` (`Vertical:""` run vs the same run on main-without-B4: `Rendered`+`Markdown` equal — guards the insertion point).

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase B of `gomagpie` — zero-LLM vertical extractors. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-B.md` (B0–B5: registry shape, 10 extractor field lists, dispatch semantics, islands hook, import-direction rule), `plan/phase-B-tests.md` (this suite's contract — §3 fakes, §6 fixture table, §7 load-bearing test), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test. Production code may or may not exist yet — write tests against the frozen signatures in phase-B §B1/B4/B5; if a signature is missing, implement the test to the plan and flag the gap rather than inventing API.

### What This Project Is
Go 1.26+ CLI web scraper (binary `magpie`, module `gomagpie`): fetch → clean → extract. Phase B adds `vertical/` (10 zero-LLM extractors: `github_repo, pypi, npm, crates_io, reddit, hackernews, arxiv, youtube, shopify_product, ecommerce_product`), `scrape.Options.Vertical` (`""|auto|name`) + `Result.Vertical` + `runVertical`, `--vertical` CLI flag, `clean` island/player rescue for thin pages. Tests are hermetic: `go test ./...` with no external network, no browser, no keys. No new test deps. Import law: `vertical` may import `fetch`+`clean`; `clean` must NEVER import `vertical` (cycle) — your tests must not create the cycle either (no `clean` test imports `vertical`; youtube-scan sharing is tested from the `vertical` side + `clean.PlayerResponseHTML` unit rows).

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | typed records, zero LLM, no key | 10 extractor DeepEqual tables + auto-hit zero-LLM (§7) | exact maps; `fx.total()==0`; `Vertical` name set |
| US-2 | permissives never auto-fire | TestOptInNeverSteals on `MatchURL` + explicit positives | 3 generic URLs → `ok==false`; explicit still matches |
| US-3 | failures loud, never silent fallback | mismatch/unknown/error-propagation + CLI exit 2 | `errors.Is(ErrURLMismatch)`; `does not handle`; exit 2 |
| US-4 | thin rescued, rich/scoped byte-identical, zero golden drift | islands_test + `git status --porcelain testdata/clean/` empty | words grow iff thin+unscoped; clean-tree diff EMPTY |
| US-5 | unknown-field tolerant, numbers stay numbers | blob-per-fixture + DeepEqual with `float64` literals | all green WITH blobs present; no string-typed numbers |

### Why Fakes Are Required
- Registry APIs + site HTML (github/pypi/npm/crates/reddit/hn/arxiv/youtube/shopify): live hosts are banned in tests — `fakeVerticalFetcher` (§3: substring→bytes + request log) serves `testdata/vertical/` fixtures deterministically, including error injection (reddit fallback, 500-propagation).
- `scrape.Run`'s internal fetcher: registry `Match` needs real-host URLs, so dispatch hits REQUIRE the additive `scrape.Deps.Fetcher` seam (§3 exact shape, nil = `NewStaticFetcher()`); without it the zero-LLM test would dial live `github.com`.
- LLM extractor: must PROVE non-invocation — panic/`t.Error`-on-call `ExtractorFor` (§7), not just usage counters.
- crates.io `User-Agent` policy: the ONE real-client test — recording `httptest.Server` (localhost, allowed) × real `StaticFetcher` asserting the UA from `fetch/http.go:25`.
- SQLite: NOT faked — real `modernc.org/sqlite` on `t.TempDir()` via existing `openScrapeDB` (Phase-1 precedent).

### What NOT to Test
- Don't test live hosts, real API keys, or browser escalation — verticals never touch rod; CLI auto-hit on real hosts is manual-smoke-only (§2).
- Don't test Go stdlib (`encoding/xml` strictness beyond one malformed row, `encoding/json` number types beyond the DeepEqual literals) or goquery selector mechanics beyond our pick assertions.
- Don't test Phase C surface (`magpie vertical` command, `list_extractors`/`vertical_scrape` tools) — unbuilt; `TestList`'s exact-name set IS the forward-compat lock for them.
- Don't create a shared `testutil` package — copy the 15-line fake into `scrape` tests (2 call sites; Phase-A precedent).
- Don't regenerate or modify ANY `testdata/clean/` golden — the empty-diff gate (§9) must pass; if your hook fires on the existing corpus, the HOOK is wrong, not the goldens.
- Don't invent extractor field names — B0 lists are frozen (github `{kind,full_name,description,stars,forks,language,license,open_issues,url}`; pypi `{name,version,summary,author,requires_python,url}`; npm `{name,version,description,latest,url}`; crates_io `{name,newest_version,description,downloads,url}`; reddit `{kind,title,author,score,comments,selftext,url}`; hackernews `{kind,title,url,points,author,comments,text[,parent_id]}`; arxiv `{title,authors,summary,published,url}`; youtube `{title,author,description,views,duration_s,url}`; shopify `{title,vendor,price,type,images,url}` NO currency; ecommerce `{name,brand,price,currency,availability,url}`). Renames are churn — flag, don't freelance.

### Critical: Fake Implementations

Copy verbatim (only new shared test code): `fakeVerticalFetcher` + `fakeResp` + `Fetch` + `verticalFixture` — full source in test-plan §3 (needs `context, fmt, net/http, os, path/filepath, strings, sync, testing, gomagpie/fetch`). Scrape-side copy: same struct minus `headers` field (unneeded there) + `verticalFixtureBytes` one-liner. Panic extractor: §7 inline closure. `Deps.Fetcher` seam: §3 exact field — if production lacks it, ADD it (one line + nil-default wiring); the suite cannot run hermetically without it.

Reuse verbatim (read first, never modify): `fakeExtractor`+`total()`, `scrapeOrigin`, `openScrapeDB`, `testSchema`, `fakeDeps` (`scrape/scrape_test.go:24ff`); `PutSelectors` seed (`:178`) + `GetSelectors` not-found (`:334`); `codeOf`, `testEnv`, `mustAbs`, `runScrape`, `scrapeOptions` (`cli/scrape_test.go`); `cleanFile`, `goldenDir`, `update` (`clean/clean_test.go`); UA value (`fetch/http.go:25`).

### Test Files to Create/Change

```
vertical/vertical_test.go     # NEW (~8 funcs): registry set/lock + MatchURL dispatch + OptIn + fetchJSON + UA-flow + fakes (§4)
vertical/github_test.go       # NEW (~5): 4-kind Match + repo/issue DeepEqual + URL-log assert
vertical/registries_test.go   # NEW (~6): 3 DeepEqual + name-vector + negatives
vertical/social_test.go       # NEW (~7): post/subreddit/fallback-gating + story/comment
vertical/arxiv_test.go        # NEW (~4): strip/entry/authors/malformed
vertical/youtube_test.go      # NEW (~6): ID shapes + blob + garbage-numbers + oEmbed + no-data error
vertical/commerce_test.go     # NEW (~6): shopify exact-stars + currency-absent + tolerance + ecommerce pick + no-Product error
scrape/vertical_test.go       # NEW (~8 incl. §7 verbatim): hit/miss/mismatch/unknown/error/cache-pair/bit-identical
cli/scrape_test.go            # ADD (3): unknown→2 pre-I/O + mismatch→2 via file:// + help-text default-off
clean/islands_test.go         # NEW (~8): IslandText table + scan table + integration ×4 (thin/scoped/rich/YT)
testdata/vertical/            # 15 files per §6 contract table (minimal shape + future_field_xyz blob EACH)
```

### Per-File Coverage Guidance

#### vertical/vertical_test.go
`TestList_ExactNameSet`: `List()` → name set == the 10 frozen names (map-compare: catches renames/adds/drops, ignores order). `TestLookup_*`: each name resolves with matching `Info.Name`; `"tumblr"` → false. `TestMatchURL_StrictOnly`: github/pypi/npm/crates/reddit/HN/arxiv/youtube URLs hit; `https://example.com/x`, `https://shop.example/products/w` (shopify MUST NOT fire), any blog URL (ecommerce MUST NOT fire) miss. `TestOptInNeverSteals` (US-2 flagship): the 3 generic URLs + assert `Lookup("shopify_product").Match(shopURL)` true (explicit path alive). `TestFetchJSON_*`: 200→map; 500→error containing `500`; invalid JSON→error; ALWAYS via fake (unit) — the real-client case lives only in the UA test. UA test: recording httptest + `fetch.NewStaticFetcher()` + `fetchJSON`-equivalent GET → observed UA equals `fetch/http.go:25` value; plus `var _ Fetcher = (*fetch.StaticFetcher)(nil)` compile line.

#### vertical/*_test.go (per extractor)
Pattern per file: `Test<Name>Match_Table` (≥6 rows: positives per URL variant + ≥3 negatives incl. near-misses like `lib.rs`, `sh.reddit.com`, bare `youtube.com/`); `Test<Name>Extract_*` (fake → `DeepEqual` want-literal, `float64` numbers, request-`order` assert for multi-fetch paths); one negative-extraction row each (404 fixture status / missing node / malformed blob → error, never partial-and-nil-error except documented oEmbed-partial). Reddit: fallback row asserts `len(order)==2` and second URL ends `.json`; non-permalink HTML-failure row asserts `len(order)==1` + error (gating proven). YouTube: `viewCount:"12ab"` row → `views==0`, no error. Commerce: currency-absent negative-key assert; `19.99`-tolerance ONLY on price.

#### scrape/vertical_test.go
§7 verbatim for the hit test. Miss tests use REAL `fakeDeps` + `fakeExtractor` (no seam needed — miss never touches vertical fetch... except the RAW fetch still goes through `d.Fetcher` when set: leave `Fetcher` NIL for miss tests so they also prove the nil-default path). Unknown-name: `Vertical:"tumblr"` → error containing `tumblr`, `len(fake.order)==0` (no I/O — validation first). Bit-identical: same origin + `Options{Render:"static"}` with and without `Vertical:""` → equal `Markdown`+`Rendered`.

#### cli/scrape_test.go (additions)
`TestScrape_VerticalUnknown`: `--vertical tumblr` on a `file://` fixture → `codeOf==2` (pre-I/O: assert by pointing at a NONEXISTENT file — exit 2 anyway proves no fetch attempted... careful: missing-file fetch errors as exit 1, so exit 2 PROVES validation-first). `TestScrape_VerticalMismatch`: `--vertical reddit` + existing article file → exit 2, stderr contains `does not handle`. `TestScrape_VerticalHelp`: usage string contains `auto` and `default` (locks the documented default-off).

#### clean/islands_test.go
`TestIslandText_Table`: array-sidecar / single-object / nested-`__NEXT_DATA__` / dedupe / `<40`-drop / CSS-noise-drop / 4000-cap (assert `len<=4000` AND word-boundary: no mid-word cut — check last char isn't a letter-split... simpler: assert result ends at a `\n\n` boundary). `TestPlayerResponseHTML_Table`: full blob → map with `videoDetails`; noise-only scripts → false; truncated `{` → false (no panic on unbalanced input — the crash row). `TestClean_ThinAppendsIslands`: `thinShell(nextDataJSON)` (helper builds `<html>` with 30-word body + `#__NEXT_DATA__` script) → `WordCount` grows AND contains a known leaf string. `TestClean_ScopedSkipsIslands`: same + `Scope{Include:["article"]}` → output has NO leaf string. `TestClean_RichIgnoresIslands`: article file + sidecar present → `Markdown` == `cleanFile(t,"article").Markdown` (rich-immunity, independent of goldens). `TestClean_YTPrepend`: thin youtube-shaped HTML → starts with `# ` + title. `TestClean_SpaShellUntouchedByIslands`: `cleanFile(t,"spa-shell")` word count == 7 (locks the no-op on the real fixture — corpus drift detector).

### Data Model Notes (Go)
- `map[string]any` records: `reflect.DeepEqual` against literals; ALL JSON numbers are `float64` (write `float64(42)`, never `42` — DeepEqual distinguishes).
- Slices from JSON are `[]any`; arxiv `authors` want-literal is `[]any{"A. One", "B. Two"}` — build via helper if noisy, but keep literals visible in-test.
- Sentinel errors: `errors.Is(err, vertical.ErrURLMismatch)` — never string-match except asserting the CLI message contains `does not handle`.
- Exit codes via existing `codeOf(err)` — never spawn subprocesses.
- Golden comparisons: none new in this phase; the only golden interaction is the empty-diff gate.

### Success Criteria
- `go test ./vertical/ ./scrape/ ./clean/ ./cli/ -v` exits 0 with ~50 new test funcs; suite-wide RUN count strictly exceeds the B0-measured baseline (`tee /tmp/phaseB-baseline.log` BEFORE writing — non-vacuous rule, testing.md)
- `go test ./clean/ -update` writes nothing (`git status --porcelain testdata/clean/` empty immediately after)
- `git status --porcelain testdata/vertical/` shows exactly the 15 new fixtures, each containing `future_field_xyz` (`grep -c` == 15 — one-line proof of US-5)
- Full gates: `go test ./...` && `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass
- Every deliverable from `plan/phase-B.md` §4 maps to §4 above (no orphan deliverable, no orphan test file; Phase C surface explicitly excluded)

### Expected File Structure at End
(Same tree as Test File List §4 — reproduce exactly. No `testutil` package, no new harness, 15 fixtures + 11 test files + 3 CLI additions.)
---

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -v 2>&1 | tee /tmp/phaseB-baseline.log; grep -c '^=== RUN' /tmp/phaseB-baseline.log

# Fast hermetic suite (every push — localhost httptest only, no external network/keys/browser)
go test ./...

# New-test count vs baseline (must strictly exceed; ~50 new funcs expected)
go test ./... -v 2>&1 | tee /tmp/phaseB-tests.log; grep -c '^=== RUN' /tmp/phaseB-tests.log

# Focused per task (mirrors phase-B sanity checks)
go test ./vertical/ -run 'TestList|TestLookup|TestMatch|TestGithub|TestRegistries' -v   # B1
go test ./vertical/ -run 'TestReddit|TestHackerNews|TestArxiv' -v                        # B2
go test ./vertical/ -run 'TestYouTube|TestCommerce|TestOptIn' -v                         # B3
go test ./scrape/ -run TestVertical -v                                                   # B4
go test ./clean/ -run 'TestIsland|TestPlayer|TestClean_' -v                              # B5
go test ./cli/ -run TestScrape_Vertical -v                                               # B4 flags

# Fixture-blob proof (US-5: every vertical fixture carries its unknown blob)
grep -l 'future_field_xyz' testdata/vertical/* | wc -l   # must equal fixture count (15)

# Empty-drift gates (US-4: islands hook fires nowhere in the existing corpus)
go test ./clean/ -update; git status --porcelain testdata/clean/   # must print nothing
git status --porcelain testdata/vertical/ | wc -l                  # exactly 15 (new files only)

# Full gate (mirrors phase-B exit criterion 8)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service/pipeline with pure-logic core; one new fake + one additive seam + reused helpers (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to B1–B5 behaviors (registry/dispatch/islands/tolerance/loudness)
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 13 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all 7 `vertical/*.go`, `clean/islands.go`+hook, `scrape/scrape.go`, `cli/scrape.go`, `testdata/vertical/`, `spa-shell.md` (via empty-diff gate); Phase C surface explicitly excluded with rationale
- [x] Every external/heavy dependency has a fake or mock equivalent — live hosts → `fakeVerticalFetcher`; `Run`'s fetcher → `Deps.Fetcher` seam; LLM → error-on-call constructor; UA policy → localhost recording server + real client (justified: the header under test is the client's own); SQLite → real pure-Go temp DB (Phase-1 precedent); browser/keyring untouched
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — fake + `file://` + localhost `httptest` only; CLI auto-hit on real hosts banned in §8
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation (Phase-A precedent): no integration tier exists; hermetic-only suite, browser smoke stays behind its build tag, manual smoke is explicitly not-a-test-file
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure reusing the existing `-update` flag; stated at the top of §5 per skill rule
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — adapted: §8 pastes the seam shape + panic-extractor + B0 field lists verbatim and points to §3's full fake source with import list
- [x] Run commands section is present — §9 with baseline/focused/blob-proof/drift-gate/full-gate/cross commands
