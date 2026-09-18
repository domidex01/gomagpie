# Phase I — Testing: Crawl Corpus Mode (schema-less RAG ingestion)

**Scope:** `crawl/crawl.go` (`Options.Corpus`, `newCrawlContext` guard scoping, `cleanPage` needLLM wrap, `extractPage` early return), `crawl/writer.go` (`writer.corpus`, `jsonRecord` → `(*writer).record`, OnRecord tee via `w.record`), `core/pipeline.go` (`PageResult.Title`/`.Text`), `cli/crawl.go` (`--corpus` flag + exit-2 validations + keyless wiring)
**Key Pattern:** **Zero new fakes — the existing crawl harness IS the mock.** Corpus runs ride `newSiteOrigin` (httptest serving robots + pages with per-path hit counts), `itemPage` (static, quality-gate-passing prose), `openCrawlDB`, and — for the zero-LLM proof — `fakeExtractor` wired *anyway* so `total()==0` pins "corpus never calls the extractor even when one is wired." Every test is default-suite hermetic (loopback only, no browser, no DNS); the only new test material is a two-line `corpusThinPage` HTML constant (fails the gate via `ThinPageWords = 200`, `clean/quality.go:58`). The load-bearing GATE is `TestWriter_RecordShapes`: `jsonRecord` is being renamed to a method (Task I.3), so the extracted envelope `{"url","extracted"}` gets an explicit shape pin for the first time — today only its *count* is pinned (`TestHooks_ProgressAndOnRecord` asserts `len`, not keys).
**Dependencies:** stdlib `testing`, `context`, `encoding/json`, `net/http/httptest`, `os`, `path/filepath`, `strings`, `sync` only — plus in-repo helpers `newSiteOrigin`/`origin.count`/`itemPage`/`sevenPages`/`openCrawlDB`/`fakeExtractor`/`crawlTruth`/`mustTestSchema` (`crawl/crawl_test.go`), `writeFileSite`/`runCrawl`/`codeOf`/`crawlCmd`-lookup/`testEnv`/`mustRead` (`cli/cmd_test.go`). No test frameworks, no new deps, **no new fixtures** — the challenge row reuses `testdata/quality/challenge-akamai.html`; `git diff testdata/` must stay empty.

**Deviation from plan/phase-I.md Task I.5 (sanctioned):** the plan names `crawl/writer_test.go` and `cli/crawl_test.go` as deliverables. Neither file exists — crawl tests live in `crawl/crawl_test.go` (`package crawl`, which the writer tests need anyway for unexported `newWriter`/`writer`) and CLI crawl e2e lives in `cli/cmd_test.go` (direct `runCrawl` calls, exit codes via `codeOf`). All new tests **append** to those two files; no new test files. Fewest files wins.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an RAG builder, I want `magpie crawl <seed> --corpus` to emit one JSONL record per page with cleaned main-content markdown, so that I can fill a vector store without writing a schema | `crawl/crawl_test.go` `TestCrawl_CorpusJSONL` (3-page chain over `newSiteOrigin`) | exit 0; file has 3 lines; each unmarshals to exactly keys `{url,title,depth,markdown}`; `depth` == 0/1/2 by position; `markdown` contains the prose sentence AND contains no `<a href`/`<nav`; `Records == PagesOK == 3` |
| US-2 | As a keyless operator, I want corpus mode to make zero LLM calls and require no API key, so that filling a corpus is free | `TestCrawl_CorpusJSONL` (fakeExtractor wired, `fx.total()==0`); `cli/cmd_test.go` `TestCrawl_CorpusCLI` (no `fakeLLM`, no `MAGPIE_*` key env, run succeeds) | `fx.total() == 0` after a 3-page corpus run even though `Extractor: fx` was passed; CLI corpus run with no provider config exits 0 and writes the file |
| US-3 | As a data-quality-conscious user, I want thin/challenge pages to become page errors, never empty corpus records, so that my vector store never ingests junk | `TestCrawl_CorpusQualityError` (akamai fixture + `corpusThinPage` among good pages) | `PagesErr ≥ 2`; JSONL line count == `PagesOK`; every record's `markdown` is non-empty; run completes with nil error |
| US-4 | As a cautious user, I want `--corpus` to refuse incompatible flags loudly and extracted-mode output to stay byte-identical, so that nothing about my existing pipelines drifts | `TestCrawl_CorpusValidation` (Options table) + CLI exit rows; `TestWriter_RecordShapes` (GATE); `git diff testdata/` | `--corpus --schema` → exit **2**; `--corpus --format csv|json|sqlite` → exit **2**; extracted+nil-schema still errors; writer test asserts extracted envelope is exactly `{"url","extracted"}` and corpus is exactly `{url,title,depth,markdown}`; `git status --porcelain testdata/` prints nothing |
| US-5 | As a long-crawl operator, I want an interrupted corpus run to resume without duplicates or refetches, so that a 10k-page corpus costs exactly one fetch per page | `TestCrawl_CorpusResume` (`sevenPages` pattern from `TestResume_DoneNeverRefetched`) | partial run (MaxPages 3) + resume (`Resume:true, ResumeID`, `Corpus:true`) → 3 + 4 records across the two out-files, **7 unique URLs total**, `o.count(path) == 1` for every done page |

---

## 1. Component Mock Strategy

Phase type: **integration** (pipeline behavior through `crawl.Run`) with a pure validation table (`newCrawlContext`) and a unit-tier writer test. Mock strategy in one sentence: **no new fakes at all — corpus behavior is driven through the real `crawl.Run` against `newSiteOrigin` origins, the zero-LLM property is pinned by wiring `fakeExtractor` and asserting `total()==0`, the writer envelope shapes are pinned by constructing `newWriter` directly (nil DB is legal for jsonl), and CLI rows go through `runCrawl`/`codeOf` exactly like the existing extracted-mode e2e.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| Corpus JSONL pipeline | `crawl/crawl_test.go` APPEND — `TestCrawl_CorpusJSONL`: `newSiteOrigin` with a 3-chain (`"/"` → `"/a"` → `"/b"`, each `itemPage(next)`), `openCrawlDB`, `Options{Corpus:true, Schema:nil, Extractor:fx, Format:"", Out:<tmp>}`; capture `OnRecord` maps | 3 lines in file; each record has exactly the 4 keys; `depth` 0/1/2 matched by URL; `markdown` contains `"fine product"` prose and does NOT contain `<a href` or `<nav`; `title` == `Widget - Buy`; `Records==PagesOK==3`; **`fx.total()==0` (the zero-LLM gate)**; each OnRecord map deep-equals the corresponding unmarshaled file line (tee/writer can never drift — the Task I.3 invariant) | US-1, US-2 |
| Corpus quality gate | `crawl/crawl_test.go` APPEND — `TestCrawl_CorpusQualityError`: origin serving 2 `itemPage`s + `/thin` = `corpusThinPage` + `/deny` = akamai fixture bytes (pattern copied from `TestCrawl_QualityCountedNotCached:719`) | `PagesErr ≥ 2` (thin + challenge); file lines == `PagesOK`; every `markdown` non-empty; `fx` wired, `total()==0`; nil error from `Run` | US-3 |
| `newCrawlContext` validation | `crawl/crawl_test.go` APPEND — `TestCrawl_CorpusValidation`: table over `newCrawlContext(Options{...})` (unexported — `package crawl` has it) | `{Corpus:true, Format:"jsonl"}` OK; `{Corpus:true, Format:""}` OK (defaults jsonl — `crawl.go:155`); `{Corpus:true, Format:"csv"/"json"/"sqlite"}` → error contains `corpus mode requires --format jsonl`; `{Corpus:false, Schema:nil}` → still `crawl: nil schema` (existing guard intact); `{Corpus:false, Extractor:nil}` → still `crawl: nil extractor` | US-4 |
| Writer record shapes (**GATE**) | `crawl/crawl_test.go` APPEND — `TestWriter_RecordShapes`: construct `newWriter(out, "jsonl", nil, nil, "", corpus)` directly for both corpus values (nil DB legal — DB only touched by the sqlite branch); `w.write(PageResult{Task: FetchTask{URL, Depth}, Title, Text})`; `w.close()`; read file | corpus=true → unmarshals to exactly `{url,title,depth,markdown}` with the right values; corpus=false → exactly `{url,extracted}` where `extracted` == the `Record` map; **this is the first explicit shape pin for the extracted envelope** (renaming `jsonRecord` → method is exactly where a silent shape split would happen — PR #11 lesson class) | US-4 |
| Corpus resume | `crawl/crawl_test.go` APPEND — `TestCrawl_CorpusResume`: `sevenPages()` on its own origin+DB, partial `Run{MaxPages:3, Corpus:true}` then resume `Run{Resume:true, ResumeID:part.RunID, Corpus:true, MaxPages:100}` (harness copied from `TestResume_DoneNeverRefetched:527`) | part.jsonl 3 lines + resume.jsonl 4 lines; union of URLs == 7 unique; `o.count(p) == 1` for the 3 done pages (never refetched); both runs nil error | US-5 |
| CLI `--corpus` e2e | `cli/cmd_test.go` APPEND — `TestCrawl_CorpusCLI`: `writeFileSite(t, 3)` (file:// — no network at all), `testEnv(t, "cache.db")`, direct `runCrawl` with `Corpus:true, Format:"", Out:<tmp>` and **no** `fakeLLM`/`Schema`/`Provider` | `err == nil`; 4 lines (index + 3 pages); every line valid JSON with the 4 keys; `markdown` non-empty; success with zero provider config IS the keyless proof | US-2, US-1 |
| CLI corpus validations | `cli/cmd_test.go` APPEND — `TestCrawl_CorpusCLIViolations`: `runCrawl` rows + `codeOf` | `Corpus+Schema` → `codeOf(err)==2`; `Corpus+Format:"csv"` → 2; `Format:"json"` → 2; `Format:"sqlite"` → 2; stderr names corpus | US-4 |
| Flag registration | `cli/cmd_test.go` APPEND — one row in the existing flag-plumbing table (`cmd_test.go:746`: walk `rootCmd` for `crawlCmd`, `crawlCmd.Flags().Lookup(flag)`) | `Lookup("corpus") != nil` | US-4 |

**Deliberately unpinned:** `Corpus:true` + `Schema != nil` at the *Options/Run* level (the plan makes the CLI the trust boundary for that exclusion — testing.md: validate at trust boundaries; don't invent a Run-level rule the plan didn't specify).

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit/Integration (default `go test ./...`) | Loopback `httptest` (`newSiteOrigin`) + `file://` sites (`writeFileSite`), temp-dir SQLite (`openCrawlDB`), in-process fakes only — **no external network, no DNS, no browser** | <20s added | Every push; the only gate |
| Manual (not a test file) | Built binary against a real docs site (e.g. `magpie crawl https://docs.example.com --corpus --max-pages 50 --out c.jsonl`) | minutes | Pre-release eyeball: records are clean prose, no nav/boilerplate, `wc -l` ≈ pages fetched. Never a CI claim |

No browser tier: corpus pages are static (`itemPage` prose keeps ScoreJSRequired at 0) and the phase adds no fetch behavior. Fixture rule unchanged: this phase adds **zero** fixtures — `git status --porcelain testdata/` must print nothing (the akamai challenge file already exists).

---

## 3. Fake / Mock Implementations

**None.** Every dependency already has an in-repo fake used verbatim: `fakeExtractor` (records every `Extract` call with purpose — its `total()` is the zero-LLM oracle), `newSiteOrigin`/`origin.count` (httptest + hit counts = the resume/refetch oracle), `openCrawlDB`, `writeFileSite`. Full sources are in §8 for the fresh session.

The only new test material in the entire phase:

```go
// corpusThinPage fails the quality gate: ThinPageWords = 200 (clean/quality.go:58)
// and this body has ~6 scored words. Never emitted as a corpus record.
const corpusThinPage = `<html><head><title>Thin</title></head><body><p>Very short page.</p></body></html>`
```

---

## 4. Test File List

```
magpie/
├── core/
│   └── pipeline.go                # DELIVERABLE (impl): PageResult gains Title, Text — covered indirectly
│                                  #   by every corpus record assertion (values survive the pipeline)
├── crawl/
│   ├── crawl.go                   # DELIVERABLE (impl): Options.Corpus, guard scoping, cleanPage/extractPage branches
│   ├── writer.go                  # DELIVERABLE (impl): corpus field, jsonRecord → (*writer).record
│   └── crawl_test.go              # APPEND (package crawl): TestCrawl_CorpusJSONL, TestCrawl_CorpusQualityError,
│                                  #   TestCrawl_CorpusValidation (newCrawlContext table), TestWriter_RecordShapes (GATE),
│                                  #   TestCrawl_CorpusResume + corpusThinPage const
├── cli/
│   ├── crawl.go                   # DELIVERABLE (impl): --corpus flag, exit-2 validations, keyless wiring
│   └── cmd_test.go                # APPEND: TestCrawl_CorpusCLI (file:// e2e, keyless), TestCrawl_CorpusCLIViolations
│                                  #   (codeOf==2 rows), +1 flag-Lookup row in the plumbing table (:746)
├── spec.md / README.md            # DELIVERABLE (docs) — no tests; checked by the gate list only
└── testdata/                      # UNCHANGED — gate: `git status --porcelain testdata/` prints nothing
```

Existing tests that must stay green **untouched** (they are the extracted-mode regression suite): `TestHooks_ProgressAndOnRecord`, `TestResume_DoneNeverRefetched`, `TestCrawl_SecondRunZeroLLM`, `TestCrawl_QualityCountedNotCached`, `TestTermination_SevenPagesDone` (crawl_test.go); all crawl rows in `cli/cmd_test.go` (`TestCrawl_FileSiteExit0`, exit-code rows); `cli/exporter_e2e_test.go` (OnRecord tee consumer); `mcp` crawl_site rows (MCP is a non-goal — corpus must not perturb it).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

No fixture framework — helpers live beside the tests. Nothing new except `corpusThinPage` (§3).

| Helper | Home | Used for | New? |
|--------|------|----------|------|
| `newSiteOrigin(t, pages, robotsBody)` / `origin.count(path)` | crawl_test.go:140 | origin per test; hit counts assert no-refetch on resume | reuse |
| `itemPage(links...)` | crawl_test.go:472 | static quality-passing page with title/h1/prose | reuse |
| `sevenPages()` | crawl_test.go:487 | resume-test site (7-page chain) | reuse |
| `openCrawlDB(t)` | crawl_test.go:110 | temp-dir store per test (t.Cleanup closes) | reuse |
| `fakeExtractor` + `.total()` | crawl_test.go:41/87 | zero-LLM oracle (wired, must stay at 0) | reuse |
| `mustTestSchema(t)` | crawl_test.go:101 | schema for the *regression* rows only (corpus rows pass `Schema:nil`) | reuse |
| `writeFileSite(t, n)` | cli/cmd_test.go:73 | file:// CLI e2e site — zero sockets | reuse |
| `runCrawl` + `codeOf` | cli/cmd_test.go:129/:207 | direct CLI invocation + exit-code assertions | reuse |
| `testEnv(t, name)` / `mustRead(t, path)` | cli/cmd_test.go | isolated config env + file slurp | reuse |
| `corpusThinPage` | crawl_test.go (new const) | quality-fail row (< 200 scored words) | **new, 2 lines** |

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Pin the extracted envelope shape explicitly | `TestWriter_RecordShapes` constructs the writer both ways and asserts exact key sets | Task I.3 renames `jsonRecord` → method; today only the record *count* is pinned (`TestHooks_ProgressAndOnRecord` asserts `len`). A silent shape split here would corrupt every golden and the MCP/exporter tee — the exact PR #11 dead-code class: all tests stay green while the contract breaks |
| Prove zero-LLM by wiring the fake anyway | corpus `Run` gets `Extractor: fx`, then `fx.total()==0` | `Extractor: nil` proves nothing (the code would also make 0 calls if a branch wrongly invoked it on a nil check pass-through). Wiring a counting fake turns "never calls" into an assertion, mirroring `TestCrawl_SecondRunZeroLLM` |
| Challenge row = existing akamai fixture, thin row = new 2-line const | `challenge-akamai.html` bytes on a 403 path (copied from `TestCrawl_QualityCountedNotCached`); `corpusThinPage` on a 200 path | The akamai path is the *proven* `PagesErr` row (browser-escalation-free under pinned static render). The thin const is backed by the named threshold (`ThinPageWords = 200`) — if the gate ever changes, this row fails loudly, which is the point |
| No new test files | append to `crawl/crawl_test.go` + `cli/cmd_test.go` | Both deliverable test files in phase-I.md don't exist; the established homes already have every helper and are `package crawl`/`package cli` as needed. Fewest files wins |
| Corpus+Schema at Run level: unpinned | CLI exit-2 rows only | The plan scopes the mutual exclusion to the CLI (trust boundary). Pinning an unspecified Run-level rule would test an invention, not the spec |
| Tee parity asserted end-to-end | `TestCrawl_CorpusJSONL` deep-equals each captured OnRecord map against the corresponding file line | `write()` and the OnRecord callback are two call sites of `record` — comparing their outputs in one test is the cheapest drift tripwire |
| Resume asserts records AND refetch counts | union of both out-files == 7 unique URLs; `o.count(p)==1` for done pages | Record-level dedup alone could pass with a refetch (rewriting the same record); the hit counts pin the real property: one fetch per page, ever |

---

## 7. Example Test Case

The core pipeline test, in full (append to `crawl/crawl_test.go`):

```go
func TestCrawl_CorpusJSONL(t *testing.T) {
	pages := map[string]string{
		"/":  itemPage("/a"),
		"/a": itemPage("/b"),
		"/b": itemPage(),
	}
	o := newSiteOrigin(t, pages, "")
	db := openCrawlDB(t)
	fx := &fakeExtractor{script: map[string]map[string]any{"default": crawlTruth}} // wired to PROVE zero calls
	var mu sync.Mutex
	var tee []map[string]any
	out := filepath.Join(t.TempDir(), "c.jsonl")
	res, err := Run(context.Background(), Options{
		SeedURL: o.srv.URL + "/", Corpus: true,
		Schema: nil, Extractor: fx, // corpus: both legal now
		MaxPages: 10, MaxDepth: 3, SameHost: true,
		FetchWorkers: 4, Rate: 1000, Format: "", Out: out, // "" defaults to jsonl
		DB: db,
		OnRecord: func(r map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			tee = append(tee, r)
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PagesOK != 3 || res.Records != 3 || res.PagesErr != 0 {
		t.Fatalf("res = ok:%d rec:%d err:%d, want 3/3/0", res.PagesOK, res.Records, res.PagesErr)
	}
	if got := fx.total(); got != 0 {
		t.Errorf("extractor calls = %d, want 0 (corpus is keyless)", got)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl lines = %d, want 3", len(lines))
	}
	wantDepth := map[string]float64{o.srv.URL + "/": 0, o.srv.URL + "/a": 1, o.srv.URL + "/b": 2}
	for i, ln := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if len(rec) != 4 {
			t.Errorf("line %d keys = %v, want exactly url,title,depth,markdown", i, keysOf(rec))
		}
		for _, k := range []string{"url", "title", "depth", "markdown"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("line %d missing key %q", i, k)
			}
		}
		md, _ := rec["markdown"].(string)
		if !strings.Contains(md, "fine product") {
			t.Errorf("line %d markdown missing main content", i)
		}
		if strings.Contains(md, "<a href") || strings.Contains(md, "<nav") {
			t.Errorf("line %d markdown contains boilerplate HTML", i)
		}
		if d, ok := wantDepth[rec["url"].(string)]; !ok || d != rec["depth"].(float64) {
			t.Errorf("line %d depth = %v for %v, want %v", i, rec["depth"], rec["url"], d)
		}
		// Tee parity: OnRecord must hand the writer's exact record shape.
		mu.Lock()
		if len(tee) != 3 {
			t.Fatalf("tee records = %d, want 3", len(tee))
		}
		if !reflect.DeepEqual(stripFloat(tee[i]), stripFloat(rec)) {
			t.Errorf("line %d: OnRecord tee drifted from writer record", i)
		}
		mu.Unlock()
	}
}
```

(`keysOf`/`stripFloat` are trivial local helpers if needed — JSON round-trips make numbers float64 on both sides, so `reflect.DeepEqual` on the two maps compares like with like. If it's simpler, marshal the tee map back to JSON and compare strings.)

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite (after or alongside the phase-I implementation):

---
You are writing the tests for Phase I of **magpie** — crawl corpus mode (`--corpus`: schema-less JSONL `{url,title,depth,markdown}` per page, zero LLM calls, jsonl only).

### What This Project Is
magpie is a Go CLI web scraper (module `magpie`, Go 1.26+, CGO-free): fetch → clean → extract. Read `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md`, `plan/phase-I.md`, and `plan/phase-I-tests.md` before writing anything. Tests are hermetic: loopback httptest + `file://` only, no live network, no browser in the default suite, no new deps.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | RAG builder: `crawl --corpus` emits cleaned per-page JSONL | `TestCrawl_CorpusJSONL` | 3 lines, keys exactly `{url,title,depth,markdown}`, depths 0/1/2, prose in / boilerplate out, `Records==PagesOK==3` |
| US-2 | Keyless: zero LLM calls, no API key | `fx.total()==0` in corpus runs; CLI corpus run with no provider config exits 0 | binary |
| US-3 | Quality: thin/challenge pages are errors, never empty records | `TestCrawl_CorpusQualityError` | `PagesErr ≥ 2`, lines == `PagesOK`, no empty markdown |
| US-4 | Loud rejections + frozen extracted shape | `TestCrawl_CorpusValidation` + CLI exit rows + `TestWriter_RecordShapes` + empty `git status --porcelain testdata/` | exits 2 where specified; extracted envelope exactly `{url,extracted}`; corpus exactly `{url,title,depth,markdown}`; zero fixture drift |
| US-5 | Resume without duplicates/refetches | `TestCrawl_CorpusResume` | 3+4 records, 7 unique URLs, done pages fetched once |

### Why There Are No New Fakes
Every dependency already has an in-repo fake — reuse them verbatim; the only new test code is one HTML constant. Do not invent a second extractor fake, a second origin builder, or a conftest equivalent (Go has none).

### What NOT to Test
- **MCP**: corpus mode is CLI-only by design (plan Decision 10 — `crawl_site` streams to os.DevNull). Don't add MCP corpus rows; don't touch `TestToolCatalog`.
- **trafilatura/clean internals**: assert corpus *output* (prose in, nav/link HTML out), not extraction algorithms — `clean/` has its own suite.
- **sqlite/csv writer branches for corpus**: corpus rejects those formats before the writer is built; the existing csv/sqlite tests already cover them.
- **Run-level `Corpus+Schema!=nil`**: unspecified — the CLI is the trust boundary for that exclusion. Don't invent a rule.
- **Store internals**: `openCrawlDB` + run rows are covered by the resume tests incidentally; don't test the store directly.

### Critical: The Harness You Must Reuse (crawl/crawl_test.go, `package crawl`)

```go
type fakeExtractor struct {
	mu     sync.Mutex
	calls  []llmCall
	script map[string]map[string]any
	err    error
}
// Extract records every call as llmCall{purpose, url} and returns script[url] or script["default"].
// The load-bearing method for this phase:
func (f *fakeExtractor) total() int { return f.count("synth") + f.count("extract") }
```

```go
func openCrawlDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newSiteOrigin serves pages[path] as text/html, 404 elsewhere, robotsBody at
// /robots.txt ("" = 404); origin.count(path) returns per-path hit counts.
func newSiteOrigin(t *testing.T, pages map[string]string, robotsBody string) *origin

// itemPage: static page (title "Widget - Buy", h1, price, EAN, nav of links,
// two prose paragraphs) that passes the quality gate WITHOUT a browser.
func itemPage(linkPaths ...string) string

func sevenPages() map[string]string // 7-page chain /0../6, all itemPage
```

The only new test material:

```go
// corpusThinPage fails the quality gate: ThinPageWords = 200 (clean/quality.go:58).
const corpusThinPage = `<html><head><title>Thin</title></head><body><p>Very short page.</p></body></html>`
```

For CLI tests (`cli/cmd_test.go`, `package cli`): `writeFileSite(t, n)` builds a `file://` site; call `runCrawl(t.Context(), seed, crawlCLIOptions{...})` directly and assert with `codeOf(err)`; `testEnv(t, "cache.db")` isolates config env. For the exit-2 rows: `Corpus:true` plus `Schema: priceSchema(t)` or `Format: "csv"|"json"|"sqlite"`. The keyless e2e passes NO Schema/Provider/API key at all.

### Test Files to Create / Edit
- **EDIT `crawl/crawl_test.go`** (append): `corpusThinPage` const; `TestCrawl_CorpusJSONL`; `TestCrawl_CorpusQualityError`; `TestCrawl_CorpusValidation` (table over unexported `newCrawlContext`); `TestWriter_RecordShapes` (construct `newWriter(out, "jsonl", nil, nil, "", corpus)` for both corpus values — nil DB is fine for jsonl; write a `PageResult{Task: core.FetchTask{URL: u, Depth: d}, Title: "T", Text: "body"}`, close, read the file, assert exact key sets); `TestCrawl_CorpusResume` (copy the structure of `TestResume_DoneNeverRefetched:527`: partial MaxPages-3 run, then `Resume:true, ResumeID: part.RunID, Corpus:true, MaxPages:100`; assert 3+4 lines across the two out-files, 7 unique URLs, `o.count(p)==1` for done pages).
- **EDIT `cli/cmd_test.go`** (append): `TestCrawl_CorpusCLI` (file:// e2e, keyless, 4 lines, 4 keys each); `TestCrawl_CorpusCLIViolations` (4 exit-2 rows); add `"corpus"` to the flag-plumbing table at `:746`.
- **NO new test files. Do not edit** any existing test body — the extracted-mode tests (`TestHooks_ProgressAndOnRecord`, `TestResume_DoneNeverRefetched`, `TestCrawl_SecondRunZeroLLM`, `TestCrawl_QualityCountedNotCached`, `TestCrawl_FileSiteExit0`, exporter e2e, MCP crawl_site rows) ARE the regression suite and must pass untouched.

### Per-File Coverage Guidance
**crawl/crawl_test.go — TestCrawl_CorpusJSONL**: full source in plan §7 (3-chain origin, `Format:""` proving the jsonl default, OnRecord tee deep-equal against file lines, `fx.total()==0`).
**TestCrawl_CorpusQualityError**: mux pattern from `TestCrawl_QualityCountedNotCached:719` — `/deny` serves `../testdata/quality/challenge-akamai.html` bytes with 403, `/thin` serves `corpusThinPage` with 200, two `itemPage` good pages. Assert `PagesErr >= 2`, file lines == `PagesOK`, all markdown non-empty, `fx.total()==0`, nil error.
**TestCrawl_CorpusValidation**: direct `newCrawlContext(Options{...})` rows — OK: `{Corpus:true, Format:"jsonl"}`, `{Corpus:true, Format:""}`; error contains `corpus mode requires --format jsonl`: `{Corpus:true, Format:"csv"|"json"|"sqlite"}`; regression: `{Corpus:false, Schema:nil}` → `crawl: nil schema`, `{Corpus:false, Extractor:nil}` → `crawl: nil extractor`. Every row needs `DB: openCrawlDB(t)` (nil-DB check fires first).
**TestWriter_RecordShapes**: two writers, two files, exact key-set assertions both ways. This is the gate protecting the `jsonRecord` → `(*writer).record` rename — extracted shape `{url,extracted}` has never been explicitly pinned before.
**cli/cmd_test.go**: corpus e2e asserts success with zero provider config; violation rows assert `codeOf(err)==2` for corpus+schema / corpus+format csv|json|sqlite.

### Data Model Notes (Go)
- `core.PageResult` gains `Title, Text string` (plain fields, no JSON tags — never marshaled directly). Corpus records get their values from `CleanedPage.Title` / `CleanedPage.Markdown`.
- Corpus records are `map[string]any` with EXACTLY `url,title,depth,markdown` — assert key *sets*, not just presence (`len(rec) != 4` fails first).
- `depth` arrives through JSON as float64 — compare against `float64(0/1/2)`.
- Error pages ride `PageResult.Err` → sink `MarkError` → `PagesErr`; they never reach the writer (existing sink logic, unchanged).

### Success Criteria
- `go test ./crawl/ ./cli/ -count=1` green with the new tests; full `go test ./...` green
- `git status --porcelain testdata/` prints NOTHING (zero new fixtures — akamai is reused)
- `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...` all clean
- Extracted-mode behavior unchanged: every pre-existing crawl/CLI/MCP/exporter test passes WITHOUT modification
- `fx.total()==0` asserted in every corpus pipeline test (zero-LLM is pinned, not implied)

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./crawl/ ./cli/ -count=1 && git status --porcelain testdata/

# Fast hermetic suite (every push — loopback + file:// only)
go test ./... -count=1

# New tests only (mirrors the phase task seams)
go test ./crawl/ -run 'TestCrawl_Corpus|TestWriter_RecordShapes' -v -count=1
go test ./cli/ -run 'TestCrawl_Corpus' -v -count=1

# The writer-shape gate in isolation
go test ./crawl/ -run TestWriter_RecordShapes -v -count=1

# Fixture-drift gate (must print nothing)
git status --porcelain testdata/

# Race on touched packages
go test -race ./crawl/ ./cli/ -count=1

# Full gate (mirrors phase-I exit criteria)
go test ./... -count=1 && go vet ./... && gofmt -l . && golangci-lint run ./... \
  && test -z "$(git status --porcelain testdata/)" && echo ALL GREEN

# Manual smoke (pre-release, real site — never CI)
go run ./cmd/magpie crawl https://docs.example.com --corpus --max-pages 50 --out /tmp/c.jsonl && head -2 /tmp/c.jsonl
```
