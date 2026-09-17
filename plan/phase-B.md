# Phase B — Zero-LLM Structured Data: Vertical Extractors (stdlib)

**Duration:** Days 1–5 (~20 hours)
**Depends on:** Phase A (`clean.Clean` + `HarvestSidecar`/`WordCount`/`Classify`, `scrape.Run` shared flow, `fetch.StaticFetcher` + `FetchRequest{Profile,Cookies}`, `testdata/clean` goldens)
**Blocks:** Phase C (`list_extractors` + `vertical_scrape` MCP tools and the standalone `magpie vertical` CLI command are thin wrappers over this phase's registry — explicitly NOT built here), Phase D (crawl fast-path reuse, if wanted)
**Risk Level:** MEDIUM — one brand-new leaf package (`vertical/`) plus an additive `scrape.Options` field and a narrow `clean` append-rule; no signature changes, no new deps, one deliberate golden regeneration (B5, owned in-task).
**Stack:** `go` — NOTE (per `AGENTS.md`): phase-plan/run-phase skills only accept `python|nextjs|react|typescript`, so `run-phase` will hard-block on this file. Execute this phase manually via the Execution Prompt at the end.

> Section count: MEDIUM risk → 6 sections (no "What Failure Looks Like"; fallbacks live inline in Tasks).

**Library research:** no `web_search` for libraries — zero new dependencies; everything is stdlib (`encoding/json`, `encoding/xml`, `net/url`, `strings`) plus pinned `goquery` (HTML) and the existing `fetch`/`clean` packages. Two endpoint facts confirmed via web search 2026-09-17 and baked into the tasks below: (1) crates.io API policy **requires a `User-Agent` header** (else 403) — `StaticFetcher` already sends one, tests assert it; (2) HN item endpoint is `GET https://hn.algolia.com/api/v1/items/{id}` returning `{id, title, url, points, author, children[]}`. All other endpoint shapes (GitHub REST, PyPI `/pypi/{name}/json`, npm registry doc, arXiv Atom, YouTube oEmbed, Shopify `/products/{handle}.js`) are long-stable public APIs; fixtures use hand-authored minimal subsets, so exact-field drift is absorbed by construction.

**Scope reconciliation with gap-spec §5 B1:** the spec says "first 8 by ROI" then lists ~13 names. GitHub's four shapes (`repo/issue/pr/release`) are one extractor with a `kind` field, and PyPI/npm/crates share one fetch helper — so all high-ROI names fit in 6 small files (~10 names total). Splitting them across two phases would cost more scaffolding than it saves; ship them together, each <120 lines.

---

## 1. Objective + What Success Looks Like

Implement gap-spec §5 Phase B: a `vertical/` package of zero-LLM typed extractors (registry JSON APIs + HTML/API fast paths) with strict auto-dispatch in `scrape.Run` before any LLM call, explicit `--vertical name` selection with URL-mismatch errors, and `clean` island/player fast paths that rescue thin SPA-shell pages — all stdlib, all backward-compatible (schema-less markdown output, existing goldens except one deliberate regeneration, and exit codes 0–8 keep working).

1. [`go test ./vertical/ -v` exits 0: `TestList` asserts the exact 10-name extractor list; every extractor has a table test replaying a `testdata/vertical/` fixture through a fake fetcher with zero network; dispatch-order test proves `shopify_product`/`ecommerce_product` never auto-match a generic URL]
2. [`go test ./scrape/ -run TestVertical -v` exits 0: auto-dispatch hit returns a `Record` with the fake `ExtractorFor` rigged to fail if called (proves zero-LLM); auto-miss falls through to today's path; explicit mismatch returns `ErrURLMismatch`; extractor fetch errors propagate as hard errors, never silent LLM fallback]
3. [`go test ./clean/ -run TestIslands -v` exits 0: island-bearing SPA shell gains appended text when scored words <200; the same page with scope applied is unchanged; rich articles are byte-identical (existing goldens pass except `spa-shell.md`, regenerated once in B5 and hand-verified)]
4. [`magpie scrape --vertical auto <github repo URL fixture via local replay>` returns the repo record with no API key configured (proves the zero-LLM path end-to-end); `--vertical reddit <github URL>` exits 2 with a URL-mismatch message]
5. [`go test ./...` exits 0 (no network/browser/keys); `go vet` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean]
6. [`CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64]

---

## 2. Key Design Decisions

### Where Phase B plugs into the existing system

```
scrape.Run ──► vertical.MatchURL(url) ──► Extract via StaticFetcher ──► Result{Record, Vertical} (no LLM, no cache)
   │                 │ strict hosts only                          ▲ explicit: --vertical name (Lookup + Match check → ErrURLMismatch)
   │                 │ (shopify/ecommerce = OptIn, never auto)    │
   │                 └─ miss → today's path untouched ────────────┘
   ▼
clean.Clean ──► trafilatura ──► thin (<200 words, unscoped) ──► append IslandText(sidecar) + YT player details
```

### Decisions (with gap-spec refs)

- **Registry shape (B1): one struct, two lookup paths.** `vertical/vertical.go` holds `Info{Name, Label, Desc, Patterns []string}`, `Extractor{Info, Match func(*url.URL) bool, Extract func(ctx, Fetcher, *url.URL) (map[string]any, error), OptIn bool}`, plus `List() []Info`, `Lookup(name) (Extractor, bool)`, `MatchURL(rawURL) (Extractor, bool)` (skips `OptIn` extractors — mirrors webclaw's opt-in-only permissive matchers). `Patterns` are human-readable example URL shapes for `List()` output (Phase C renders them), NOT regexes — matching is code (`host + path-prefix` checks), so there is no ReDoS surface and no glob-validation machinery to port.
- **Fetcher reuse, not a new client.** Extractors take `type Fetcher interface { Fetch(ctx, fetch.FetchRequest) (*fetch.FetchResponse, error) }` — satisfied by `*fetch.StaticFetcher` directly (import is one-way: `fetch` never imports `vertical`). `Profile`/`Cookies` from `scrape.Options` flow into the `FetchRequest`, so header profiles and challenge warmup apply to vertical fetches for free. No browser escalation inside verticals (API/HTML-first by construction; truly-JS targets fall through to the normal render path when `auto` misses — stated non-goal, not silent behavior).
- **Dispatch semantics in `scrape.Run` (`Options.Vertical`: `""` off, `"auto"`, or a name).** `""` = today's behavior bit-for-bit (default everywhere — CLI flag default empty, MCP untouched until Phase C). `"auto"` hit → `Result{Record, Vertical: name}`, `FromCache=false`, zero LLM calls, **no selector-cache read or write** (vertical output isn't selector-derived; caching it would poison schema-keyed lookups — explicit rule). `"auto"` miss → proceed exactly as today. Explicit name: unknown name = usage error; known name + `!Match(url)` = sentinel `ErrURLMismatch` (CLI exit 2, message `vertical %q does not handle %s`); extractor fetch/parse error = hard error returned as-is (no LLM fallback — fallback would hide outages and confuse cost accounting; fail loudly per AGENTS.md).
- **Output shape: plain `map[string]any`, fixed picked fields, no validation.** Verticals return small maps (`title`, `author`, `stars`, `price`…); numbers stay numbers (no string coercion — the stringy-client problem belongs to Phase C3's MCP layer, not here). `scrape.Result` gains `Vertical string` (extractor name, `""` when unused). Schema + vertical combined: vertical wins, schema is ignored with no error (documented; combining them is a Phase C UX question, not a B blocker).
- **B2 fast paths: append-only, thin-only, unscoped-only.** `clean/islands.go`: `IslandText(sidecar json.RawMessage, maxChars int) string` walks sidecar JSON leaf strings (drop <40-char noise, dedupe, cap ~4 KB) and `PlayerDetails(html []byte) map[string]string` extracts `ytInitialPlayerResponse` → `videoDetails{title, author, shortDescription, viewCount}` via balanced-brace scan (Go regexp has no lookahead — Phase A precedent: hand-scan, don't regex). `Clean` appends island/player text iff `len(Scope.Include)+len(Scope.Exclude)==0 && WordCount(md) < 200`. LinkedIn gets NO bespoke parser — its embedded JSON already lands in the sidecar, so island-append covers it (explicit non-goal, stated here so nobody builds it). **Golden impact (own it):** `testdata/clean/spa-shell.md` will change if the fixture carries islands — B5 regenerates it deliberately with hand-verification; article/product goldens must stay byte-identical (they're rich, so the rule can't fire).
- **Fixtures are hand-authored minimal subsets, never recorded live traffic** (testing.md: no live network). Each fixture keeps just the fields its extractor reads, plus one unknown-field blob to prove forward tolerance. Tests use a fake `Fetcher` (map URL-substring → fixture bytes) — lazier than httptest and fully hermetic; one httptest test in B4 proves a real `StaticFetcher` satisfies the `vertical.Fetcher` interface (compile-time assertion `var _ Fetcher = (*fetch.StaticFetcher)(nil)` covers the rest).

---

## 3. Tasks

### Task B0 — Scaffolding + contract inventory (1h)

Goal: lock the extractor list, output field names, and the exact `scrape.Run` insertion point before writing extractors.

Read `scrape/scrape.go` `Run` (insertion: after `cleaned` quality gate, before the `o.Schema == nil` early return — verticals serve both schema and schema-less callers), `fetch/http.go` default headers (confirm UA string for the crates.io test assertion), and `clean/clean.go` `HarvestSidecar` (B2/B3-ecommerce reuse). Confirm no test asserts schema-less output shape beyond markdown (grep `Rendered`/`Record` in `scrape/*_test.go`, `cli/scrape_test.go`). Create empty `vertical/vertical.go` with the types + `testdata/vertical/` dir. Decide final field names per extractor now (they're asserted in tests, renames later are churn): github `{kind, full_name, description, stars, forks, language, license, open_issues, url}`, pypi `{name, version, summary, author, requires_python, url}`, npm `{name, version, description, latest, url}`, crates_io `{name, newest_version, description, downloads, url}`, reddit `{kind, title, author, score, comments, selftext, url}`, hackernews `{kind, title, url, points, author, comments, text}`, arxiv `{title, authors[], summary, published, url}`, youtube `{title, author, description, views, duration_s, url}`, shopify_product `{title, vendor, price, currency, images[], url}`, ecommerce_product `{name, brand, price, currency, availability, url}`.

**Sanity check:** `go test ./scrape/ ./clean/ ./fetch/ 2>&1 | tail -3` exits 0 before touching anything.

### Task B1 — Registry + GitHub + registry APIs (pypi/npm/crates.io) (5h)

**Depends on:** B0.

Goal: the registry skeleton plus the four pure-JSON extractors live, with the crates.io UA assertion proving header flow.

`vertical/vertical.go`:

```go
// vertical/vertical.go
type Info struct {
    Name, Label, Desc string
    Patterns          []string // example URL shapes, human-readable only
}
type Fetcher interface {
    Fetch(ctx context.Context, req fetch.FetchRequest) (*fetch.FetchResponse, error)
}
type Extractor struct {
    Info    Info
    Match   func(u *url.URL) bool
    Extract func(ctx context.Context, f Fetcher, u *url.URL) (map[string]any, error)
    OptIn   bool // true = explicit-only, skipped by MatchURL
}
func List() []Info
func Lookup(name string) (Extractor, bool)
func MatchURL(rawURL string) (Extractor, bool)
var ErrNoMatch = errors.New("no vertical extractor matches")
var ErrURLMismatch = errors.New("vertical extractor does not handle URL")
```

Shared helper (unexported, same file): `fetchJSON(ctx, f, url, profile, cookies string) (map[string]any, error)` — one GET, status must be 2xx (else hard error naming status), `json.Unmarshal` into `map[string]any` (field-picking via type-assert helpers `str(m, keys...)`, `num(m, keys...)` — 20 lines, no schema lib). `fetch` types only; no new client, no retry beyond what `StaticFetcher` already does.

`vertical/github.go` (one extractor, `Match`: host `github.com`, path `/{owner}/{repo}[/issues|pull/{n}|releases...]`): maps to `https://api.github.com/repos/{owner}/{repo}[/issues/{n}|/pulls/{n}|/releases/{...}]` with `Accept: application/vnd.github+json` (pass via `Cookies`? No — headers aren't settable per-request; `StaticFetcher` sends fixed profile headers, which GitHub accepts unauthenticated. State this: no custom-header plumbing needed.) `kind` = repo|issue|pr|release from path shape; pick fields per B0.

`vertical/registries.go` (three extractors, one file): pypi `https://pypi.org/pypi/{name}/json` → `info{}` block; npm `https://registry.npmjs.org/{name}` → `dist-tags.latest` + `versions[latest]`; crates_io `https://crates.io/api/v1/crates/{name}` → `crate{}` block. Name extraction: last non-empty path segment (pypi.org/project/{name}/, npmjs.com/package/{name}, crates.io/crates/{name}, lib.rs passthrough NOT matched — keep matchers to the three canonical hosts, no scope creep). crates.io test asserts the server observed a non-empty `User-Agent` (policy requirement — this is the test that locks header flow for all verticals).

Fixtures: `testdata/vertical/{github-repo,github-issue,pypi,npm,crates}.json` (minimal + one unknown blob each). Tests: `TestList` (exact 10 names — write all 10 now, B2/B3 tests fill the behavior; catches renames early), `TestLookup` (unknown → false), per-extractor table (fake fetcher → exact-map equality on picked fields), crates UA assertion.

**Sanity check:** `go test ./vertical/ -run 'TestList|TestLookup|TestGithub|TestRegistries' -v` exits 0.

### Task B2 — Discussion + science: reddit, hackernews, arxiv (4h)

**Depends on:** B0 (independent of B1; parallelizable).

Goal: cover the HTML-first, API-first, and XML transports.

`vertical/social.go` (two extractors): **reddit** (`Match`: `reddit.com` hosts incl. `old.`/`www.`/`new.`/`sh.` shortlinks NOT matched — canonical hosts only): fetch `https://old.reddit.com{path}` HTML (Appendix: old.reddit serves server-rendered HTML, no JS), goquery picks: post title (`a.title`/`h1`), author (`a.author`), score (`div.score`), selftext (`div.usertext-body`), comments count; if old.reddit fetch fails AND path is a `/r/{sub}/comments/{id}/` permalink, retry `{url}.json` once and pick `data.children[0].data{title,author,score,selftext,num_comments}`. `kind` = post|subreddit (subreddit URLs → community info: title, subscribers, description). **hackernews** (`Match`: `news.ycombinator.com/item?id=N`): `GET https://hn.algolia.com/api/v1/items/{N}` → `{title, url, points, author, text, children-len as comments}`; `kind` = story|comment (comment URLs carry the same shape with `parent_id` set — pick `parent_id` too).

`vertical/arxiv.go` (one extractor, `Match`: `arxiv.org/abs/{id}` + `arxiv.org/pdf/{id}` + export.arxiv.org): `GET https://export.arxiv.org/api/query?id_list={id}` (strip version suffix `v\d+` for the query, keep full ID in output `url`), stdlib `encoding/xml` Atom parse: `entry{title, summary, published, author[name]+}`. No new dep — `encoding/xml` is stdlib (stating this because XML tempts a dep; resist).

Fixtures: `reddit-post.html` (minimal old.reddit-shaped: title/author/score/selftext nodes), `reddit-post.json` (`.json` fallback shape), `hn-item.json` (Algolia shape per confirmed research), `arxiv.xml` (2-entry Atom). Tests: post + subreddit + `.json`-fallback paths; HN story + comment; arXiv versioned-ID + multi-author join.

**Sanity check:** `go test ./vertical/ -run 'TestReddit|TestHackerNews|TestArxiv' -v` exits 0.

### Task B3 — Video + commerce: youtube, shopify_product, ecommerce_product (4h)

**Depends on:** B0 (independent of B1/B2; parallelizable).

Goal: script-blob parsing and the two opt-in permissive extractors with their never-steal guarantee.

`vertical/youtube.go` (`Match`: `youtube.com/watch?v=`, `youtu.be/{id}`, `youtube.com/shorts/{id}`, `/embed/{id}` — all normalize to an 11-char ID, else no match): fetch watch HTML, balanced-brace scan for `ytInitialPlayerResponse = {...}` → `videoDetails{title, author, shortDescription, viewCount, lengthSeconds}` (viewCount/length arrive as strings — convert with `strconv`, keep on parse failure as 0, never error); on player-parse failure, fallback `GET https://www.youtube.com/oembed?url={watch-url}&format=json` → `{title, author_name}` only (description empty, views 0 — partial record + no error; document that oEmbed is title/author-only). Fixture: `youtube-watch.html` (script blob with full player response + noise scripts), `youtube-oembed.json`.

`vertical/commerce.go` (two `OptIn: true` extractors): **shopify_product** (`Match`: path contains `/products/` — deliberately permissive, hence opt-in): `GET {scheme}://{host}/products/{handle}.js` → `{title, vendor, description, price (cents→major units as float), type, images[].src}`. Currency isn't in the `.js` payload — omit the field (don't guess; output omits `currency` rather than lying). **ecommerce_product** (`Match`: any http(s) URL — maximally permissive, hence opt-in): fetch page HTML, reuse `clean.HarvestSidecar(html)` → first block with `@type` containing `Product` → pick `{name, brand, offers.price, offers.priceCurrency, offers.availability}`. Reuse, don't re-parse (one import: `clean` — one-way, no cycle).

Tests: `TestOptInNeverSteals` — `MatchURL` on a generic blog URL, a github URL, and a bare shopify-store homepage returns no match (proves the dispatch-order guarantee from gap-spec B1); explicit `Lookup("shopify_product")` + `Match` check still works (opt-in path); YouTube all four URL shapes + oEmbed-fallback path; ecommerce fixture `product-jsonld.html` incl. a page with NO Product block → clean "no product data" error (not empty map — fail loudly).

**Sanity check:** `go test ./vertical/ -run 'TestYouTube|TestCommerce|TestOptIn' -v` exits 0.

### Task B4 — Dispatch in `scrape.Run` + `--vertical` flag (3h)

**Depends on:** B1–B3 (registry complete).

Goal: `scrape --vertical auto|name` works end-to-end with zero LLM on hit.

`scrape/scrape.go`: `Options` += `Vertical string`; `Result` += `Vertical string`. After the quality gate, before the `o.Schema == nil` return:

```go
if o.Vertical != "" && o.Vertical != "auto" {
    ex, ok := vertical.Lookup(o.Vertical)
    if !ok { finish(0,1,"error"); return Result{}, fmt.Errorf("scrape: vertical %q unknown (see `magpie vertical --list`)", o.Vertical) }
    // ^ `--list` ships in Phase C; message names it anyway so the string never changes. State this in-task.
    if u, err := url.Parse(rawURL); err != nil || !ex.Match(u) {
        finish(0,1,"error"); return Result{}, fmt.Errorf("scrape: vertical %q: %w for %s", o.Vertical, vertical.ErrURLMismatch, rawURL)
    }
    return runVertical(ctx, d, static, rawURL, ex, o, runID, finish, base)
}
if o.Vertical == "auto" {
    if ex, ok := vertical.MatchURL(rawURL); ok {
        return runVertical(...) // same helper
    }
}
```

`runVertical` helper (same file, ~25 lines): `static` is the already-constructed `*fetch.StaticFetcher` — pass it as the `vertical.Fetcher` (compile-time: it already has the method); build `FetchRequest{URL, Profile: o.Profile, Cookies: o.Cookies}` inside each extractor call? No — extractors build their own downstream URLs (api.github.com etc.), so pass profile/cookies through a small `vertical.Request{Profile, Cookies}`? Laziest correct: extractors take the `Fetcher` plus separate `profile, cookies string` params — but the `Extractor.Extract` signature from B1 is `(ctx, f, u)`. Fix now while B1–B3 are fresh: `Extract func(ctx context.Context, f Fetcher, u *url.URL)`. Profile/cookies: vertical fetches are API calls where the default profile suffices (crates.io UA test in B1 locks the UA on the default path). Decision: extractors always fetch with zero-value profile (default headers); `--header-profile/--cookies` do NOT propagate into vertical sub-fetches (documented limitation, one line in help text — profiles exist for challenge-prone HTML pages, not registry APIs). This kills the plumbing problem entirely. On success: `base.Record = m; base.Vertical = ex.Info.Name; finish(1,0,"finished")`; no cache interaction at all.

`cli/scrape.go`: `--vertical` flag (`""` default; value `auto` or name; unknown validated early via `vertical.Lookup` → exit 2 before any I/O, like `--page-format`). Help text notes default-off + Phase C `vertical` command coming.

Tests (`scrape/vertical_test.go`, fake `vertical.Fetcher` + real registry `Match` against fixture hosts? No — Match checks real hosts (github.com), but Extract uses the fake fetcher so no network): auto-hit on `https://github.com/o/r` with `ExtractorFor` rigged to panic-on-call; auto-miss on `https://example.com/x` proceeds to markdown; explicit mismatch (`reddit` × github URL) → `ErrURLMismatch`; extractor error → propagates unwrapped-identity (`errors.Is`); vertical hit writes no selector cache (seed a cache entry, prove `GetSelectors` untouched / run still `FromCache=false`).

**Sanity check:** `go test ./scrape/ -run TestVertical -v` exits 0.

### Task B5 — `clean` fast paths: islands + player details (3h)

**Depends on:** B0 (code-independent of B1–B4; run last to own golden regeneration).

Goal: thin pages get rescued, rich pages byte-identical.

`clean/islands.go`:

```go
// clean/islands.go
func IslandText(sidecar json.RawMessage, maxChars int) string
func PlayerDetails(html []byte) map[string]string // ytInitialPlayerResponse → videoDetails subset; nil when absent
```

`IslandText`: unmarshal sidecar (array-or-single — mirror `HarvestSidecar` output shapes), depth-first leaf strings, keep len ≥ 40, drop strings matching CSS/noise (`{`, `;`, `function(` prefixes), dedupe preserving order, join `\n\n`, cap `maxChars` (call site passes 4000). `PlayerDetails`: locate `ytInitialPlayerResponse` marker, balanced-brace scan from first `{`, unmarshal fragment into `map[string]any`, pick `videoDetails{title, author, shortDescription, viewCount}`. (Shared scan helper with `vertical/youtube.go`? Different packages — duplicating ~15 lines vs exporting from vertical: export `vertical.PlayerResponse(html) (map[string]any, bool)` and have `clean` call it. One-way import `clean → vertical`? Check: `vertical/commerce.go` imports `clean` (HarvestSidecar) — a `clean → vertical` import would CYCLE. So: put the scan helper in `clean` (unexported) and let `vertical/youtube.go` import `clean` for it too — `vertical → clean` already exists via commerce. Single home: `clean.PlayerResponseHTML(html []byte) (map[string]any, bool)` exported, used by both. Decide this in B5 and refactor youtube.go's copy — 10-line move, no behavior change.)

`clean/clean.go` hook (after `capTokens`, before `HarvestMetadata` so word count covers the rescue):

```go
if len(raw.Scope.Include)+len(raw.Scope.Exclude) == 0 && WordCount(md) < 200 {
    if extra := IslandText(sidecar, 4000); WordCount(md+"\n"+extra) > WordCount(md) {
        md += "\n\n" + extra
    }
    if pd, ok := PlayerDetails(raw.HTML); ok && isYouTubeHost(raw.FinalURL) {
        md = "# " + pd["title"] + "\n" + pd["author"] + "\n\n" + pd["description"] + "\n\n" + md
    }
}
```

islands first, player-prepend second; player block only when title non-empty. `ponytail:` island text is unranked leaf order (ceiling = boilerplate strings may precede content; upgrade = score leaves by length/density — 5 lines if ever needed).

Tests: `clean/islands_test.go` — island SPA shell (`__NEXT_DATA__`-heavy, trafilatura-thin) gains text; same HTML with `Scope{Include:["article"]}` unchanged; rich article unchanged; YouTube watch-shaped thin page gets `# title` prepend; `PlayerDetails` on non-YT HTML → nil. Golden: regenerate `testdata/clean/spa-shell.md` via `-update`, hand-diff to confirm only appended content, document the regeneration in the PR (the phase's single allowed golden change).

**Sanity check:** `go test ./clean/ -v` exits 0 with `git diff --stat testdata/clean/` showing only `spa-shell.md` changed.

---

## 4. Deliverables

```
plan/phase-B.md              # this plan
vertical/vertical.go         # Info/Extractor/Fetcher/Lookup/MatchURL/List + ErrNoMatch/ErrURLMismatch + fetchJSON helper (B1)
vertical/github.go           # repo|issue|pr|release via api.github.com (B1)
vertical/registries.go       # pypi + npm + crates_io registry JSON (B1)
vertical/social.go           # reddit (old.reddit HTML + .json fallback) + hackernews (Algolia items) (B2)
vertical/arxiv.go            # arXiv export API, encoding/xml Atom (B2)
vertical/youtube.go          # ytInitialPlayerResponse + oEmbed fallback (B3)
vertical/commerce.go         # shopify_product + ecommerce_product, both OptIn (B3)
vertical/vertical_test.go    # TestList/TestLookup/TestOptInNeverSteals/dispatch-order (B1/B3)
vertical/*_test.go            # per-extractor tables on fixtures (B1–B3)
clean/islands.go             # IslandText + PlayerDetails + PlayerResponseHTML shared scan (B5)
clean/clean.go               # thin+unscoped append hook (B5)
scrape/scrape.go             # Options.Vertical + Result.Vertical + runVertical (B4)
cli/scrape.go                # --vertical flag, exit-2 validation (B4)
testdata/vertical/           # 11 fixtures: github-repo/issue, pypi, npm, crates, reddit-post.{html,json}, hn-item, arxiv, youtube-watch.html, youtube-oembed.json, shopify-product, product-jsonld.html (B1–B3)
testdata/clean/spa-shell.md  # regenerated once, hand-verified (B5)
clean/islands_test.go        # thin/scoped/rich/player cases (B5)
scrape/vertical_test.go      # zero-LLM proof, miss fallthrough, mismatch, error propagation, no-cache-write (B4)
```

One-line rule: `vertical/` never imports `scrape`/`cli`/`mcp` (leaf package); `clean ↔ vertical` sharing goes one way only (`vertical → clean`: `HarvestSidecar`, `PlayerResponseHTML`) — never the reverse, or the build cycles.

---

## 5. Exit Criteria

- [ ] `go test ./vertical/ -v` exits 0 — all 10 extractors green on fixtures, `TestList` locks the exact name list, `TestOptInNeverSteals` proves permissive matchers never auto-fire (covers all `vertical/*.go` + `testdata/vertical/`)
- [ ] `go test ./scrape/ -run TestVertical -v` exits 0 with the panic-on-LLM-call fake passing (covers `scrape/scrape.go` dispatch + `Result.Vertical`)
- [ ] `go test ./clean/ -v` exits 0 — island/player rescue works, rich-article goldens byte-identical, `git diff --stat testdata/clean/` shows only `spa-shell.md` (covers `clean/islands.go` + hook)
- [ ] `cli` `--vertical` validation exits 2 on unknown names pre-I/O; mismatch exits 2 with the `does not handle` message (covers `cli/scrape.go`)
- [ ] Vertical hit performs zero LLM calls AND zero selector-cache reads/writes (asserted in-test, not eyeballed)
- [ ] Every `testdata/vertical/` fixture contains an unknown-field blob its extractor ignores (forward-tolerance proven, not assumed)
- [ ] `magpie scrape --vertical auto` smoke-tested against a local replay of a GitHub API fixture with no API key set (manual, 10 min)
- [ ] Full gates: `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass

---

## 6. Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are implementing Phase B of gomagpie (`magpie`) — zero-LLM structured data via vertical extractors, stdlib only. Spec of record: `plan/webclaw-gap-spec.md` §5 Phase B (B1–B2). Source of truth for pipeline behavior is `spec.md`; gap-spec defines only the deltas. Prior-phase contract: `plan/phase-A.md`.

### What This Project Is
Go CLI web scraper (module `gomagpie`, Go 1.26+, binary `magpie`): fetch → clean → extract. `cmd/magpie/` is a 10-line shim; the cobra tree lives in importable `cli/`. Single-URL flow is shared `scrape.Run(ctx, Deps, url, Options) (Result, error)` in `scrape/` (CLI + MCP both call it; formatting stays at the edges). Cleaning is `clean.Clean(ctx, RawPage) (CleanedPage, error)` (scope pre-pass → sidecar harvest → trafilatura → quality classify → metadata). Static fetch is `fetch.StaticFetcher` with `FetchRequest{URL, Timeout, Profile, Cookies}` → `FetchResponse{URL, FinalURL, StatusCode, HTML, Headers}` (stock TLS, profile header bundles, cookiejar, 50 MB LimitReader). `clean.HarvestSidecar(html)` returns JSON-LD + `__NEXT_DATA__` + `__NUXT__` blocks; `clean.WordCount(s)` counts words. Tests are hermetic: goldens in `testdata/`, fake fetchers, `gofmt`/`go vet`/`golangci-lint` + 3-way `CGO_ENABLED=0` builds. Go regexp has no lookahead — hand-scan braces/HTML instead.

### Established in Prior Phases
- `scrape/scrape.go`: `Options{Schema, Render, Provider, Model, MaxCost, UseCache, PageFormat, Scope, Profile, Cookies}`, `Result{RunID, URL, FinalURL, Title, Markdown, StructuredData, Record, FromCache, Usage, Provider, Model, Rendered}`; sentinels `ErrMissingKey`; quality gate → `clean.ErrQuality` (exit 8); insertion point for this phase is after the quality gate, before the `o.Schema == nil` early return.
- `clean/clean.go`: `RawPage{HTML, URL, FinalURL, StructuredRaw, Scope, StatusCode}`, `CleanedPage{Markdown, StructuredData, Title, FinalURL, Metadata, Quality}`; `Classify` + `<200-word` guard philosophy; `HarvestMetadata`; `OnlyMainContent` is a documented no-op; existing `testdata/clean/*.md` goldens are byte-locked (this phase regenerates ONLY `spa-shell.md`, once, in B5).
- `fetch/fetcher.go` + `http.go`: `FetchRequest/Response`, profile bundles (`default|chrome|firefox`), `Cookie` passthrough, challenge warmup retry. `*fetch.StaticFetcher` already sends a real `User-Agent` (crates.io policy needs it — B1 asserts this server-side).
- `cli/scrape.go`: `--page-format/--include/--exclude/--only-main-content/--header-profile/--cookies` (all flag-only, never in `Config`); exit matrix 0–8 (`fail(2,…)` = usage error).
- Rules (AGENTS.md + .pi/rules/): stdlib + pinned goquery only (plus `encoding/xml` — stdlib, no approval needed); no live-network default tests; no CGO; never import go-rod outside `fetch/`; `ponytail:`-comment deliberate shortcuts with ceiling + upgrade path.

### Your Goal for This Phase
Ship gap-spec B1–B2: a `vertical/` leaf package (10 extractors, strict auto-dispatch, opt-in permissives), `--vertical auto|name` on `scrape` with zero LLM on hit, and `clean` island/player fast paths for thin pages. Do NOT build the standalone `magpie vertical` command or the `list_extractors`/`vertical_scrape` MCP tools — those are Phase C thin wrappers over your registry (your `List()`/`Lookup()`/`MatchURL` are their API; keep signatures stable).

### Data Model Rules (Go — follow exactly)
- API-boundary structs (`vertical.Info`, `scrape.Options/Result` additions, CLI flag structs): plain structs with `json` tags; additive fields only, `omitempty` where compat matters.
- Internal logic: plain functions on value types; one `Fetcher` interface total (in `vertical`, satisfied by `*fetch.StaticFetcher`); no other interfaces, no new packages beyond `vertical`.
- Import direction is load-bearing: `vertical` may import `fetch` (types) and `clean` (`HarvestSidecar`, `PlayerResponseHTML`); `clean` must NEVER import `vertical` (cycle). `scrape` imports `vertical`. Nothing imports `scrape` except `cli`/`mcp`.
- Errors: sentinels `vertical.ErrNoMatch` / `vertical.ErrURLMismatch`, wrapped with `%w`; extractor fetch/parse failures are hard errors (never fall back to LLM — fail loudly); unknown-field blobs in fixtures must be ignored, never rejected.

### Architecture
```
scrape.Run ──► [Vertical != "" ] ──► explicit Lookup (+Match check → ErrURLMismatch) or auto MatchURL (strict only)
   │                                      │ hit: runVertical → Result{Record, Vertical}, finish ok, NO cache, NO LLM
   │                                      └─ auto-miss → today's path bit-for-bit
   ▼
clean.Clean ──► md = trafilatura ──► if unscoped && WordCount(md)<200: md += IslandText(sidecar,4000); if YT host: prepend PlayerDetails
```
Fail-safe rules: vertical hit with extractor error = hard error (no silent fallback); `MatchURL` never returns an `OptIn` extractor (shopify/ecommerce are explicit-only); island append only fires on unscoped thin pages (scoped/rich pages provably untouched); `markdown` default path with `Vertical==""` is byte-identical to today.

### Confirmed Endpoint Notes (baked in — do not re-research)
- crates.io: `GET https://crates.io/api/v1/crates/{name}` → `{crate: {name, description, downloads, newest_version}}`; API policy REQUIRES `User-Agent` (else 403) — assert observed-UA in test. (confirmed 2026-09-17)
- HN: `GET https://hn.algolia.com/api/v1/items/{id}` → `{id, title, url, points, author, text, parent_id, children[]}`; comments count = len(children) recursive? No — top-level len only, one line, documented. (confirmed 2026-09-17)
- GitHub: `GET https://api.github.com/repos/{o}/{r}[/issues/{n}|/pulls/{n}|/releases]` + `Accept: application/vnd.github+json`; unauthenticated works (rate-limited — irrelevant for fixtures/fakes).
- PyPI `…/pypi/{name}/json` → `info{}`; npm `registry.npmjs.org/{name}` → `dist-tags.latest` + `versions[]`; arXiv `export.arxiv.org/api/query?id_list={id}` (Atom, `encoding/xml`); YouTube watch-HTML `ytInitialPlayerResponse` → `videoDetails`, fallback oEmbed `youtube.com/oembed?url=…&format=json`; Shopify `{shop}/products/{handle}.js` (no currency field — omit, don't guess).

### Files to Create/Change
Per-file guidance — implement in task order B0→B5 (B1/B2/B3 are code-independent once B0 locks field names; integrate B4 after the registry is complete; B5 last):

#### vertical/vertical.go (NEW, B1)
`Info`, `Fetcher` (single-method, satisfied by `*fetch.StaticFetcher`), `Extractor{Info, Match, Extract, OptIn}`, `List/Lookup/MatchURL` (MatchURL skips OptIn), `ErrNoMatch/ErrURLMismatch`, unexported `fetchJSON` + `str/num` pick-helpers. `Patterns` are human-readable example URLs, never regexes (no ReDoS surface).

#### vertical/github.go + vertical/registries.go (NEW, B1)
Per B0 field lists. GitHub `kind` from path shape (repo|issue|pr|release); registries share `fetchJSON`; name = last non-empty path segment on the three canonical hosts only (`pypi.org`, `npmjs.com`, `crates.io`).

#### vertical/social.go + vertical/arxiv.go (NEW, B2)
Reddit: `old.reddit.com{path}` HTML first (goquery: title/author/score/selftext/comments), `.json` retry once for comment permalinks only; `kind` = post|subreddit. HN: item URLs → Algolia items API. arXiv: strip `v\d+` for query, keep full ID in output; `encoding/xml`, authors joined.

#### vertical/youtube.go + vertical/commerce.go (NEW, B3)
YouTube: 4 URL shapes → ID; player-response first (`strconv` conversions, 0-on-failure, never error), oEmbed fallback (title/author only). Commerce: both `OptIn: true`; shopify via `.js` (omit currency); ecommerce via `clean.HarvestSidecar` first-Product-block (no Product → error, never empty map).

#### scrape/scrape.go + cli/scrape.go (B4)
`Options.Vertical` / `Result.Vertical`; `runVertical` helper (~25 lines, uses the already-built `static` fetcher, default headers only — profiles/cookies do NOT propagate into vertical sub-fetches, one-line doc); CLI `--vertical` validated pre-I/O (unknown → exit 2). The unknown-name error message may reference Phase C's `magpie vertical --list` — keep the string stable so C never changes it.

#### clean/islands.go + clean/clean.go hook (B5)
`IslandText(sidecar, 4000)`, `PlayerDetails(html)`, exported `PlayerResponseHTML(html) (map[string]any, bool)` (single home for the balanced-brace scan — refactor `vertical/youtube.go` to use it); hook fires iff unscoped && `WordCount(md) < 200`. Regenerate ONLY `testdata/clean/spa-shell.md` via `-update` + hand-diff.

#### Tests + fixtures
`testdata/vertical/` (11 hand-authored minimal fixtures, each with an unknown-field blob); `vertical/*_test.go` (fake fetcher, exact-map equality, `TestList` 10 names, `TestOptInNeverSteals`); `scrape/vertical_test.go` (panic-on-LLM fake, miss fallthrough, mismatch, error propagation, no-cache-write); `clean/islands_test.go` (thin/scoped/rich/player).

### Success Criteria
- `go test ./vertical/ ./scrape/ ./clean/ -v` exits 0 with the zero-LLM proof, opt-in guarantee, and "Just a moment"-style negatives all green
- `git diff --stat testdata/clean/` shows only `spa-shell.md`
- `magpie scrape --vertical auto` returns a record with no API key configured (local replay)
- Full gates: `go test ./...` && `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...` clean, 3-way `CGO_ENABLED=0` builds pass

### Expected File Structure at End
```
vertical/vertical.go vertical/github.go vertical/registries.go vertical/social.go vertical/arxiv.go vertical/youtube.go vertical/commerce.go (+ *_test.go)
clean/islands.go (+ clean.go 8-line hook)
scrape/scrape.go cli/scrape.go (Vertical wiring only)
testdata/vertical/ (11 fixtures)
```

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available (`scrape.Run` insertion point, `HarvestSidecar`/`WordCount`, `StaticFetcher` UA behavior, `testdata/clean` goldens, exit-2 usage pattern — all read in source, §6 prompt inlines them)
- [PASS] Every sub-task has a clear, testable completion condition (each task ends with a Sanity check one-liner; §5 exit criteria map 1:1 to deliverables)
- [PASS] Execution prompt is self-contained: includes (a) what prior phases established (struct shapes + insertion point + goldens rule inline), (b) confirmed endpoint notes (crates.io UA + HN Algolia verified 2026-09-17, rest long-stable with fixture-absorbed drift), (c) a "Data Model Rules" section, (d) per-file guidance, and (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables (8 criteria covering registry, dispatch, islands, CLI validation, zero-LLM/no-cache proofs, fixture tolerance, smoke test, full gates)
- [PASS] Any heavy external dependency has a fake/stub strategy noted (no heavy deps — stdlib + pinned goquery; fake `Fetcher` replaces network everywhere; browser/LLM excluded per testing.md)
- [PASS] New libraries have a confirmed usage snippet in the execution prompt (N/A — zero new libraries; `encoding/xml` is stdlib and its use is spelled out in B2)
