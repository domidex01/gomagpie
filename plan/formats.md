# Output formats — verification & plan (2026-09-19)

Question: "we render the scrape in markdown — should we be able to render in
json, pdf, csv, excel?"

## Verification: what already exists

Two separate axes, often conflated:

1. **`--page-format`** (scrape; how the cleaned *page content* is rendered):
   `markdown` (default) | `llm` | `text` | **`json`** | `html` | `raw` | `screenshot`
   — `clean.Render` (`clean/llm.go:366`), JSON via `renderJSON`.
2. **`--format`** (how extracted *records* are serialized):
   - `scrape`: `json|jsonl` only (csv/sqlite rejected, `cli/scrape.go:120`).
   - `batch`: `jsonl|json` only (no `--schema`; records are fat envelopes).
   - `crawl`: `jsonl|json|csv|sqlite` — full impl in `crawl/writer.go`
     (stdlib `encoding/csv`; flat schemas only, nested → loud error;
     sqlite → `records` table via `store.InsertRecord`).

| Format asked about | Page render | Records | Verdict |
| :-- | :-- | :-- | :-- |
| JSON | ✅ exists | ✅ exists | done, nothing to do |
| CSV | — | ✅ crawl only | done where it makes sense (see below) |
| PDF | ❌ (`clean/pdf.go` is PDF **input**, not output) | ❌ | defer (see below) |
| Excel/xlsx | ❌ | ❌ | defer to GUI phase (see below) |

## Decisions

### 1. JSON — no work
Exists on both axes.

### 2. CSV for scrape/batch — do NOT build; fix the spec instead
- `scrape` = one record → one CSV row + header. Pointless.
- `batch` has no `--schema` → records are full envelopes (markdown blob in a
  cell). CSV is meaningless for that shape.
- CSV only means something for schema-extracted flat records = crawl's job,
  and crawl already does it.
- **Task F1 (spec fix):** spec §10 CLI tree says scrape
  `--format jsonl|json|csv|sqlite`. Change to `jsonl|json` and note
  "csv|sqlite are crawl-only". One-line spec/impl drift correction; no code.

### 3. PDF output — defer; do not add a dependency
- Data consumers want json/csv; humans want markdown. PDF is presentation.
- If ever genuinely requested: rod `Page.PrintToPDF` behind
  `--page-format pdf` (~40 lines in `fetch/`, browser-render only,
  **zero new deps** — rod is already the sanctioned browser seam).
- Never add a static Go PDF-writer dep (fonts/layout/pagination = heavy).

### 4. Excel — CSV is the Excel format; xlsx belongs to the GUI
- **Task F2 (tiny, optional):** UTF-8 BOM before the CSV header in
  `crawl/writer.go` (`--out` path) so Excel opens non-ASCII (é, €, ¥)
  without mojibake. ~3 lines + one test.
- Native `.xlsx` = `excelize` (pure Go, CGO-free — allowed but heavy).
  Deferred to the Wails GUI phase: an "Export to Excel" button in the GUI
  reading from sqlite/json. excelize lives in the GUI layer, not CLI core.

## Task list

| # | Task | Size | Priority |
| :-- | :-- | :-- | :-- |
| F1 | spec §10: scrape `--format` → `json\|jsonl` (csv/sqlite crawl-only) | 1 line | do |
| F2 | UTF-8 BOM in crawl CSV `--out` (Excel compat) + test | ~5 lines | optional |

That's the whole plan. PDF/xlsx are deliberate non-goals for the CLI;
revisit only at the Wails GUI phase (rod PrintToPDF / excelize seams above).

## RAG corpora (added 2026-09-19)

`scrape`/`batch` already feed small corpora (`--page-format llm`, batch ≤100 with
`{url,title,markdown}` envelopes); `crawl` requires `--schema` and drops page text —
the site-scale gap. Plan: **`plan/phase-I.md`** — `crawl --corpus` emitting keyless
JSONL `{url,title,depth,markdown}`. Chunk/embed/store stay downstream (not magpie's job).
