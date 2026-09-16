# Phase 1 — Testing: Single-URL fetch→clean→extract

**Scope:** `fetch/` (http, detect, rod), `clean/` (clean, sidecar), `extract/` (schema, openai, anthropic, coerce, cost, repair loop), `config/`, `store/`, `cmd/magpie/` (scrape, extract, config commands)
**Key Pattern:** Fake LLM providers and origin servers with `httptest.Server`; real pure-Go SQLite on `t.TempDir()`; browser tests gated behind the `browser` build tag — `go test ./...` never touches network, keys, or Chrome.
**Dependencies:** stdlib `net/http/httptest`, `testing`, `os`, `path/filepath` only — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As a user, I want `magpie scrape <url> --schema price.yaml` to print schema-valid JSON, so that I get structured data from one command | `TestScrape_EndToEnd_FileURL` in `cmd/magpie` (file:// URL + httptest fake LLM, asserts `jsonschema.Validate` passes on stdout) | exit 0 AND stdout validates against schema |
| US-2 | As a user, I want static pages to never launch a browser and SPA shells to escalate, so that scrapes stay fast and cheap | `TestDetect_*` table test + scrape log-line assertion (`RodFetcher` constructor never called on static path) | `NeedsBrowser(score)>=2` iff SPA fixture; static scrape emits no browser log line |
| US-3 | As a user, I want `magpie extract` to work from stdin/file with no fetch, so that I can pipe saved HTML | `TestExtractCmd_StdinHTML` (pipe `testdata/clean/article.html` via stdin, fake LLM) | exit 0 AND stdout is schema-valid JSON with 0 fetch calls |
| US-4 | As a user, I want a transiently-invalid LLM output to be repaired automatically but persistent garbage to fail loudly, so that I don't silently get bad data | `TestRepairLoop_InvalidThenValid` (fake provider: invalid → valid, assert exactly 2 calls) + `TestRepairLoop_Exhausted` (3× invalid → error containing validator text) | 2 calls then valid JSON; exhausted case returns non-nil error |
| US-5 | As a user, I want `--max-cost` to abort before overspending and missing keys to explain themselves, so that I never get a surprise bill | `TestMaxCost_AbortsBeforeCall` (ceiling below projected cost → exit 6, 0 LLM calls) + `TestMissingKey_Exit7` | exit 6 with zero provider hits; exit 7 with "set via flag, GOMAGPIE_* env, or `magpie config set-key`" on stderr |

---

## 1. Component Mock Strategy

Phase type: **Service/pipeline** (CLI fetch→clean→extract with LLM and browser edges). Mock strategy in one sentence: **fake every network peer with `httptest.Server` (origin pages + OpenAI/Anthropic provider endpoints), use the real pure-Go SQLite driver on temp files, and gate the one real-browser smoke test behind `//go:build browser`.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `fetch/http.go` StaticFetcher | `httptest.Server`: redirect chain (11 hops), gzip body, 429 status, plus `file://` fixture read | >10 redirects → error; gzip auto-decodes; 429 returned faithfully (status+body, no retry); `file:///abs` returns fixture bytes | US-2 |
| `fetch/detect.go` ScoreJSRequired | No mock (pure logic on `[]byte` + headers) — inline HTML fixtures | Empty React root scores ≥2; `<app-root>` ≥2; text-rich static = 0; `__NEXT_DATA__` → `hasEmbeddedData=true`; `<meta name="fragment">` +1 | US-2 |
| `fetch/rod.go` RodFetcher | No fake — single smoke test with `//go:build browser` + `launcher.LookPath()` skip guard, `data:` URL only | Launches, renders, `Close()` leaves no zombie; skipped (not failed) when no Chrome | US-2 |
| `clean/` Clean + sidecar | No mock — golden files under `testdata/clean/` + `-update` flag | `article/product/spa-shell` markdown matches golden; tables are GFM; JSON-LD/`__NEXT_DATA__`/`__NUXT__` harvested verbatim pre-trafilatura | US-1 |
| `extract/` providers (openai, anthropic) | `httptest.Server` canned-sequence fake provider: scripted response list, records request count + bodies | Invalid→valid converges in exactly 2 calls; repair prompt (attempt ≥1) embeds validator `err.Error()` text; `stop_reason` truncation surfaced | US-4 |
| `extract/` schema + coerce | No mock (pure logic) — `price.yaml`/`book.yaml` + table vectors | Wrong-type doc fails with `err.Error()` containing `at '/price'`; `eur_decimal` ("1 234,56 €"→1234.56), `int/float/iso_date/bool/trim/regex`; unknown `coerce` value errors at load | US-1, US-4 |
| `extract/cost.go` + `--max-cost` | Fake provider + temp-file store (counts real `llm_calls` rows, no mock) | `--max-cost 0.000001` → exit 6 with 0 provider hits (pre-check = running total + prompt-bytes/4 × input price); unknown model warns + costs 0 | US-5 |
| `config/` precedence + keyring | Env-var path only — no OS keyring in tests; temp `XDG_CONFIG_HOME` + temp YAML file | flags > env (`GOMAGPIE_*`) > file > defaults; key-lookup order flag > env > keyring > file; `show` prints `***redacted***` | US-5 |
| `store/` SQLite | Real `modernc.org/sqlite` driver on `t.TempDir()` file (pure Go, ~ms) — no mock | All five §9.2 tables exist after `Open`; `PRAGMA foreign_keys` == 1; `BeginRun`→`LogLLMCall`→`FinishRun` round-trips; ≥1 `llm_calls` row + 1 finished `run_history` row post-scrape | US-1, US-5 |
| `cmd/magpie` scrape/extract/config | Wire together: `file://` URL (Task 1.4 transport) + fake-provider base URL via test-only `GOMAGPIE_BASE_URL` env override | Full matrix: no-schema → markdown JSON + 0 `llm_calls`; with-schema → valid JSON + ≥1 call; `--render static` never constructs `RodFetcher`; `--format` rejects `csv`/`sqlite` loudly | US-1, US-2, US-3 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | httptest fakes, temp-dir SQLite, inline fixtures — no network, no browser, no keys | <30s total | Every push; the only default gate |
| Browser (`go test -tags browser ./fetch/`) | Real Chrome/Chromium via `launcher.LookPath()`; skips (never fails) when absent | ~30–60s when Chrome present | Separate CI job, never in default suite |
| E2E (manual, not a test file) | Built binary + `file://` fixtures + optional local Ollama | minutes | Pre-release sanity: the 10 Exit Criteria command lines from `plan/phase-1.md` §5 |

No integration tier with live providers: real OpenAI/Anthropic keys must never appear in any test. The httptest fake provider IS the provider contract test (request shape asserted on the recorded body).

---

## 3. Fake / Mock Implementations

One fake per network peer. Both are ~30-line `httptest.Server` wrappers living in the test package that uses them (not shared — Go has no cross-package test imports without an extra package; duplication of a 30-line helper beats a `testutil` package in Phase 1).

### `fakeProvider` — replaces the OpenAI chat-completions / Anthropic messages endpoint

```go
// extract/extract_test.go (and mirrored minimal copy in cmd/magpie for the e2e scrape test)
type fakeProvider struct {
    t         *testing.T
    mu        sync.Mutex
    script    []string // canned response BODIES, one per call (invalid JSON first, valid JSON later)
    calls     int
    bodies    []string // recorded request bodies for shape assertions
    status    int      // default 200
}

func newFakeProvider(t *testing.T, script ...string) (*httptest.Server, *fakeProvider) {
    t.Helper()
    fp := &fakeProvider{t: t, script: script, status: 200}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        fp.mu.Lock()
        defer fp.mu.Unlock()
        fp.calls++
        fp.bodies = append(fp.bodies, string(body))
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(fp.status)
        idx := min(fp.calls-1, len(fp.script)-1)
        _, _ = io.WriteString(w, fp.script[idx])
    }))
    t.Cleanup(srv.Close)
    return srv, fp
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
```

Response bodies: for the OpenAI adapter, each script entry is a full chat-completions envelope `{"choices":[{"message":{"content": <RAW>}}]}` where `<RAW>` is the JSON-string-escaped LLM output under test; for Anthropic, `{"content":[{"type":"text","text": <RAW>}],"stop_reason":"end_turn"}`. Tests assert `callCount()` (repair convergence = exactly 2) and grep `bodies[1]` for the validator error text (repair prompt embeds `err.Error()` verbatim).

**Matches real call:** production `openai.go` does `POST {base}/chat/completions` and `anthropic.go` does `POST {base}/v1/messages`; tests set `base = srv.URL` (via the `GOMAGPIE_BASE_URL` env override in `cmd`, direct constructor arg in `extract`), so request path, headers (`x-api-key`, `anthropic-version: 2023-06-01`), and body shape are asserted against the real wire format.

### `fakeOrigin` — replaces the scraped website (fetch tests)

```go
// fetch/fetch_test.go
func newFakeOrigin(t *testing.T, mux *http.ServeMux) *httptest.Server {
    t.Helper()
    srv := httptest.NewServer(mux)
    t.Cleanup(srv.Close)
    return srv
}
```

Routes registered per test: `/redirect-loop` (302 chain of 11 → expect error), `/gzip` (gzip-encoded article → expect decoded body), `/rate-limited` (429 with body → expect faithful 429 passthrough, no retry), `/spa-shell` (empty `#root` + bundle script → detect score ≥2), `/static` (text-rich → score 0), `/next` (`__NEXT_DATA__` payload → embedded=true). No canned-HTML files needed — handlers write small inline strings.

---

## 4. Test File List

```
gomagpie/
├── fetch/
│   ├── fetch_test.go        # httptest origin: redirect cap, gzip, 429 passthrough, file:// read; detect table (6+ subtests: react-root/app-root/static/__NEXT_DATA__/fragment/noscript)
│   └── rod_smoke_test.go    # //go:build browser — LookPath guard, data: URL render + Close only
├── clean/
│   └── clean_test.go        # goldens (article/product/spa-shell .html→.md) + -update flag def; sidecar harvest assertions; empty-node/table-span regression
├── extract/
│   └── extract_test.go      # fake-provider repair (invalid→valid=2 calls; 3×invalid=error); schema error-text asserts at '/price'; coerce vectors; cost-table + max-cost projection
├── config/
│   └── config_test.go       # precedence flags>env>file>defaults; key-lookup order via env path; show redacts; 0600-perm warning
├── store/
│   └── sqlite_test.go       # temp-file DDL (5 tables), PRAGMA foreign_keys==1, BeginRun/LogLLMCall/FinishRun round-trip
├── cmd/magpie/
│   └── scrape_test.go       # file:// e2e matrix: no-schema→markdown+0 calls; schema→valid JSON+≥1 call; --render static never constructs rod; csv/sqlite rejected; exit 6/7 paths
├── testdata/
│   ├── clean/article.html/.md, product.html/.md, spa-shell.html/.md   # golden fixtures (created by these tests' -update run)
│   └── extract/price.yaml, book.yaml, invalid-doc.json                # schema fixtures
└── plan/phase-1-tests.md    # this file
```

Every deliverable in `plan/phase-1.md` §4 has a test file: `cmd/magpie/*.go` → `scrape_test.go`; `fetch/*.go` → `fetch_test.go` + `rod_smoke_test.go`; `clean/*.go` → `clean_test.go`; `extract/*.go` → `extract_test.go`; `config/*.go` → `config_test.go`; `store/*.go` → `sqlite_test.go`; `go.mod` pins → cross-compile gate is a manual Exit-Criterion command ( Task 1.1 sanity check), not a Go test.

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): fixtures are per-package test helpers, integration gating is the `browser` build tag, and golden regeneration is the `-update` flag defined in `clean_test.go`. Additions only — no existing test files to preserve (greenfield; `testdata/` is empty).

```go
// fetch/fetch_test.go helpers
func newFakeOrigin(t *testing.T, mux *http.ServeMux) *httptest.Server // §3, routes per test

// fetch/rod_smoke_test.go — entire gate, first lines:
//go:build browser
package fetch_test
func TestRodSmoke(t *testing.T) {
    if _, ok := launcher.LookPath(); !ok { t.Skip("no Chrome/Chromium found") }
    ...
}

// clean/clean_test.go
var update = flag.Bool("update", false, "regenerate golden files") // default off; -update rewrites testdata/clean/*.md
func golden(t *testing.T, name, got string) // compares vs testdata/clean/<name>.md, or writes when *update

// extract/extract_test.go helpers
// newFakeProvider per §3 +:
//   openAIEnvelope(raw string) string  — wraps raw LLM text in chat-completions JSON
//   anthropicEnvelope(raw string) string
//   mustLoadSchema(t, "../testdata/extract/price.yaml")

// config/config_test.go helpers
func isolatedXDG(t *testing.T) string // t.Setenv XDG_CONFIG_HOME/HOME/APPDATA to t.TempDir(); file perms via os.Chmod
// keyring is NEVER touched: tests exercise the env-var lookup path only

// store/sqlite_test.go helpers
func openTempDB(t *testing.T) *store.DB // Open(filepath.Join(t.TempDir(), "t.db")); t.Cleanup(db.Close)

// cmd/magpie/scrape_test.go helpers
// minimal fakeProvider mirror (§3) + t.Setenv("GOMAGPIE_BASE_URL", srv.URL) + file:// URL to testdata/clean/article.html
```

Scope rationale: everything is function-scoped (`t.TempDir()`, per-test `httptest.Server`) — no shared state, no `TestMain`, safe for parallel `go test`. Nothing is worth session scope at this size.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Fake providers with `httptest.Server`, not interfaces-only | Canned script + recorded bodies; assert exact call counts and repair-prompt content | The wire format (response_format json_schema, output_config.format, headers) is the highest-risk surface — two plain-HTTP adapters with no SDK safety net; shape assertions on recorded bodies lock it |
| Real SQLite driver in tests, not a mock | Temp-file DB via `t.TempDir()` | `modernc.org/sqlite` is pure Go and millisecond-fast; mocking would hide the exact DSN-pragma and multi-statement-Exec behaviors the phase plan verified live |
| Browser: one smoke test behind build tag, not coverage | `//go:build browser` + `LookPath` skip + `data:` URL | Rod rendering is inherently non-hermetic; full coverage stays in static/httptest tests. The guard order matters — `Launch()` with no Chrome silently downloads ~150MB Chromium |
| Golden files for clean output | `-update` flag regenerates; default run compares | Locks the trafilatura→html-to-markdown contract (incl. the v2.5.0 empty-node panic regression and table colspan mirroring) across dependency bumps |
| No OS keyring in tests | Exercise flag>env>file paths; keyring only on the dev machine manually | Secret Service/dbus is absent on headless CI; the env fallback IS the tested path, keyring is a deployment convenience |
| Repair convergence asserted via call count, not timing | `callCount()==2` for invalid→valid; error text for exhausted | Deterministic, hermetic, and directly proves the 3-attempt loop from spec §3.5 |
| Cross-compile + vet + gofmt are CI commands, not Go tests | `CGO_ENABLED=0` × 3 targets, `go vet`, `gofmt -l` in the pipeline | A Go test cannot change GOOS; the phase-1 Task 1.1 sanity-check line is the test |
| Duplicate the 30-line fakeProvider in `cmd/magpie` | Copy, don't create a `testutil` package | Rule of three: two call sites don't justify a shared package with its own import graph in a CLI repo |

---

## 7. Example Test Case

```go
// extract/extract_test.go
package extract_test

import (
    "strings"
    "testing"

    "gomagpie/extract"
)

func TestRepairLoop_InvalidThenValid(t *testing.T) {
    // Attempt 0 returns a price as string (schema wants number); repair must converge on attempt 1.
    srv, fp := newFakeProvider(t,
        openAIEnvelope(`{"name":"Widget","price":"12.99"}`),
        openAIEnvelope(`{"name":"Widget","price":12.99}`),
    )
    ex := extract.NewOpenAI(srv.URL, "test-key", "gpt-4o-mini", mustLoadSchema(t, "../testdata/extract/price.yaml"))

    got, err := ex.Extract(t.Context(), extract.ExtractInput{
        Markdown: "# Widget\n\nPrice: 12.99",
    })
    if err != nil {
        t.Fatalf("Extract: %v", err)
    }
    if fp.callCount() != 2 {
        t.Fatalf("calls = %d, want exactly 2 (initial + one repair)", fp.callCount())
    }
    if !strings.Contains(fp.bodies[1], "at '/price'") {
        t.Errorf("repair prompt missing validator text; body[1] = %q", fp.bodies[1])
    }
    if got.Record["price"] != 12.99 {
        t.Errorf("price = %v, want 12.99", got.Record["price"])
    }
}

func TestRepairLoop_Exhausted(t *testing.T) {
    // Garbage three times → loud error carrying the validator output, never silent bad data.
    srv, fp := newFakeProvider(t,
        openAIEnvelope(`{"name":"Widget","price":"x"}`),
        openAIEnvelope(`{"name":"Widget","price":"y"}`),
        openAIEnvelope(`{"name":"Widget","price":"z"}`),
    )
    ex := extract.NewOpenAI(srv.URL, "test-key", "gpt-4o-mini", mustLoadSchema(t, "../testdata/extract/price.yaml"))

    _, err := ex.Extract(t.Context(), extract.ExtractInput{Markdown: "# Widget"})
    if err == nil {
        t.Fatal("expected error after 3 invalid attempts, got nil")
    }
    if !strings.Contains(err.Error(), "at '/price'") {
        t.Errorf("error missing validator text: %v", err)
    }
    if fp.callCount() != 3 {
        t.Errorf("calls = %d, want 3 (no fourth attempt)", fp.callCount())
    }
}

func TestCoerce_EURDecimal(t *testing.T) {
    cases := map[string]struct{ in string; want float64 }{
        "euro suffix + comma": {"1 234,56 €", 1234.56}, // nbsp between 1 and 234
        "plain":               {"12.99", 12.99},
        "symbol only":         {"€7,5", 7.5},
    }
    for name, c := range cases {
        t.Run(name, func(t *testing.T) {
            got, err := extract.Coerce("eur_decimal", c.in)
            if err != nil {
                t.Fatalf("Coerce: %v", err)
            }
            if got != c.want {
                t.Errorf("got %v, want %v", got, c.want)
            }
        })
    }
}
```

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase 1 of `gomagpie` — single-URL fetch→clean→extract. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-1.md` (the implementation plan — all 10 tasks, confirmed library APIs, wire formats), `spec.md` §§1–3, 9–10 (behavioral source of truth), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test.

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`, module `gomagpie`): static fetch → JS detection (browser escalate iff score ≥ 2) → trafilatura boilerplate removal → GFM markdown + JSON-LD sidecar → stdlib-HTTP LLM extraction (OpenAI-compatible adapter doubling for Ollama; Anthropic messages API with GA `output_config.format`) with jsonschema validation + 3-attempt repair. Pure-Go, zero CGO. Tests are hermetic: `go test ./...` must pass with no network, no browser, no API keys.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | scrape <url> --schema prints schema-valid JSON | TestScrape_EndToEnd_FileURL (file:// + fake LLM) | exit 0 AND stdout validates |
| US-2 | static never launches browser; SPA escalates | TestDetect_* table + no-browser log-line assert | score≥2 iff SPA; no browser line on static |
| US-3 | extract works from stdin/file with no fetch | TestExtractCmd_StdinHTML | exit 0, valid JSON, 0 fetches |
| US-4 | invalid→valid repairs; persistent garbage errors loudly | TestRepairLoop_InvalidThenValid (exactly 2 calls) + TestRepairLoop_Exhausted (error contains validator text) | 2 calls then JSON; 3rd failure errors |
| US-5 | --max-cost aborts pre-call; missing key explains itself | TestMaxCost_AbortsBeforeCall (0 provider hits) + TestMissingKey_Exit7 | exit 6 / exit 7 with hint text |

### Why Fakes Are Required
- OpenAI/Anthropic endpoints: real calls need keys, billing, and network — unit tests must never make them. Fake with `httptest.Server` canned scripts.
- Scraped websites: redirect/gzip/429/SPA behaviors must be deterministic — fake origin with `httptest.Server` routes.
- Chrome: absent on CI/WSL and `rod.Launcher` auto-downloads ~150MB Chromium when no binary is found — only a `//go:build browser` smoke test touches it, with a `launcher.LookPath()` skip guard FIRST.
- SQLite is NOT faked: `modernc.org/sqlite` is pure Go and fast — test against the real driver on `t.TempDir()` files.
- OS keyring is NOT touched: Secret Service is absent headless — tests use the env-var lookup path; keyring round-trip is a manual dev-machine check.

### What NOT to Test
- Don't test go-trafilatura / html-to-markdown / jsonschema / rod internals — golden files lock OUR contract across their upgrades.
- Don't test Cobra flag parsing or `net/http` redirect mechanics beyond our 10-cap policy.
- Don't test live providers, real websites, or real Chrome in the default suite.
- Don't create a shared `testutil` package — copy the 30-line fakeProvider where needed (two call sites).
- Don't stub Phase 2/3 commands (crawl/serve/build/cache) — reject unknown `--format csv|sqlite` loudly instead.

### Critical: Fake Implementations

Copy these verbatim into the owning test files (`fakeProvider` full copy in BOTH `extract/extract_test.go` and `cmd/magpie/scrape_test.go`; `fakeOrigin` in `fetch/fetch_test.go`):

```go
type fakeProvider struct {
    t         *testing.T
    mu        sync.Mutex
    script    []string
    calls     int
    bodies    []string
    status    int
}

func newFakeProvider(t *testing.T, script ...string) (*httptest.Server, *fakeProvider) {
    t.Helper()
    fp := &fakeProvider{t: t, script: script, status: 200}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        fp.mu.Lock()
        defer fp.mu.Unlock()
        fp.calls++
        fp.bodies = append(fp.bodies, string(body))
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(fp.status)
        idx := min(fp.calls-1, len(fp.script)-1)
        _, _ = io.WriteString(w, fp.script[idx])
    }))
    t.Cleanup(srv.Close)
    return srv, fp
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
```

Envelopes: OpenAI script entries are `{"choices":[{"message":{"content": <RAW>}}]}`; Anthropic entries are `{"content":[{"type":"text","text": <RAW>}],"stop_reason":"end_turn"}` where `<RAW>` is the JSON-string-escaped LLM output. Helpers `openAIEnvelope(raw)/anthropicEnvelope(raw)` build these. Truncation case: `stop_reason":"max_tokens"` entry → test asserts a truncation error before parsing.

### Test Files to Create

```
fetch/fetch_test.go        # httptest origin (redirect-cap/gzip/429/file://) + detect table (react-root/app-root/static/__NEXT_DATA__/fragment/noscript)
fetch/rod_smoke_test.go    # //go:build browser, LookPath skip first, data: URL render + Close
clean/clean_test.go        # goldens + -update flag + golden() helper; sidecar assertions; empty-node/table regression
extract/extract_test.go    # repair (2-call converge; 3-fail error text); schema at '/price'; coerce vectors; cost + max-cost projection
config/config_test.go      # precedence flags>env>file>defaults; key order flag>env>keyring>file (env path); redacted show; 0600 warning
store/sqlite_test.go       # temp DB: 5 tables, PRAGMA foreign_keys==1, BeginRun/LogLLMCall/FinishRun
cmd/magpie/scrape_test.go  # file:// e2e matrix (no-schema/schema/render/format/exit-6/exit-7)
testdata/clean/{article,product,spa-shell}.html + .md   # write .html fixtures first, generate .md via -update
testdata/extract/{price.yaml,book.yaml,invalid-doc.json}
```

### Per-File Coverage Guidance

#### fetch/fetch_test.go
Redirect chain of 11 → error (not silent truncation at 10). Gzip body auto-decodes to article text. 429 returns status+body faithfully (no retry — taxonomy is Phase 2). `file:///abs/path` returns fixture bytes (needs the `RegisterProtocol("file", ...)` line — this test proves it). Detect table, each an inline HTML string: empty `#root`+bundle → ≥2; `<app-root>` → ≥2; 2KB visible text, no scripts → 0; `#__NEXT_DATA__` → embedded=true (caller must skip browser); `<meta name="fragment" content="!">` → +1; noscript "enable javascript" (mixed case) → +1. Assert `NeedsBrowser(s) == (s>=2)` boundary at exactly 2.

#### clean/clean_test.go
Write the three `.html` fixtures first: article (headings, nested lists, images with alt, a 3×4 table with one colspan=2 cell, two empty `<p></p>` nodes — the v2.5.0 panic regression), product (JSON-LD Product with price + `__NEXT_DATA__` duplicate), spa-shell (empty root, expect short markdown but non-error). `-update` regenerates `.md`; committed `.md` files are the contract. Assert sidecar contains the full JSON-LD verbatim AND markdown contains a GFM table (`|`) with the spanning cell's text mirrored. Assert markdown token estimate (≈len/4) ≤ 8000 on a long fixture (repeat article body ×50 → capped top-down, sidecar intact).

#### extract/extract_test.go
Repair: invalid→valid script asserts `callCount()==2` and `bodies[1]` contains `at '/price'` (validator text embedded verbatim). Exhausted: 3× invalid → error contains `at '/price'`, `callCount()==3`. Schema: `price.yaml` valid doc passes; `invalid-doc.json` fails with `at '/price'` (v6 prints `/price`, never `#/price`); unknown `x-gomagpie.coerce` value errors at `LoadSchema`. Coerce vectors: `eur_decimal` ("1 234,56 €"→1234.56 with nbsp, "€7,5"→7.5), `int` ("42"→42), `iso_date` ("12.03.2024"→"2024-03-12"), `bool` ("ja"→true? only if implemented — test what schema.go documents), `trim`, `regex` capture group. `jsonld_path` walker: `$.offers.0.price` resolves. Cost: known model estimates > 0; unknown model warns + 0; max-cost projection (running 0 + prompt-bytes/4 × input price > ceiling) aborts before call 1.

#### config/config_test.go
Temp `XDG_CONFIG_HOME` + YAML file; `t.Setenv("GOMAGPIE_EXTRACT_PROVIDER","ollama")` overrides file; explicit param overrides env. Key order: flag > env (`GOMAGPIE_ANTHROPIC_API_KEY`) > file; keyring never called (no assertion possible — assert the env path wins). `show` output contains `***redacted***` and never the raw key. Config file with mode 0644 containing a key → warning logged.

#### store/sqlite_test.go
`Open` on `t.TempDir()` path creates all five tables (`selector_cache, crawl_state, dedup, run_history, llm_calls` — query `sqlite_master`). `PRAGMA foreign_keys` returns 1. `BeginRun`→`LogLLMCall` (purpose=extract)→`FinishRun` round-trips; `run_history` row is `finished`. Second `Open` on the same file migrates idempotently (no error, no duplicate tables).

#### cmd/magpie/scrape_test.go
Matrix over `file://` + `GOMAGPIE_BASE_URL=srv.URL`: (a) no `--schema` → exit 0, stdout parses as JSON with markdown field, `llm_calls` count 0; (b) with `--schema price.yaml` + valid script → exit 0, stdout validates, `llm_calls` ≥1, `run_history` finished; (c) `--render static` on SPA-shell HTML → exit 0, no `RodFetcher` construction (assert via log hook or constructor counter — never launch); (d) `--format csv` → non-zero exit with "not supported" text; (e) `--max-cost 0.000001` with schema → exit 6, 0 provider hits; (f) no key anywhere (unset env, empty keyring path, no flag) → exit 7 with the set-key hint. Stdin variant: pipe `article.html` bytes to `extract --content-type html` → valid JSON, zero fetch calls.

### Data Model Notes
- Plain structs with json/yaml tags: assert fields directly (`got.Markdown`, `rec["price"]`), never via reflection helpers.
- `*jsonschema.ValidationError`: assert on `err.Error()` containing `at '/price'` (human text is the repair input); optionally assert `Causes[0].InstanceLocation`.
-Golden comparisons: exact string equality after `strings.TrimSpace` normalization; show a unified diff on mismatch (use `os.WriteFile(t.TempDir()...)` + `t.Logf`, no diff dep).
- Exit codes asserted via the cobra `Execute()` error-to-code mapping helper, not by spawning subprocesses.

### Success Criteria
- `go test ./...` exits 0 with >0 tests in `fetch`, `clean`, `extract` (non-vacuous: `go test ./... -v 2>&1 | grep -c '^=== RUN'` > 20)
- `go test -tags browser ./fetch/ -run TestRodSmoke -v` SKIPs (no Chrome on WSL) rather than failing
- `go test ./clean/ -update` regenerates goldens and a subsequent `go test ./...` passes
- `go vet ./...` exits 0; `gofmt -l .` prints nothing
- Every deliverable from `plan/phase-1.md` §4 has at least one test file (see §4 list above)

### Expected File Structure at End
(Same tree as Test File List §4 — reproduce exactly, no `testutil` package, no extra harnesses.)
---

---

## 9. Run Commands

```bash
# Fast hermetic suite (every push — no network, no browser, no keys)
go test ./...

# Verbose with test counts (non-vacuous check: must show >20 RUN lines)
go test ./... -v 2>&1 | tee /tmp/phase1-tests.log; grep -c '^=== RUN' /tmp/phase1-tests.log

# Single package, focused
go test ./extract/ -run 'TestRepair|TestCoerce' -v
go test ./fetch/ -run TestDetect -v
go test ./fetch/ -run TestStatic -v
go test ./clean/ -v
go test ./config/ ./store/ -v

# Regenerate clean goldens (then re-run full suite to lock)
go test ./clean/ -update && go test ./... && git diff --stat testdata/

# Browser smoke (separate CI job; SKIPs without Chrome — never fails the default suite)
go test -tags browser ./fetch/ -run TestRodSmoke -v

# Full gate (mirrors Exit Criteria §8)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Service/pipeline; httptest fakes + real temp-file SQLite + build-tagged browser smoke (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to Milestone-1 behaviors (§1–§10 of phase plan)
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 10 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all §4 deliverables (`cmd`, `fetch`+rod smoke, `clean`, `extract`, `config`, `store`, `testdata`, `go.mod` via cross-build command)
- [x] Every external/heavy dependency has a fake or mock equivalent — LLM→fakeProvider, origin→fakeOrigin, Chrome→build-tag smoke, SQLite→real pure-Go temp DB (justified, not mocked), keyring→env path (justified, §6)
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — `GOMAGPIE_BASE_URL` override + `file://` transport; "What NOT to Test" bans live providers
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation: browser suite gated behind the `browser` build tag + `LookPath` skip; no live-provider tier exists by design
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure with `-update` flag registration documented and justified
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — full `fakeProvider` Go source pasted verbatim in §8
- [x] Run commands section is present — §9 with fast/focused/golden/browser/full-gate commands
