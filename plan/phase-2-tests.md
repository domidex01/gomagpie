# Phase 2 — Testing: Selector cache + concurrency + crawl

**Scope:** `core/pipeline.go`, `extract/extractor.go` (Purpose field), `selector/` (synth, validate, cache, heal), `crawl/` (crawl, frontier, bloom, robots, ratelimit, backoff), `store/sqlite.go` (12 new accessors), `cmd/magpie/` (crawl, cache_cmd, scrape + shared writers)
**Key Pattern:** Fake every network peer with `httptest.Server` (origin sites + provider endpoints) and fake the LLM with a counting `fakeExtractor` / canned `propose` closure; real pure-Go SQLite on `t.TempDir()`; no network, no browser, no keys in the default suite.
**Dependencies:** stdlib `testing`, `net/http/httptest`, `os`, `path/filepath`, `sync`, `sync/atomic` only — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an operator, I want the second crawl of the same site+schema to make 0 LLM calls, so that steady-state crawling is free | `TestCrawl_SecondRunZeroLLM` in `crawl` (two `Run()` calls against httptest site, assert `LLMCallCount` unchanged) + `TestScrape_FromCache` in `cmd` | `select count(*) from llm_calls` delta == 0 AND records still served |
| US-2 | As an operator, I want a renamed price node to re-synthesize price only while title keeps serving from cache, so that one site redesign doesn't trash the whole template | `TestHeal_BrokenFieldOnly` in `selector` (serve A → swap to B → feed 20 pages, assert fake-LLM call log) | heal fires for `price` ONLY; `title`/`ean` served with 0 `synth` calls before threshold crossing |
| US-3 | As an operator, I want SIGINT mid-crawl + `--resume` to finish without re-fetching done pages, so that large crawls survive interruption | `TestResume_DoneNeverRefetched` in `crawl` + `TestCrawl_ResumeCLI` in `cmd` (stranded `inflight` rows, compare `pages_ok` totals) | resumed `pages_ok` == uninterrupted `pages_ok`; done URLs fetched exactly once |
| US-4 | As a site owner, I want robots.txt deny (or 5xx/unreachable) to stop the crawl before any page fetch, so that the crawler is polite by default | `TestRobots_Gate` matrix in `crawl` + `TestCrawl_RobotsExit5` in `cmd` | deny/503/closed-port → 0 page fetches AND exit 5; 404 → crawl proceeds; `--ignore-robots` warns on stderr and proceeds |
| US-5 | As an operator, I want `cache inspect/clear/heal` to manage cached selectors, so that I can debug and repair without re-crawling | `TestCacheCmd_*` in `cmd` (inspect prints selectors+null rates; clear evicts; heal with <2 samples errors) | inspect output contains field names; clear → `GetSelectors` miss; heal with 1 sample → non-zero exit + "loud" stderr |

---

## 1. Component Mock Strategy

Phase type: **Service** (bounded-concurrency crawl pipeline with cache + LLM edges). Mock strategy in one sentence: **fake the LLM with a call-logging `fakeExtractor` and canned `propose` closures, fake every origin with `httptest.Server`, use the real pure-Go SQLite driver on temp files, and never launch a browser or touch a real provider in the default suite.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `store` frontier + selector accessors | No mock — real `modernc.org/sqlite` on `t.TempDir()` file via existing `openTempDB` | Enqueue 5 → re-enqueue same 5 inserts 0; Claim 2 → stats 3/2/0/0; ResetInflight re-queues; ResumeRun unknown id errors; Seen twice true only 2nd; Put+Get round-trip; reopen file → data survives | US-3 |
| `crawl/robots.go` Checker | `httptest.Server` origins with canned `/robots.txt` bodies + one closed-port URL (no mock objects) | `Disallow: /private` → false/true split; `User-agent: magpie` group denies (header-vs-token regression); `Crawl-Delay: 2` (capital D) → limiter ≤0.5/s; 503 → `ErrRobotsUnreachable`; 404 → allow; closed port → deny + sentinel | US-4 |
| `crawl/ratelimit.go` HostLimiters | No mock (pure in-memory buckets); millisecond rates in tests | Two hosts get independent buckets; `SetFloor` only lowers (never raises); `Wait` blocks ~expected duration at tiny rate | US-4 |
| `crawl/frontier.go` Canonicalize + links | No mock (pure `net/url` logic) + inline HTML strings for link extraction; temp DB for `Add` | Vector: `HTTP://EX.com:80/a?utm_x=1&b=2` ≡ `http://ex.com/a?b=2`; `gclid`/`fbclid`/`msclkid`/fragment dropped; invalid → error; page with 2 same-host + 1 external links → 2 enqueued at depth+1 | US-3 |
| `crawl/bloom.go` Filter | No mock — real `bloom.NewWithEstimates(1000000, 0.01)` in-memory | Pre-seed filter with hash NOT in DB → `Add` still enqueues (DB is truth, bloom is front); concurrent Add/Test under `-race` clean | US-3 |
| `crawl/backoff.go` classify + FetchWithRetry | Counting fake `do func() (*fetch.FetchResponse, error)` — no httptest needed; `MaxInterval: time.Millisecond` in tests | Permanent (400/401/403/404/410) → exactly 1 call; transient → exactly `MaxTries` calls; unparseable Retry-After → exponential fallback (no crash); canceled ctx → 1 call + `context.Canceled` | US-3 |
| `selector/synth.go` + `validate.go` | Canned `propose` closure (records invocation count, returns fixed map); fixture HTML ×3 variants — NO provider, NO keys | `css_hint` valid → chosen with 0 propose calls; wrong hint → falls through (stderr note); heuristic finds `#productTitle`-style selector with 0 propose calls; 2/3-agreeing field caches WITH named-field warning; 1/3 field → non-cacheable; `only=["price"]` leaves other fields untouched; 0 samples → error; `xpath_hint` → one stderr warning | US-1, US-2 |
| `selector/cache.go` Applier + `heal.go` Healer | `fakeExtractor` (scripted ground truth + purpose-tagged call log); page version A then B fixtures | Cache hit → 0 LLM calls; 20 × B pages → heal fires for `price` ONLY (`title`/`ean` uninterrupted); 2/3 fields broken → full re-synthesis; `jsonld_path` field needs no selector | US-1, US-2 |
| `core/pipeline.go` Run | Fake stage funcs (`fetchFn`/`cleanFn`/`extractFn` returning canned structs, one failing variant); in-memory `source` chan + collecting `sink` | All items flow source→sink in order; failing fetch surfaces via `g.Wait()`; closing `source` drains and closes downstream chans (no hang); `gctx` cancel stops workers promptly | US-3 |
| `crawl/crawl.go` Run | `httptest.Server` 7-page interlinked site + `fakeExtractor`; temp-file DB | 7 pages → exactly 7 `done` rows and process exits (no hang, no premature close); `--max-pages` stops claiming (pending rows remain); `FinishRun` counts come from `CrawlStats`, not memory; cold domain runs `Purpose:"synth"` then flips to cache-apply | US-1, US-3 |
| `extract/extractor.go` Purpose | Existing `fakeProvider` (httptest) — assert recorded request is unchanged AND `Log` received `"synth"` on first attempt | Empty Purpose → logs `"extract"` (backward compat); `Purpose:"synth"` → first-attempt log is `"synth"`, retry still `"repair"` | US-1 |
| `cmd/magpie` crawl/cache/scrape/shared | `file://` URLs + `GOMAGPIE_BASE_URL` fake-provider override (reuse Phase 1 pattern) + temp DB; `httptest` origin for robots/exit-code matrix | Crawl matrix: exit 0 + ≥1 valid record; exit 3 zero records; exit 4 partial; exit 5 robots (+stderr WARNING path for `--ignore-robots`); exit 6 cost ceiling with 0 new calls; `cache inspect/clear/heal` behaviors per US-5; warm-cache `scrape` prints `from_cache=true` + 0 calls; `--no-cache` forces direct LLM | US-1…US-5 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | httptest fakes, `fakeExtractor`/canned `propose`, temp-dir SQLite, inline + `testdata/` fixtures — no network, no browser, no keys | <60s total (backoff tests use ms intervals; A/B heal test ~20 fast pages) | Every push; the only default gate |
| Browser | No new browser tests this phase — existing `//go:build browser` smoke stays as-is | n/a | Separate CI job (unchanged) |
| E2E (manual, not a test file) | Built binary + `file://` fixtures + optional local Ollama; the 12 Exit Criteria command lines from `plan/phase-2.md` §5 | minutes | Pre-release sanity |

No live-provider tier by design: real OpenAI/Anthropic keys must never appear in any test. The `fakeExtractor` call log (purpose-tagged) IS the LLM contract test for this phase; the wire format stays locked by the Phase 1 `fakeProvider` tests.

---

## 3. Fake / Mock Implementations

Two new fakes. Both live in the test package that uses them. Reuse (don't reinvent): `fakeProvider` + `openAIEnvelope` (extract), `openTempDB` (store), `newFakeOrigin` pattern (fetch), `GOMAGPIE_BASE_URL` override (cmd).

### `fakeExtractor` — replaces `extract.Extractor` (selector + crawl tests)

```go
// selector/selector_test.go and crawl/crawl_test.go (small copy per package — same rule-of-three
// reasoning as Phase 1: two call sites don't justify a shared testutil package)
type llmCall struct {
    purpose string // "synth" or "extract" — what the code under test passed/used
    url     string // which page (from ExtractInput.PromptExtra or test harness)
}

type fakeExtractor struct {
    mu    sync.Mutex
    calls []llmCall
    // script maps a page key (e.g. version "A"/"B") to the ground-truth record to return.
    script map[string]map[string]any
    err    error // when non-nil, every call fails (fail-loud paths)
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
    if err := ctx.Err(); err != nil {
        return extract.ExtractResult{}, err
    }
    if f.err != nil {
        return extract.ExtractResult{}, f.err
    }
    f.mu.Lock()
    defer f.mu.Unlock()
    purpose := in.Purpose
    if purpose == "" {
        purpose = "extract"
    }
    f.calls = append(f.calls, llmCall{purpose: purpose, url: in.PromptExtra})
    rec := f.script[in.PromptExtra]
    if rec == nil {
        rec = f.script["default"]
    }
    raw, _ := json.Marshal(rec)
    return extract.ExtractResult{Record: rec, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) count(purpose string) int {
    f.mu.Lock()
    defer f.mu.Unlock()
    n := 0
    for _, c := range f.calls {
        if c.purpose == purpose {
            n++
        }
    }
    return n
}

func (f *fakeExtractor) total() int { return f.count("synth") + f.count("extract") }
```

**Matches real call:** production extract workers call `ex.Extract(ctx, extract.ExtractInput{Markdown, StructuredData, Schema, Purpose, PromptExtra})` on the `extract.Extractor` interface (same interface the Phase 1 OpenAI/Anthropic adapters implement). Tests inject `*fakeExtractor` wherever the code accepts the interface; purpose-tagged counts prove "0 LLM calls" (US-1) and "synth only after threshold" (US-2) without asserting on timing. NOTE: this fake assumes `ExtractInput` gains `Purpose string` (Task 2.6's 3-line change) — if `Purpose` doesn't exist yet, the test file doesn't compile, which is the correct forcing function.

### Canned `propose` closure — replaces the LLM-proposal step inside `Synthesize`

```go
// selector/selector_test.go
func cannedPropose(t *testing.T, canned map[string]string, calls *int) func(ctx context.Context, fields []string, trimmedHTML string) (map[string]string, error) {
    t.Helper()
    return func(ctx context.Context, fields []string, trimmedHTML string) (map[string]string, error) {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        *calls++
        out := map[string]string{}
        for _, f := range fields {
            s, ok := canned[f]
            if !ok {
                return nil, fmt.Errorf("canned propose: no selector for field %q", f)
            }
            out[f] = s
        }
        return out, nil
    }
}
```

**Matches real call:** production wraps the ad-hoc-schema adapter (`ParseSchema` on a generated one-string-property-per-field schema, trimmed HTML ≤ `clean.MaxTokens`, `Purpose:"synth"`). Tests assert `calls == 0` when `css_hint`/heuristic wins (free paths first) and `calls == 1` only for the fields the heuristic missed. The closure errors loudly on unscripted fields so a test that unexpectedly reaches the LLM path fails with a field name, not a nil-map panic.

### Counting `do` func — replaces the network inside `FetchWithRetry` (no httptest needed)

```go
// crawl/crawl_test.go
func countingDo(calls *int, script []struct {
    resp *fetch.FetchResponse
    err  error
}) func() (*fetch.FetchResponse, error) {
    return func() (*fetch.FetchResponse, error) {
        idx := *calls
        *calls++
        if idx >= len(script) {
            idx = len(script) - 1
        }
        return script[idx].resp, script[idx].err
    }
}
```

Script entries: `{nil, permanentErr}` → assert exactly 1 call; `{transient, transient, success}` with `MaxInterval: time.Millisecond` + `MaxTries: 3` → assert exactly 3 calls and the success value. `FetchResponse` values can be minimal structs (only the status/body the classifier reads) — never real HTTP.

### `fakeOrigin` site builders — replace crawled websites (crawl + cmd tests)

```go
// crawl/crawl_test.go (cmd/magpie mirrors the robots subset for exit-code tests)
func newRobotsOrigin(t *testing.T, robotsBody string) *httptest.Server // serves robotsBody at /robots.txt, trivial page at /
func newSiteOrigin(t *testing.T, pages map[string]string, robots string) *httptest.Server
// pages: path → HTML; every page links to the next (7-page chain + cross-links for the
// termination test); version flag swaps /item body between variant A and B for the heal test.
```

Routes needed: `/robots.txt` variants (allow-all, `Disallow: /private`, `User-agent: magpie` deny, `Crawl-Delay: 2`, HTTP 503, HTTP 404 via handler `WriteHeader`), `/private/x`, `/public`, 7 interlinked pages (`/0`…`/6`), `/item` (A/B variants), `/sitemap.xml` target for `Sitemaps()` seeding. Closed-port case needs no server: `http://127.0.0.1:<closedPort>/` where the port comes from opening then closing a listener.

---

## 4. Test File List

```
gomagpie/
├── core/
│   └── pipeline_test.go     # NEW: fake stage funcs — ordering, drain-on-close, error propagation, ctx-cancel promptness (this phase's only new pure-logic unit)
├── extract/
│   └── extract_test.go      # EXTEND: Purpose propagation (empty→"extract", "synth" first attempt, "repair" on retry) via existing fakeProvider + Log capture
├── selector/
│   └── selector_test.go     # synth (css_hint-first/fallthrough, heuristic 0-propose win, canned-propose path, only filter, 0/1-sample edges, xpath warning) + validate (3/3, 2/3+warning, 1/3 non-cacheable) + A/B heal isolation + full-redesign trigger
├── crawl/
│   └── crawl_test.go        # robots matrix (incl. token-vs-header regression, 503/404/closed-port, capital-D Crawl-Delay) + canonical vectors + dedup truth (bloom FP path) + link extraction + classify/retry counts + 7-page termination + resume round-trip
├── store/
│   └── sqlite_test.go       # EXTEND in place: selector round-trip + DeleteSelectors hash-scoping; Enqueue/Claim/MarkDone/MarkError/CrawlStats vector; ResumeRun unknown/finished; ResetInflight; Seen twice; reopen-file persistence
├── cmd/magpie/
│   └── cmd_test.go          # NEW: crawl e2e matrix (exits 0/3/4/5/6, from_cache + --no-cache, --ignore-robots warning) + cache inspect/clear/heal + heal-too-few-samples loud error
├── testdata/
│   ├── selector/product-A.html, product-B.html, product-C.html   # 3 variants (B renames price node); expected SelectorDoc JSON
│   └── crawl/robots-allow.txt, robots-deny.txt, robots-delay.txt # robots fixtures + canonical-URL vectors (Go table, not a file) + spa-vs-static link pages
└── plan/phase-2-tests.md    # this file
```

Every deliverable in `plan/phase-2.md` §4 has a test file: `core/pipeline.go` → `pipeline_test.go` (+ covered again via crawl e2e); `extract/extractor.go` delta → `extract_test.go` additions; `selector/*.go` → `selector_test.go`; `crawl/*.go` → `crawl_test.go`; `store/sqlite.go` delta → `sqlite_test.go` additions; `cmd` +2 new / +2 extended → `cmd_test.go`; `testdata/selector|crawl` → fixtures asserted by the above; `go.mod` pins → cross-compile gate is a manual CI command (§9), not a Go test (same as Phase 1).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): fixtures are per-package test helpers, hermetic gating is "default suite never touches network/browser/keys" (no flag needed — everything new this phase is hermetic by construction), and golden regeneration needs no flag (selector fixtures are hand-written variants, not generated goldens). Additions only to existing files; two new test files.

```go
// core/pipeline_test.go — NEW, self-contained
func cannedStages(failFetch bool) (fetchFn, cleanFn, extractFn func(...)...) // scripted stage funcs + call counters
func drainCollect(...) // collecting sink

// extract/extract_test.go — ADD (reuse newFakeProvider, openAIEnvelope, mustLoadSchema)
func TestExtractPurposeSynth(t *testing.T)   // Purpose:"synth" → Log sees "synth" first, "repair" on retry
func TestExtractPurposeDefault(t *testing.T) // empty Purpose → Log sees "extract" (backward compat)

// selector/selector_test.go — NEW (fakeExtractor + cannedPropose per §3)
func loadSelectorFixture(t *testing.T, name string) string // os.ReadFile ../testdata/selector/<name>
func mustParseTestSchema(t *testing.T) *extract.Schema    // ParseSchema on inline price/title/ean schema

// crawl/crawl_test.go — NEW (countingDo + newRobotsOrigin/newSiteOrigin per §3; reuse openTempDB pattern)
func closedPort(t *testing.T) string // open-then-close listener → guaranteed-refused 127.0.0.1:port

// store/sqlite_test.go — ADD (reuse openTempDB)
func TestSelectorRoundTrip / TestFrontierVector / TestResumeRun / TestSeenTwice / TestReopenPersists

// cmd/magpie/cmd_test.go — NEW (mirror minimal fakeProvider copy per Phase 1 rule-of-three;
// t.Setenv("GOMAGPIE_BASE_URL", srv.URL); file:// URLs to testdata fixtures; temp DB via cobra flag)
```

Scope rationale: everything is function-scoped (`t.TempDir()`, per-test `httptest.Server`, per-test fakes) — no shared state, no `TestMain`, safe for `go test -race`. Nothing is worth package-level scope at this size.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Fake the `Extractor` interface, not the provider HTTP, for selector/crawl tests | Call-logging `fakeExtractor` with purpose tags; keep Phase 1 `fakeProvider` for the one `Purpose`-plumbing test in `extract` | This phase's risk is orchestration (when do we call the LLM, with what purpose), not the wire format — Phase 1 already locks the wire; purpose counts prove US-1/US-2 deterministically |
| Real SQLite in tests, extended not mocked | Temp-file DB via `openTempDB`; reopen-file persistence test | `Claim`'s `IN (SELECT … LIMIT ?) RETURNING` form and `SetMaxOpenConns(1)` row-draining discipline are exactly what must be real — a mock would hide the deadlock the phase plan warns about |
| `core/pipeline_test.go` as a new file (beyond the phase plan's minimal list) | Fake stage funcs, <1s, asserts ordering/drain/error/cancel | Pipeline close-order and errgroup-cancel bugs are unlocalizable through crawl e2e alone; one small unit file pays for itself the first time a stage hangs |
| Retry timing tested with millisecond backoff, counts not durations | `MaxInterval: time.Millisecond`, assert exact call counts (1 vs MaxTries) | Durations are flaky in CI; the taxonomy contract is "how many attempts", which is deterministic |
| Heal test feeds 20 synthetic pages, not 50 | Window is 50 but the trigger is a rate (0.30) — 20 consecutive nulls trips it the same way | 50 pages × real goquery parses is still fast, but 20 proves per-field isolation with less log noise; the 50-ring boundary itself gets one direct `Healer.Observe` unit test with 50 crafted outcomes |
| Canonicalization vectors as a Go table, not fixture files | `map[input]want` incl. the `HTTP://EX.com:80` case from the plan | One-line cases; files would add indirection for zero benefit |
| Termination test uses a 7-page interlinked httptest site | Assert exactly 7 `done` rows + `Run` returns (with a test timeout) | Directly proves Exit Criterion 11 (no hang, no premature close) — the outstanding-counter + pump-close logic is the subtlest new concurrency in the phase |
| Resume test strands rows in `inflight` deliberately | Claim N, never mark, then `ResetInflight` + resume; assert no double-fetch via origin hit counts | Reproduces the SIGKILL path (not just graceful SIGINT) — the hole the validation pass found |
| Robots 5xx/closed-port deny asserted as behavior, not message text | Assert `Allowed==false` + `errors.Is(err, ErrRobotsUnreachable)` + 0 page fetches + exit 5 | Message wording may evolve; the RFC 9309 semantics (deny + sentinel + exit code) are the contract |
| Cross-compile + vet + gofmt + golangci-lint are CI commands, not Go tests | §9 gate lines (same as Phase 1) | A Go test cannot change GOOS; the Task 2.1 sanity-check line is the test |
| Duplicate the ~40-line `fakeExtractor` in `selector` and `crawl` | Copy, don't create a `testutil` package | Same rule-of-three call as Phase 1's `fakeProvider`: two call sites don't justify a shared package |

---

## 7. Example Test Case

```go
// selector/selector_test.go
package selector_test

import (
    "context"
    "strings"
    "testing"

    "gomagpie/extract"
    "gomagpie/selector"
)

func TestHeal_BrokenFieldOnly(t *testing.T) {
    sch := mustParseTestSchema(t) // price/title/ean, price has css_hint-free heuristic path
    pageA := loadSelectorFixture(t, "product-A.html") // all selectors valid
    pageB := loadSelectorFixture(t, "product-B.html") // price node id renamed, title/ean intact

    fx := &fakeExtractor{script: map[string]map[string]any{
        "default": {"price": 12.99, "title": "Widget", "ean": "4001234567890"},
    }}
    // Cold start: retain 3 validated A-samples, synthesize, cache.
    h := selector.NewHealer(50, 0.30, 3)
    for i := 0; i < 3; i++ {
        res, err := fx.Extract(context.Background(), extract.ExtractInput{
            Markdown: "Widget 12.99", Schema: sch, Purpose: "synth", PromptExtra: "default",
        })
        if err != nil {
            t.Fatal(err)
        }
        h.Retain(selector.SynthSample{URL: "http://ex.com/a", HTML: pageA, Truth: res.Record})
    }
    var proposeCalls int
    doc, err := selector.Synthesize(context.Background(), h.Samples(), sch,
        cannedPropose(t, map[string]string{"price": "#price"}, &proposeCalls), nil)
    if err != nil {
        t.Fatalf("Synthesize: %v", err)
    }
    applier := selector.NewApplier(doc, sch)

    // Steady state on A: 0 LLM calls.
    before := fx.total()
    if _, nulls := applier.Apply(pageA, nil); len(nulls) != 0 {
        t.Fatalf("Apply(A) nulls = %v, want none", nulls)
    }
    if got := fx.total() - before; got != 0 {
        t.Fatalf("Apply(A) made %d LLM calls, want 0", got)
    }

    // Swap to B: price nulls, title/ean keep serving. Feed 20 B-pages past the 0.30 trigger.
    var healed []string
    for i := 0; i < 20; i++ {
        _, nulls := applier.Apply(pageB, nil)
        for _, f := range nulls {
            if trig := h.Observe(f, true); len(trig) > 0 {
                healed = append(healed, trig...)
            }
        }
        for _, f := range []string{"title", "ean"} {
            h.Observe(f, false)
        }
    }
    if len(healed) == 0 {
        t.Fatal("heal never triggered after 20 broken-price pages")
    }
    if healed[0] != "price" {
        t.Fatalf("first healed field = %q, want price-only (title/ean uninterrupted)", healed[0])
    }
    // No synth-purpose LLM before the crossing: all calls so far are the 3 cold-start synth calls.
    if got := fx.count("synth"); got != 3 {
        t.Errorf("synth calls = %d, want exactly 3 (none before threshold crossing)", got)
    }
}

func TestSynth_CSSHintWinsWithZeroPropose(t *testing.T) {
    sch := mustParseHintedSchema(t) // price carries x-gomagpie css_hint="#price" (valid in fixture)
    samples := threeSamples(t, "product-A.html")
    var proposeCalls int
    doc, err := selector.Synthesize(context.Background(), samples, sch,
        cannedPropose(t, map[string]string{}, &proposeCalls), nil)
    if err != nil {
        t.Fatalf("Synthesize: %v", err)
    }
    if proposeCalls != 0 {
        t.Errorf("propose calls = %d, want 0 (valid css_hint costs zero work)", proposeCalls)
    }
    if doc.Fields["price"].Selector != "#price" {
        t.Errorf("price selector = %q, want #price from css_hint", doc.Fields["price"].Selector)
    }
}

func TestValidate_TwoOfThreeCachesWithWarning(t *testing.T) {
    // Captures stderr; asserts the 2/3 field caches AND names the field in the warning.
    // (Stderr capture via os.Pipe around selector.Validate; details in execution prompt.)
    _ = strings.TrimSpace // placeholder to keep imports honest in this sketch
}
```

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase 2 of `gomagpie` — selector cache + concurrency + crawl. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-2.md` (the implementation plan — all 8 tasks, confirmed library APIs, exact pins), `plan/phase-1-tests.md` (established fake patterns to reuse), `spec.md` §§4–5, 9–11 (behavioral source of truth), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test.

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`, module `gomagpie`): Phase 1 built the single-URL fetch→clean→extract pipeline; Phase 2 adds the cost-saving loop (synthesize CSS selectors once from N=3 samples, cache by domain+schema_hash, serve with 0 LLM calls, per-field self-heal at 0.30 null rate) and bounded-concurrency crawling (errgroup stages, per-host rate limits, backoff retries, bloom+SQLite dedup, robots enforcement, checkpoint/resume). Pure-Go, zero CGO. Tests are hermetic: `go test ./...` must pass with no network, no browser, no API keys.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | Second crawl of same site+schema makes 0 LLM calls | TestCrawl_SecondRunZeroLLM + TestScrape_FromCache | llm_calls delta == 0, records still served |
| US-2 | Renamed price node re-synthesizes price only | TestHeal_BrokenFieldOnly (20 B-pages) | heal fires for price ONLY; synth calls == 3 pre-crossing |
| US-3 | SIGINT + --resume finishes without re-fetching done pages | TestResume_DoneNeverRefetched + TestCrawl_ResumeCLI | resumed pages_ok == uninterrupted pages_ok; done URLs fetched once |
| US-4 | Robots deny / 5xx / unreachable stops crawl pre-fetch | TestRobots_Gate + TestCrawl_RobotsExit5 | 0 fetches + exit 5; 404 proceeds; --ignore-robots warns + proceeds |
| US-5 | cache inspect/clear/heal manage selectors | TestCacheCmd_* | inspect shows fields; clear evicts; heal <2 samples errors loudly |

### Why Fakes Are Required
- LLM extraction: real calls need keys, billing, network — and this phase must prove ZERO calls in steady state. Fake with `fakeExtractor` (§Critical) that logs purpose-tagged calls; assert counts, never timing.
- LLM proposal inside synthesis: must prove the free paths (css_hint, heuristic) cost zero work. Fake with the canned `propose` closure (§Critical); assert invocation counts.
- Crawled websites: robots bodies, redirect/link graphs, and A/B selector-break variants must be deterministic. Fake with `httptest.Server` site builders (§Critical).
- Retry backoff: real 500ms–30s intervals would make the suite minutes long and flaky. Fake the `do` func with a counting closure and millisecond `MaxInterval`; assert exact attempt counts.
- Chrome: absent on CI/WSL — no new browser tests; existing `//go:build browser` smoke stays untouched.
- SQLite is NOT faked: `modernc.org/sqlite` is pure Go and millisecond-fast — test the real driver on `t.TempDir()` files (the `Claim … IN (SELECT … LIMIT ?) RETURNING` form and single-conn row-draining discipline must be real).
- OS keyring is NOT touched (same as Phase 1).

### What NOT to Test
- Don't test backoff/v5, bloom/v3, grobotstxt, errgroup, or rate internals — test OUR taxonomy, OUR wrapper mutex, OUR token-vs-header fix, OUR stage wiring.
- Don't test goquery selector matching beyond our candidate emission (no `#id` vs `.class` engine tests) — fixtures prove end-to-end synthesis.
- Don't test Cobra flag parsing or `net/http` mechanics beyond our policies (robots mapping, 10-cap redirects stay Phase 1's).
- Don't test live providers, real websites, or real Chrome in the default suite.
- Don't create a shared `testutil` package — copy `fakeExtractor` (~40 lines) into `selector` and `crawl`; copy minimal `fakeProvider` into `cmd/magpie` (Phase 1 rule-of-three).
- Don't build any registry/module/plugin scaffolding or its tests — that is Phase 3.
- Don't test `json`-format RAM buffering limits or robots `Expires` handling — both are documented `ponytail:` ceilings, not behaviors.

### Critical: Fake Implementations

Copy these verbatim into the owning test files (`fakeExtractor` full copy in BOTH `selector/selector_test.go` and `crawl/crawl_test.go`; `cannedPropose` in `selector`; `countingDo` + site builders in `crawl`; minimal `fakeProvider` mirror in `cmd/magpie/cmd_test.go` per Phase 1 §3):

```go
type llmCall struct {
    purpose string
    url     string
}

type fakeExtractor struct {
    mu     sync.Mutex
    calls  []llmCall
    script map[string]map[string]any
    err    error
}

func (f *fakeExtractor) Name() string { return "fake" }

func (f *fakeExtractor) Extract(ctx context.Context, in extract.ExtractInput) (extract.ExtractResult, error) {
    if err := ctx.Err(); err != nil {
        return extract.ExtractResult{}, err
    }
    if f.err != nil {
        return extract.ExtractResult{}, f.err
    }
    f.mu.Lock()
    defer f.mu.Unlock()
    purpose := in.Purpose
    if purpose == "" {
        purpose = "extract"
    }
    f.calls = append(f.calls, llmCall{purpose: purpose, url: in.PromptExtra})
    rec := f.script[in.PromptExtra]
    if rec == nil {
        rec = f.script["default"]
    }
    raw, _ := json.Marshal(rec)
    return extract.ExtractResult{Record: rec, Raw: raw, Provider: "fake", Model: "fake", Attempts: 1}, nil
}

func (f *fakeExtractor) count(purpose string) int {
    f.mu.Lock()
    defer f.mu.Unlock()
    n := 0
    for _, c := range f.calls {
        if c.purpose == purpose {
            n++
        }
    }
    return n
}

func (f *fakeExtractor) total() int { return f.count("synth") + f.count("extract") }
```

```go
func cannedPropose(t *testing.T, canned map[string]string, calls *int) func(ctx context.Context, fields []string, trimmedHTML string) (map[string]string, error) {
    t.Helper()
    return func(ctx context.Context, fields []string, trimmedHTML string) (map[string]string, error) {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        *calls++
        out := map[string]string{}
        for _, f := range fields {
            s, ok := canned[f]
            if !ok {
                return nil, fmt.Errorf("canned propose: no selector for field %q", f)
            }
            out[f] = s
        }
        return out, nil
    }
}
```

Site builders (crawl): `newRobotsOrigin(t, robotsBody)`, `newSiteOrigin(t, pages, robots)` per §3 of this plan; `closedPort(t)` via open-then-close listener. Cmd tests reuse the Phase 1 `GOMAGPIE_BASE_URL` + `file://` pattern — read `cmd/magpie/scrape_test.go` first and mirror it.

### Test Files to Create

```
core/pipeline_test.go     # NEW: fake stage funcs — ordering, drain-on-close, error propagation, ctx-cancel
extract/extract_test.go   # ADD: TestExtractPurposeSynth + TestExtractPurposeDefault
selector/selector_test.go # NEW: synth/validate/heal incl. TestHeal_BrokenFieldOnly + TestSynth_CSSHintWinsWithZeroPropose
crawl/crawl_test.go       # NEW: robots/canonical/dedup/links/retry/termination/resume matrix
store/sqlite_test.go      # ADD: selector round-trip, frontier vector, ResumeRun, ResetInflight, Seen, reopen persistence
cmd/magpie/cmd_test.go    # NEW: crawl exits 0/3/4/5/6, from_cache/--no-cache, --ignore-robots warning, cache cmds
testdata/selector/product-{A,B,C}.html + expected SelectorDoc JSON
testdata/crawl/robots-{allow,deny,delay}.txt
```

### Per-File Coverage Guidance

#### core/pipeline_test.go
Three scripted stage funcs (fetch returns `FetchedPage`, clean appends links, extract returns `PageResult`); feed 5 tasks, assert sink got all 5 in order. Failing `fetchFn` on task 3 → `Run` returns non-nil error (first-error-wins). Close `source` → all stages' output chans close and `Run` returns (wrap in `time.AfterFunc` + `t.Fatal` on hang — a hung pipeline must fail, not block CI). Cancel ctx mid-run → workers return promptly (assert `Run` returns within 2s). Browser semaphore is NOT unit-tested here (needs fetch internals) — covered by the crawl e2e never launching rod on static fixtures.

#### extract/extract_test.go (additions only — do not modify existing tests)
`TestExtractPurposeSynth`: fakeProvider script with one valid doc; `Extract(ctx, ExtractInput{…, Purpose:"synth"})` → capture the `Log` func's purpose args (construct the adapter, then read `llm_calls` purposes from a temp DB OR intercept via a wrapper — simplest: run against temp DB and `SELECT DISTINCT purpose`); assert first-attempt purpose is `"synth"`. Then an invalid→valid script with `Purpose:"synth"` → purposes are `["synth","repair"]`. `TestExtractPurposeDefault`: empty Purpose → `["extract"]` (and `["extract","repair"]` on retry). This is the forcing function for the 3-line `ExtractInput.Purpose` change.

#### selector/selector_test.go
Fixtures first: `product-A.html` (price `#price`, title `#productTitle`, ean in JSON-LD + visible `.ean`), `product-B.html` (price id renamed to `#cost-now`, rest identical), `product-C.html` (different layout, same values — 3rd agreement sample). Schema: inline YAML price/title/ean with one `required: [price, title]`, price WITHOUT css_hint for heuristic tests, plus a hinted variant WITH `css_hint: "#price"` for the zero-propose test. Tests: (1) heuristic finds price+title with 0 propose calls on A×3; (2) hinted schema → exact hint chosen, 0 calls; (3) WRONG hint (`#nope`) → falls through to heuristic + stderr note (capture os.Pipe); (4) canned-propose path for a field the heuristic can't find (e.g. price as bare text node with no hook) → 1 call, only that field requested; (5) `only=["price"]` → other fields' selectors byte-identical before/after; (6) 0 samples → error containing "no samples"; 1 sample → success + stderr "warn"; (7) 2/3-agreeing field caches + warning names it; 1/3 field → absent from doc (non-cacheable); (8) `xpath_hint` in schema → one stderr warning, synthesis otherwise unaffected; (9) `TestHeal_BrokenFieldOnly` per §7 (the US-2 proof); (10) full-redesign: break 2/3 fields → whole-template re-synthesis (assert propose called for all fields); (11) `Healer.Observe` unit: 50 crafted outcomes at exactly 15/50 nulls → no trigger, 16th null in window → trigger (boundary of 0.30).

#### crawl/crawl_test.go
Robots matrix (each: httptest origin, `Checker.Allowed` + crawl-delay effect): allow-all → true; `Disallow: /private` → false/true split; `User-agent: magpie` deny → false (THE header-vs-token regression — comment it as such); `Crawl-Delay: 2` capital-D → `Limiter.Limit() <= 0.5`; body 503 → false + `errors.Is(err, ErrRobotsUnreachable)`; 404 → true; closed-port → false + sentinel. Sitemaps: body with `Sitemap: /sitemap.xml` → `Sitemaps()` returns it (frontier seeding covered in crawl e2e). Canonical table (8+ rows incl. `HTTP://EX.com:80/a?utm_x=1&b=2` → `http://ex.com/a?b=2`, default-port strip, query sort, utm/gclid/fbclid/msclkid/fragment drops, invalid → error). Dedup: temp DB + real bloom — add 3, re-add → 0 new; pre-seed bloom with foreign hash → `Add` still enqueues (DB-is-truth path). Links: raw HTML with 2 same-host + 1 external + 1 over-maxDepth → 2 enqueued at depth+1. Retry: permanent set {400,401,403,404,410} → 1 call each (table); transient 500 → exactly MaxTries; Retry-After(1) → waits ~1s then succeeds (only ONE such test — it's the slow one, keep the rest at ms scale); unparseable Retry-After → success within MaxTries (fallback, no hang); canceled ctx → 1 call + `context.Canceled`. Termination: 7-page interlinked site + fakeExtractor → `Run` returns, `CrawlStats` == 0/0/7/0. Resume: claim 3 without marking (stranded inflight), `ResetInflight` → 3, resume `Run` → done total == uninterrupted total, origin hit counter shows done URLs fetched exactly once. Second-run-zero-LLM: two `Run` calls same site+schema → `LLMCallCount` delta == 0 (US-1 proof at crawl level). Run everything with `-race` in mind: shared counters via `sync/atomic` or the fake's mutex.

#### store/sqlite_test.go (additions only)
`TestSelectorRoundTrip`: Put → Get (fields_json byte-identical) → DeleteSelectors(domain, hash) → Get miss; Put twice same key → update path (samples_used changes); `DeleteSelectors(domain, "")` clears all for domain, other domain untouched (rows-evicted counts asserted). `TestFrontierVector`: the exact plan vector — enqueue 5 (inserted==5) → re-enqueue (0) → claim 2 → stats 3/2/0/0 → ResetInflight==2 → stats 5/0/0/0 → claim 2 → MarkDone/MarkError → recount 3/0/1/1. `TestResumeRun`: unknown id → error; finished run → status back to running (`SELECT status`). `TestSeenTwice`: false then true. `TestReopenPersists`: close + `Open` same path → Get hits, stats intact. NEVER hold `*sql.Rows` across calls in test helpers (same single-conn discipline as production).

#### cmd/magpie/cmd_test.go
Mirror `scrape_test.go`'s harness (fakeProvider copy + `GOMAGPIE_BASE_URL` + temp `--db` flag or env). Matrix: (a) `crawl file://<site-dir>` → exit 0, jsonl has ≥1 valid record per page; (b) repeat → exit 0, `llm_calls` unchanged (US-1 CLI proof); (c) zero-record site (all fetches 404) → exit 3; (d) partial (one good page + one 500 page) → exit 4; (e) robots-deny origin → exit 5 + 0 page fetches; robots-503 origin → exit 5; (f) `--ignore-robots` on deny origin → exit 0 + `WARNING: --ignore-robots` on stderr; (g) `--max-cost 0.000001` → exit 6 + 0 new provider hits; (h) `cache inspect --domain` prints field names + null rates; `cache clear --domain` → re-inspect misses; (i) `cache heal` with 1-sample seed → non-zero exit + loud stderr; (j) warm-cache `scrape --schema` → stdout has `from_cache=true` + 0 calls; `--no-cache` → ≥1 call. Assert exit codes via the cobra error-to-code helper (no subprocesses), same as Phase 1.

### Data Model Notes
- Plain structs with json tags: assert fields directly (`doc.Fields["price"].Selector`, `stats.done`), never via reflection.
- `ClaimedURL{URL, URLHash, Depth}`: assert all three (depth proves the +1 link logic).
- `CrawlStats` pending/inflight/done/errors order: assert the full 4-tuple every time (a 3/2/0/0 vs 3/0/2/0 mixup is exactly the bug this catches).
- Exit codes asserted via the cobra `Execute()` error-to-code mapping helper, not subprocesses.
- Stderr assertions (css_hint fallthrough, 2/3 warning, xpath warning, --ignore-robots): capture via `os.Pipe` swap, restore with cleanup; assert `strings.Contains`, not exact equality (log prefixes evolve).
- CSV assertions: header order = required[] then remaining sorted (exact string match on the header line); nested value → error containing the field name.

### Success Criteria
- `go test ./...` exits 0 with >0 tests in `selector` AND `crawl` AND `core` (non-vacuous: `go test ./... -v 2>&1 | grep -c '^=== RUN'` grows by ≥40 vs Phase 1)
- `go test -race ./crawl/ ./selector/ ./core/` exits 0 (new concurrency must be race-clean)
- `go test ./...` passes with NO network, NO browser, NO keys (verify by unsetting all `GOMAGPIE_*` except test overrides; no `sleep`-based timing except the single Retry-After(1) test)
- `go vet ./...` exits 0; `gofmt -l .` prints nothing
- Every deliverable from `plan/phase-2.md` §4 has at least one test file (see §4 list above)

### Expected File Structure at End
(Same tree as Test File List §4 — `core/pipeline_test.go` + `cmd/magpie/cmd_test.go` new, `extract_test.go` + `sqlite_test.go` extended, `selector/selector_test.go` + `crawl/crawl_test.go` new, `testdata/selector/` + `testdata/crawl/` fixtures. No `testutil` package, no extra harnesses.)
---

---

## 9. Run Commands

```bash
# Fast hermetic suite (every push — no network, no browser, no keys)
go test ./...

# Verbose with test counts (non-vacuous check: must grow by >=40 RUN lines vs Phase 1)
go test ./... -v 2>&1 | tee /tmp/phase2-tests.log; grep -c '^=== RUN' /tmp/phase2-tests.log

# Focused: the three proofs that carry the phase
go test ./selector/ -run 'TestHeal_BrokenFieldOnly|TestSynth|TestValidate' -v
go test ./crawl/ -run 'TestRobots|TestCanonical|TestDedup|TestLinks|TestClassify|TestRetry|TestResume|TestTermination' -v
go test ./core/ -v

# Race detector over the new concurrency (must be clean)
go test -race ./crawl/ ./selector/ ./core/ -v

# Store + extract deltas
go test ./store/ ./extract/ -v

# Cmd matrix (exits 0/3/4/5/6, cache cmds, from_cache)
go test ./cmd/... -run 'TestCrawl|TestCache|TestScrape' -v

# Full gate (mirrors Exit Criteria)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
# golangci-lint run ./...  # CI or local install (not on this WSL box per phase plan §1 item 8)
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service; `fakeExtractor`/canned-`propose` + httptest origins + real temp-file SQLite (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to Milestone-2 behaviors (cheap steady state, heal isolation, resume, robots, cache ops)
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 12 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all §4 deliverables (`core`→`pipeline_test.go`, `extract` delta→`extract_test.go`, `selector/*`→`selector_test.go`, `crawl/*`→`crawl_test.go`, `store` delta→`sqlite_test.go`, `cmd`→`cmd_test.go`, `testdata` fixtures, `go.mod` via cross-build command)
- [x] Every external/heavy dependency has a fake or mock equivalent — LLM→`fakeExtractor`/canned-`propose`, origins→httptest builders, retry-net→counting `do`, Chrome→no new tests (existing tag untouched), SQLite→real pure-Go temp DB (justified, §6), keyring→env path (Phase 1, unchanged)
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — `GOMAGPIE_BASE_URL` override + `file://` transport + httptest only; "What NOT to Test" bans live providers
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation (same as Phase 1): no live-provider tier exists by design; the only non-hermetic test remains the `browser` build-tag smoke, untouched
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure with scope rationale, same as `phase-1-tests.md` §5
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — full `fakeExtractor` + `cannedPropose` Go source pasted verbatim in §8
- [x] Run commands section is present — §9 with fast/focused/race/golden-equivalent/full-gate commands
