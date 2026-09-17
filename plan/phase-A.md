# Phase A — Output + Scoping + Quality (stdlib, no API breaks)

**Duration:** Days 1–5 (~21 hours)
**Depends on:** Phase 3 (shared `scrape.Run` flow, `clean.Clean` trafilatura pipeline, `StaticFetcher`, MCP `scrape_url`, `testdata/clean` goldens)
**Blocks:** Phase B (vertical extractors consume `CleanedPage` metadata + quality gate), Phase C (new MCP tools reuse `--format` + coercion patterns), Phase D (crawl scope filters extend A2 scoping)
**Risk Level:** MEDIUM — touches the `clean` output contract, fetch error paths, and CLI/MCP surfaces, but every change is additive (new struct fields, new format values, new exit code 8) with hermetic tests; no new dependencies, no signature changes to exported functions.
**Stack:** `go` — NOTE (per `AGENTS.md`): phase-plan/run-phase skills only accept `python|nextjs|react|typescript`, so `run-phase` will hard-block on this file. Execute this phase manually via the Execution Prompt at the end.

> Section count: MEDIUM risk → 6 sections (no "What Failure Looks Like"; fallbacks live inline in Tasks).

**Library research:** no `web_search` — every item is stdlib plus `PuerkitoBio/goquery`, already pinned and imported by `clean/`. One go.mod promotion: `github.com/andybalholm/cascadia` (already in the tree as a goquery dependency) becomes direct for A2's selector pre-validation — one line, no new supply chain. (`header profiles` are plain `map[string]string` bundles on stock TLS — explicitly NOT uTLS impersonation, dep-gated Phase E1.)

---

## 1. Objective + What Success Looks Like

Implement gap-spec §5 Phase A (A1–A5): token-optimized `llm`/`text`/`json` output formats, user scoping flags (`--include/--exclude/--only-main-content`), fetch hardening (cookies + header profiles + challenge warmup retry), a typed quality gate, and rich page metadata — all stdlib, all backward-compatible (existing callers, golden files, and exit codes 0–7 keep working untouched).

1. [`go test ./clean/ -v` exits 0: `ToLLMText` golden on 3 fixtures proves token reduction (llm-bytes < markdown-bytes on each) with zero link-target loss (`## Links` covers every href in the markdown body)]
2. [`go test ./clean/ -run 'TestScope|TestQuality|TestMetadata' -v` exits 0: include-only returns scoped markdown, exclude strips nav, invalid selectors warn-and-skip; "article mentioning Just a moment" does NOT trip the challenge detector (negative test)]
3. [`go test ./fetch/ -v` exits 0: httptest challenge→homepage-warmup→success round-trip passes and the server observes the `Cookie` header on retry; `chrome`/`firefox` profiles assert distinct `User-Agent`/`Sec-CH-UA` server-side]
4. [`magpie scrape --page-format llm <local fixture URL>` prints the `llm` envelope (metadata header + deduped `## Links` + gated `## Structured Data`); `--page-format json` prints the page envelope (today's fields plus metadata/links/images); blocked/challenge pages exit 8 with a typed message, never empty markdown]
5. [`go test ./...` exits 0 (no network/browser/keys); `go vet` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean]
6. [`CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64]

---

## 2. Key Design Decisions

### Where Phase A plugs into the existing system

```
fetch.StaticFetcher (+profiles/cookies/challenge A3) ──► clean.Clean (unchanged signature)
   │                                                          │  ├─ scope filter (A2, goquery pre-pass)
   │                                                          │  ├─ trafilatura + recovery-unchanged
   │                                                          │  ├─ quality gate (A4, counts words, classifies)
   │                                                          │  └─ metadata harvest (A5, goquery meta/OG tags)
   ▼                                                          ▼
scrape.Run ──► Render(CleanedPage, format) (A1: markdown|llm|text|json)
   │              ├─ CLI: magpie scrape --page-format markdown|llm|text|json (flag-only, never enters Config)
   │              ├─ crawl writer: new per-record string rendering (jsonl/json/csv/sqlite untouched)
   │              └─ MCP scrape_url: +format/cookies/profile inputs, quality errors as tool errors
```

### Decisions (with gap-spec refs)

- **Additive only, with one explicit behavior change (A4 — see deviations below).** `Clean(ctx, RawPage) (CleanedPage, error)`, `scrape.Run`, `StaticFetcher.Fetch`, and all MCP tool names keep their signatures. `CleanedPage`/`RawPage` gain fields, `scrape.Options`/`Result` gain fields, `ScrapeIn` gains optional fields. Existing goldens (`testdata/clean/*.md`) must pass byte-identical — `Clean` output for default paths does not change. The single exception: A4 replaces the non-2xx early return in `scrape/fetchURL` with a pass-through to classification, so the old `fetch: HTTP %d` error text becomes typed quality errors (no test locks the old text — A0 verifies this).
- **A1: `Render` is a pure function on `CleanedPage`, not a pipeline stage.** `func Render(p CleanedPage, format string) (string, error)` in `clean/llm.go` handles `markdown` (identity: `p.Markdown`), `llm` (`ToLLMText`), `text` (`ToText`), `json` (marshal envelope `{url,final_url,title,metadata,content,links,images,structured_data,word_count}`). `scrape.Result` gains `LLMText/Text/Rendered + Format` (or just `Rendered string` + resolved `Format`); CLI and MCP call `Render` at the edge. `json` here is the *page envelope* — distinct from crawl's record `jsonl` writer, which is untouched.
- **`ToLLMText` ports webclaw `to_llm_text` section order verbatim:** metadata header → body cleanup (strip bold/italic markers, drop decorative images, collapse logo runs, merge stat lines, strip CSS-class noise lines, dedup paragraphs/headings, drop pagination/comment links) → deduped `## Links` footer → gated `## Structured Data` (≤16 KB, drop `WebSite`/`WebPage` chrome blocks, scrub `articleBody`/long-text dupes already in body). `ToText` = `strip_markdown` (regex strip of `*#>\`[]()!` markers + link→text). `ponytail:` token counts are `len/4` chars like existing `capTokens` (ceiling = ±20% budget error; upgrade = real tokenizer dep — rejected, needs approval).
- **A2: scoping is a goquery pre-pass on raw HTML *before* trafilatura, applied inside `clean` via a new `Scope` param struct — but `Clean`'s signature is frozen, so scope travels as fields on `RawPage`** (`Include, Exclude []string`, `OnlyMainContent bool`; empty = today's behavior, zero-cost). Semantics: `OnlyMainContent` = trafilatura as-is (it already extracts main content — flag is a no-op marker for API parity, documented as such); `Include` = replace doc with the union of matched subtrees (no match → empty + warning, NOT an error); `Exclude` = remove matched nodes (exclude wins over include). Selectors are pre-validated with `cascadia.Compile`: invalid → warn-and-skip, cap 100 per list. Flags: `magpie scrape --include/--exclude/--only-main-content` + `ScrapeIn` fields. Crawl does NOT get scoping in this phase (Phase D owns crawl filters).
- **A3: profiles are header bundles, stock TLS stays.** `fetch/profiles.go`: `var HeaderProfiles = map[string]map[string]string{"default": ..., "chrome": {...}, "firefox": {...}}` (realistic UA + `Accept` + `Accept-Language` + `Sec-CH-UA`/`Sec-Fetch-*` for chrome). `FetchRequest` gains `Profile string` + `Cookies string` (raw `Cookie` header value, passed through verbatim — no jar surgery). Challenge detect: `IsChallengePage(body, headers, status)` = body <15 KB AND (status ∈ {401,403,429,503} OR title/marker match: `just a moment`, `attention required`, `_abck`, `akamai`, `cf-chl`, `__cf_chl`, `datadome`, `perimeterx`, `captcha`). On challenge AND profile-fetch: one homepage GET (scheme+host of target, jar persists `Set-Cookie`), then exactly one retry of the original URL. `Retry-After` handling already exists — verify, don't rebuild. `ponytail:` single-proxy env passthrough already works via `http.ProxyFromEnvironment` on the transport (Phase D5 formalizes `GOMAGPIE_PROXY` docs).
- **A4: quality is computed in `clean`, enforced at the edges.** `clean/quality.go`: `type Issue string` (`""` = ok, `empty`, `access-denied`, `unavailable`, `login-required`) + `func Classify(p CleanedPage, statusCode int, body []byte) Issue`. Rules: <200 scored words + body-has-more-text → `empty` (retry signal, not fatal inside clean); title/marker match WITHOUT the word-count guard → the specific issue; the <200-word guard applies to challenge classification too (this is what stops the "article mentioning Just a moment" false positive — markers only count when content is thin). `scrape.Run` maps non-empty `Issue` to sentinel errors (`clean.ErrQuality` wrapping the `Issue`); CLI maps to **new exit code 8** (`quality blocked: access-denied for <url>` — additive, 0–7 matrix untouched); MCP returns it as a tool error; crawl counts the page as errored (never caches, never writes a record). No retries inside the gate — one classification, loud result.
- **A5: metadata rides on `CleanedPage` (new fields, omitempty JSON).** `clean/metadata.go`: `func HarvestMetadata(html []byte, pageURL string, markdown string) Metadata` (bytes in — same shape as the other helpers; `pageURL` resolves favicon to absolute) — `description` (meta name/og), `author` (meta/JSON-LD fallback), `date` (`article:published_time`/`<time datetime>`), `lang` (`<html lang>`), `site_name` (og:site_name), `image` (og:image), `favicon` (`link[rel~=icon]`), `word_count` (words in final markdown). Surfaced in the `llm` header block and the `json` envelope. Parse budget: with A5 `Clean` parses up to 4× per page (title, sidecar, metadata, scope). A5 first attempts hoisting one shared `*goquery.Document` in `Clean` — if the diff stays ≤ ~15 lines do it, else keep the duplicate parses and leave a `ponytail:` comment (parse cost is noise next to one fetch).
- **Deliberate deviations from gap-spec (explicit per Phase 3 precedent).** (a) A2 scopes as a raw-HTML *pre*-pass, not post-trafilatura: trafilatura strips nav and unwraps the article, so a post-pass would see too little DOM to scope against. (b) `--only-main-content` is a no-op marker — trafilatura already returns main content, so the flag documents intent and keeps CLI/MCP parity. (c) A1 ships `--page-format`, not `--format`: the name is taken by the shared `config.Config.Format` envelope selector (crawl validates it at `cli/crawl.go:90`), and `json` already names today's envelope. (d) A4 changes `fetchURL` non-2xx behavior (pass-through to classification) — the only non-additive change in the phase.
- **Test strategy per testing.md:** goldens under `testdata/llm/` (3 fixtures: article, product, SPA-shell — reuse `testdata/clean/*.html` inputs, new `.llm.md`/`.txt` expectations) + `testdata/quality/` (challenge-page HTML table); httptest for A3; golden regeneration via the existing `-update` flag pattern.

---

## 3. Tasks

### Task A0 — Scaffolding + fixture inventory (1h)

Goal: confirm exactly which fixtures exist and which `Clean` paths the goldens lock, so A1–A5 tests reuse inputs instead of inventing new ones.

Inventory `testdata/clean/` (article/product/spa-shell pairs), read `clean/clean_test.go` for the `-update` pattern, and confirm the exit-code matrix in `cli/root.go` (`fail(code,…)` takes arbitrary ints; 1–7 in use, 8 free). Also grep-confirm no test asserts the old non-2xx error text (`fetch: HTTP %d` — A4 deletes that return). No code changes except what later tasks need. Decide: new goldens live in `testdata/llm/` + `testdata/quality/` (new dirs, per gap-spec §7) reusing `testdata/clean/*.html` as inputs where possible.

**Sanity check:** `go test ./clean/ ./fetch/ ./scrape/ 2>&1 | tail -5` exits 0 before touching anything.

### Task A1 — `clean`: `ToLLMText` + `ToText` + `Render` + `--page-format` wiring (6h)

**Depends on:** A0.

Goal: `magpie scrape --page-format llm|text|json` works end-to-end; `markdown` stays byte-identical.

New file `clean/llm.go` (pure functions, no I/O):

```go
// clean/llm.go
func ToLLMText(p CleanedPage) string   // metadata header → cleaned body → ## Links → gated ## Structured Data
func ToText(markdown string) string    // strip_markdown: markers/links → plain text
func Render(p CleanedPage, format string) (string, error) // markdown|llm|text|json; "" = markdown
```

Port order inside `ToLLMText` (mirror webclaw sections): (1) `# title` + `> desc/author/date` header from `p.Metadata` (A5 struct — define the struct in A5 but code defensively: empty metadata = header omitted, so A1 works standalone); (2) body passes: drop image lines with empty alt, strip `**`/`*`/backticks (keep link text), collapse ≥2 consecutive logo/site-name lines, merge single-number+label stat line pairs, drop lines that are pure CSS classes (`.`-prefixed tokens / `{...}` blobs), dedup paragraphs+headings preserving order, drop pagination/comment links (`/page/\d`, `#comments`, `reply`); (3) `## Links` — dedupe by URL, `[text](url)`; (4) `## Structured Data` — pretty JSON capped at 16 KB, dropping top-level `WebSite`/`WebPage` blocks and any string value >500 chars already contained in the body (articleBody dupes). `ToText`: regex-strip headings/emphasis/code-fences/blockquotes, `![alt](u)`→drop, `[t](u)`→`t`, collapse whitespace.

Wiring (each a few lines): `scrape.Options` += `PageFormat string`; `scrape.Result` += `Rendered string`; `scrape.Run` calls `clean.Render` once before return (default `""`→`markdown`, and `Markdown` field stays populated regardless). `cli/scrape.go`: new flag-only `--page-format markdown|llm|text|json` (flag-only on purpose — it must NOT enter shared `config.Config.Format`, which crawl validates at `cli/crawl.go:90`; a `format: llm` config file would otherwise make crawl exit 2). Schema-less branch prints `res.Rendered`; schema branch keeps `extractedDoc` — extraction output shape unchanged. The `json` page value is a SUPERSET of today's `markdownDoc` envelope: keep `url, final_url, title, markdown, structured_data` verbatim and add `page_format, content, metadata, links, images, word_count` — no field is renamed or removed. Crawl writer untouched: `jsonRecord` stays the single envelope home (verified — no page-content field exists there; gap-spec's "wire through crawl writer" is satisfied by `scrape.Run` + `scrape_url`, where page content surfaces). MCP `ScrapeIn` += `page_format string`; `ScrapeOut` += `content` field carrying the rendered text when no schema (keep `markdown` field populated for backward compat — deprecated in docs, removed never in this phase).

Goldens: `testdata/llm/{article,product,spa-shell}.{llm.md,txt}` generated with `-update`, then hand-verified: assert `len(llm) < len(md)` per fixture in-test (not just golden equality) + a link-preservation test (every `](http` target in md appears in `## Links`).

**Sanity check:** `go test ./clean/ -run 'TestLLM|TestText|TestRender' -v` exits 0; `go test ./testdata/...` n/a.

### Task A2 — Scoping: `RawPage.Scope` + `--include/--exclude/--only-main-content` (3h)

**Depends on:** A0 (independent of A1; can run parallel).

Goal: scoped scrapes return scoped markdown; invalid selectors never fail the scrape.

`clean/scope.go`:

```go
// clean/scope.go
type Scope struct {
    Include         []string
    Exclude         []string
    OnlyMainContent bool
}
func ApplyScope(html string, s Scope, warn func(string)) string
```

`ApplyScope`: parse with goquery; first pre-validate every selector with `cascadia.Compile` (promoted to direct dep — one go.mod line): compile failure → warn `"invalid selector %q, skipped"` and drop it. Rationale: `Find` never fails — it returns zero nodes for `??bad[[` exactly like a valid selector matching nothing — so without the pre-check the invalid-selector test is unbuildable. Valid-but-empty selectors stay in the union and simply contribute nothing. If `len(Include)+len(Exclude) > 100` each side is truncated to 100 with one warning. Exclude applied after include (exclude wins). No selectors + `OnlyMainContent` → input unchanged (trafilatura already main-contents; flagged as an explicit deviation in §2). `RawPage` += `Scope Scope` field; `Clean` calls `ApplyScope` first when scope is non-empty.

Wiring: `scrape.Options` += `Scope clean.Scope`; `cli/scrape.go` += `--include/--exclude` (comma-separated, `StringSlice`) + `--only-main-content` (bool); `ScrapeIn` += `include/exclude []string`, `only_main_content *bool`. Pre-pass (not post-trafilatura) is an explicit deviation — see §2(a). MCP strict-schema note: slices are fine (no coercion issue — that arrives in Phase C3).

Tests (hermetic, table-driven, no goldens needed): shell page (nav+article+footer) with include `article` → nav/footer gone; include union `article,aside` → both kept; exclude `nav,.sidebar` → article kept; invalid selector `??bad[[` → full content + "invalid selector" warning captured (assert the warning text proves the cascadia path, not a no-match); valid-no-match include `section.nope` → empty + "matched nothing" warning; only-main-content on shell → same as unscoped output.

**Sanity check:** `go test ./clean/ -run TestScope -v` exits 0.

### Task A3 — Fetch hardening: profiles + cookies + challenge warmup (4h)

**Depends on:** A0 (independent of A1/A2; can run parallel).

Goal: header-profiled fetches + cookie passthrough + one warmup retry, all proven by httptest.

`fetch/profiles.go`:

```go
// fetch/profiles.go
var HeaderProfiles = map[string]map[string]string{
    "default": {...existing defaultHeaders...},
    "chrome":  {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36", "Accept": ..., "Accept-Language": "en-US,en;q=0.9", "Sec-CH-UA": `"Chromium";v="126", ...`, "Sec-Fetch-Dest": "document", ...},
    "firefox": {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0", ...},
}
func IsChallengePage(body []byte, h http.Header, status int) bool
```

`FetchRequest` += `Profile string` (unknown → `default`, no error) + `Cookies string` (set as literal `Cookie` header). `StaticFetcher.Fetch`: resolve profile map, apply; on challenge-classified response AND `req.Profile != ""` attempt: GET `scheme://host/` (same client → jar warms), then exactly one re-GET of the original URL and return that response whatever it is (success or final failure — no loops). Challenge markers (lowercased substring on title+body): `just a moment`, `attention required`, `verify you are human`, `_abck`, `akamai`, `cf-chl`, `__cf_chl`, `datadome`, `perimeterx`, `captcha`, `access denied`; size+status pre-gate: `len(body) < 15*1024 && status ∈ {401,403,429,503}` OR marker hit on a <200-word page (shares the A4 guard philosophy). Also *verify* existing `Retry-After` handling (grep `backoff`/429 path in `crawl/backoff.go` + fetch retry) and note the result in the PR — no rebuild.

Wiring: `scrape.Options` += `Profile, Cookies string`; `cli/scrape.go` += `--header-profile chrome|firefox|default` + `--cookies "a=b; c=d"`; `ScrapeIn` += `profile, cookies` optional strings. Default profile everywhere = `default` (today's headers byte-identical — prove with existing fetch tests untouched).

Tests: httptest server with (a) `/challenge` returning 403 + `Just a moment` body on first hit, success on second hit iff request carried warmed cookie from `/` — assert retry happened and final body is the real page; (b) profile test asserting server-observed UA/Sec-CH-UA per profile; (c) unknown profile → default headers, no error.

**Sanity check:** `go test ./fetch/ -run 'TestProfile|TestChallenge|TestCookies' -v` exits 0.

### Task A4 — Quality gate: `clean/quality.go` + StatusCode plumbing + non-2xx pass-through + exit 8 (4h)

**Depends on:** A0 (independent of A1–A3 in code; integrate after).

Goal: blocked/empty pages produce typed errors everywhere, never silent empty markdown. Structural prerequisite (validator-confirmed): `scrape/fetchURL` (`scrape/scrape.go:168`) and the crawl fetch closure (`crawl/crawl.go:286`) reject non-2xx before `Clean` is reached, and `RawPage` carries no status — so a status-driven gate literally cannot fire without (a) plumbing the status through and (b) replacing the early return with a pass-through. Both are in this task, not later.

`clean/quality.go`:

```go
// clean/quality.go
type Issue string
const (
    IssueNone         Issue = ""
    IssueEmpty        Issue = "empty"
    IssueAccessDenied Issue = "access-denied"
    IssueUnavailable  Issue = "unavailable"
    IssueLoginRequired Issue = "login-required"
)
var ErrQuality = errors.New("quality blocked")
func Classify(p CleanedPage, statusCode int, body []byte) Issue
func WordCount(s string) int
```

`Classify` rules (order matters): status 401/407 + login markers (`sign in`, `log in to continue`, `login required`) → `login-required`; 403 + challenge/deny markers → `access-denied`; 429/503/5xx → `unavailable`; scored words (`WordCount(p.Markdown)`) < 200 AND body has substantially more text → `empty`; challenge markers with ≥200 scored words → `IssueNone` (the false-positive guard — "article mentioning Just a moment" stays clean). `Clean` itself does NOT fail on quality (keeps signature/behavior); it attaches `CleanedPage.Quality Issue` (new field, `""` = ok — zero value preserves old comparisons). `Clean` reads the status from a new `RawPage.StatusCode int` field (2xx callers leave it 0/200 — both classify identically; only non-2xx branches consult it).

Enforcement — scrape: delete the `resp.StatusCode/100 != 2` early return in `fetchURL` and return the response for ANY status (the §2(d) behavior change; A0 confirmed no test locks the old `fetch: HTTP %d` text). `scrape.Run` passes `StatusCode` into `RawPage`; non-empty `Quality` → wrapped `ErrQuality`, run finished `error`, NO cache write (assert the no-cache-write in test). 2xx behavior is byte-identical to today. Enforcement — crawl: in the `crawl/crawl.go` fetch closure, route non-2xx responses through the same `Clean`+`Classify` path so blocked pages record typed `Issue` errors on the existing page-error path (counted, not cached, no record); touch `core/pipeline.go` sink only if the error path requires it. Leave `crawl/backoff.go:classify` retry taxonomy (transport retries, `Retry-After`) untouched — quality classification happens on the outcome, not inside the retry loop. Edges: `cli` maps `ErrQuality` → **exit 8** `quality blocked (<issue>) for <url>` (new code, additive); MCP `scrape_url` returns it as a tool error (SDK surfaces it; no new envelope). No retries inside the gate — one classification, loud result.

Tests: table test incl. (i) 403+Akamai+thin → access-denied; (ii) rich article containing "Just a moment" → none; (iii) empty SPA shell → empty; (iv) login wall → login-required; (v) 503 → unavailable; (vi) scrape-level httptest 403+deny page → `ErrQuality` access-denied (proves the pass-through; old text gone); (vii) scrape quality failure writes no selector cache; (viii) crawl-level: quality page counted as error with zero records written.

**Sanity check:** `go test ./clean/ -run TestClassify -v && go test ./scrape/ -run TestQuality -v` exits 0.

### Task A5 — Metadata: `HarvestMetadata` on `CleanedPage` (3h)

**Depends on:** A0 (independent of A1–A4 in code; A1 consumes it — coordinate the struct name now).

Goal: every scrape carries description/author/date/lang/site/image/favicon/word_count.

`clean/metadata.go`:

```go
// clean/metadata.go
type Metadata struct {
    Description string `json:"description,omitempty"`
    Author      string `json:"author,omitempty"`
    Date        string `json:"date,omitempty"`
    Lang        string `json:"lang,omitempty"`
    SiteName    string `json:"site_name,omitempty"`
    Image       string `json:"image,omitempty"`
    Favicon     string `json:"favicon,omitempty"`
    WordCount   int    `json:"word_count,omitempty"`
}
func HarvestMetadata(html []byte, pageURL string, markdown string) Metadata
```

Sources: `meta[name=description]`, `og:description`, `meta[name=author]` (+ JSON-LD `author` fallback via existing sidecar), `article:published_time` / `time[datetime]`, `html[lang]`, `og:site_name`, `og:image`, `link[rel~=icon][href]` resolved against `pageURL` to absolute. `WordCount` = words in final (post-cap) markdown. `Clean` populates `CleanedPage.Metadata`; `ToLLMText` header + `json` envelope read it (A1 already codes against this struct — same shape, no rework). Attempt the §2 shared-parse hoist in this task (all byte-taking helpers are touched here anyway). Golden: `testdata/llm/metadata.json` for the article fixture (existing goldens stay byte-locked — no extension of current expectations).

**Sanity check:** `go test ./clean/ -run TestMetadata -v` exits 0.

---

## 4. Deliverables

```
plan/phase-A.md              # this plan
clean/llm.go                 # ToLLMText + ToText + Render (A1)
clean/scope.go               # Scope struct + ApplyScope pre-pass (A2)
clean/quality.go             # Issue enum + Classify + ErrQuality + WordCount (A4)
clean/metadata.go            # Metadata struct + HarvestMetadata (A5)
clean/clean.go               # += Scope/StatusCode on RawPage; += Metadata/Quality on CleanedPage; shared-parse hoist attempt (A2/A4/A5)
fetch/profiles.go            # HeaderProfiles + IsChallengePage (A3)
fetch/fetcher.go             # += Profile/Cookies on FetchRequest (A3; struct lives here, not http.go)
fetch/http.go                # warmup-retry in Fetch (A3)
scrape/scrape.go             # += PageFormat/Scope/Profile/Cookies options; Render call; non-2xx pass-through; quality→ErrQuality (A1–A4)
cli/scrape.go                # += --page-format (flag-only), --include/--exclude/--only-main-content, --header-profile, --cookies; exit 8 (A1–A4)
crawl/crawl.go               # non-2xx → Clean+Classify on page-error path (counted, not cached, no record); core/pipeline.go sink only if required (A4)
mcp/tools.go                 # ScrapeIn += page_format/include/exclude/only_main_content/profile/cookies; ScrapeOut += content (A1–A3)
go.mod                       # promote andybalholm/cascadia to direct (A2)
testdata/llm/                # {article,product,spa-shell}.{llm.md,txt} + metadata.json goldens (A1/A5)
testdata/quality/            # challenge/deny/login/empty HTML fixtures (A4)
clean/*_test.go              # TestLLMText (reduction+links), TestText, TestRender, TestScope, TestClassify, TestMetadata (A1/A2/A4/A5)
fetch/*_test.go              # TestProfiles, TestChallengeWarmup, TestCookies (A3)
scrape/*_test.go             # page-format passthrough, scope passthrough, quality no-cache-write (A1/A2/A4)
cli/*_test.go                # exit-8 mapping, new flags validation (A4/A1)
```

One-line annotations: every `clean/*.go` file is pure (no I/O, no globals); `fetch/profiles.go` is data + predicate; all edge behavior (exit codes, envelopes, caching) lives in `scrape`/`cli`/`mcp`, never in `clean`/`fetch`.

---

## 5. Exit Criteria

- [ ] `go test ./clean/ -v` exits 0 — `ToLLMText` goldens show byte-reduction + full link preservation on all 3 fixtures (covers `clean/llm.go`)
- [ ] `go test ./clean/ -run 'TestScope|TestClassify|TestMetadata' -v` exits 0 with the "Just a moment" negative passing (covers `clean/scope.go`, `clean/quality.go`, `clean/metadata.go`)
- [ ] Existing `testdata/clean/*.md` goldens pass byte-identical — default `Clean` output unchanged (covers `clean/clean.go` additive-only rule)
- [ ] `go test ./fetch/ -v` exits 0 — challenge→warmup→success + profile-UA + cookie tests green, existing fetch tests untouched (covers `fetch/profiles.go`, `fetch/http.go`)
- [ ] `go test ./scrape/ ./cli/ ./mcp/ -v` exits 0 — page-format/scope passthrough, quality no-cache-write, exit-8 mapping, crawl quality page counted as error with zero records (covers `scrape/scrape.go`, `cli/scrape.go`, `crawl/crawl.go`, `mcp/tools.go`)
- [ ] `testdata/llm/` + `testdata/quality/` goldens exist and are hand-verified (not just `-update` output) — reduction + link assertions live in-test, not eyeballed
- [ ] `magpie scrape --page-format llm|text|json` smoke-tested against a local fixture; challenge fixture exits 8 with a typed message (manual, 10 min)
- [ ] Full gates: `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass

---

## 6. Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are implementing Phase A of gomagpie (`magpie`) — output + scoping + quality, stdlib only, no API breaks. Spec of record: `plan/webclaw-gap-spec.md` §5 Phase A (A1–A5). Source of truth for pipeline behavior is `spec.md`; gap-spec defines only the deltas.

### What This Project Is
Go CLI web scraper (module `gomagpie`, Go 1.26+, binary `magpie`): fetch → clean → extract. `cmd/magpie/` is a 10-line shim; the cobra tree lives in importable `cli/`. Single-URL flow is shared `scrape.Run(ctx, Deps, url, Options) (Result, error)` in `scrape/` (CLI + MCP both call it; output formatting stays at the edges). Cleaning is `clean.Clean(ctx, RawPage) (CleanedPage, error)` (trafilatura → markdown, sidecar harvest, `capTokens` at 8k). Static fetch is `fetch.StaticFetcher` (stock TLS, one UA, cookiejar, 50 MB LimitReader, `http.ProxyFromEnvironment`). MCP tools (`scrape_url`, `crawl_site`, `extract_structured`, `get_cached_selectors`) live in `mcp/tools.go`. Exit codes 0–7 are taken (`cli/root.go: fail(code,…)`); cost ceiling → 6, missing key → 7. Tests are hermetic: goldens in `testdata/clean/`, httptest for fetch, fake Extractor, `gofmt`/`go vet`/`golangci-lint` + 3-way `CGO_ENABLED=0` builds.

### Established in Prior Phases
- `scrape/scrape.go`: `Options{Schema, Render, Provider, Model, MaxCost, UseCache}`, `Result{RunID, URL, FinalURL, Title, Markdown, StructuredData, Record, FromCache, Usage, Provider, Model}`; sentinel `ErrMissingKey`; cost-ceiling via `crawl.ErrCostCeiling`; selector-cache hit path with `UseCache`.
- `clean/clean.go`: `RawPage{HTML, URL, FinalURL, StructuredRaw}`, `CleanedPage{Markdown, StructuredData, Title, FinalURL}`; `MaxTokens=8000`, `capTokens` = len/4 naive counting (`ponytail:` ±20% error accepted).
- `clean/markdown.go` + `fallback.go`: trafilatura→HTML→markdown, `fallbackMarkdown` for SPA shells; goldens `testdata/clean/{article,product,spa-shell}.{html,md}` are byte-locked.
- `fetch/fetcher.go`: `FetchRequest{URL, Timeout}`, `FetchResponse{URL, FinalURL, StatusCode, HTML, Headers}`; `fetch/http.go`: `defaultHeaders` map (magpie UA, FR Accept-Language); `NewStaticFetcher` with jar + file:// transport.
- `fetch/detect.go`: `ScoreJSRequired`/`NeedsBrowser` (rod escalation, stays untouched).
- `mcp/tools.go`: `ScrapeIn{URL, Schema, Render, UseCache *bool}`, `ScrapeOut{URL, FinalURL, Title, Markdown, Extracted, FromCache, Usage}`.
- `crawl/writer.go`: `jsonRecord` is the single home for the JSON envelope; jsonl streams, json buffers (`ponytail:` RAM ceiling documented).
- Rules (AGENTS.md + .pi/rules/): stdlib + pinned goquery only, plus one go.mod line promoting `andybalholm/cascadia` to direct (A2); no live-network default tests; no CGO; never import go-rod outside `fetch/`; `ponytail:`-comment deliberate shortcuts with ceiling + upgrade path.

### Your Goal for This Phase
Ship A1–A5 (§5 of gap-spec): llm/text/json page formats, scoping flags, fetch hardening, quality gate, metadata — additive except A4's fetchURL pass-through (old `fetch: HTTP %d` text → typed quality errors; A0 confirms no test locks it).

### Data Model Rules (Go — follow exactly)
- API-boundary structs (CLI flags structs, `ScrapeIn/ScrapeOut`, `scrape.Options/Result`, `clean.RawPage/CleanedPage`): plain structs with `json` tags; additive fields only, `omitempty` where backward-compat matters.
- Internal logic: plain functions on value types (`func ToLLMText(p CleanedPage) string`); no interfaces with a single implementation; no new package (everything lands in `clean`, `fetch`, `scrape`, `cli`, `mcp`).
- Hot path (per-page clean/fetch): `Clean` parses up to 4× per page — attempt one shared-parse hoist in A5 (≤ ~15 lines) else `ponytail:`-comment it; no other per-page allocation growth.
- Errors: sentinels at the edge — `clean.ErrQuality` (wrap with `%w` + issue), CLI maps to exit codes (`fail(8, …)` new), MCP returns Go errors (SDK surfaces them). Never swallow: invalid selectors warn (injected `warn func(string)`) AND continue.

### Architecture
```
fetch.StaticFetcher (+profiles/cookies/challenge) → clean.Clean [scope pre-pass → trafilatura → Classify(status) → metadata harvest] → scrape.Run [non-2xx pass-through → ErrQuality, Render(PageFormat)] → edges (CLI flags+exit 8, MCP fields, crawl counts-not-caches)
```
Fail-safe rules: quality never fails inside `clean` (attaches `Issue`, returns content); enforcement happens in `scrape.Run` and the crawl fetch closure. Scoping with zero selectors = input unchanged. Challenge retry = exactly one, then return whatever arrived. `markdown` page-format = `p.Markdown` identity, always populated. `json` page envelope = today's `markdownDoc` fields verbatim + new ones (never rename/remove). `--page-format` is flag-only and never enters `config.Config.Format` (crawl's `jsonl|json|csv|sqlite` validation at `cli/crawl.go:90` stays untouched).

### Confirmed API Notes (one promotion, one verify-first)
- `cascadia.Compile` (promoted to direct dep in A2) distinguishes invalid selectors from valid-no-match — `goquery.Find` alone cannot (returns zero nodes for both, never panics). Confirmed pattern:
```go
// github.com/andybalholm/cascadia — pre-check per selector:
if _, err := cascadia.Compile(expr); err != nil {
    warn(fmt.Sprintf("invalid selector %q, skipped", expr))
    continue
}
// doc.Find(expr) now known-valid
```
- `http.Header` + `cookiejar` already on the client: homepage warmup is just another `client.Do` GET — jar persistence is automatic. No new API surface.
- `ScrapeOut` keeps `markdown` populated AND adds `content` (rendered format) — strict clients never break.

### Files to Create/Change
Per-file guidance — implement in task order A0→A5 (A1/A2/A3/A5 are code-independent; integrate A4 last):

#### clean/llm.go (NEW, A1)
Pure functions `ToLLMText(p CleanedPage) string`, `ToText(md string) string`, `Render(p CleanedPage, format string) (string, error)`. Section order: metadata header → body cleanup passes (strip bold/italic/code markers; drop empty-alt images; collapse logo runs; merge stat lines; strip CSS-class lines; dedup paragraphs/headings; drop pagination/comment links) → deduped `## Links` → `## Structured Data` gated at 16 KB (drop WebSite/WebPage blocks, scrub >500-char strings already in body). `ToText` = regex strip_markdown. `Render`: `""`/`markdown` = identity; unknown format = error naming valid values. Read `p.Metadata` defensively (empty = skip header). The `json` value marshals the page envelope: today's `markdownDoc` fields (`url, final_url, title, markdown, structured_data`) PLUS `page_format, content, metadata, links, images, word_count`. Do NOT touch `Clean`.

#### clean/scope.go (NEW, A2) + clean/clean.go (Scope field + pre-pass)
`Scope{Include, Exclude []string, OnlyMainContent bool}`, `ApplyScope(html string, s Scope, warn func(string)) string`. Pre-validate each selector with `cascadia.Compile` (invalid → warn "invalid selector, skipped"; valid-no-match → warn "matched nothing" but still applies). Cap 100 per list, exclude-wins, union of include matches, empty-scope = identity, no-match include = empty + warning (not error). `RawPage` += `Scope Scope`; `Clean` applies pre-pass when non-empty. Pre-pass (not post-trafilatura) and the `OnlyMainContent` no-op are explicit §2 deviations. Warn via injected `warn func(string)` for testability.

#### fetch/profiles.go (NEW, A3) + fetch/http.go (Profile/Cookies + retry)
`HeaderProfiles` map (default = today's headers verbatim; chrome + firefox bundles with realistic UA/Accept/Sec-Fetch-*), `IsChallengePage(body, headers, status) bool` (<15 KB + status/marker pre-gate; word-count guard philosophy shared with A4). `FetchRequest` (struct lives in `fetch/fetcher.go`) += `Profile, Cookies string`. `Fetch` in `http.go`: apply profile (unknown → default), set `Cookie` header verbatim, single homepage-warmup + one retry on challenge. Then grep-verify existing `Retry-After`/429 handling and note it in the PR — do not rebuild it.

#### clean/quality.go (NEW, A4) + scrape/scrape.go + crawl/crawl.go (enforcement)
`Issue` string enum + `Classify(p CleanedPage, statusCode int, body []byte) Issue` + `WordCount` + `ErrQuality`. Rule order: login-required → access-denied → unavailable → empty (<200 words + body-richer) → none (markers on rich content = none). `CleanedPage` += `Quality Issue`; `RawPage` += `StatusCode int`. `Clean` classifies but never fails. `scrape/fetchURL`: delete the non-2xx early return, return the response for any status; `Run` maps non-empty quality → wrapped `ErrQuality`, no cache write. `crawl/crawl.go` fetch closure: same Clean+Classify routing onto the page-error path (counted, not cached, no record); `backoff.go:classify` retry taxonomy untouched; `core/pipeline.go` sink only if the error path requires it. CLI → exit 8. MCP → tool error.

#### clean/metadata.go (NEW, A5) + clean/clean.go (populate)
`Metadata{Description, Author, Date, Lang, SiteName, Image, Favicon, WordCount}` + `HarvestMetadata(html []byte, pageURL string, markdown string) Metadata` (meta/og/time/link-icon sources, favicon→absolute via `pageURL`, WordCount on final markdown). `CleanedPage` += `Metadata Metadata`. Attempt the §2 shared goquery-parse hoist here (all byte-taking helpers are in flux anyway); ≤ ~15 lines or `ponytail:`-comment the 4-parse cost. Do NOT touch goldens.

#### scrape/scrape.go, cli/scrape.go, mcp/tools.go (wiring, A1–A4)
`scrape.Options` += `PageFormat string; Scope clean.Scope; Profile, Cookies string`; `Result` += `Rendered string`. `Render` called once in `Run`. CLI flags: `--page-format` (flag-only — never enters `Config`), `--include/--exclude StringSlice`, `--only-main-content`, `--header-profile`, `--cookies`. `ScrapeIn` += `page_format/include/exclude/only_main_content/profile/cookies`; `ScrapeOut` += `content`. Crawl writer UNTOUCHED (no page-content field exists — `jsonRecord` stays the single envelope home).

#### Tests + goldens
`testdata/llm/{article,product,spa-shell}.{llm.md,txt}` + `metadata.json` (reuse `testdata/clean/*.html` inputs; `-update` then hand-verify + in-test reduction/link assertions). `testdata/quality/` HTML fixtures. `clean/*_test.go`, `fetch/*_test.go` (httptest warmup/profile/cookies), `scrape/*_test.go` (passthrough + no-cache-write), `cli/*_test.go` (exit 8 + flag validation). Existing goldens must stay byte-identical.

### Success Criteria
- `go test ./clean/ -v` exits 0 with reduction + link-preservation + "Just a moment" negative all green
- `go test ./fetch/ ./scrape/ ./cli/ ./mcp/ -v` exits 0; existing goldens byte-identical
- `magpie scrape --page-format llm <local fixture>` renders the envelope; challenge fixture exits 8
- Full gates: `go test ./...` && `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass

### Expected File Structure at End
```
clean/llm.go clean/scope.go clean/quality.go clean/metadata.go (+ clean.go fields + StatusCode/Scope on RawPage)
fetch/profiles.go (+ Profile/Cookies on FetchRequest in fetch/fetcher.go, retry in http.go)
scrape/scrape.go cli/scrape.go crawl/crawl.go mcp/tools.go (wiring + enforcement only)
testdata/llm/ testdata/quality/ (new goldens)
go.mod (cascadia → direct)
```

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available (`scrape.Run`, `clean.Clean`, `StaticFetcher`, `ScrapeIn/Out`, `testdata/clean` goldens, exit-code matrix — all read in source, §6 prompt inlines them)
- [PASS] Every sub-task has a clear, testable completion condition (each task ends with a Sanity check one-liner; §5 exit criteria map 1:1 to deliverables)
- [PASS] Execution prompt is self-contained: includes (a) what prior phases established (struct shapes + sentinel patterns inline), (b) a confirmed `cascadia.Compile` snippet (one go.mod promotion, no new supply chain), (c) a "Data Model Rules" section, (d) per-file guidance, and (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables (8 criteria covering 4 new clean files, fetch changes, wiring, goldens, smoke test, full gates)
- [PASS] Any heavy external dependency has a fake/stub strategy noted (no heavy deps — stdlib + pinned goquery only; browser/LLM/network explicitly excluded from tests per testing.md)
- [PASS] New libraries have a confirmed usage snippet in the execution prompt (N/A — zero new libraries; stated explicitly at top of plan and in prompt)
