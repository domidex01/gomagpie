# Phase A — Testing: Output + Scoping + Quality

**Scope:** `clean/` (llm, scope, quality, metadata + `Clean` wiring), `fetch/` (profiles, challenge warmup, cookies), `scrape/` (PageFormat/Scope/Profile passthrough, non-2xx pass-through, ErrQuality), `cli/` (`--page-format`, scoping flags, exit 8), `crawl/` (quality page-error path), `mcp/` (`scrape_url` new inputs + `content`), `testdata/llm/` + `testdata/quality/` goldens, `go.mod` cascadia promotion
**Key Pattern:** Pure-function table tests for `clean/` (no mocks at all); `httptest.Server` route shapes for fetch/scrape/crawl/MCP edges; reuse the three existing `fakeExtractor` copies and temp-file SQLite helpers; new goldens in `testdata/llm/` via the existing `-update` flag; `go test ./...` stays hermetic (no network, no browser, no keys).
**Dependencies:** stdlib `testing`, `net/http/httptest`, `os`, `path/filepath`, `strings`, `encoding/json` only — no test frameworks, no extra deps (cascadia is imported by production `clean/scope.go`, never by tests directly).

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an agent dev, I want `--page-format llm` to cost fewer tokens than markdown with zero link loss, so that long pages fit in context | `TestLLMText_Reduction` + `TestLLMText_LinksPreserved` in `clean/llm_test.go` (per-fixture byte shrink + every `](http` target in `## Links`) | `len(llm) < len(md)` on all 3 fixtures AND link-set(md) ⊆ link-set(llm) |
| US-2 | As a user, I want `--include/--exclude` to scope scrapes and invalid selectors to warn-not-fail, so that shell pages yield clean article text | `TestScope_*` table in `clean/scope_test.go` (include/exclude/union/cascadia-invalid/no-match/cap/exclude-wins) | scoped markdown contains article, not nav; `??bad[[` → full content + "invalid selector" warning, exit unaffected |
| US-3 | As a user behind bot protection, I want challenge warmup + header profiles + cookies to retry once with warmed state, so that protected pages load without a browser | `TestChallenge_WarmupRetry` + `TestProfiles_*` in `fetch/profiles_test.go` (hit-count + server-observed headers) | 403-challenge → homepage hit → retry carries `Cookie` → 200 real page; chrome/firefox UAs distinct server-side |
| US-4 | As a user, I want blocked pages to fail loudly with typed errors (exit 8), and rich articles mentioning "Just a moment" to pass clean, so that I never get silent empty markdown nor false blocks | `TestClassify_*` table in `clean/quality_test.go` + `TestScrape_Exit8` in `cli/scrape_test.go` + `TestRun_QualityNoCacheWrite` in `scrape/` | 403+Akamai+thin → `access-denied`; rich+marker → `IssueNone`; CLI exit 8 with `quality blocked (access-denied)`; 0 selector-cache writes on quality failure |
| US-5 | As an agent, I want every scrape to carry metadata (desc/author/date/lang/site/image/favicon/word_count) while existing goldens stay byte-identical, so that downstream tools get structured context for free | `TestMetadata_*` in `clean/metadata_test.go` + untouched `TestCleanArticle/Product/SpaShell` | `metadata.json` golden matches; favicon absolute; `WordCount == words(markdown)`; `testdata/clean/*.md` byte-identical |

---

## 1. Component Mock Strategy

Phase type: **Service/pipeline** (same as Phase 1 — CLI fetch→clean→extract with network edges, no model in this phase). Mock strategy in one sentence: **no new fake structs — `clean/` units are pure functions on inline/file fixtures, and every network peer reuses the established `httptest.Server` + per-package `fakeExtractor` + temp-file SQLite helpers; the only new shared test code is a dir-parameterized golden helper and two origin route shapes.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `clean/llm.go` ToLLMText/ToText/Render | No mock (pure) — inputs are `cleanFile()` outputs from `testdata/clean/*.html` + synthetic `CleanedPage` structs for gate cases | Per-fixture `len(llm) < len(md)`; link-set(md) ⊆ `## Links`; structured ≤16 KB, no `WebSite`/`WebPage` blocks, no >500-char body dupes; `Render("")` == `Markdown`; unknown format errors naming valid values | US-1 |
| `clean/scope.go` ApplyScope | No mock (pure) — inline shell HTML (nav+article+aside+footer), `warn` captured into `[]string` | include `article` drops nav/footer; union `article,aside` keeps both; `??bad[[` warns "invalid selector" + full content; `section.nope` warns "matched nothing" + empty; >100 selectors truncated + 1 warning; exclude beats include; empty scope == input | US-2 |
| `clean/quality.go` Classify | No mock (pure) — `testdata/quality/*.html` files via `qualityFixture()` + status code per row | 8-row table incl. rich+marker → `IssueNone` (the false-positive guard); `WordCount` vectors; `Clean` attaches `Quality` and returns nil error on blocked input (fail-safe: never fails inside clean) | US-4 |
| `clean/metadata.go` HarvestMetadata | No mock (pure) — `testdata/clean/article.html` (has meta/OG tags; extend fixture minimally if a field is untestable — one tag, not a new file) | All 8 fields populated on article; favicon resolved absolute against pageURL; `WordCount == len(strings.Fields(md))`; minimal HTML → zero struct, no crash | US-5 |
| `clean/clean.go` wiring | No mock — existing `TestCleanArticle/Product/SpaShell` goldens ARE the regression gate, plus one new fields test | `testdata/clean/*.md` byte-identical (default paths unchanged); `Metadata.WordCount > 0` and `Quality == ""` on article HTML | US-5 |
| `fetch/` profiles + warmup | `httptest.Server` route shapes (§3): challenge server (stateful hit counter), header-echo server | Challenge: exactly 3 hits (`/challenge` 403 → `/` Set-Cookie → `/challenge` 200 with `Cookie`); clean rich page → 1 hit (no retry); unknown profile → default UA, nil error; chrome vs firefox UA + `Sec-CH-UA` distinct as server-observed | US-3 |
| `scrape/` passthrough + pass-through | Existing `fakeExtractor` + `scrapeOrigin` + `openScrapeDB` (zero new helpers); origin serving `testdata/quality/challenge-akamai.html` bytes with 403 | `Rendered` set + `Markdown` still populated; `json` envelope has old keys + new keys; 403+deny → `errors.Is(err, clean.ErrQuality)`, issue `access-denied`, old `fetch: HTTP` text absent; quality failure → `fx.total()==0` and `GetSelectors` not-found | US-1, US-4 |
| `cli/` flags + exit 8 | Existing `codeOf` + `GOMAGPIE_BASE_URL` httptest pattern (`cli/scrape_test.go:144ff`); `file://` fixtures for flag tests | 403 origin → exit 8, stderr `quality blocked (access-denied)`; `--page-format bogus` → exit 2; `--page-format llm` prints `## Links`; `--page-format` leaves `cfg.Format` at `json` (crawl contract intact) | US-1, US-2, US-4 |
| `crawl/` quality page-error path | Existing `newSiteOrigin` + crawl `fakeExtractor` + `openCrawlDB` (zero new helpers) | 2-page site (good + 403-deny): `PagesErr` counts the blocked page, records == 1 (good URL only), no selector-cache row for blocked host, no record JSON containing blocked URL | US-4 |
| `mcp/` scrape_url new inputs | Existing `testMCPServer` + `dialInMemory` + `callTool` + `decodeOut` (zero new helpers) | `page_format: llm` → `content` has `## Links` AND `markdown` still populated; `include` scopes; `cookies` observed server-side; 403 origin → `CallTool` error containing `access-denied` | US-1, US-2, US-3, US-4 |
| `go.mod` cascadia promotion | No Go test — CI grep gate (§9): `go.mod` lists `andybalholm/cascadia` without `// indirect` | `grep` passes; `go mod tidy` is a no-op (`git diff --stat go.mod go.sum` empty after) | US-2 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | Inline fixtures, `testdata/` files, httptest origins, temp-dir SQLite — no network, no browser, no keys | <60s total | Every push; the only default gate |
| Golden regen (`go test ./clean/ -update`) | Same as unit; rewrites `testdata/llm/*` + `metadata.json` | seconds | After intentional `ToLLMText`/metadata changes only — followed by `git diff` hand-review (never blind-commit regen output) |
| Manual smoke (not a test file) | Built binary + `file://` fixtures + httptest challenge stub via `python3 -m http.server`? No — local fixture files only | minutes | Pre-release: the 8 Exit Criteria command lines from `plan/phase-A.md` §5 |

No browser tier touched (rod escalation unchanged; existing `//go:build browser` smoke still skips without Chrome). No live-provider tier: no LLM is invoked anywhere in Phase A tests — `fakeExtractor` returns `script` maps with `Attempts: 1`.

---

## 3. Fake / Mock Implementations

No new fake structs. The three existing `fakeExtractor` copies (`scrape/`, `crawl/`, `mcp/` — same 30-line shape, rule-of-three respected) and both origin helpers (`newFakeOrigin`, `newSiteOrigin`, `scrapeOrigin`) are reused verbatim. Two new shared pieces, shown in full:

### `goldenDir` — generalizes `clean_test.go:golden` (which hardcodes `testdata/clean`)

```go
// clean/clean_test.go — ADD alongside existing golden(); do not modify golden().
func goldenDir(t *testing.T, dir, name, got string) {
    t.Helper()
    path := filepath.Join("..", "testdata", dir, name)
    got = strings.TrimSpace(got) + "\n"
    if *update {
        if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
            t.Fatal(err)
        }
        return
    }
    want, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("golden %s missing (run with -update): %v", path, err)
    }
    if strings.TrimSpace(string(want)) != strings.TrimSpace(got) {
        t.Errorf("golden %s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
    }
}

// clean/quality_test.go
func qualityFixture(t *testing.T, name string) []byte {
    t.Helper()
    raw, err := os.ReadFile(filepath.Join("..", "testdata", "quality", name+".html"))
    if err != nil {
        t.Fatal(err)
    }
    return raw
}
```

**Matches real use:** `TestLLMText_Goldens` calls `goldenDir(t, "llm", "article.llm.md", clean.ToLLMText(cleanFile(t, "article")))` — same TrimSpace+`.md` contract as `golden()`, different directory. `metadata.json` golden uses `goldenDir(t, "llm", "metadata.json", string(prettyJSON))`.

### `challengeOrigin` — stateful challenge→warmup→success server (fetch tests)

```go
// fetch/profiles_test.go
type challengeHits struct {
    mu       sync.Mutex
    challenge int
    homepage  int
    cookies   []string // Cookie headers seen on /challenge hits
}

func newChallengeOrigin(t *testing.T, h *challengeHits, challengeBody string) string {
    t.Helper()
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        h.mu.Lock()
        h.homepage++
        h.mu.Unlock()
        http.SetCookie(w, &http.Cookie{Name: "session", Value: "warmed"})
        _, _ = io.WriteString(w, "<html><body>home</body></html>")
    })
    mux.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
        h.mu.Lock()
        h.challenge++
        h.cookies = append(h.cookies, r.Header.Get("Cookie"))
        n := h.challenge
        h.mu.Unlock()
        if n == 1 {
            w.WriteHeader(http.StatusForbidden)
            _, _ = io.WriteString(w, challengeBody)
            return
        }
        _, _ = io.WriteString(w, "<html><head><title>Real page</title></head><body><p>"+strings.Repeat("actual article content ", 50)+"</p></body></html>")
    })
    srv := httptest.NewServer(mux)
    t.Cleanup(srv.Close)
    return srv.URL + "/challenge"
}
```

Test asserts: `h.challenge == 2`, `h.homepage == 1`, `h.cookies[1]` contains `session=warmed`, final body contains "Real page". The rich-body second response matters: it proves the retry result flows through, not just the status.

### Header-echo origin (profile tests) — 10 lines, inline per test, no helper

```go
mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    fmt.Fprintf(w, "UA=%s|CH=%s", r.Header.Get("User-Agent"), r.Header.Get("Sec-CH-UA"))
})
```

Assert body contains `Chrome/126` for chrome profile, `Firefox/128` for firefox, and today's magpie UA for `default`/unknown.

---

## 4. Test File List

```
gomagpie/
├── clean/
│   ├── clean_test.go        # ADD: TestClean_PopulatesNewFields (WordCount>0, Quality=="" on article); existing goldens UNTOUCHED = byte-identical gate
│   ├── llm_test.go          # NEW: goldens (3 fixtures) + reduction + link-preservation + structured-gate + ToText table + Render matrix
│   ├── scope_test.go        # NEW: include/exclude/union/invalid/no-match/cap-100/exclude-wins/only-main/empty-scope table tests
│   ├── quality_test.go      # NEW: Classify 8-row table + WordCount vectors + Clean-attaches-without-failing + default-empty
│   └── metadata_test.go     # NEW: article fields + favicon-absolute + word-count-equality + missing-tags + metadata.json golden
├── fetch/
│   ├── fetch_test.go        # UNTOUCHED (default-profile byte-identical gate — proves today's headers didn't move)
│   └── profiles_test.go     # NEW: default/unknown/chrome/firefox headers + IsChallengePage table + warmup-retry + no-retry-when-clean + cookies
├── scrape/
│   └── scrape_test.go       # ADD: PageFormat llm/json, scope passthrough, profile/cookies passthrough, quality pass-through (ErrQuality + old-text-absent), no-cache-write
├── cli/
│   └── scrape_test.go       # ADD: exit-8 mapping, --page-format valid/bogus, --page-format-leaves-Config.Format, --include/--header-profile/--cookies flags
├── crawl/
│   └── crawl_test.go        # ADD: TestCrawl_QualityCountedNotCached (good + 403-deny site; errors counted, 1 record, no cache row) — or NEW quality_test.go if the file grows past one test
├── mcp/
│   └── server_test.go       # ADD: scrape_url page_format/content-backcompat, include scoping, cookies observed, 403 → tool error
├── testdata/
│   ├── llm/article.llm.md + article.txt + product.llm.md + product.txt + spa-shell.llm.md + spa-shell.txt + metadata.json  # NEW via -update, then HAND-VERIFIED
│   └── quality/challenge-akamai.html + login-wall.html + empty-shell.html + rich-with-marker.html                           # NEW static fixtures
├── go.mod                   # cascadia → direct: CI grep gate, not a Go test (§9)
└── plan/phase-A-tests.md    # this file
```

Every deliverable in `plan/phase-A.md` §4 has a test file: `clean/llm|scope|quality|metadata.go` → new same-name test files; `clean/clean.go` → `clean_test.go` addition + untouched goldens; `fetch/profiles|fetcher|http.go` → `profiles_test.go` + untouched `fetch_test.go`; `scrape/scrape.go` → `scrape_test.go` additions; `cli/scrape.go` → `cli/scrape_test.go` additions; `crawl/crawl.go` → `crawl_test.go` addition; `mcp/tools.go` → `server_test.go` additions; `go.mod` → grep gate; `testdata/llm|quality/` → produced by the tests' `-update` run + hand-review gate (§6).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): fixtures are per-package test helpers, golden regen is the `-update` flag in `clean/clean_test.go:14`, and there is no integration-tier flag (hermetic-only suite; browser stays behind its build tag). State at top of section per skill rule: **additions to existing test files — no existing helper is modified except `clean_test.go` gaining `goldenDir` alongside `golden`.**

```go
// clean/clean_test.go — ADD (existing golden(), cleanFile(), update flag untouched)
func goldenDir(t *testing.T, dir, name, got string) // §3 full source above

// clean/quality_test.go — NEW helpers
func qualityFixture(t *testing.T, name string) []byte // §3 full source above
func classifyCase(...)                                 // table row struct {name, file, status, want Issue}

// fetch/profiles_test.go — NEW helpers
type challengeHits struct{...} + newChallengeOrigin(t, h, body) string // §3 full source above
// header-echo mux inline per test (10 lines, §3)

// scrape/scrape_test.go — REUSE: fakeExtractor, scrapeOrigin(t, body), openScrapeDB(t), fakeDeps(db, fx, key), testSchema(t)
//   origin serving quality fixture bytes with 403: build inline with httptest.NewServer + w.WriteHeader(403) + qualityFixture bytes
//   no-cache-write assert: db.GetSelectors(t.Context(), host, hash) → expect not-found (mirror existing selector-cache assertions)

// cli/scrape_test.go — REUSE: codeOf(err), GOMAGPIE_BASE_URL httptest pattern, file:// fixture URLs
//   exit-8 test serves 403 challenge-akamai.html bytes from httptest (CLI tests already drive HTTP origins this way)

// crawl/crawl_test.go — REUSE: newSiteOrigin(t, pages, robotsBody), crawl fakeExtractor, openCrawlDB(t)
//   quality page: newSiteOrigin map entry served with 403 status? newSiteOrigin writes 200 — check its shape: if status is fixed 200,
//   serve the deny page through a second httptest server OR extend newSiteOrigin with a status map ONLY if ≤5 lines; else hand-roll mux inline.

// mcp/server_test.go — REUSE: testMCPServer, dialInMemory, callTool(t, cs, name, args, ""), decodeOut
//   page_format/cookies/include passed as Arguments map; quality case asserts CallTool error (not result).
```

Scope rationale: everything function-scoped (`t.TempDir()`, per-test servers) — no `TestMain`, no shared state, safe for `go test` parallelism. The only cross-test artifact is `testdata/` goldens, written exclusively under `-update`.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Goldens + in-test property asserts for `llm` output | `testdata/llm/*` equality AND `len(llm)<len(md)` AND link-set inclusion computed in-test | Golden equality alone would pass a bloated-but-stable renderer; the reduction + link assertions are the actual US-1 contract and survive `-update` regens |
| Hand-verify regen output, never blind-commit | §9 regen command ends with `git diff --stat` + manual read; exit criterion 6 in phase plan requires it | `-update` makes any output "pass" — the gate is human review of the first regen, in-test properties thereafter |
| Classify table reads files, not inline strings | `testdata/quality/*.html` via `qualityFixture()` | Files double as scrape/crawl-level origin bodies (serve same bytes with 403) — one fixture corpus, three tiers; inline strings would fork |
| Rich-with-marker negative is a first-class fixture | `rich-with-marker.html`: 400+ word article containing "Just a moment" + `__cf_chl` in a code sample | The false-positive guard is the highest-risk A4 behavior (blocks real content); a dedicated file beats an inline string nobody can read |
| Challenge warmup asserts hit counts, not just final body | `challengeHits` struct records per-path counts + observed Cookie headers | Final-body-only would pass a fetcher that skips warmup when the origin is lenient; counts prove the exact 3-request shape (challenge → homepage → retry-with-cookie) |
| No-retry-when-clean negative | Rich page containing "captcha" served 200 → assert exactly 1 hit | Prevents an over-eager detector from doubling every fetch; mirrors the Classify negative at the fetch layer |
| `fetch_test.go` untouched as the default-profile gate | Zero modifications — existing tests prove today's headers byte-identical | Stronger than a new assertion: if profiles.go disturbs the default path, the old suite screams |
| Duplicate quality-origin bytes per package, no shared fixture server | Each package serves `qualityFixture`-equivalent bytes from its own httptest server | Cross-package test imports need an exported helper package — rule of three says copy the 10-line handler (3 call sites: scrape, cli, crawl; mcp reuses scrape path via tool call) |
| Crawl quality test asserts absence (no record, no cache row) | Negative assertions on writer output + `GetSelectors` not-found | The A4 contract is "counts, never caches" — presence-only assertions would miss a leaky writer |
| MCP content back-compat asserted, not just new field | `markdown` still populated AND `content` rendered when `page_format` set | Strict-schema clients (Claude Desktop) break on removed fields — the plan promises deprecation-without-removal; the test locks it |
| cascadia promotion is a CI grep, not a Go test | `! grep -q 'cascadia.*indirect' go.mod` in §9 gate | A Go test cannot meaningfully assert direct-vs-indirect; prose + gate command is the honest test |
| Cross-compile + vet + lint stay CI commands | Same as Phase 1 (§9 full gate) | A Go test cannot change GOOS; `go vet`/`gofmt`/`golangci-lint` are pipeline steps |

---

## 7. Example Test Case

```go
// clean/quality_test.go
package clean_test

import (
    "errors"
    "strings"
    "testing"

    "gomagpie/clean"
)

// Table-driven = Go's parametrize. Statuses mirror what fetch hands in:
// non-2xx reach Classify via RawPage.StatusCode (A4 plumbing); 200 rows
// prove the content-only paths (empty detection, false-positive guard).
func TestClassify_Table(t *testing.T) {
    rich := qualityFixture(t, "rich-with-marker") // 400+ words, contains "Just a moment" + __cf_chl in a code sample
    cases := []struct {
        name   string
        md     string // scored markdown (stand-in for trafilatura output)
        status int
        body   []byte
        want   clean.Issue
    }{
        {"akamai 403 thin", "# Blocked\n\nJust a moment.", 403, qualityFixture(t, "challenge-akamai"), clean.IssueAccessDenied},
        {"login wall 401", "# Sign in\n\nLog in to continue.", 401, qualityFixture(t, "login-wall"), clean.IssueLoginRequired},
        {"unavailable 503", "# Down\n\nTry later.", 503, []byte("<html><body>down</body></html>"), clean.IssueUnavailable},
        {"empty spa shell", "# App\n\nLoading.", 200, qualityFixture(t, "empty-shell"), clean.IssueEmpty},
        {"rich article mentioning Just a moment", string(richMD(rich)), 200, rich, clean.IssueNone}, // THE false-positive guard
        {"rich article served 403 is still fine", string(richMD(rich)), 403, rich, clean.IssueNone}, // status alone never blocks rich content
        {"clean 200 article", "# News\n\n" + strings.Repeat("honest prose ", 60), 200, []byte("<html></html>"), clean.IssueNone},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            got := clean.Classify(clean.CleanedPage{Markdown: c.md}, c.status, c.body)
            if got != c.want {
                t.Errorf("Classify() = %q, want %q", got, c.want)
            }
        })
    }
}

// Fail-safe: Clean attaches Quality but NEVER fails on it — enforcement
// lives in scrape.Run / crawl (exit 8 / counted errors), never here.
func TestClean_AttachesQualityWithoutFailing(t *testing.T) {
    raw := qualityFixture(t, "challenge-akamai")
    got, err := clean.Clean(t.Context(), clean.RawPage{HTML: raw, StatusCode: 403, FinalURL: "https://example.com/waf"})
    if err != nil {
        t.Fatalf("Clean must not fail on quality input: %v", err)
    }
    if got.Quality == "" {
        t.Error("Quality empty on a 403 challenge page — gate can never fire downstream")
    }
    if !errors.Is(wrapQuality(got.Quality), clean.ErrQuality) {
        t.Errorf("Quality %q does not wrap ErrQuality — scrape.Run cannot map it", got.Quality)
    }
}

func TestWordCount_Vectors(t *testing.T) {
    cases := map[string]struct {
        in   string
        want int
    }{
        "empty":        {"", 0},
        "markdown mix": {"# Hi\n\n**bold** and [link](http://x) here", 5}, // Hi bold and link here
        "code fences":  {"```go\nfmt.Println()\n```", 1},                 // fence markers are not words
    }
    for name, c := range cases {
        t.Run(name, func(t *testing.T) {
            if got := clean.WordCount(c.in); got != c.want {
                t.Errorf("WordCount(%q) = %d, want %d", c.in, got, c.want)
            }
        })
    }
}
```

Notes for the executor: `richMD` is a 3-line helper extracting the fixture's body text (or inline `strings.Repeat` prose — either is fine, keep it readable); `wrapQuality` mirrors the one-line `fmt.Errorf("…: %w", clean.ErrQuality)` mapping `scrape.Run` performs — if `Issue` already carries an `Err()`/`Unwrap()` method, use it instead and drop the helper. The `t.Context()` idiom matches existing `cleanFile()`.

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase A of `gomagpie` — output + scoping + quality. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-A.md` (implementation plan — A0–A5 tasks, `Render`/`Scope`/`Issue`/`Metadata` shapes, `cascadia.Compile` pattern, `--page-format` flag-only rule, non-2xx pass-through), `plan/webclaw-gap-spec.md` §5 + §7 (acceptance + goldens), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test.

### What This Project Is
Go 1.26+ CLI web scraper (binary `magpie`, module `gomagpie`): fetch → clean → extract. Phase A adds `clean/llm.go` (`ToLLMText`/`ToText`/`Render`), `clean/scope.go` (`Scope`+`ApplyScope`), `clean/quality.go` (`Issue`+`Classify`+`ErrQuality`), `clean/metadata.go` (`Metadata`+`HarvestMetadata`), fetch header profiles + challenge warmup + cookies, `scrape.Options.PageFormat/Scope/Profile/Cookies` + non-2xx pass-through, CLI `--page-format` (flag-only) + scoping flags + exit 8, crawl quality page-error path, MCP `scrape_url` new inputs + `content`. Tests are hermetic: `go test ./...` with no network, no browser, no keys. No new test deps. Production code will exist (implement first, or write tests against the §3 shapes below — either order works since pure-function signatures are frozen by the plan).

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | llm format shrinks tokens, zero link loss | TestLLMText_Reduction + LinksPreserved | len(llm)<len(md) ×3 fixtures; link-set(md) ⊆ ## Links |
| US-2 | scoping works; invalid selectors warn-not-fail | TestScope_* table | article kept/nav dropped; `??bad[[` → full content + "invalid selector" warning |
| US-3 | challenge warmup retries once with warmed cookies; profiles distinct | TestChallenge_WarmupRetry + TestProfiles_* | 3-hit shape with Cookie observed; chrome≠firefox UA server-side |
| US-4 | blocked → typed errors/exit 8; rich+marker passes clean | TestClassify_* + TestScrape_Exit8 + no-cache-write | access-denied/login-required/unavailable/empty classified; "Just a moment" article → none; exit 8; 0 cache writes |
| US-5 | metadata on every scrape; old goldens byte-identical | TestMetadata_* + untouched clean goldens | 8 fields incl. absolute favicon; metadata.json golden; testdata/clean/*.md unchanged |

### Why Fakes Are Required
- Scraped websites (challenge flows, profiles, 403 bodies): must be deterministic hit-count-assertable — `httptest.Server` route shapes (§3-equivalent: `challengeHits`+`newChallengeOrigin`, header-echo mux). No live sites, ever.
- LLM extractor: Phase A never invokes it — the existing per-package `fakeExtractor` (scrape/crawl/mcp copies, `Attempts: 1`) proves zero-call-on-quality-failure. Do not invent a new fake.
- SQLite: NOT faked — real `modernc.org/sqlite` on `t.TempDir()` via existing `openScrapeDB`/`openCrawlDB`/`openMCPDB` helpers.
- Browser: untouched — existing `//go:build browser` smoke stays as-is; no new browser test.

### What NOT to Test
- Don't test trafilatura/html-to-markdown/goquery/cascadia internals — goldens + `cascadia.Compile`-invalid vectors lock OUR contract only.
- Don't test Cobra parsing beyond our flags; don't test `net/http` redirect/gzip/cookie-jar mechanics (Phase 1 locked those).
- Don't test `config.Config.Format` envelope behavior — assert only that `--page-format` leaves it at `json`.
- Don't create a shared `testutil`/`testhelpers` package — copy the 10-line quality-origin handler per package (3 call sites max).
- Don't test Phase B/C/D surface (verticals, batch/map tools, crawl globs) — out of scope, even if tempting.
- Don't commit `-update` regen output without hand-reading the diff (US-1 property asserts are the real gate).

### Critical: Helper Implementations

Copy these verbatim (new code — the ONLY new shared test code in this phase):

```go
func goldenDir(t *testing.T, dir, name, got string) {
    t.Helper()
    path := filepath.Join("..", "testdata", dir, name)
    got = strings.TrimSpace(got) + "\n"
    if *update {
        if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
            t.Fatal(err)
        }
        return
    }
    want, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("golden %s missing (run with -update): %v", path, err)
    }
    if strings.TrimSpace(string(want)) != strings.TrimSpace(got) {
        t.Errorf("golden %s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
    }
}
```

`challengeHits` + `newChallengeOrigin` + header-echo mux: full source in test-plan §3 — copy from there (stateful 403→homepage→200 shape with Cookie assertions; needs `sync`, `io`, `strings` imports).

Reuse verbatim (existing — read before use, do not modify): `golden`, `cleanFile`, `update` flag (`clean/clean_test.go`); `newFakeOrigin` (`fetch/fetch_test.go`); `fakeExtractor`+`total()`, `scrapeOrigin`, `openScrapeDB`, `fakeDeps`, `testSchema` (`scrape/scrape_test.go`); `codeOf` + `GOMAGPIE_BASE_URL` pattern (`cli/scrape_test.go`); `newSiteOrigin`, crawl `fakeExtractor`, `openCrawlDB` (`crawl/crawl_test.go`); `testMCPServer`, `dialInMemory`, `callTool`, `decodeOut`, `openMCPDB` (`mcp/server_test.go`).

### Test Files to Create/Change

```
clean/llm_test.go          # NEW (~7 funcs): goldens×3 + reduction + links + structured-gate + ToText table + Render matrix
clean/scope_test.go        # NEW (~9 funcs): include/exclude/union/invalid/no-match/cap/exclude-wins/only-main/empty-scope
clean/quality_test.go      # NEW (~4 funcs): §7 full source (Classify table + attach-without-failing + WordCount) + default-empty
clean/metadata_test.go     # NEW (~4 funcs): article fields + favicon-absolute + word-count-equality + missing-tags + json golden
clean/clean_test.go        # ADD goldenDir + TestClean_PopulatesNewFields; existing tests UNTOUCHED
fetch/profiles_test.go     # NEW (~8 funcs): default/unknown/chrome/firefox + IsChallengePage table + warmup + no-retry + cookies
fetch/fetch_test.go        # UNTOUCHED (default-profile regression gate)
scrape/scrape_test.go      # ADD (~7 funcs): page-format llm/json, scope/profile/cookies passthrough, ErrQuality + old-text-absent, no-cache-write
cli/scrape_test.go         # ADD (~4 funcs): exit 8 + stderr text, page-format valid/bogus, Config.Format untouched, scope/profile/cookie flags
crawl/crawl_test.go        # ADD (1 func): TestCrawl_QualityCountedNotCached (or new file if it grows)
mcp/server_test.go         # ADD (~3 funcs): page_format+backcompat, include+cookies, 403 → tool error
testdata/llm/              # 7 files via -update + HAND-REVIEW (article/product/spa-shell .llm.md + .txt, metadata.json)
testdata/quality/          # 4 files: challenge-akamai.html, login-wall.html, empty-shell.html, rich-with-marker.html
```

### Per-File Coverage Guidance

#### clean/llm_test.go
Goldens: `goldenDir(t, "llm", "<name>.llm.md", ToLLMText(cleanFile(t, "<name>")))` and `.txt` via `ToText(got.Markdown)` for article/product/spa-shell. Reduction: loop the 3 fixtures, `len(llm) >= len(md)` → Error (bytes; note len/4 token proxy in a comment). Links: extract `](http…)` targets from md with a 5-line regex helper, assert each appears in the `## Links` section (split output at the `## Links` header first — body links don't count). Structured gate: synthetic `CleanedPage{StructuredData: <16KB+ JSON with WebSite/WebPage/articleBody-dupe>}` → output has no `WebSite`, total structured block ≤16 KB. ToText table: `# H`→`H`, `**b**`→`b`, `` `c` ``→`c`, `[t](u)`→`t`, `![a](u)`→dropped, `> q`→`q`, `| a |` kept as text. Render matrix: `""`→identity; `"bogus"`→ error containing `markdown|llm|text|json`; `json`→ unmarshal to map, assert old keys (`url,final_url,title,markdown,structured_data`) + new keys (`content,metadata,links,word_count`) all present.

#### clean/scope_test.go
Inline shell HTML const (nav with 2 links, article with H1+2 paragraphs, aside, footer). Table rows: include `article` → contains H1 text, not "nav-link-1", not "footer"; union `article,aside` → both kept; exclude `nav,.sidebar,footer` → article intact; `??bad[[` → output == input AND warns contains "invalid selector"; `:::`  → same invalid path (second vector, cheap); `section.nope` → output empty AND warns contains "matched nothing"; 101 generated selectors (`div.c0…div.c100`) → one truncation warning + still scopes; include `body` + exclude `nav` → exclude wins. Only-main-content + empty scope → byte-identical to input (deviation lock).

#### clean/quality_test.go
Full source in §7 — implement verbatim, adapting only `richMD`/`wrapQuality` micro-helpers to the real `Issue` API (if `Issue` has no wrap helper, assert `errors.Is` against the `scrape.Run`-style wrap you implement in A4 — the test and the mapping must agree). Add `TestClean_DefaultQualityEmpty`: `cleanFile(t, "article").Quality == clean.IssueNone`.

#### clean/metadata_test.go
Article fixture: Description non-empty, Lang == `en` (check what the fixture actually declares — read the `<html>` tag first; assert equality with the real value, don't guess), SiteName/Image per OG tags, Favicon starts with `http` (absolute — pass `https://example.com/article` as pageURL), `WordCount == len(strings.Fields(got.Markdown))`. Missing-tags: `<html><body><p>` + 300 words → zero Metadata except WordCount. Golden: `goldenDir(t, "llm", "metadata.json", pretty(Metadata))` with 2-space indent.

#### clean/clean_test.go (additions only)
`TestClean_PopulatesNewFields`: article → `WordCount > 100`, `Quality == ""`, `Metadata.Lang != ""`. Do NOT touch existing tests — they are the byte-identical gate (US-5).

#### fetch/profiles_test.go
Default: fetch from echo origin with `FetchRequest{URL}` (no profile) → body contains `magpie/1.0`. Unknown `Profile: "safari"` → same UA, nil error. Chrome/firefox → `Chrome/126`+`Sec-CH-UA` / `Firefox/128` asserted from echo body. `Cookies: "a=b; c=d"` → echo a second header (`X-Cookie` echo of `r.Header.Get("Cookie")`) contains verbatim string. IsChallengePage table: (403+`Just a moment`+2KB→true), (200+rich+`captcha` word→false), (200+thin+`captcha`→true — marker-on-thin-page rule), (404+5KB no markers→false), (503+thin→true via status gate). Warmup: `newChallengeOrigin` → fetch → assert 3-hit shape + warmed Cookie + "Real page" body. No-retry: rich 200 page with "captcha" → 1 hit. Verify-only: grep that `crawl/backoff.go` handles `Retry-After` (no test — PR note).

#### scrape/scrape_test.go (additions)
`TestRun_PageFormatLLM`: `scrapeOrigin` + `Options{Render:"static", PageFormat:"llm"}` → `res.Rendered` contains `## Links`, `res.Markdown` unchanged non-empty. `TestRun_PageFormatJSON`: unmarshal `res.Rendered` → old+new keys present. `TestRun_ScopePassthrough`: shell body + `Scope{Include:["article"]}` → markdown lacks nav text. `TestRun_ProfileCookiesPassthrough`: echo origin + `Profile:"chrome", Cookies:"a=b"` → body shows chrome UA and cookie (proves Options→FetchRequest wiring). `TestRun_QualityPassthrough`: 403 origin serving challenge-akamai bytes → `errors.Is(err, clean.ErrQuality)`, `!strings.Contains(err.Error(), "fetch: HTTP")`. `TestRun_QualityNoCacheWrite`: same but with schema + `UseCache:true` → `fx.total()==0` and `GetSelectors` returns not-found.

#### cli/scrape_test.go (additions)
`TestScrape_Exit8`: httptest 403 origin + run scrape cmd → `codeOf == 8`, stderr contains `quality blocked (access-denied)`. `TestScrape_PageFormatFlags`: file:// article + `--page-format llm` → stdout contains `## Links`; `--page-format bogus` → exit 2. `TestScrape_PageFormatLeavesConfig`: `--page-format llm` run then assert no `format`-validation error path taken (exit 0 proves it — `csv`/`sqlite` rejection lives on `Config.Format`, untouched). `TestScrape_ScopeAndProfileFlags`: `--include article --header-profile firefox --cookies "a=b"` on shell fixture → exit 0, nav absent.

#### crawl/crawl_test.go (addition)
`TestCrawl_QualityCountedNotCached`: `newSiteOrigin` with index linking `/good` + `/deny` (deny served 403+challenge bytes — extend origin or second server per §5 note) → `Run` → `PagesErr >= 1`, records == 1 and its URL is `/good`, `GetSelectors(denyHost)` not-found, writer output contains no `/deny` URL. Assert `backoff.go` retry taxonomy untouched: no new retry test (existing backoff tests cover it).

#### mcp/server_test.go (additions)
`TestScrapeURL_PageFormat`: `callTool(cs, "scrape_url", {url, page_format: "llm"})` → `decodeOut` has `content` with `## Links` AND `markdown` non-empty (back-compat lock). `TestScrapeURL_ScopeAndCookies`: `{url, include: ["article"], cookies: "a=b"}` on shell origin → content lacks nav; origin observed `Cookie: a=b` (needs header-recording origin — 10-line mux). `TestScrapeURL_QualityError`: 403 origin → `CallTool` returns non-nil error containing `access-denied` (tool error, not result).

### Data Model Notes (Go)
- Plain structs: assert fields directly (`got.Quality`, `meta.Favicon`); `map[string]any` envelopes (`decodeOut`, Render-json): assert key presence + spot values, never full-map equality (key order varies).
- Sentinel errors: `errors.Is(err, clean.ErrQuality)` — never string-match except asserting the OLD text is gone.
- Golden comparisons: exact equality after TrimSpace via `goldenDir`; diff printed by the helper (no diff dep).
- Exit codes via existing `codeOf(err)` helper — never spawn subprocesses.

### Success Criteria
- `go test ./...` exits 0 with ~40 new test funcs (`go test ./... -v 2>&1 | grep -c '^=== RUN'` ≥ 219 vs measured baseline 179 on 2026-09-17 — recount if main moved)
- `go test ./clean/ -update` regenerates `testdata/llm/*` + `metadata.json`, hand-reviewed (`git diff` read, not skimmed), then `go test ./...` green
- Existing `testdata/clean/*.md` untouched by any regen (`git status --porcelain testdata/clean/` empty)
- `go vet ./...` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean
- Every deliverable from `plan/phase-A.md` §4 maps to §4 list above (no orphan deliverable, no orphan test file)

### Expected File Structure at End
(Same tree as Test File List §4 — reproduce exactly. No `testutil` package, no new harness, no extra fixtures beyond the 11 listed files.)
---

---

## 9. Run Commands

```bash
# Fast hermetic suite (every push — no network, no browser, no keys)
go test ./...

# New-test count vs baseline (non-vacuous: baseline 179 RUN lines on 2026-09-17; require ≥219 after)
go test ./... -v 2>&1 | tee /tmp/phaseA-tests.log; grep -c '^=== RUN' /tmp/phaseA-tests.log

# Focused per task (mirrors phase-A sanity checks)
go test ./clean/ -run 'TestLLM|TestText|TestRender' -v          # A1
go test ./clean/ -run TestScope -v                              # A2
go test ./fetch/ -run 'TestProfile|TestChallenge|TestCookies|TestIsChallenge' -v  # A3
go test ./clean/ -run TestClassify -v && go test ./scrape/ -run TestQuality -v    # A4
go test ./clean/ -run TestMetadata -v                           # A5
go test ./cli/ -run 'TestScrape_Exit8|TestScrape_PageFormat|TestScrape_Scope' -v
go test ./mcp/ -run TestScrapeURL -v
go test ./crawl/ -run TestCrawl_Quality -v

# Regenerate llm/metadata goldens (then HAND-REVIEW before committing)
go test ./clean/ -update && git status --porcelain testdata/ && git diff testdata/llm/ | head -100

# Prove old goldens untouched
git status --porcelain testdata/clean/   # must print nothing

# Full gate (mirrors phase-A exit criterion 8)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK

# go.mod direct-dep gate (cascadia promotion)
! grep -q 'andybalholm/cascadia.*indirect' go.mod && echo DEPMOD-OK
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service/pipeline; pure-function tables + httptest route shapes + reused fakeExtractors/temp-DBs (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to A1–A5 behaviors
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 11 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all 14 §4 deliverables (`clean/*.go`×5, `fetch/*`×3, `scrape`, `cli`, `crawl`, `mcp`, `go.mod` via grep gate, `testdata/` via regen+review)
- [x] Every external/heavy dependency has a fake or mock equivalent — no heavy deps in Phase A (stdlib + pinned goquery only); network peers → httptest origins; LLM → existing fakeExtractor (zero-call asserts); SQLite → real pure-Go temp DB (justified, Phase-1 precedent); keyring/browser untouched
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — httptest + `file://` + `GOMAGPIE_BASE_URL` only; "What NOT to Test" bans live sites/keys
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation (Phase-1 precedent): no integration tier exists; hermetic-only suite, browser smoke stays behind its build tag, manual smoke is explicitly not-a-test-file
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure reusing the existing `-update` flag; stated at the top of §5 per skill rule
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — adapted: §8 pastes `goldenDir` + `challengeHits`/`newChallengeOrigin` full source verbatim and names every reused helper with file:line-level precision
- [x] Run commands section is present — §9 with fast/focused/regen/smoke/full-gate/depmod commands
