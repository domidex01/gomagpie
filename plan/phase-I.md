# Phase I — Crawl Corpus Mode (schema-less RAG ingestion)

**Duration:** 1–2 days (~8h)
**Depends on:** master (Phase H) — crawl writer, quality gate, resume, exporter tee all stable
**Blocks:** future Wails-GUI "build RAG corpus" export; nothing hard-blocked
**Risk Level:** MEDIUM — touches shared structs (`core.PageResult`) and crawl validation, but every change is additive with `omitempty`; pipeline stages, frontier, robots, resume untouched
**Stack:** go

---

## Objective

`magpie crawl` today **requires `--schema`** (`cli/crawl.go:173`) and throws the cleaned page
text away — `core.PageResult` carries only `Record` + `Links`. The pipeline cleans every page
(trafilatura main-content extraction, boilerplate strip, quality gate) and then discards exactly
the text a RAG pipeline needs. This phase adds **corpus mode**:

```
magpie crawl https://docs.example.com --corpus --max-pages 500 --out corpus.jsonl
```

emitting one JSONL record per page:

```json
{"url":"https://docs.example.com/guide","title":"Guide","depth":1,"markdown":"# Guide\n\n..."}
```

Keyless (zero LLM calls — no extractor, no API key), robots/rate-limit/dedup/resume/quality-gate
all inherited from the existing crawl machinery. This is the "fill a RAG" ingestion half; chunking
and embedding stay downstream (deliberate non-goal).

## What Success Looks Like

1. `magpie crawl <seed> --corpus --max-pages 3 --out /tmp/c.jsonl` against a local test site
   exits 0 and `/tmp/c.jsonl` has 3 lines, each valid JSON with keys `url`, `title`, `depth`,
   `markdown`; `markdown` is cleaned main-content text (no nav/boilerplate).
2. The run makes **zero LLM calls** and requires **no API key** (runs with provider config absent).
3. `magpie crawl <seed> --corpus --schema s.yaml` exits **2**; `--corpus --format csv` exits **2**.
4. A thin/challenge page in the corpus run is counted as a **page error** (exit 4 semantics), never
   emitted as an empty record.
5. `go test ./...` green, `gofmt -l .` empty, `golangci-lint run ./...` clean; existing extracted-mode
   JSONL goldens byte-identical (no output-shape drift).

---

## Architecture / Key Design Decisions

```
frontier ──► fetchPage ──► cleanPage ──► extractPage ──► sink ──► writer(jsonl)
             (unchanged)   (corpus:      (corpus: 3-line    │      corpus record shape:
                           always full    pass-through:    │      {url,title,depth,markdown}
                           clean.Clean,   Title+Text, no   └─ OnRecord tee uses the
                           skip cache     selectors/heal)     same record() func)
                           needLLM read)
```

### Decisions

1. **Explicit `--corpus` flag, not implicit schema-optional magic.** Boring, self-documenting,
   mutually exclusive with `--schema` (exit 2). A silent "no schema = corpus" overload would
   surprise every existing caller and the MCP tool.
2. **`Corpus bool` on `crawl.Options`; nil-guards scoped to it.** `newCrawlContext` currently
   rejects `Schema == nil` / `Extractor == nil` — both guards become `&& !opts.Corpus`.
   In corpus mode: `schemaHash = ""` (the selector-cache reads in `cleanPage` are skipped
   entirely, so the hash is never consulted), `Extractor = nil` is legal.
3. **`cleanPage` change is 3 lines:** wrap the existing `needLLM` cache lookup in
   `if !c.opts.Corpus { ... }`. Corpus always falls through to full `clean.Clean`
   (we *need* the markdown — never take the StructuredData-only shortcut).
4. **`extractPage` gets one early return before all domain/heal state:**
   `if c.opts.Corpus { return core.PageResult{Task: cl.Task, Err: cl.Err, Title: cl.Page.Title, Text: cl.Page.Markdown}, nil }`.
   The entire `domainState`/healer/applier machinery is never reached.
5. **`core.PageResult` gains `Title, Text string` (additive, no JSON tags needed — the struct is
   never marshaled directly).** Only `writer.record()` reads them. (Same pattern as the Phase G
   `CleanedPage.HTML` additive-omitempty change: existing output shapes must not drift.)
6. **`jsonRecord` becomes `(*writer).record` — still the single home for the JSON envelope**
   (preserve that invariant; the doc comment says the OnRecord tee must never drift from the
   writer). Corpus shape: `{url, title, depth, markdown}`. `depth` is free (`Task.Depth`) and
   genuinely useful for RAG filtering (shallow pages weigh more); do not invent more fields.
7. **Corpus forces format `jsonl`.** Validated in `newCrawlContext` (loud error) and in the CLI
   (exit 2). CSV of full-page markdown is nonsense; `--format json` buffers in RAM (existing
   documented ceiling) — don't widen it.
8. **Quality/challenge pages stay page errors.** `clean.Clean` → `QualityError` → `cl.Err` →
   sink `MarkError` → `PagesErr` — the existing path, unchanged. A corpus never contains empty
   rows silently.
9. **Resume works, corpus-ness is not persisted.** `--resume ID` re-derives everything from
   flags; resuming a corpus run without `--corpus` fails loudly at the nil-schema guard.
   Acceptable (ponytail ceiling: run rows don't record mode; upgrade path = a `mode` column if
   this ever bites).
10. **MCP is a non-goal.** `crawl_site` streams records to `os.DevNull` and returns counts only —
    a corpus cannot cross a tool-result boundary sanely. CLI-first; MCP exposure only if a real
    consumer appears.
11. **Chunk/embed/store stay downstream.** Magpie ends at cleaned markdown records. Chunking
    strategy is retrieval-specific — YAGNI.

### Data Model Rules (Go)

- Plain structs, no new types/interfaces. One `bool` field on `crawl.Options`, two on
  `core.PageResult`, one on `crawl.writer`.
- No new package. Every change lands in `core/pipeline.go`, `crawl/crawl.go`, `crawl/writer.go`,
  `cli/crawl.go`, `spec.md`.
- Existing JSON output shapes are frozen: extracted-mode records must remain
  `{"url":...,"extracted":...}` byte-for-byte (golden tests already pin this).

---

## Tasks

### Task I.1 — Options + validation seams (1.5h)

**Depends on:** nothing

- `core.PageResult`: add `Title string` and `Text string` after `Record` (plain fields).
- `crawl.Options`: add `Corpus bool` (document: "schema-less corpus mode; jsonl only; no LLM").
- `newCrawlContext`: scope both nil-guards with `&& !opts.Corpus`; when `opts.Corpus`,
  error if `opts.Format != "" && opts.Format != "jsonl"` (message:
  `crawl: corpus mode requires --format jsonl`); set `cc.schemaHash = ""` only when
  `opts.Schema == nil` (leave the existing `selector.SchemaHash(opts.Schema)` call for
  extracted mode — verify it tolerates nil or is already unreachable then).

**Sanity check:** `go build ./...` and a table test asserting `newCrawlContext` accepts
`{Corpus: true, Schema: nil, Extractor: nil, Format: "jsonl"}` and rejects
`{Corpus: true, Format: "csv"}` / `{Corpus: false, Schema: nil}`.

### Task I.2 — Pipeline branches (1.5h)

**Depends on:** I.1

- `cleanPage`: wrap the `needLLM` cache-read block (`c.db.GetSelectors(domainOf(...))` …
  `hasNonCacheable`) in `if !c.opts.Corpus`. Everything else verbatim — link extraction,
  sidecar harvest, quality-gated `clean.Clean` all stay on the corpus path.
- `extractPage`: immediately after the `cl.Err` check, add the corpus early return
  (Decision 4). Do not touch `domainState` init order — the early return precedes it.

**Sanity check:** existing crawl tests still pass untouched: `go test ./crawl/ -run TestCrawl -count=1`.

### Task I.3 — Writer record shape (1h)

**Depends on:** I.1

- `crawl/writer.go`: add `corpus bool` to `writer`; thread it through `newWriter`
  (one extra param, called from `runPipeline` with `c.opts.Corpus`).
- Rename `jsonRecord(r)` → method `func (w *writer) record(r core.PageResult) map[string]any`
  with the corpus branch returning
  `{"url": r.Task.URL, "title": r.Title, "depth": r.Task.Depth, "markdown": r.Text}`.
  Update the doc comment: still the single home for the envelope; the sink's `OnRecord`
  call must use `w.record(r)` so the exporter tee can't drift (this fixes a latent
  shape-split risk, same class as the PR #11 lesson).

**Sanity check:** unit test in `crawl/writer_test.go` (or new) asserting both shapes:
corpus → 4 keys; extracted → `{"url","extracted"}` unchanged.

### Task I.4 — CLI `--corpus` (1h)

**Depends on:** I.1–I.3

- `cli/crawl.go`: `--corpus` bool flag; validation order:
  `--corpus` + `--schema` → `fail(2, ...)`;
  `--corpus` + `--format` ∉ {"", "jsonl"} → `fail(2, ...)`.
  When corpus: skip the schema-required check (line ~173), skip `ExtractorFor` /
  provider / API-key resolution entirely, pass `Extractor: nil, Propose: nil, Corpus: true`.
- Keep the existing exit-3 (`records == 0`) and exit-4 (partial) handling untouched.

**Sanity check:** `go run ./cmd/magpie crawl --help` shows `--corpus`; `--corpus --schema x` exits 2.

### Task I.5 — Tests (2h)

**Depends on:** I.1–I.4

Reuse the `crawl_test.go` harness (`testCrawl` httptest mux serving robots.txt + pages,
`fakeExtractor` NOT needed — corpus is keyless). All hermetic, no live network:

- `TestCrawl_CorpusJSONL` — 3 linked pages; assert line count, record keys, markdown is the
  cleaned main content, `depth` values correct, `Records == PagesOK == 3`.
- `TestCrawl_CorpusQualityError` — one thin/challenge page among good ones; assert it lands in
  `PagesErr`, absent from the JSONL, run still completes.
- `TestCrawl_CorpusValidation` — corpus+schema, corpus+format csv, corpus+format json → errors
  (Options-level and/or CLI exit 2).
- `TestCrawl_CorpusResume` — interrupted corpus run resumed with `--corpus --resume`; completes
  without duplicate records (visited-hash dedup inherited).
- `TestWriter_CorpusRecordShape` — both envelope shapes pinned.
- CLI test in `cli/crawl_test.go`: corpus run against the local server end-to-end (exit 0, file written).

### Task I.6 — Docs (0.5h)

**Depends on:** I.5

- `spec.md` §10 crawl block: `--schema <file> (required unless --corpus)` + add
  `--corpus` line; §10.1 add bullet: **Corpus** (`--corpus`): schema-less JSONL
  `{url,title,depth,markdown}` per page, zero LLM calls (keyless), jsonl only.
- `README.md` crawl section: one example line.

---

## Deliverables

```
magpie/
├── core/pipeline.go        # PageResult gains Title, Text (additive)
├── crawl/crawl.go          # Options.Corpus; nil-guard scoping; cleanPage/extractPage corpus branches
├── crawl/writer.go         # writer.corpus; jsonRecord → (*writer).record with corpus shape; OnRecord via w.record
├── cli/crawl.go            # --corpus flag, mutual exclusion, keyless wiring
├── crawl/crawl_test.go     # corpus pipeline tests (JSONL, quality error, resume, validation)
├── crawl/writer_test.go    # envelope shape pins (both modes)
├── cli/crawl_test.go       # end-to-end corpus CLI test
├── spec.md                 # §10 flags + §10.1 corpus bullet
└── README.md               # crawl corpus example
```

## Exit Criteria

- [ ] `TestCrawl_CorpusJSONL` passes: 3 pages → 3 JSONL records with `url/title/depth/markdown`, cleaned content, zero extractor calls
- [ ] `TestCrawl_CorpusQualityError` passes: thin page → `PagesErr`, never an empty record
- [ ] Validation tests pass: `--corpus --schema` → 2, `--corpus --format csv` → 2, `--corpus --format json` → 2
- [ ] `TestCrawl_CorpusResume` passes: resumed corpus run dedups via visited hashes
- [ ] Writer shape pins pass: corpus 4-key record; extracted `{"url","extracted"}` byte-identical to before
- [ ] `go test ./...` green, `go vet ./...` clean, `gofmt -l .` empty, `golangci-lint run ./...` clean
- [ ] Spec §10/§10.1 and README updated; extracted-mode JSONL goldens show an empty diff (`git diff testdata/` empty)

---

## Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are building Phase I of **magpie** — crawl corpus mode (schema-less RAG ingestion).

### What This Project Is
magpie is a Go CLI web scraper: fetch → clean → extract (module `magpie`, binary `magpie`,
Go 1.26+, CGO-free). Source of truth: `spec.md`. Read `AGENTS.md`, `.pi/rules/go.md`,
`.pi/rules/testing.md`, and `plan/phase-I.md` before writing any code. Ethos: lazy senior dev —
minimum diff, no new deps, no new abstractions; deletion over addition.

### Established in Prior Phases (verified on master)
- `core.Run` pipelines source → fetch → clean → extract → sink with bounded channels;
  per-page failures ride the structs' `Err` field, never stage errors.
- `crawl.Run` → `newCrawlContext` currently rejects `opts.Schema == nil` AND
  `opts.Extractor == nil` unconditionally — these two guards are the entire schema coupling
  at the entry.
- `crawlContext.cleanPage` computes `needLLM` via a per-domain `c.db.GetSelectors(domain, c.schemaHash)`
  read; `needLLM == false` short-circuits trafilatura (StructuredData-only CleanedPage). Corpus mode
  must ALWAYS take the full `clean.Clean` path (it needs `CleanedPage.Markdown` and `CleanedPage.Title`).
- `crawlContext.extractPage` owns all selector-apply/heal/domainState machinery — corpus mode
  returns before any of it.
- `crawl/writer.go`: `jsonRecord(r)` is THE single home for the JSON envelope
  (`{"url":..., "extracted":...}`); the sink's `OnRecord` exporter tee must use the same func.
  `--format json` buffers all records in RAM (documented ceiling) — corpus mode must not widen it.
- Quality gate: `clean.Clean` returns `QualityError` (challenge/thin pages) → `Cleaned.Err` →
  sink `MarkError` → `PagesErr`. Corpus mode inherits this: bad pages are errors, never empty records.
- Resume: frontier/visited-hashes/run rows are schema-independent; corpus-ness is NOT persisted —
  `--resume` re-derives mode from flags; resuming corpus without `--corpus` fails loudly. Fine.
- CLI crawl (`cli/crawl.go`): `--schema` required (exit 2 at the "crawl: --schema is required" check);
  provider/`ExtractorFor`/API-key resolution happens before `crawl.Run`; exit 3 = no records,
  exit 4 = partial. Validation failures use `fail(2, ...)`.
- Existing extracted-mode JSONL output is pinned by golden tests — do not let record shapes drift.
- Tests are hermetic (httptest servers serving robots.txt + pages, `fakeExtractor` for LLM).
  Corpus mode needs NO fakeExtractor. No live-network tests in the default suite. No new deps. No CGO.

### Your Goal for This Phase
Add `magpie crawl <url> --corpus` emitting schema-less JSONL `{"url","title","depth","markdown"}`
per page — cleaned main-content text for RAG ingestion, zero LLM calls, jsonl only.

### Data Model Rules (follow exactly)
- Plain Go structs only. Add: `Corpus bool` to `crawl.Options`; `Title, Text string` to
  `core.PageResult`; `corpus bool` to `crawl.writer`. Nothing else. No interfaces, no new packages.
- Record envelope stays a `map[string]any` built in ONE place: `(*writer).record`.
- Existing JSON shapes are frozen; additive fields only.

### Architecture (follow exactly)
- `newCrawlContext`: scope the nil-guards — `if opts.Schema == nil && !opts.Corpus { err }`,
  same for Extractor; corpus rejects `Format ∉ {"", "jsonl"}` with
  `crawl: corpus mode requires --format jsonl`; `cc.schemaHash` stays `""` when `Schema == nil`.
- `cleanPage`: wrap ONLY the `needLLM` cache-lookup block in `if !c.opts.Corpus` — link
  extraction, sidecar harvest, and the full `clean.Clean` call stay unconditional.
- `extractPage`: right after the `cl.Err` check, corpus early-return:
  `core.PageResult{Task: cl.Task, Err: cl.Err, Title: cl.Page.Title, Text: cl.Page.Markdown}`.
- `writer`: rename `jsonRecord` → method `record` on `*writer` with a `corpus` branch
  (`{"url","title","depth","markdown"}`); `runPipeline`'s `OnRecord` callback must call
  `w.record(r)` (tee and writer can never drift).
- `cli/crawl.go`: `--corpus` flag; `corpus && schema != ""` → exit 2; `corpus && format ∉ {"", "jsonl"}`
  → exit 2; corpus skips schema-required check AND skips ExtractorFor/provider/API-key resolution
  (pass `Extractor: nil, Propose: nil`).

### Files to Change
#### core/pipeline.go
Add `Title string` and `Text string` to `PageResult` after `Record`. No JSON tags needed
(struct is never marshaled directly). Nothing else.

#### crawl/crawl.go
`Options.Corpus bool` with doc comment ("schema-less corpus mode: emit {url,title,depth,markdown}
per page; jsonl only; zero LLM calls"). `newCrawlContext` guard scoping + format validation +
schemaHash skip. `cleanPage` needLLM wrap. `extractPage` early return. `runPipeline` passes
`c.opts.Corpus` to `newWriter` and uses `w.record(r)` in OnRecord.

#### crawl/writer.go
`corpus bool` field + `newWriter` param. `jsonRecord` → `(*writer).record` with both branches;
update the "single home" doc comment to mention the corpus shape.

#### cli/crawl.go
`--corpus` flag registration, the two exit-2 validations, and the corpus branch that skips
schema-required + extractor wiring. Exit 3/4 paths untouched.

#### Tests (crawl/crawl_test.go, crawl/writer_test.go, cli/crawl_test.go)
Per `plan/phase-I.md` Task I.5: corpus JSONL (3 pages, keys/content/depth/zero-LLM),
quality-error page (error not record), validation matrix (exit 2s), corpus resume (dedup),
writer shape pins (both modes), CLI end-to-end. Reuse the existing `testCrawl` httptest harness.

#### spec.md + README.md
§10 crawl flags (`--schema (required unless --corpus)`, add `--corpus`), §10.1 corpus bullet,
README one-line example. `git diff testdata/` must be empty at the end.

### Success Criteria
- All new tests pass; `go test ./...` exits 0
- `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...` all clean
- Extracted-mode goldens unchanged: `git diff testdata/` empty
- Zero LLM calls provable in corpus tests (no extractor wired, no fakeExtractor count > 0)

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — crawl writer/quality/resume/exporter verified on master with exact line references
- [PASS] Every sub-task has a clear, testable completion condition — each task ends in a named sanity check or test
- [PASS] Execution prompt is self-contained: (a) prior-phase facts with function/line names, (b) confirmed code-level seams (nil-guards, needLLM block, jsonRecord contract), (c) data model rules (exact fields, no new types), (d) per-file guidance, (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables — every changed file has a pinning test or a docs diff check
- [PASS] Heavy external dependencies: none — corpus mode is keyless by design, `fakeExtractor` explicitly not needed
- [PASS] New libraries: none (stdlib only) — no research snippets required
