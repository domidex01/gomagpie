# Phase C — Testing: Agent Surface (Tools + Commands)

**Scope:** `extract/prompt.go` (new: `Prompter` + 3 adapter impls), `scrape/` (new: `summarize.go`, `diff.go`, `brand.go`), `clean/brand.go` (new: pure `Brand`), `crawl/sitemap.go` (new: `ListSitemapURLs`), `mcp/` (`agent.go` 7 tools, `coerce.go` widen+flex, `server.go` registration), `cli/` (6 new commands + `root.go`/`shared.go` edits), `testdata/brand/` (new goldens)
**Key Pattern:** Fake `Fetcher` (substring→bytes, third rule-of-three copy) + fake `Prompter`-implementing extractor for all LLM-text paths; real scripted `httptest` providers only inside `extract/` (adapter-level) and `cli/` (existing `newFakeProvider`); in-memory MCP server (`dialInMemory`) for all 7 tools; pure tables for diff/coercion-units; existing helpers reused everywhere (`fakeExtractor`, `openScrapeDB`, `testSchema`, `codeOf`, `testEnv`, `goldenDir`, `stubCodex`).
**Dependencies:** stdlib `testing`, `net/http/httptest`, `compress/gzip` (in-test generation only), `encoding/json`, `strings`, `sync/atomic`, `os`, `path/filepath` only — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an agent dev, I want to scrape ≤100 URLs in one call with bounded concurrency and per-URL ok/error records, so that one bad page never fails the batch | `scrape` batch fan-out test (max-inflight counter + order + mixed good/bad) + MCP `batch` via `dialInMemory` + CLI `magpie batch` exit 0 | inflight ≤ limit; results in input order; bad URL → `ok:false` record, exit still 0; `fx.total()==0` (zero LLM — markdown-only) |
| US-2 | As an agent dev, I want sitemap URL listing with gzip + caps, so that `map` works on real (large) sites without a 50k-fetch footgun | `crawl/sitemap_test.go` (urlset/index/gzip/cap tables) + MCP `map` + CLI `magpie map` lines/JSON | exact `<loc>` sets; `.xml.gz` decoded; 101-child index → `truncated:true`, ≤100 followed; 10,001-URL set → `truncated:true`, ≤10k returned |
| US-3 | As a user, I want prompt-mode extract + summarize to return text with no schema and a hard sentence cap, so that free-text LLM tasks don't touch the validator | `extract/prompt_test.go` (3 adapters × scripted providers + Codex stub) + `scrape/summarize_test.go` (rambling-fake truncation, cap, ceiling, Purpose) + CLI `--prompt` xor tests | text returned, `Log`/purpose recorded, validator never consulted; rambling 10-sentence fake → ≤N sentences; `--prompt`+`--schema` → exit 2 |
| US-4 | As a stringy MCP client, I want `"3"`/`"true"`/string-urls to coerce and the 11-tool catalog to stay stable, so that Claude Desktop stops erroring and renames get caught | `TestToolCatalog` (exact 11-name set) + widening-unchanged test + in-memory string-arg tool calls (`batch`, `summarize`, `map`) | catalog DeepEqual; `required`+descriptions byte-identical pre/post widen; `"concurrency":"2"`, `"max_sentences":"2"`, single-string `urls`, JSON-string `schema` all succeed |
| US-5 | As a keyless agent, I want diff/brand/vertical/list_extractors to work with zero LLM calls and loud failures, so that every blocked page is a typed error, not empty markdown | `scrape/diff_test.go` + `clean/brand_test.go` + `scrape/brand_test.go` + MCP `diff`/`brand`/`vertical_scrape`/`list_extractors` + CLI mirrors incl. exit codes | diff pairs correct, >~2k window errors; brand goldens match, favicon absolute; mismatch → `ErrURLMismatch`/exit 2; all with `fx.total()==0` and no key |

---

## 1. Component Mock Strategy

Phase type: **Service/pipeline with a pure-logic core** (Phase-A/B precedent). Mock strategy in one sentence: **fake the `Fetcher` seam and the new `Prompter` seam everywhere above the adapters; test the real adapters against scripted `httptest` providers inside `extract/` only; drive all 7 MCP tools through the existing in-memory server harness; pure tables for diff, widening, and provider ordering.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `extract.Prompter` × 3 adapters | Reuse `newFakeProvider` (scripted OpenAI/Anthropic envelopes); reuse `stubCodex` for Codex | Text returned verbatim; usage recorded; `Log` called with caller `Purpose`; schema doc is nil (assert via `lastBody` having NO schema key — validator bypass proven, not assumed) | US-3 |
| `scrape.Summarize` | `Deps.Fetcher` fake (markdown) + `fakePrompterExtractor` (§3) with 10-sentence ramble script | Output ≤N sentences even when model rambles; input capped at named const (assert via recorded `user` length); `Purpose=="summarize"`; ceiling error when `MaxCost` tiny; model-error propagates, no truncation of errors | US-3 |
| `scrape.DiffWords` | No mock (pure) — word tables | identical→`""`; one-word change → single `-old +new`; common prefix/suffix trimmed (assert LCS runs on window, not full input — via >2k-identical-flanks + small middle case); 2001-word window → error; 1999-word → ok (boundary lock) | US-5 |
| `clean.Brand` (pure) | No mock — `testdata/brand/*.html` + `goldenDir` | `DeepEqual` on `BrandInfo`: colors deduped/order-stable; fonts list; logo precedence (img-logo beats og:image; no-logo page → `""`); favicon is the ABSOLUTE resolved URL (not the raw `href`) | US-5 |
| `scrape.BrandPage` | `Deps.Fetcher` fake serving brand HTML | Same `BrandInfo` as pure test (proves seam wiring, not logic); fetch error propagates; quality-blocked page → `*clean.QualityError` (brand never returns empty markdown silently) | US-5 |
| `crawl.ListSitemapURLs` | `Deps`-style fake fetcher (crawl-side copy) serving robots/sitemap/index/gzip(non-)XML | Exact URL sets; `.xml.gz` (generated in-test, never committed) decoded; 101-child index → `truncated`, follow-count == 100; 10,001-loc set → `truncated`, len == 10k; truncated garbage → hard error naming the body | US-2 |
| `widenToolInput` + flex types | Pure unit on built `*sdk.Tool` (no server) + in-memory tool calls | `required` + all descriptions byte-identical pre/post widen (regression lock on the validator lesson); `Types` unions exactly `[integer,string]` etc.; `"abc"` for int → tool ERROR naming the param (loud, not zero-value) | US-4 |
| MCP 7 tools (via `dialInMemory`) | `testMCPServer` + `ScrapeDeps.Fetcher` fake copy (§3) + `fakePrompterExtractor` as `ExtractorFor` product | `batch`: 3 mixed URLs → 2 ok + 1 error record, string `"concurrency"` coerces; `map`: loc list; `summarize`: `"max_sentences":"2"` coerces, ≤2 sentences; `diff`: pair output; `brand`: colors present; `vertical_scrape`: mismatch → error containing `does not handle`; `list_extractors`: exact 10 names | US-1, US-2, US-3, US-4, US-5 |
| Batch fan-out (scrape-level) | Fake fetcher with sleep + atomic max-inflight counter | Max inflight ≤ limit (limit 2 test); results in INPUT order despite staggered completion; per-URL error is data (`ok:false` + quality string); >100 URLs rejected pre-I/O; limit ≤0 rejected pre-I/O | US-1 |
| CLI 6 commands + `--prompt`/`auto` | Reuse `codeOf` + `testEnv` + `file://` + `newFakeProvider` (scripted text envelope for prompt paths) | `batch` mixed → exit 0 with error record; `map`/`diff`/`brand`/`vertical --list` happy paths; `extract --prompt`+`--schema` → exit 2; neither → today's required error; `--provider auto` unknown → exit 2; new-command help texts present (extend the `UsageString` pattern) | US-1…US-5 |
| Provider `auto` ordering | No mock (pure) — selection function on fake provider→key map | Keyed-first in documented priority order; keyless skipped; none-keyed → single error listing attempted providers; failure falls through to next (fake-Prompter error injection) — ONLY if C.8 ships, else this row is void | US-3 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | Fake fetchers/prompters, localhost `httptest` only, `testdata/` files, temp-dir SQLite — no external network, no browser, no keys | <60s total | Every push; the only default gate |
| Golden regen (`go test ./clean/ -update`) | Same as unit; WRITES the new `testdata/brand/` goldens on first run, then locks them | seconds | Once when brand fixtures land (review the diff!) — afterwards any change must be an intentional `git diff` on goldens |
| Manual smoke (not a test file) | Built binary + real key (summarize/prompt) or real large site (`map` on a `.xml.gz` host) | minutes | Pre-release: keyed `summarize` sanity; `map` against one gzip-sitemap host by hand |

No browser tier (new surface never escalates to rod; `//go:build browser` suite untouched). No live-provider tier in the default suite: prompt paths are proven by scripted providers + fake prompters; keyed runs are manual-smoke-only (hermetic law).

---

## 3. Fake / Mock Implementations

Two new fakes. Everything else is reused verbatim (file:line pointers in §5).

### `fakePrompterExtractor` — `extract.Extractor` + `extract.Prompter` for `scrape` + `mcp` tests

```go
// scrape/summarize_test.go (package scrape_test) — mcp/agent_test.go carries a copy (§6: third copy, rule-of-three rationale)
type fakePrompterExtractor struct {
    t        *testing.T
    promptScript []string        // PromptText returns script[i] per call (last repeats)
    promptErr  error             // non-nil: PromptText fails (fallback/error paths)
    mu       sync.Mutex
    calls    int                 // PromptText call count
    systems  []string            // recorded system prompts
    users    []string            // recorded user prompts (input-cap assert reads these)
    purposes []string            // recorded via... see below
}

func (f *fakePrompterExtractor) Name() string { return "fake-prompt" }

// Extract must NEVER fire on prompt paths — loud proof, Phase-B precedent.
func (f *fakePrompterExtractor) Extract(_ context.Context, _ extract.ExtractInput) (extract.ExtractResult, error) {
    f.t.Error("Extract called on prompt path — validator bypass violated")
    return extract.ExtractResult{}, errors.New("must not be called")
}

func (f *fakePrompterExtractor) PromptText(_ context.Context, system, user string) (string, extract.TokenUsage, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    if f.promptErr != nil {
        return "", extract.TokenUsage{}, f.promptErr
    }
    f.calls++
    f.systems, f.users = append(f.systems, system), append(f.users, user)
    idx := min(f.calls-1, len(f.promptScript)-1)
    return f.promptScript[idx], extract.TokenUsage{PromptTokens: 10, CompletionTokens: 5}, nil
}
```

Purpose capture: `Summarize` threads `Purpose` through `ExtractorFor`'s logging closure in production, which unit tests can't observe — so purpose is asserted at the `extract/` adapter level (real adapters call `Log(purpose, usage)`; scripted-provider tests capture it via the existing `captureLog` helper, extract_test.go:329). The `purposes` field above is therefore DROPPED — do not record what you can't observe; assert purpose in `extract/prompt_test.go` via `captureLog`, not here. (This paragraph is load-bearing: it stops the executor writing a fake that asserts fiction.)

**Matches real use:** `ExtractorFor(...)` returns it as `extract.Extractor`; `Summarize` type-asserts to `extract.Prompter` — the assertion succeeding IS the seam test. Compile-time lock in each test file: `var _ extract.Prompter = (*fakePrompterExtractor)(nil)`.

### Fetcher fake, third copy (mcp) + crawl-side variant

```go
// mcp/agent_test.go (package mcp_test) — 15-line copy of scrape's fakeVerticalFetcher
// (bodies keyed on "example.com"/"api.github.com"/sitemap hosts + order log).
// crawl/sitemap_test.go — same shape keyed on robots/sitemap bodies; needs a
// headers/status extension ONLY if sitemap tests assert content-type (they don't —
// sniff 0x1f8b on bytes, so status+body suffice; keep the struct identical).
```

No new fields, no new methods — if the executor wants a fourth behavior, that's the signal to generalize (rule of three now satisfied at 3 copies: vertical, scrape, mcp — generalization still REJECTED: test-only code must not become a shared prod-visible helper package; the copies are 15 lines each and diverge by key domain).

### Reused verbatim (read first, never modify)

- `fakeExtractor` + `total()` (`scrape/scrape_test.go:24`, `mcp/server_test.go:24` — separate copies already exist per package; use each package's own)
- `openScrapeDB`, `testSchema`, `fakeDeps`, `scrapeOrigin` (`scrape/scrape_test.go:61ff`)
- `openMCPDB`, `testMCPServer`, `dialInMemory`, `callTool`, `decodeOut`, `pageHTML`, `origin3Pages` (`mcp/server_test.go:56ff`) — `testMCPServer` takes only `(db, fx)`; the Fetcher fake is injected by REBUILDING `magpiemcp.Deps` inline in `agent_test.go` (same literal shape, plus `Fetcher:`), not by editing the helper
- `newFakeProvider` + `openAIEnvelope`/`anthropicEnvelope` (`cli/scrape_test.go:27`, `extract/extract_test.go:29` — per-package copies; prompt-path CLI tests script message-content text through the same envelopes)
- `captureLog` + `logCall` (`extract/extract_test.go:324`), `stubCodex` + `stubCalls` (`:401`), `captureStderr` (`:335`)
- `codeOf`, `testEnv`, `mustAbs`, `mustRead`, `mustOpenDB`, `mustCount`, `captureOutput`, `newTestServer`, `writeFileSite` (`cli/*_test.go`)
- `golden`, `goldenDir`, `cleanFile` (`clean/clean_test.go:15ff`); robots seed pattern `TestRobots_Sitemaps` (`crawl/crawl_test.go:261`)

---

## 4. Test File List

```
gomagpie/
├── extract/
│   └── prompt_test.go       # NEW: OpenAI/Anthropic PromptText via scripted providers (text verbatim, schema-key ABSENT from lastBody, captureLog purpose) + Codex schema-less via stubCodex + compile asserts (3 adapters satisfy Prompter)
├── scrape/
│   ├── summarize_test.go    # NEW: fakePrompterExtractor (above) + Fetcher fake: ramble-truncation, input-cap bytes, ceiling-abort, model-error passthrough, Purpose constate — plus batch fan-out (inflight/order/per-URL-error/limits) — SPLIT if >400 lines: batch_test.go
│   ├── diff_test.go         # NEW: DiffWords pure tables (identical/one-word/trim-window/1999-ok/2001-error) + error-path via MCP/CLI only (no scrape.Run wrapper to unit-test beyond the pure fn)
│   ├── brand_test.go        # NEW: BrandPage via Fetcher fake (BrandInfo == pure-test value) + fetch-error + quality-blocked page → QualityError
│   └── vertical_test.go     # REUSE (no change): MatchURL/ErrURLMismatch semantics relied on by vertical_scrape tests
├── clean/
│   └── brand_test.go        # NEW: Brand pure tables + testdata/brand/ goldens via goldenDir (colors dedupe/order, fonts, 3 logo precedences, favicon-absolute, no-logo empty)
├── crawl/
│   └── sitemap_test.go      # NEW: robots→seeds, urlset exact-set, index fan-out, in-test gzip round-trip, 101-child + 10,001-loc cap tables, garbage-body error
├── mcp/
│   └── agent_test.go        # NEW: TestToolCatalog (11 names) + widen-unchanged + flex-error-naming + 7 tool happy/error paths via dialInMemory (+ Fetcher-fake Deps rebuild + fakePrompterExtractor copy)
├── cli/
│   └── agent_test.go        # NEW: 6 commands (exits/outputs/help) + extract --prompt xor×2 + --provider auto unknown→2 (+ ordering unit iff C.8 ships)
├── testdata/
│   └── brand/               # NEW: shop.html (style+inline+logo-img+icon), minimal.html (no logo, no style), svg-logo.html, noise.html (ad/logo-like decoys) + goldens
└── plan/phase-C-tests.md    # this file
```

Every deliverable in `plan/phase-C.md` §4 maps: `extract/prompt.go`→prompt_test; `scrape/{summarize,diff,brand}.go`→matching tests; `clean/brand.go`→brand_test+goldens; `crawl/sitemap.go`→sitemap_test; `mcp/{agent,coerce}.go`+`server.go`→agent_test; 6 `cli/*.go`+`root.go`+`shared.go`→agent_test; `testdata/brand/`→produced here. `scrape/scrape.go` and `vertical/` need NO new tests (reused, behavior locked by existing suites).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): per-package test files + the existing `-update` flag; hermetic-only suite, no integration-tier flag (Phase-A/B precedent). **Additions only — no existing test file is modified.** New helpers and their homes:

```go
// extract/prompt_test.go — package extract_test; REUSE newFakeProvider/anthropicEnvelope/openAIEnvelope/captureLog/stubCodex.
// ADD: textEnvelope(raw string): wraps plain TEXT as message content (prompt path returns
// content verbatim, not parsed JSON — the envelope's content field is the whole script).
// ADD: schemaKeyAbsent(t, body map): fails if any known schema key ("output_config", "json_schema",
// "response_format", "require_parameters") appears — the validator-bypass proof per adapter.

// scrape/summarize_test.go — package scrape_test; REUSE openScrapeDB/testSchema/fakeDeps/scrapeOrigin.
// ADD: fakePrompterExtractor (§3 verbatim) + rambleScript(): 10 numbered sentences.
// ADD: inflightFetcher: fakeVerticalFetcher-shape copy with time.Sleep + atomic current/max counters
// (batch fan-out test only — keep OUT of vertical_test.go's fake; single-purpose copy).

// scrape/diff_test.go — package scrape_test; NO helpers (pure tables). words(n): n distinct words
// ("w0001"…) for boundary rows — generated, never fixtures.

// scrape/brand_test.go — package scrape_test; REUSE fakeVerticalFetcher copy + verticalFixtureBytes-pattern
// reading ../testdata/brand/ (mirror verticalFixtureBytes one-liner, don't cross-import).
// clean/brand_test.go — package clean_test; REUSE golden/goldenDir/cleanFile; ADD brandFile(t, name):
// os.ReadFile ../testdata/brand/<name>.html (same one-liner shape as cleanFile's read).

// crawl/sitemap_test.go — package crawl_test (check actual package name first — follow the file's own
// convention); REUSE TestRobots_Sitemaps seed pattern; ADD: gzipBytes(t, xml string): compress/gzip
// in-test (never commit .gz fixtures); locSet(n): synthetic <urlset> with n <loc> rows.

// mcp/agent_test.go — package mcp_test (matches server_test.go); REUSE openMCPDB/dialInMemory/callTool/
// decodeOut/pageHTML/origin3Pages; ADD: agentDeps(db, fx, fetcher): magpiemcp.Deps literal = testMCPServer's
// shape + Fetcher field (rebuild, don't edit the helper); fakePrompterExtractor copy (§3 minus purposes).

// cli/agent_test.go — package cli; REUSE codeOf/testEnv/mustAbs/mustRead/mustOpenDB/mustCount/
// captureOutput/newTestServer/writeFileSite/newFakeProvider; ADD: runAgentCmd helper ONLY if 3+ tests
// share arg-shaping (rule of three — else call runBatch/runMap/… directly like runScrape precedent).
```

Scope rationale: everything function-scoped (per-test fakes, `t.TempDir()` DBs, per-test `httptest` servers, in-memory MCP transports) — safe for parallel `go test`. The sole shared mutable artifact is `testdata/brand/`, written only under `-update` on first creation, then locked.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Widen-unchanged test is the validator-lesson lock | `required` + every property description compared pre/post `widenToolInput`; plus a string-`"3"` tool call per scalar kind | Prevents silent revert to `UnmarshalJSON`-only coercion (the exact bug class that would ship broken tools again) — the test fails if anyone "simplifies" widen away |
| `StringList` single-string = JSON-array-try then ONE url (never comma-split) | Table: `'["a","b"]'`→2 urls; `"https://x/?a=b,c"`→1 url intact; `"  https://x/  "`→trimmed 1 | URLs legally contain commas in query strings — comma-splitting corrupts them; JSON-or-single is the only safe reading (locks the §2 semantics) |
| Diff boundary rows at 1999/2001 words, not just "big" | `words(1999)` ok, `words(2001)` errors, trim-fixture (5k identical flanks + 3-word middle) passes | Locks the ~2k WINDOW (not input) semantics: the trim case proves large inputs with small diffs WORK — a whole-input cap test would bless the wrong (pre-validator) design |
| Sitemap caps tested with generated bodies, not fixtures | `locSet(10001)`, 101-child index built in-test; gzip via `gzipBytes` | 10k-URL fixture files would bloat the repo; generation is 5 lines and deterministic — fixtures reserved for REAL shapes (robots/urlset/index/gzip-magic samples) |
| Purpose asserted at adapter level, never in fakes | `captureLog` in `extract/prompt_test.go` asserts `"summarize"`/`"extract"` reach `Log`; `fakePrompterExtractor` has NO purposes field (§3) | The fake cannot observe what production doesn't pass it — a fake asserting purpose would test fiction. Two-level proof: adapters log purpose (real), Summarize passes it (code review + ceiling-path test) |
| Batch order test uses staggered sleeps, not mocks | Fake serves URL[i] after `(n-i)*5ms`; assert output order == input order | Proves collect-then-write (input order) rather than completion order — the JSONL contract CLI users will parse against |
| Inflight counter is atomic, limit test uses limit=2 | `atomic.Int64` current/max in `inflightFetcher`; 5 URLs × 20ms sleeps | Catches `SetLimit` misplacement (e.g. limit applied per-call instead of shared group) with zero flakiness — 20ms ≫ scheduler jitter, max==2 deterministic |
| Third fetcher-fake copy stays a copy | mcp + crawl-side copies duplicate the 15-line struct; NO shared `testutil` package | Copies are test-only and diverge by key domain; a shared package would be new exported surface for 15 lines (Phase-A/B precedent, rule of three cited and consciously capped) |
| CLI prompt paths reuse `newFakeProvider`, never a key | Script message-content text; assert `mustCount(llm_calls)==1` + stdout == script text | Proves the prompt path bills exactly one call and returns text unvalidated — keyed runs are manual-smoke-only (hermetic law) |
| Brand goldens created by `-update`, then locked | First run writes, executor REVIEWS the diff (colors/fonts/logo/absolute favicon eyeballed once), subsequent runs compare | New goldens can't use the empty-diff gate (nothing to drift from yet) — the review step is the gate; `git status` must show ONLY the 4 new fixture+golden pairs afterwards |
| C.8 tests ship iff C.8 ships | Ordering unit + error-injection fallback live in `cli/agent_test.go` behind the feature; if cut, the row is void, no stub tests | Dead tests for cut features rot — the plan marks C.8 optional, the suite mirrors that exactly |
| No MCP tool-count-sensitive test outside `TestToolCatalog` | `grep -rn "FourTools\|len(tools)==4"` must return nothing after the phase (add to §9) | Stale count asserts in neighboring tests are the classic false-red after adding tools — one catalog, one home |

---

## 7. Example Test Case

```go
// mcp/agent_test.go
package mcp_test

import (
    "context"
    "strings"
    "testing"

    "gomagpie/extract"
    magpiemcp "gomagpie/mcp"
    "gomagpie/scrape"
    "gomagpie/store"
)

// TestToolCatalog pins the 11-tool surface: a rename, drop, or accidental
// addition fails here, not in a user's agent session. Kept alongside
// TestMCP_FourTools (functional, not a catalog — never merge them).
func TestToolCatalog(t *testing.T) {
    db := openMCPDB(t)
    fx := &fakeExtractor{}
    cs := dialInMemory(t, testMCPServer(t, db, fx), nil)
    tools, err := cs.ListTools(context.Background(), nil)
    if err != nil {
        t.Fatalf("ListTools: %v", err)
    }
    var got []string
    for _, tl := range tools.Tools {
        got = append(got, tl.Name)
    }
    want := []string{"scrape_url", "crawl_site", "extract_structured", "get_cached_selectors",
        "batch", "map", "summarize", "diff", "brand", "list_extractors", "vertical_scrape"}
    if len(got) != len(want) {
        t.Fatalf("tool count = %d (%v), want %d", len(got), got, len(want))
    }
    have := map[string]bool{}
    for _, n := range got {
        have[n] = true
    }
    for _, n := range want {
        if !have[n] {
            t.Errorf("missing tool %q in %v", n, got)
        }
    }
}

// TestBatch_StringArgsCoerce is the phase's load-bearing test: the EXACT
// failure the validator reproduced (string args dying in schema validation)
// driven end-to-end. "concurrency":"2" must survive validation (widened
// union) AND arrive as 2 (flex unmarshal); the mixed batch must return
// per-URL records with zero LLM calls.
func TestBatch_StringArgsCoerce(t *testing.T) {
    db := openMCPDB(t)
    fx := &fakeExtractor{} // must never be constructed — batch is markdown-only
    fetch := &fakeAgentFetcher{bodies: map[string]fakeAgentResp{
        "example.com/good1": {body: []byte(`<html><head><title>One</title></head><body><p>` + strings.Repeat("alpha ", 60) + `</p></body></html>`)},
        "example.com/good2": {body: []byte(`<html><head><title>Two</title></head><body><p>` + strings.Repeat("beta ", 60) + `</p></body></html>`)},
        // "example.com/bad" deliberately ABSENT → fake returns error → ok:false record
    }}
    cs := dialInMemory(t, magpiemcp.NewServer(magpiemcp.Deps{
        DB: db,
        ScrapeDeps: scrape.Deps{
            DB: db, Fetcher: fetch,
            ExtractorFor: func(_, _, _ string, _ *extract.Schema, _ string) (extract.Extractor, error) {
                t.Error("ExtractorFor called on markdown-only batch — zero-LLM contract violated")
                return nil, errors.New("must not be called")
            },
            APIKeyFor: func(string) string { return "" },
        },
        DefaultProvider: "fake", DefaultModel: "fake",
    }), nil)
    out := decodeOut(t, callTool(t, cs, "batch", map[string]any{
        "urls":        []any{"https://example.com/good1", "https://example.com/bad", "https://example.com/good2"},
        "concurrency": "2", // STRING — the validator's repro, must coerce
    }, ""))
    // Shape asserts (exact keys per plan/phase-C.md C1 — flag drift, don't freelance):
    // out["results"] is [ {url,ok,markdown?}, {url,ok:false,error}, ... ] in INPUT order.
    results, ok := out["results"].([]any)
    if !ok || len(results) != 3 {
        t.Fatalf("results = %#v, want 3 records", out["results"])
    }
    r0 := results[0].(map[string]any)
    if r0["ok"] != true || r0["url"] != "https://example.com/good1" {
        t.Errorf("results[0] = %#v, want ok:true in input order", r0)
    }
    r1 := results[1].(map[string]any)
    if r1["ok"] != false || r1["error"] == nil || r1["error"] == "" {
        t.Errorf("results[1] = %#v, want ok:false + error string", r1)
    }
    if fx.total() != 0 {
        t.Errorf("extractor calls = %d, want 0 (markdown-only batch)", fx.total())
    }
    if n := llmCalls(t, db); n != 0 {
        t.Errorf("llm_calls rows = %d, want 0", n)
    }
}
```

Notes for the executor: `fakeAgentFetcher`/`fakeAgentResp` are the §3 mcp-side copy (same 15-line shape as `fakeVerticalFetcher` — copy, don't import); `llmCalls(t, db)` is a 5-line `SELECT COUNT(*) FROM llm_calls` helper local to `agent_test.go` (or reuse `mustCount`-equivalent if `mcp` tests already have one — check `server_test.go` first); `errors` must be added to imports (the snippet above elides it — `goimports` will tell you). If `batch`'s output key isn't `results`, the test is wrong — re-read `plan/phase-C.md` C1 before "fixing" the test to match code. Companion string-coercion rows in the same file: `summarize` with `"max_sentences":"2"` → ≤2 sentences; `map` with no string fields (control: proves widening didn't break normal calls); `extract_structured` with JSON-string `schema` → coerced map (FlexMap proof); `"true"` for a bool field (`use_cache`/`same_host`) → honored.

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase C of `gomagpie` — agent surface (tools + commands, stdlib-only). Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-C.md` (full design: 7 tools, 6 commands, prompt-mode, widen-first coercion, caps — AS AMENDED), `plan/phase-C-tests.md` (this suite's contract — §3 fakes, §6 decisions, §7 load-bearing test), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test. Production code may or may not exist yet — write tests against the frozen signatures in phase-C §§2–4; if a signature is missing, implement the test to the plan and flag the gap rather than inventing API. If `batch`'s output keys differ from §7's assumption (`results[]` with `url/ok/markdown?/error?`), the TEST is suspect first — re-read phase-C C1.

### What This Project Is
Go 1.26 CLI web scraper (binary `magpie`, module `gomagpie`): fetch → clean → extract. Phase C adds MCP tools (`batch, map, summarize, diff, brand, list_extractors, vertical_scrape` → 11 total), CLI commands (`batch|map|summarize|diff|brand|vertical`), prompt-mode LLM text (`extract.Prompter` on 3 adapter types), and widen-first MCP coercion (`widenToolInput` + `FlexInt/FlexBool/StringList/FlexMap`). Tests are hermetic: `go test ./...` with no external network, no browser, no keys. No new test deps. The validator already proved `UnmarshalJSON`-only coercion silently broken against go-sdk v1.8.0 (validation precedes unmarshal) — your widening tests are the regression lock on that lesson.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | batch ≤100, bounded, per-URL records, zero LLM | fan-out + MCP batch + CLI exit 0 | inflight ≤ limit; input order; bad URL → `ok:false`; `fx.total()==0` |
| US-2 | map with gzip + caps | sitemap tables + MCP map + CLI lines/JSON | exact sets; gzip decoded; `truncated` at caps |
| US-3 | prompt/summarize text, sentence cap, no validator | adapter + Summarize + CLI xor tests | verbatim text; ≤N sentences; `--prompt`+`--schema` → exit 2 |
| US-4 | string coercion + 11-tool catalog | catalog + widen-unchanged + string-arg calls | exact 11 names; required/descriptions identical; `"2"`/`"true"`/string-urls coerce |
| US-5 | keyless diff/brand/vertical, loud failures | diff/brand/vertical tests + CLI exits | pairs/goldens/mismatch-exit-2; `fx.total()==0`, no key |

### Why Fakes Are Required
- Site/sitemap/registry HTML (batch, map, brand, vertical_scrape): live hosts banned — substring→bytes fake `Fetcher` serves fixtures/deterministic bodies, including error injection (bad URL, 500, garbage XML).
- LLM text (summarize, prompt extract): keyed providers banned — `fakePrompterExtractor` (§3 full source) scripts text + records prompts; real adapters tested ONLY against scripted `httptest` providers inside `extract/` (existing `newFakeProvider`, `stubCodex`).
- Selector/LLM constructor: must PROVE non-invocation on markdown-only/vertical paths — error-on-call `ExtractorFor` (Phase-B precedent), not just usage counters.
- MCP transport: real go-sdk client+server over in-memory transports (existing `dialInMemory`) — the string-coercion bug lives in validation-before-unmarshal, so tests MUST go through `callTool`, never call handlers directly.
- SQLite: NOT faked — real `modernc.org/sqlite` on `t.TempDir()` via existing openers (Phase-1 precedent).

### What NOT to Test
- Don't test live hosts, real API keys, or keyed runs — prompt/summarize keyed paths are manual-smoke-only; CLI tests script `newFakeProvider` text envelopes.
- Don't test go-sdk internals (`applySchema`, inference) — test OUR widened behavior through `callTool` + the widen-unchanged unit; the SDK is a pinned dep, not our code.
- Don't test stdlib (`encoding/xml` strictness beyond one malformed row, `compress/gzip` round-trip beyond the helper, LCS math beyond the tables) or goquery mechanics beyond our pick assertions.
- Don't test Phase D/E surface (depth-5 recursion, partial-budget, SSRF, proxy, TLS, PDF) — unbuilt; cap/gzip tests are Phase-C's frozen boundary, flag anything deeper as out-of-scope.
- Don't create a shared `testutil` package and don't touch existing test files — additions only (one rule-of-three exception: NOTHING existing is modified).
- Don't commit binary `.gz` fixtures or 10k-row fixture files — gzip and cap bodies are generated in-test (§5 helpers); fixtures are for real shapes only.
- Don't write stub tests for C.8 if cut — the ordering tests ship iff `--provider auto` ships.

### Critical: Fake Implementations

Copy verbatim: `fakePrompterExtractor` — full source in test-plan §3 (needs `context, errors, sync, testing, gomagpie/extract`; DROP the purposes field per §3's load-bearing paragraph — purpose is proven via `captureLog` at the adapter level). Fetcher copies: 15-line `fakeVerticalFetcher` shape duplicated into `mcp/agent_test.go` (keyed on tool-test hosts) and `crawl/sitemap_test.go` (keyed on robots/sitemap bodies) — copy, don't share (§6 rationale). `inflightFetcher` (batch only): same shape + `time.Sleep` + `atomic.Int64` current/max.

Reuse verbatim (read first, never modify): `fakeExtractor`+`total()`, `openScrapeDB`, `testSchema`, `fakeDeps`, `scrapeOrigin` (scrape); `openMCPDB`, `testMCPServer`, `dialInMemory`, `callTool`, `decodeOut`, `pageHTML`, `origin3Pages` (mcp — rebuild `magpiemcp.Deps` inline to add `Fetcher:`, don't edit the helper); `newFakeProvider`, `openAIEnvelope`, `anthropicEnvelope`, `captureLog`, `stubCodex`, `captureStderr` (extract/cli per-package copies — use each package's own); `codeOf`, `testEnv`, `mustAbs`, `mustRead`, `mustOpenDB`, `mustCount`, `captureOutput`, `newTestServer`, `writeFileSite` (cli); `golden`, `goldenDir`, `cleanFile` (clean); `TestRobots_Sitemaps` seed pattern (crawl).

### Test Files to Create

```
extract/prompt_test.go       # NEW (~6): 2 adapters × scripted text + schema-absent proof + purpose-via-captureLog + Codex stub + 3 compile asserts
scrape/summarize_test.go     # NEW (~7): ramble-truncation + input-cap + ceiling-abort + error-passthrough (+ batch fan-out ~5 here or batch_test.go if >400 lines)
scrape/diff_test.go          # NEW (~5 pure tables, zero helpers)
scrape/brand_test.go         # NEW (~3): seam-wiring equality + fetch-error + quality-blocked
clean/brand_test.go          # NEW (~5): precedence tables + 4 goldens via goldenDir
crawl/sitemap_test.go        # NEW (~7): seeds/exact/index/gzip/101-child/10001-loc/garbage
mcp/agent_test.go            # NEW (~14 incl. §7 verbatim): catalog + widen-unchanged + flex-errors + 7 tools × (happy + key error path)
cli/agent_test.go            # NEW (~12): 6 commands + prompt-xor×2 + auto-unknown→2 (+ ordering iff C.8)
testdata/brand/              # 4 HTML fixtures + goldens (first -update writes, executor REVIEWS, then locked)
```

### Per-File Coverage Guidance

#### extract/prompt_test.go
`TestOpenAIPromptText`: script envelope with content `"plain summary"` → returns exactly that; `lastBody` has NO `response_format`/`json_schema`/`output_config` key (validator-bypass proof — enumerate the keys each adapter would set in schema mode, assert all absent); `captureLog` got `Purpose=="extract"`. Mirror for Anthropic (`TestAnthropicPromptText`, purpose `"summarize"` via direct `Log` capture — purpose is a CALLER-provided string, prove it flows). `TestCodexPromptText_Schemaless`: `stubCodex` success mode → text returned without `--output-schema` in the logged command line (`stubCalls` log assert). Compile asserts: `var _ Prompter = (*OpenAIAdapter)(nil)` (+Anthropic, +CodexExec) — a 4th adapter type later fails HERE first.

#### scrape/summarize_test.go
`TestSummarize_TruncatesRamble`: 10-sentence script, N=3 → exactly 3 sentences (count via `strings.Count(s, ".")`… careful: abbreviations — use the SAME splitter production uses; assert `len(sentences)==3` through the exported behavior only, i.e. count sentence-terminators the way the test's own splitter does is circular — instead assert the output is a PREFIX of the script's first-3-sentences reconstruction… simplest honest assert: output has ≤3 terminators AND is a prefix of the script (truncation, never rewrite). `TestSummarize_InputCap`: 10k-word markdown → recorded `user` field ≤ cap-bytes (+documented const name). `TestSummarize_CeilingAborts`: `MaxCost: 0.000001` → `crawl.ErrCostCeiling`, zero prompter calls. `TestSummarize_ModelError`: `promptErr` → error propagates verbatim, no empty-string success. Batch block (`TestBatch_InflightLimit/Order/PerURLError/Over100/BadLimit`): inflight fake, staggered sleeps, mixed good/missing/bad-status URLs.

#### scrape/diff_test.go + scrape/brand_test.go
Diff: table rows `{name, prev, cur, wantContains/wantExact/wantErr}` — `identical→""`, `one-word`, `multi-line grouping`, `flanks-5k+middle-3` (window proof), `words(1999)` ok, `words(2001)` err contains `too large` (match the REAL message — read `diff.go` first). Brand: `TestBrandPage_MatchesPure` (same HTML through `BrandPage` and `clean.Brand` → DeepEqual); fetch-error row; quality-blocked row (`status 403` challenge-shaped body → `errors.As` `*clean.QualityError`).

#### clean/brand_test.go
`TestBrand_LogoPrecedence`: shop.html → img-logo; svg-logo.html → svg value; minimal.html → `""` (og:image absent too — proves the fallthrough, not just the happy path); noise.html (`.logo-ad`, `div.brand-decoy`) → `""` (decoy immunity). `TestBrand_FaviconAbsolute`: icon `href="/i.png"` on `https://x.com/p` → `https://x.com/i.png` (proves HarvestMetadata reuse — a relative-URL failure means someone reimplemented it). `TestBrand_Goldens`: `goldenDir(t, "brand", name, render(BrandInfo))` × 4 — deterministic render (sorted colors/fonts) or the golden flakes on map order.

#### crawl/sitemap_test.go
`TestSitemap_RobotsSeeds`: robots with 2 `Sitemap:` lines (+ case variant + inline comment, webclaw parity rows) → both fetched. `TestSitemap_UrlsetExact`: 3-loc body → exact set (map-compare, order-insensitive). `TestSitemap_IndexOneLevel`: index→2 children→union of locs. `TestSitemap_Gzip`: `gzipBytes(urlset)` served with AND without `.xml.gz` path (sniff, not extension). `TestSitemap_ChildCap`: 101-child index → `truncated==true`, follow-count==100 (assert via fetcher order log). `TestSitemap_URLCap`: `locSet(10001)` → len==10000, truncated. `TestSitemap_Garbage`: `"<not xml"` → error containing the body URL (debuggability).

#### mcp/agent_test.go
§7 verbatim × 2 (catalog + batch-coerce). `TestWiden_PreservesRequired`: build a representative `In` (or reuse a real tool's), snapshot `required`+descriptions, widen, compare. `TestFlex_BadString`: `"concurrency":"abc"` → tool ERROR (not success-with-0 — zero-value coercion would silently uncap the fan-out). Per-tool rows: map/diff/brand/list/vertical happy + vertical-mismatch error + summarize `"max_sentences":"2"` truncation + extract_structured JSON-string-schema coercion. `llmCalls==0` asserted on every keyless path (US-5 audit in one place).

#### cli/agent_test.go
`TestBatch_MixedExit0` (file:// good + missing → exit 0, JSONL has `ok:false` line), `TestMap_LinesAndJSON`, `TestSummarize_MaxSentences` (scripted text envelope, `--max-sentences 2`), `TestDiff_AgainstFile`, `TestBrand_JSON`, `TestVertical_List` (contains `github_repo`), `TestExtract_PromptXor` (both→2, neither→required-error), `TestProviderAuto_Unknown` (→2). Help-text: extend the `UsageString` pattern per new command (one `strings.Contains` each — cheap, catches unregistered commands).

### Data Model Notes (Go)
- `map[string]any` tool outputs: navigate via type asserts with `, ok` + `t.Fatalf` on shape mismatch (never blind-index — a shape change should fail the TEST, not panic it… except where a panic pinpoints better; prefer Fatalf).
- `In` structs with flex fields: construct DIRECTLY in widen-unit tests (`FlexInt` JSON round-trip table: `3`, `"3"`, `" 3 "`, `"abc"`→err, `3.5`→err — floats must NOT silently truncate).
- Sentence counting: assert truncation via prefix-property (output is a prefix of script) + terminator bound — never reimplement the splitter in the test.
- Exit codes via existing `codeOf(err)` — never spawn subprocesses; env isolation via `testEnv`.
- Golden rendering of `BrandInfo` must sort slices (colors/fonts) — map iteration order otherwise flakes the golden.

### Success Criteria
- `go test ./...` exits 0; RUN count strictly exceeds the pre-phase baseline (`tee /tmp/phaseC-baseline.log` BEFORE writing — non-vacuous rule, testing.md; Phase-B close was 304 RUN lines)
- `go test ./clean/ -update` writes ONLY `testdata/brand/` goldens on first run (reviewed), nothing on second; `git status --porcelain testdata/clean/` empty throughout
- `grep -rn "FourTools\|len(tools)==4" mcp/ cli/` returns nothing (no stale count asserts)
- Full gates: `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass
- Every deliverable from `plan/phase-C.md` §4 maps to §4 above (no orphan deliverable, no orphan test file)

### Expected File Structure at End
(Same tree as Test File List §4 — reproduce exactly. No `testutil` package, no modified existing test files, 4 HTML fixtures + goldens, 8 test files.)
---

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -v 2>&1 | tee /tmp/phaseC-baseline.log; grep -c '^=== RUN' /tmp/phaseC-baseline.log  # expect 304 (Phase-B close)

# Fast hermetic suite (every push — localhost httptest only, no external network/keys/browser)
go test ./...

# New-test count vs baseline (must strictly exceed; ~45 new funcs expected)
go test ./... -v 2>&1 | tee /tmp/phaseC-tests.log; grep -c '^=== RUN' /tmp/phaseC-tests.log

# Focused per task (mirrors phase-C sanity checks)
go test ./extract/ -run TestOpenAIPromptText -v                              # C.1 adapter
go test ./extract/ -run 'TestCodexPromptText|TestAnthropicPromptText' -v     # C.1 rest
go test ./scrape/ -run 'TestBatch_' -v                                       # C.2
go test ./crawl/ -run TestSitemap_ -v                                        # C.3
go test ./scrape/ -run TestSummarize_ -v                                     # C.4
go test ./scrape/ -run 'TestDiff|TestBrandPage' -v; go test ./clean/ -run TestBrand_ -v  # C.5+C.6
go test ./mcp/ -run 'TestToolCatalog|TestWiden|TestFlex|TestBatch_|TestVertical' -v      # C.0+C.3+C.7
go test ./cli/ -run 'TestBatch_|TestMap_|TestSummarize_|TestDiff_|TestBrand_|TestVertical_|TestExtract_Prompt|TestProviderAuto' -v  # commands

# Brand-golden discipline (US-5: new goldens reviewed once, then locked)
go test ./clean/ -update; git status --porcelain testdata/brand/   # first run: 8 new files (4 html + 4 golden); review diff
go test ./clean/ -update; git status --porcelain testdata/         # afterwards: must print nothing
git status --porcelain testdata/clean/                             # must print nothing throughout

# Stale-count-assert sweep (US-4: one catalog, one home)
grep -rn "FourTools\|len(tools)==4" mcp/ cli/ || echo NO-STALE-COUNTS

# Full gate (mirrors phase-C exit criterion 9)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service/pipeline with pure-logic core; fake Fetcher + fake Prompter + scripted providers + in-memory MCP (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to C1 (batch/map/diff/brand/vertical), C2 (prompt/summarize), C3 (coercion/catalog)
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 11 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all of phase-C §4 (code + testdata/brand); `scrape/scrape.go` + `vertical/` need no new tests (reused, locked by existing suites — stated, not silent)
- [x] Every external/heavy dependency has a fake or mock equivalent — live hosts → fake fetchers; LLM text → `fakePrompterExtractor` + scripted providers; MCP transport → in-memory (real SDK, localhost); SQLite → real pure-Go temp DB (precedent); browser/keyring untouched by this phase
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — fakes + `file://` + localhost `httptest` only; keyed runs are manual-smoke-only (§2, §8)
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation (Phase-A/B precedent): no integration tier exists; hermetic-only suite, browser smoke stays behind its build tag, keyed smoke is explicitly not-a-test-file
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure reusing the existing `-update` flag; stated at the top of §5 per skill rule
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — adapted: §8 pastes the seam/fake contracts verbatim and points to §3's full fake source with import list (same adaptation Phase-B's plan used)
- [x] Run commands section is present — §9 with baseline/focused/golden/stale-count/full-gate/cross commands
