# Phase F — Map+ (link-walk fallback, scoping, search, metadata)

**Duration:** Days 1–3 (~25 hours)
**Depends on:** Phase C (`magpie map` CLI + MCP `map` tool, `scrape.Prompt` + `--provider auto`), Phase D (`crawl.CompileScope`/`Scope.Allows` URL globs, `Canonicalize`, `SkipExtension`, robots `Checker`, `HostLimiters`), Phase E (`--browser` TLS fingerprint passthrough convention)
**Blocks:** Nothing. Desktop robot-builder URL-tree (future GUI) will consume `map --format json` output — keep `links` objects stable.
**Risk Level:** MEDIUM — additive feature work on a read-only listing command; no schema migration, no security-boundary change (SSRF guard lives in the fetch layer). The one real hazard is the new fetch loop being a rude crawler (robots/crawl-delay violations, runaway fetches) — mitigated by reusing Phase D's `Checker`/`HostLimiters` and hard caps, each proven by test. 6 sections (failure section omitted per MEDIUM risk).
**Stack:** go *(per AGENTS.md: `run-phase` hard-blocks on `stack: go` — execute manually via the execution prompt in §6)*
**New libraries:** none. goquery, grobotstxt, cobra, encoding/json all already in use — no dependency ask.

---

## 1. Objective + What Success Looks Like

Turn `magpie map` from sitemap-only listing into Firecrawl-/v1/map-class discovery: URL-glob scoping, a bounded same-origin link walk when the sitemap is missing or thin, free metadata (sitemap `lastmod`, walk-harvested anchor text), deterministic zero-LLM relevance search, and an optional one-call LLM compile of a natural-language intent into globs + keywords. Zero LLM by default; one cheap call only when `--intent` is passed.

1. `magpie map example.com --include '**/docs/**' --exclude '**/blog/**' --limit 50` prints at most 50 URLs, all matching the include glob, none matching the exclude (exclude wins). Bad globs exit 2 pre-I/O naming the offending glob — same grammar and error shape as `magpie crawl` (Phase D), *not* the scrape CSS-selector flags.
2. Against a fixture site with **no** sitemap, `magpie map site` discovers URLs by walking the origin (homepage → linked pages), returns them with `source:"crawl"` in JSON, and never exceeds the walk caps: ≤200 fetches, depth ≤2, ≤25s budget, ≤4 concurrent per host.
3. Against a fixture with a **thin** sitemap (<10 URLs), results are sitemap ∪ walk (deduped, canonicalized), `source:"sitemap+crawl"`. `--no-fallback` skips the walk (sitemap-only behavior, `source:"sitemap"`).
4. `magpie map site --search "pricing"` keeps only URLs scoring >0 and ranks them by keyword weight (path hit 3 pts > anchor hit 2 pts > query hit 1 pt), ties broken by URL asc. Same input ⇒ same order, always: `/pricing` outranks `/blog/pricing-update` because a path hit outranks an anchor hit, regardless of discovery order.
5. `--format json` gains `links:[{url, title?, lastmod?}]` (title = best anchor text from the walk; lastmod = sitemap `<lastmod>`), plus `source`; `urls` stays for compatibility, and lines output stays one-URL-per-line (titles never break scripts).
6. `magpie map site --intent "product pages, not the blog"` makes **exactly one** LLM call (attribution `purpose="map"` in `llm_calls`), compiles `{include, exclude, keywords}`, applies them through the same deterministic engine, and merges with any explicit `--include/--exclude/--search`. With no provider key: exit 2 naming the provider (`scrape.ErrMissingKey` path). Malformed LLM JSON: exit 2, never a silent unfiltered map.
7. The walk respects robots: a URL disallowed by robots.txt never appears in results, the seed page itself is still robots-checked, `Crawl-delay` is honored (sleepy-fetcher test proves elapsed time), and `--browser chrome` reaches the walk's fetches (TLS fingerprint, Phase E convention).
8. `go test ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...`, and 3-way `CGO_ENABLED=0` cross-builds all pass; no live network in the default suite.

---

## 2. Architecture / Key Design Decisions

```
CLI: magpie map <site> [--format lines|json] [--include G]... [--exclude G]...
                      [--limit N] [--search Q] [--intent TEXT] [--no-fallback]
                      [--browser CHROME|FIREFOX|RANDOM]
MCP: map tool — same fields as StringList/FlexInt (include, exclude, limit,
     search; NO intent — agents write globs natively; intent is a human
     convenience, and skipping it keeps the MCP surface unchanged in spirit)
  → crawl.Map(ctx, fetcher, MapOptions)  ← the single orchestrator
      1. sitemap phase:  ListSitemapEntries (loc+lastmod; existing caps/budget)
      2. fallback gate:  len(entries) < 10 && !NoFallback → walk()
      3. merge+dedupe:   canonical URL is the identity (Canonicalize)
      4. scope:          Scope.Allows (compile once; bad glob = error pre-I/O)
      5. search:         score(keywords, path, anchors) > 0 filter, then sort
      6. limit:          applied last
  → MapResult{URLs []string, Links []Entry, Source string, Truncated bool}
```

- **New `crawl.Map` orchestrator; `ListSitemapURLs` untouched.** Crawl seeding still calls `ListSitemapURLs`; the sitemap internals gain a lower-level `ListSitemapEntries` returning `[]Entry{URL, Lastmod}` with `ListSitemapURLs` becoming a thin wrapper over it. No caller changes.
- **The link walk is NOT `crawl.Run`.** `crawl.Run` is the LLM-extraction pipeline (SQLite runs, selector cache, healers) — massive overkill for listing URLs. `crawl/walk.go` is a standalone in-memory walker: no DB, no store, no records. It reuses only the pure/boring pieces: `Canonicalize`, `SkipExtension`, `CompileScope`/`Allows`, robots `Checker`, `HostLimiters`. Caps: `--walk` defaults depth 2, 200 fetches, 25s budget, 4 workers, same-host only. Budget expiry returns partial results with `truncated:true` (same stop-now semantics as the sitemap BFS).
- **Anchor text is harvested during the walk, never by extra fetches.** One goquery pass per fetched page collects `(absURL, anchorText)` pairs; the longest anchor text seen for a URL wins as its title. This deliberately duplicates ~25 lines of the resolve/normalize loop from `Frontier.ExtractLinks` instead of refactoring the DB-coupled frontier — two callers is not three; the frontier stays untouched (`ponytail:` revisit if a third link-walker appears).
- **Fallback threshold = 10, a constant.** Sitemap yields <10 URLs → walk. Not configurable (YAGNI); `--no-fallback` is the escape hatch. `source` field makes the behavior observable either way.
- **Search is a pure function.** `score(url, keywords, anchors) int`: tokenize the query on non-alphanumerics, lowercase, drop tokens <2 chars; 3 points per keyword hit in the path, 2 per hit in harvested anchor text, 1 per hit in the raw query string. `--search` set ⇒ zero-score URLs are dropped (filter), then sort by score desc, URL asc. Deterministic by construction — no maps iteration order in the output path.
- **`--intent` is one `scrape.Prompt` call, then deterministic everything.** System prompt demands strict JSON `{"include":[globs],"exclude":[globs],"keywords":[words]}`; unmarshal into a plain struct; globs merge with flag globs and pass through `CompileScope` (LLM-hallucinated globs fail the same way typos do: exit 2); keywords feed the same scorer as `--search`. Attribution `Purpose:"map"`, cost ceiling via `MaxCost` — identical shape to Summarize.
- **Output compatibility.** JSON keeps `urls` (derived from `links`) and adds `links` + `source`; `truncated` semantics unchanged plus walk-cap hits. Lines format unchanged. MCP `MapOut` gains optional `links`/`source` — additive, non-breaking.
- **Walk fetches honor `MapOptions.Browser`** via `fetch.FetchRequest.Browser` (Phase E uTLS fingerprints), defaulting to stock. One struct field; real-world homepage bot-walls are exactly where walks die.
- **Test-injectable budgets** follow the existing `sitemapBudget` var pattern (`walkBudget` var, production 25s).

## 3. Tasks

Estimates include their tests. Order is dependency-true: T1 before everything; T4 before T5/T7-integration.

1. **T1 — Orchestrator skeleton (behavior-preserving) (2h).** New `crawl/map.go`: `MapOptions{NoFallback bool, Browser string, Limit int, Include, Exclude []string, Search, Intent string}`, `Entry{URL string, Lastmod, Title string}`, `MapResult{URLs []string, Links []Entry, Source, Truncated}`, and `Map(ctx, f vertical.Fetcher, opts MapOptions) (MapResult, error)` that today only runs the sitemap phase (`source:"sitemap"`). Add `ListSitemapEntries` in `sitemap.go` (extend `urlSet` with `Lastmod string` via `xml:"lastmod"`), rewire `ListSitemapURLs` as its wrapper — existing 15 sitemap tests must pass unchanged. Wire CLI `runMap` and MCP `handleMap` through `Map`. **Done when:** all existing tests green, `magpie map` output byte-identical to today for sitemap-complete fixtures.
2. **T2 — Scope + limit (3h).** `CompileScope(true, false, "", opts.Include, opts.Exclude)` in `Map` (bad globs error before any I/O); filter merged entries via `Allows(seed, canonical)`; apply `--limit` last. CLI `--include/--exclude/--limit`; MCP `include`/`exclude` (`StringList`) + `limit` (`FlexInt`). **Done when:** success criterion 1 demo passes; exit-2 test for a bad glob.
3. **T3 — Search scoring (3h).** Pure `scoreURL(e Entry, keywords []string) int` + tokenizer in `map.go` (table test it: path hit=3, anchor hit=2, query hit=1, no-hit dropped, tiebreak URL asc). CLI/MCP `--search`/`search`. **Done when:** criterion 4 demo passes; determinism test (run twice, identical order).
4. **T4 — Link walk (6h).** New `crawl/walk.go`: `walk(ctx, f vertical.Fetcher, seed string, scope Scope, opts) ([]Entry, bool)` — in-memory visited set, 4 workers over a buffered channel frontier, per-page: robots `Checker.Allowed` (disallowed ⇒ skip silently, never fetched), `CrawlDelay` ⇒ `SetFloor` on `NewHostLimiters(1, 3)`, fetch via the seam with `Browser`, `SkipExtension` drop, goquery anchor harvest (absURL via `clean.ResolveURL`, canonicalize, `Allows`), longest-anchor-wins titles, caps (depth 2 / 200 pages / `walkBudget` 25s) each setting `truncated`. **Done when:** fake-fetcher fixture walk returns expected URL set, robots-disallowed URL absent, crawl-delay test observes the sleep, cap tests prove depth/pages/budget truncation.
5. **T5 — Fallback wiring (2h).** Threshold-10 gate in `Map`, merge+dedupe (canonical identity; walk entries never clobber a sitemap entry's `lastmod`), `source` = `sitemap|crawl|sitemap+crawl`, `--no-fallback` flag (CLI + MCP `no_fallback *FlexBool`). **Done when:** criteria 2–3 demos pass against no-sitemap and thin-sitemap fixtures.
6. **T6 — `--intent` LLM compile (3h).** CLI-only flag; build `scrape.PromptOptions{System: intentSystemPrompt, User: intent, Purpose: "map", Provider/Model/MaxCost: passthrough}` + `--provider/--model/--max-cost` flags mirroring Summarize; strict-JSON parse (`json.Unmarshal` into `intentSpec{Include, Exclude []string; Keywords []string}`); merge into scope/scorer; malformed JSON or missing key ⇒ exit 2 with the verbatim reason. **Done when:** criterion 6 demo passes using the fake provider seam (Summarize's test pattern); `llm_calls` row with `purpose='map'` asserted via the test DB.
7. **T7 — Integration + docs (6h).** CLI end-to-end over httptest/fake fixtures (all 8 criteria); MCP round-trips for the new fields; JSON golden updates; README + `spec.md` map section (flags, source field, intent cost note: "one LLM call, not one per URL"). Full suite + vet + fmt + lint + 3-way cross-builds. **Done when:** criterion 8 is literally true.

## 4. Deliverables

```
crawl/map.go        NEW   Map orchestrator: options/result types, scope+search+limit pipeline, intent compile
crawl/map_test.go   NEW   scoring/lastmod/fallback/limit/search/intent tests (fake fetcher, fake provider)
crawl/walk.go       NEW   bounded in-memory same-origin link walk (robots, rate limit, anchors, caps)
crawl/walk_test.go  NEW   robots/delay/caps/anchor-harvest tests (sleepy + fake fetchers)
crawl/sitemap.go    MOD   urlSet gains lastmod; new ListSitemapEntries; ListSitemapURLs → wrapper
crawl/sitemap_test.go MOD lastmod parse cases
cli/map.go          MOD   --include/--exclude/--limit/--search/--intent/--provider/--model/--max-cost/--no-fallback/--browser
mcp/agent.go        MOD   MapIn gains include/exclude/limit/search/no_fallback; MapOut gains links/source
README.md           MOD   map section: flags, source field, intent pricing honesty
spec.md             MOD   map behavior + intent cost model
```

## 5. Exit criteria

- [ ] `crawl/map.go` exports `Map`/`MapOptions`/`MapResult`/`Entry`; criterion 1 (glob scope + limit, exit 2 on bad glob) demonstrated. *(T2)*
- [ ] `scoreURL` unit-tested with the 3/2/1 weighting and URL-asc tiebreak; criterion 4 order-determinism proof. *(T3)*
- [ ] `ListSitemapEntries` returns lastmod; the 15 pre-existing sitemap tests pass unmodified; criterion 5 JSON shape proven. *(T1)*
- [ ] `crawl/walk.go` proves robots-skip, crawl-delay sleep, depth/pages/budget caps, anchor titles under fake fetchers; criterion 7. *(T4)*
- [ ] Fallback gate + merge + `source` values proven for no-sitemap / thin-sitemap / `--no-fallback` fixtures; criteria 2–3. *(T5)*
- [ ] `--intent` end-to-end with fake provider: one `llm_calls` row (`purpose='map'`), globs applied, malformed-JSON and no-key exit 2; criterion 6. *(T6)*
- [ ] CLI + MCP round-trip tests for every new field; README/spec updated; full local gate green (`go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./...` + 3× `CGO_ENABLED=0` cross-builds); criterion 8. *(T7)*

## 6. Execution prompt

```text
You are implementing Phase F of gomagpie (`magpie`), a Go 1.26 CLI web scraper
(fetch → clean → extract) at /home/domidex/projects/gomagpie. Module
`gomagpie`, binary `magpie`. AGENTS.md rules apply: NO new dependencies, NO
CGO, no live-network tests in the default suite, never import go-rod outside
fetch/. Phases A–E are merged: map exists (CLI `magpie map <site>
[--format lines|json]` + MCP `map` tool) listing robots-declared sitemap URLs
via crawl.ListSitemapURLs with caps (100 children / 10k URLs / depth 5 / 25s
budget, truncated flag). Phase D gave crawl.CompileScope URL globs, robots
Checker, HostLimiters; Phase C gave scrape.Prompt + --provider auto; Phase E
gave --browser TLS fingerprints.

GOAL: bring `magpie map` to Firecrawl-/v1/map parity — link-walk fallback when
the sitemap is missing/thin, --include/--exclude/--limit (URL globs, same
grammar as `magpie crawl`), free metadata (sitemap lastmod + walk anchor-text
titles), deterministic zero-LLM --search ranking, and CLI-only --intent that
spends exactly ONE LLM call compiling NL into {include globs, exclude globs,
keywords} which the deterministic engine then applies.

Read plan/phase-F.md sections 1–5 first; it is the spec. Key confirmed APIs
(all already in the tree — verify with `go doc` before use):

  crawl.CompileScope(sameHost, allowSubdomains bool, pathPrefix string,
      include, exclude []string) (Scope, error)        // crawl/scope.go
  scope.Allows(base, cand *url.URL) bool               // exclude wins; match
                                                       // against Canonicalize output
  crawl.Canonicalize(rawURL string) (string, error)    // crawl/frontier.go
  crawl.SkipExtension(urlPath string) bool             // path ext, not raw URL
  crawl.NewChecker() — .Allowed(ctx, url) (bool, error), .CrawlDelay(ctx, url) time.Duration
  crawl.NewHostLimiters(rate float64, capacity int) — .Wait(ctx, host), .SetFloor(host, delay)
  clean.ResolveURL(base, ref string) string            // relative→absolute
  scrape.Prompt(ctx, scrape.Deps, runID, scrape.PromptOptions{
      Provider, Model string; MaxCost float64; System, User, Purpose string,
  }) (scrape.PromptResult{Text, Provider string; Usage extract.TokenUsage}, error)
  vertical.Fetcher interface{ Fetch(ctx, fetch.FetchRequest)
      (*fetch.FetchResponse, error) }                  // fetch.FetchResponse: HTML []byte,
                                                       // StatusCode int, Headers http.Header, FinalURL string
  MCP coercion: mcp.StringList, mcp.FlexInt, *mcp.FlexBool (see mcp/agent.go batch tool)
  CLI helper: fail(exitCode, format, args...)           // cli/shared.go
  Test patterns: fakeSitemapFetcher (crawl/sitemap_test.go:25), sleepyFetcher
  (crawl/sitemap_test.go:425), sitemapBudget injectable-var pattern; fake LLM
  provider via scrape.Deps.ExtractorFor seam (see scrape/summarize_test.go).

Data model rules (Go, match repo conventions):
- Plain structs, Options-in/Result-out like crawl.Options/crawl.Result. No
  interfaces with one implementation; no config flags beyond those specified.
- Entry{URL string; Lastmod, Title string} — omitempty in JSON.
- MapResult{URLs []string; Links []Entry; Source string; Truncated bool};
  URLs is derived from Links (compat); Source ∈ sitemap|crawl|sitemap+crawl.
- Validate everything pre-I/O: CompileScope before the first fetch; intent
  JSON parse before the first fetch; bad input ⇒ exit 2 (CLI) / error (MCP).
- Determinism: sort by (score desc, URL asc); never range a map into output.

FILES (full task detail + hour estimates in plan/phase-F.md §3):
1. crawl/sitemap.go — extend urlSet with Lastmod (`xml:"lastmod"`); add
   ListSitemapEntries(ctx, f, site) ([]Entry, bool, error); make
   ListSitemapURLs a thin wrapper. Existing tests must pass unmodified.
2. crawl/walk.go — walk(ctx, f, seed, scope, opts) ([]Entry, bool): in-memory
   frontier (no DB!), 4 workers, same-host only (Scope with SameHost:true),
   robots Allowed check per URL (disallowed = silent skip, never fetched),
   CrawlDelay → SetFloor, fetch with Browser field, SkipExtension drop,
   goquery a[href] harvest → ResolveURL → Canonicalize → Allows, longest
   anchor text wins as Title, caps: depth 2 / 200 fetches / walkBudget var
   (25s production) — budget/cap expiry ⇒ partial results + truncated=true.
   ~25 lines of resolve/normalize loop will resemble Frontier.ExtractLinks;
   duplicate it, do NOT refactor the DB-coupled frontier (ponytail comment).
3. crawl/map.go — Map orchestrator: sitemap phase → fallback gate
   (len < 10 && !NoFallback ⇒ walk, merge+dedupe on canonical URL, sitemap
   lastmod wins) → CompileScope filter → search filter+sort (score: 3×path
   hit, 2×anchor hit, 1×query hit; tokens lowercased, len≥2) → limit.
   intentSystemPrompt demands ONLY strict JSON {"include":[],"exclude":[],
   "keywords":[]}; one scrape.Prompt call, Purpose:"map"; merge with flags.
4. cli/map.go — add flags: --include/--exclude (repeatable URL globs),
   --limit, --search, --intent (implies LLM: also add --provider/--model/
   --max-cost mirroring cli/summarize.go), --no-fallback, --browser.
   Lines output stays URL-per-line; JSON adds links+source, keeps urls.
5. mcp/agent.go — MapIn gains include/exclude (StringList), limit (FlexInt),
   search (string), no_fallback (*FlexBool); MapOut gains links/source. NO
   intent field (agents write globs themselves).
6. Tests per plan §3 T7 + each task's "done when"; httptest/fake fetchers
   only. Update README + spec.md map sections (note: intent costs ONE call,
   not one per URL).

SUCCESS CRITERIA: plan/phase-F.md §1 items 1–8, especially: byte-identical
map output for sitemap-complete sites; source field correct in all three
modes; robots-disallowed URLs never fetched (test proves it); deterministic
search order; one llm_calls row with purpose='map' per --intent run; the
full gate — `go build ./... && go test ./... && go vet ./... && gofmt -l .`
(empty) `&& golangci-lint run ./...` plus 3-way CGO_ENABLED=0 cross-builds —
green. Commit as phase-F-map on branch phase-F-map.
```

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — C/D/E merged on master (PRs #6–#9); CompileScope/Checker/HostLimiters/Prompt verified present in the tree.
- [PASS] Every sub-task has a clear, testable completion condition — each of T1–T7 ends in a "Done when" demo or test.
- [PASS] Execution prompt is self-contained — prior-phase context, confirmed API snippets with file locations, Go data-model rules, per-file guidance, and observable success criteria (a-c-e).
- [PASS] Exit criteria map 1:1 to deliverables — every file in §4 is exercised by at least one criterion; no untested deliverable.
- [PASS] Heavy external dependency has a stub strategy — no new deps; LLM calls go through the existing fake-provider seam; network through fake/sleepy fetchers (established patterns in crawl/sitemap_test.go).
- [PASS] New libraries have a confirmed usage snippet — none added; all snippets verified against the working tree this session.
