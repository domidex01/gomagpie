# Phase C — Agent Surface: Tools + Commands (stdlib)

**Duration:** Days 1–4 (~24 hours)
**Depends on:** Phase A (scrape `Options`/`Scope`/`PageFormat`, `clean.Render`, quality errors, fetch profiles) and Phase B (`vertical/` registry + `Lookup`/`MatchURL` + `OptIn`, `scrape.Options.Vertical`, `ErrURLMismatch`)
**Blocks:** Phase D (crawl scope filters reuse `map`'s sitemap listing; SSRF guard wraps `batch`'s fan-out), Phase E (nothing — dep-gated work is independent)
**Risk Level:** MEDIUM — additive surface only (7 MCP tools, 6 CLI commands, prompt-mode extract); no pipeline/fetch/clean signature changes, no new dependencies. 6 sections (failure section omitted per MEDIUM risk).

---

## 1. Objective + What Success Looks Like

Give agents everything webclaw's agent surface has that is stdlib-buildable: bounded batch scrape, sitemap listing, LLM summarize, word diff, brand extraction, and zero-LLM vertical scraping — as both MCP tools and CLI commands — plus prompt-mode (schema-less) extraction and string-coercing MCP inputs so stringy clients stop erroring.

1. `magpie batch <10 urls> --format jsonl` exits 0 with one record per URL, each carrying `ok`/`error` (one bad URL never fails the batch).
2. `magpie map https://example.com` prints sitemap-derived URLs (robots-declared sitemaps, bodies parsed via stdlib `encoding/xml`, gzip decoded inline, output capped with a `truncated` flag).
3. `magpie summarize <url> --max-sentences 3` prints ≤3 sentences (fake Prompter in tests; real run needs a key, same as `extract` today).
4. `magpie diff <url> --against snapshot.txt` prints a word-level diff of current markdown vs the snapshot file.
5. `magpie brand <url>` prints JSON with `colors`, `fonts`, `logo`, `favicon`.
6. `magpie vertical --list` prints all 10 extractor infos; `magpie vertical <url> --name github_repo` prints the typed record with zero LLM calls.
7. `magpie extract --prompt "summarize" page.html` returns text with no `--schema` (and `--prompt` + `--schema` together exits 2).
8. MCP exposes exactly 11 tools (4 existing + 7 new); new `TestToolCatalog` (alongside `TestMCP_FourTools`) pins the list; `max_pages: "3"` (string) coerces instead of erroring.
9. `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, and 3-way `CGO_ENABLED=0` cross-builds all pass.

---

## 2. Architecture / Key Design Decisions

```
                    ┌─ batch ───── scrape.Run (nil schema, errgroup fan-out ≤100)
                    ├─ map ─────── crawl.ListSitemapURLs (robots + encoding/xml)
MCP (mcp/agent.go) ─┼─ summarize ─ scrape.Summarize (scrape.Run + extract prompt call)
  + CLI (cli/*.go) ─┼─ diff ────── scrape.Run + scrape.DiffWords (stdlib LCS, capped)
                    ├─ brand ───── scrape.BrandPage (Fetcher seam → clean.Brand + HarvestMetadata)
                    ├─ list_extractors ─ vertical.List()
                    └─ vertical_scrape ─ vertical.Lookup + Match + Extract
prompt-mode: extract.Prompter (PromptText per provider) ← summarize + extract --prompt
coercion: mcp/coerce.go widenToolInput (union schemas) + flex unmarshal ← all scalar/array/object inputs
```

### Data model strategy (stack: go)

| Layer | Type | Why |
|---|---|---|
| MCP/tool + CLI boundary | Plain structs with `json`/`jsonschema` tags (existing `ScrapeIn` pattern) | The go-sdk generates tool schemas from these; no validation framework exists — keep it that way |
| Internal results | Plain structs (`BrandInfo`, `DiffOut`, `SummaryOut`) in the owning package | Hot-path batch fan-out allocates per URL; structs, never `map[string]any`, past the boundary |
| LLM text path | `string` + `extract.TokenUsage` (existing) | Prompt-mode returns text, never validated JSON — no schema type involved |

**Rules for this phase (follow exactly):**
- **Reuse, don't re-fetch:** `batch`, `summarize`, `diff` go through `scrape.Run` (markdown-only, nil schema). `brand` and `map` go through the injected `Fetcher` seam directly (`scrape.Result` carries no raw HTML, so `scrape.Run` cannot serve brand). No handler dials HTTP directly.
- **Fail loudly per item:** batch/diff/summarize item errors are data (`{url, ok:false, error}`, typed quality errors preserved), never silent skips and never whole-batch aborts — except `batch` input validation (>100 URLs, bad concurrency) which fails pre-I/O with exit 2.
- **No new dependency, no new interface with one implementation:** `golang.org/x/sync/errgroup` is already a direct dep (go.mod) — use `SetLimit` for fan-out. `encoding/xml`, `regexp`, `strings` cover map/brand/diff/summarize-splitting.
- **Tests stay hermetic:** `httptest` servers + fake `Fetcher` + fake `Prompter`; any test needing an LLM key is forbidden in the default suite (same rule as today).
- **`search` stays OUT** (spec §6: no key story). If anyone asks mid-phase, the answer is "not in Phase C" — no thin client, no flag.

### Per-item design notes

- **batch (C1):** `errgroup.Group.SetLimit(concurrency)` (default 8, same as crawl's `--concurrency`; safe against the store — `store.Open` pins `SetMaxOpenConns(1)` with 5s `busy_timeout`); each goroutine calls `scrape.Run` with nil schema + caller's scope/profile flags; results stream to JSONL in input order (collect-then-write, ≤100 items — bounded memory, no streaming machinery needed). CLI accepts URLs as args plus `--file` (one URL per line, `-` = stdin).
- **map (C1):** new `crawl/sitemap.go`: `ListSitemapURLs(ctx, Fetcher, siteURL) (urls []string, truncated bool, err error)` — start from `Checker.Sitemaps` (robots-declared, already exists), fetch each body through the `Fetcher` seam, sniff `0x1f8b` and gunzip via stdlib `compress/gzip` (`.xml.gz` is normal for the large sites that need `map` most — ~4 lines, do it now, not in Phase D), parse `<urlset>`/one `<sitemapindex>` level with `encoding/xml`. Bound the fan-out: follow ≤100 child sitemaps, return ≤10k URLs, set `truncated:true` past either cap (an index may legally list 50k sitemaps of 50k URLs each). Index-recursion depth ≥5 and partial-on-budget results stay **Phase D (D2)** hardening. `magpie map <site>` prints one URL per line (default) or JSON (`{urls, truncated}`) with `--format json`.
- **summarize (C1, after C2):** new `scrape/summarize.go`: `Summarize(ctx, d Deps, url string, maxSentences int, provider, model string)` — markdown-only `scrape.Run`, cap input at first ~4000 words (cost bound, note the cutoff in output? No — keep output clean; the cap is a documented constant), prompt `Summarize in ≤N sentences`, sentences counted on the way out and hard-truncated to N with stdlib splitting (`.!?` + `strings.Fields`). `Purpose: "summarize"` so `llm_calls` stays attributable. Cost ceiling via the existing `checkCostCeiling` (same file — no export needed). Prompter seam, decided: `Summarize` type-asserts the `ExtractorFor` product to `extract.Prompter` — all three adapter types implement it, assertion failure is a loud error, no new `Deps` field.
- **diff (C1):** new `scrape/diff.go`: `DiffWords(prev, cur string) (string, error)` — trim common prefix/suffix words first (real snapshot diffs are a handful of words), then word-level LCS on the remaining window producing `-old +new` token stream grouped by line. `ponytail:` O(n·m) time/memory on the window — windows over ~2k words return a loud error suggesting narrower snapshots (a raw 20k-word cap bounds nothing: 20k×20k cells ≈ 1.6 GB at int32; Hirschberg is the approved upgrade path). No difflib/testify in the graph — hand-roll it. MCP `diff` takes `{url, previous_snapshot}` (snapshot inline — matches webclaw shape); CLI `magpie diff <url> --against <file>` (`-` = stdin).
- **brand (C1):** `scrape.Run` cannot serve brand — `scrape.Result` (scrape/scrape.go:59) carries Markdown/StructuredData/Record but no raw HTML. So: new `scrape/brand.go` `BrandPage(ctx, d Deps, rawURL)` fetches through the `Fetcher` seam (nil-safe, same nil→`NewStaticFetcher` fallback `scrape.Run` uses) and calls pure `clean.Brand(html)` from new `clean/brand.go`. Favicon and `og:image` are NOT reimplemented: reuse `clean.HarvestMetadata(html, url, …)` (clean/metadata.go:26 — resolves `link[rel~="icon"]` to absolute, pulls `og:image`), so C.6 is only colors (hex regexp over `<style>` + `style` attrs), fonts (`font-family` regexp), and the logo precedence `img[src*=logo i]` → `svg[class*=logo i]` → harvested `og:image` → `""` (cascadia v1.3.5 parses the `i` flag). Favicon logic must never exist in two places. Zero LLM.
- **list_extractors / vertical_scrape (C1):** `vertical.List()` straight through; `vertical_scrape {url, vertical}` = `vertical.Lookup` → `url.Parse` → `Match` check reusing `vertical.ErrURLMismatch` wording → `Extract` via `d.ScrapeDeps.Fetcher` (nil-safe: same nil→`NewStaticFetcher` fallback `scrape.Run` uses — factor a tiny shared helper if the second call site offends, don't export new API). CLI: `magpie vertical --list`, and `magpie vertical <url> [--name X]` where omitted name = `Vertical: "auto"` through `scrape.Run` (reuse, no new path), explicit name = pre-validated `Vertical: name` (exit 2 on unknown/mismatch, same as `scrape --vertical` today).
- **Prompt-mode (C2, before summarize):** new `extract/prompt.go`: `Prompter` interface `{ PromptText(ctx, system, user string) (string, TokenUsage, error) }`; implement on all three adapter types (`OpenAIAdapter` serves openai/ollama/openrouter/zen-chat, `AnthropicAdapter` serves anthropic/zen-messages, `CodexExecAdapter` serves codex — zen reuses the first two via `newZenExtractor`). Each adapter builds its single call as a closure *inside* `Extract` closing over the schema doc, so this is closure-lifting to a method with a nil-doc branch, not a five-line addition. Codex is the easy one — verified schema-less (`-o` emits the plain-text final message independently of `--output-schema`). No repair loop, no validator; keep each adapter's run-logging hook. `extract` CLI gains `--prompt` (xor `--schema`, enforced exit 2); `extract_structured` gains `prompt` (xor `schema`, same error shape as today's "schema is required"). Wire `Purpose` through so cost attribution keeps working.
- **Coercion (C3):** `UnmarshalJSON`-only is proven broken: go-sdk v1.8.0 validates arguments against the resolved `InputSchema` (`applySchema`, mcp/server.go:402) *before* unmarshaling into `In` (:411), so a string `"3"` dies in validation and the custom unmarshaler never runs (reproduced in a throwaway server: `type: 3 has type "string", want "integer"`). The design is therefore schema-widening first, flex-unmarshal second. One helper `widenToolInput(t *sdk.Tool)`: let `AddTool` infer the schema from the `In` type as today (keeps every `jsonschema:` description for free), then walk `InputSchema.Properties` and widen `Type` only — `integer→Types[integer,string]`, `boolean→[boolean,string]`, `array→[array,string]`, `object→[object,string]` — never touching `required` or descriptions; a test asserts both are byte-identical pre/post widen. Then named flex types coerce post-validation in `UnmarshalJSON`: keep `FlexInt`/`FlexBool` (`"3"→3`, `"true"→true`, loud error naming the param), add `StringList []string` for `batch.urls` (string → try JSON-array decode, else single trimmed URL — stringification hits arrays too per Claude Code #60963, and `urls` is the only required param of the flagship tool) and `FlexMap map[string]any` for `schema` object params (string → JSON-object decode). Swap into **all** scalar/array/object fields of all 11 tools' `In` structs (old and new). Keep `,omitempty` tags on the swap — inference marks non-omitempty fields required. New `TestToolCatalog` asserts the 11-tool set; it sits *alongside* `TestMCP_FourTools` (a functional per-tool test, not a catalog — do not rename it).
- **Provider chain (C4, last, optional):** `--provider auto` for prompt/summarize paths only: fixed priority order defined once in `cli/shared.go` (document it there), skip providers with no key (`cfg.APIKey == ""`), first success wins, total failure returns one error listing every attempted provider. Never touches schema paths (strict-schema differences matter there — spec says so).

---

## 3. Tasks

### Task C.0 — Scaffolding + catalog test skeleton (1h)

Add `TestToolCatalog` (new file `mcp/agent_test.go`, alongside `TestMCP_FourTools`) asserting the exact tool-name set; start it at the 4 current tools so every later task turns one entry green. Build `widenToolInput` + the four flex types here and apply to the 4 existing tools' scalar/array/object fields first (smallest surface), with the required/descriptions-unchanged test.

**Sanity check:** `go test ./mcp/ -run TestToolCatalog -v` exits 0 with 4 tools listed.

### Task C.1 — Prompt-mode extraction (4h)

**Depends on:** C.0. **Blocks:** summarize (C.4), provider chain (C.8).

New `extract/prompt.go` (`Prompter` interface + `PromptText` on the 3 adapter types via closure-lifting with nil-doc branch; Codex first, it's the easy one). CLI `extract --prompt` xor `--schema` (exit 2 both-set/neither-set… careful: neither-set keeps today's "schema is required" error; both-set is the new exit-2). MCP `extract_structured` `prompt` field, same xor rule.

Implementation notes: no validator, no coercion, no repair loop on this path; log usage with the caller's `Purpose`; cost-ceiling check stays (reuse existing call in cli path). Every provider adapter in `cli/shared.go` must satisfy `Prompter` — compile-time assert (`var _ extract.Prompter = ...`) per adapter type (3, not 7 — zen reuses OpenAI/Anthropic) so a new provider can't silently miss it.

**Sanity check:** `echo '<p>hi</p>' | magpie extract --prompt 'say ok' --provider ollama` prints text (ollama needs no key).

### Task C.2 — `batch` tool + command (3h)

**Depends on:** C.0 (only). MCP `batch {urls[≤100], concurrency, scope/profile passthrough}` → errgroup fan-out over `scrape.Run` (nil schema), per-URL `{url, ok, markdown?, error?}` preserving quality-error strings. CLI `magpie batch [urls...] [--file] [--concurrency 8] [--format jsonl|json]`.

**Sanity check:** `magpie batch file:///a.html file:///missing.html` exits 0; second record has `ok:false`.

### Task C.3 — `map` tool + command (3h)

**Depends on:** C.0. New `crawl/sitemap.go` `ListSitemapURLs` (robots seeds + stdlib xml + gzip-now + ≤100 children / ≤10k URLs / `truncated` flag; depth-5 + partial-budget stay Phase D). MCP `map {site}`; CLI `magpie map <site>` (lines default, `--format json`).

**Sanity check:** against an httptest robots+sitemap pair, `magpie map` prints exactly the `<loc>` set.

### Task C.4 — `summarize` tool + command (2h)

**Depends on:** C.1. New `scrape/summarize.go` `Summarize` (markdown-only scrape + prompt call + N-sentence hard truncation). MCP `summarize {url, max_sentences}` (`max_sentences` = `FlexInt`, default 3, clamp 1–20); CLI `magpie summarize <url> [--max-sentences 3] [--provider]`.

**Sanity check:** fake-Prompter unit test asserts output has ≤N sentences even when the model rambles.

### Task C.5 — `diff` tool + command (2h)

**Depends on:** C.0. New `scrape/diff.go` `DiffWords` (prefix/suffix trim + LCS on window; >~2k-word window errors loudly). MCP `diff {url, previous_snapshot}`; CLI `magpie diff <url> --against <file|->`.

**Sanity check:** identical snapshot → empty diff, exit 0; one-word change → single `-old +new` pair in output.

### Task C.6 — `brand` tool + command (2h)

**Depends on:** C.0. New `scrape/brand.go` `BrandPage` (Fetcher seam) + pure `clean.Brand(html)` in `clean/brand.go` (+ `BrandInfo` struct); favicon/og:image via `clean.HarvestMetadata`, never reimplemented. Golden fixtures under `testdata/brand/` (page with `<style>`, inline styles, logo img, icon link). MCP `brand {url}`; CLI `magpie brand <url>` (JSON out).

**Sanity check:** `magpie brand file://$PWD/testdata/brand/shop.html` prints the golden JSON (file transport exercises the Fetcher-seam path; unit tests inject a fake Fetcher).

### Task C.7 — `list_extractors` + `vertical_scrape` + `vertical` command (2h)

**Depends on:** C.0. Straight-through handlers (see design notes). CLI `magpie vertical --list` / `magpie vertical <url> [--name]`. Reuse `ErrURLMismatch` message; unknown name exits 2 pre-I/O.

**Sanity check:** `magpie vertical --list | grep github_repo`; `magpie vertical <file> --name github_repo` exits 2 with mismatch error.

### Task C.8 — Provider chain `--provider auto` (2h, optional — drop if over budget)

**Depends on:** C.1, C.4. Priority order in `cli/shared.go` + skip-keyless + aggregate error. Wire into prompt-extract and summarize only (CLI flag + MCP `provider:"auto"` passthrough).

**Sanity check:** unit-test the ordering/selection function pure (keyed-first, keyless skipped); provider-failure fallback via fake-Prompter error injection. Output names the winning provider.

### Task C.9 — Docs + full gates (3h)

`cli` docs test already asserts help text (`scrape_test.go:393` pattern) — extend to the 6 new commands. Update any tool-count-sensitive docs. Run: `go test ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds (`GOOS=linux|windows|darwin`, matching prior phases).

---

## 4. Deliverables

```
plan/phase-C.md              # this file
extract/prompt.go            # Prompter interface + per-provider PromptText
scrape/summarize.go          # Summarize helper (markdown scrape + prompt + truncate)
scrape/diff.go               # DiffWords: prefix/suffix trim + windowed LCS
scrape/brand.go              # BrandPage helper (Fetcher seam → clean.Brand)
clean/brand.go               # Brand() pure: colors/fonts/logo (favicon+og:image via HarvestMetadata)
crawl/sitemap.go             # ListSitemapURLs (robots seeds + xml + gzip + caps)
mcp/agent.go                 # 7 new tool handlers + In/Out structs (batch, map,
                             #   summarize, diff, brand, list_extractors, vertical_scrape)
mcp/coerce.go                # widenToolInput + FlexInt/FlexBool/StringList/FlexMap
mcp/server.go                # (edit) register 7 tools → 11 total
cli/batch.go                 # `magpie batch` command
cli/map.go                   # `magpie map` command
cli/summarize.go             # `magpie summarize` command
cli/diff.go                  # `magpie diff` command
cli/brand.go                 # `magpie brand` command
cli/vertical.go              # `magpie vertical [--list] [<url> --name]` command
cli/root.go                  # (edit) register 6 commands
cli/shared.go                # (edit) provider priority order for `auto` (C.8)
testdata/brand/              # brand golden fixtures
mcp/agent_test.go            # TestToolCatalog + widening + coercion + per-tool hermetic tests
cli/agent_test.go            # 6 command tests (help text, exit codes, file:// runs)
extract/prompt_test.go       # xor rules, fake-provider PromptText wiring
scrape/summarize_test.go     # sentence truncation, cap, ceiling passthrough
scrape/diff_test.go          # LCS cases, identical→empty, window-cap error
clean/brand_test.go          # fixture goldens (colors/fonts/logo/favicon)
crawl/sitemap_test.go        # robots+urlset+index+gzip fixtures, cap/truncated cases
```

One file per CLI command follows the existing `cli/` convention (`scrape.go`, `extract.go`, `crawl.go`, …). One `mcp/agent.go` (not seven files) — the handlers are thin shims over `scrape/`/`crawl/`/`clean/`/`vertical/`, same as today's `tools.go`.

---

## 5. Exit Criteria

- [ ] `magpie batch <mixed good/bad urls>` exits 0 with per-URL ok/error records (C.2)
- [ ] `magpie map <httptest site>` prints exactly the fixture `<loc>` set; capped fixtures set `truncated` (C.3)
- [ ] `magpie summarize <url> --max-sentences 3` (fake provider in tests) returns ≤3 sentences (C.4)
- [ ] `magpie diff <url> --against <file>` shows `-old +new` pairs; identical input → empty diff (C.5)
- [ ] `magpie brand <file-url>` returns colors/fonts/logo/favicon matching the golden (C.6)
- [ ] `magpie vertical --list` shows 10 extractors; `--name` mismatch exits 2 pre-I/O (C.7)
- [ ] `extract --prompt` xor `--schema` enforced (exit 2); prompt path bills no validator (C.1)
- [ ] new `TestToolCatalog` (alongside `TestMCP_FourTools`) asserts exactly 11 tools; `"3"`/`"true"` string inputs coerce (C.0 — all tools)
- [ ] `--provider auto` tries keyed providers in documented order (C.8 — or explicit drop-note if cut)
- [ ] `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds pass (C.9)

---

## 6. Execution Prompt

---
You are implementing Phase C (agent surface: tools + commands, stdlib-only) of gomagpie (`magpie`, module `gomagpie`, Go 1.26).

### What this project is

Go CLI web scraper: fetch → clean → extract. Single static binary (`CGO_ENABLED=0`, 3-way cross-builds), pure-Go, no CGO ever. Source of truth for this phase: `plan/webclaw-gap-spec.md` §5-Phase C. Rules: `AGENTS.md` + `.pi/rules/go.md` + `.pi/rules/testing.md`. Lazy senior dev: reuse helpers, stdlib first (`encoding/xml`, `regexp`, `golang.org/x/sync/errgroup` is already a direct dep), no new dependency without asking, no new abstraction for one caller. Hermetic tests only (`httptest`, fakes) — no live network in the default suite.

### Established in prior phases (reuse all of it)

- `scrape.Run(ctx, Deps, url, Options) Result` is the shared single-URL flow (fetch→clean→extract). `Deps{Fetcher}` nil → `NewStaticFetcher`. `Options{Schema(nil)=markdown-only, Render, Provider, Model, MaxCost, UseCache, PageFormat, Scope(clean.Scope), Profile, Cookies, Vertical}`. `Vertical`: `""` off, `"auto"` strict auto-dispatch, name = explicit (unknown name fails pre-I/O; mismatch → `vertical.ErrURLMismatch`). `Result{URL, FinalURL, Title, Markdown, StructuredData, Record, FromCache, Usage, Provider, Model, Rendered, Vertical}`. Quality failures return `*clean.QualityError` (CLI maps to exit 8); missing key → `ErrMissingKey` (exit 7); cost ceiling → `crawl.ErrCostCeiling` (exit 6); usage errors exit 2.
- `clean.HarvestMetadata(html, url, …)` (clean/metadata.go:26) owns favicon (absolute) + og:image — reuse it, never reimplement. Neither `clean.Brand` nor `scrape.BrandPage` exists yet — brand fetches through the `Fetcher` seam because `scrape.Result` carries no raw HTML.
- `vertical/` registry: `List() []Info`, `Lookup(name)`, `MatchURL(url)` (skips `OptIn` permissives), 10 extractors. `Fetcher` interface = `{Fetch(ctx, fetch.FetchRequest) (*fetch.FetchResponse, error)}`, satisfied by `*fetch.StaticFetcher`.
- `extract`: `runRepairLoop(providerCall, …)` with 3-attempt repair; `ExtractInput{Markdown, StructuredData, Schema, PromptExtra, Purpose}`; providers post via shared `httpClient` (120s timeout). Three adapter types only (`OpenAIAdapter`, `AnthropicAdapter`, `CodexExecAdapter` — zen reuses the first two via `newZenExtractor`); each builds its single call as a closure inside `Extract`, so prompt-mode = closure-lifting with a nil-doc branch. `extract.ProjectedCost` / `EstimatePromptTokens` exist for ceilings.
- `crawl.Checker.Sitemaps(ctx, url)` returns robots-declared sitemap URLs (`crawl/robots.go`). `cli` commands live one-file-per-command (`cli/scrape.go`, `extract.go`, …) with `fail(code, …)` + `exitFor`; `ProviderHelp = "anthropic|openai|ollama|openrouter|codex|opencode-go|opencode-zen"` in `cli/shared.go`; `newExtractor(provider, key, model, sch, db, runID)` builds adapters. `needsAPIKey`: ollama/codex/"" need no key.
- `mcp`: `NewServer(Deps{DB, ScrapeDeps, DefaultProvider, DefaultModel, MaxCost})` registers 4 tools (`scrape_url`, `crawl_site`, `extract_structured`, `get_cached_selectors`); add `TestToolCatalog` alongside `TestMCP_FourTools` (a functional per-tool test, not a catalog) — 11 tools at end. go-sdk validates args against `InputSchema` *before* unmarshaling (`applySchema` mcp/server.go:402 runs before unmarshal at :411) — coercion MUST widen the schema first (see snippet).
- Gates: `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds.

### Data model rules (follow exactly)

- Boundary structs (MCP In/Out, CLI option structs): plain structs with `json`/`jsonschema` tags, mirroring `ScrapeIn`/`ScrapeOut` in `mcp/tools.go`.
- Internal results (`BrandInfo`, summary/diff outputs): plain structs in the owning package (`clean`, `scrape`); `map[string]any` only at the JSON edge.
- Prompt-mode returns `string` + `extract.TokenUsage` — never validated, never coerced.

### Architecture (from §2 above — implement as drawn)

Shared logic lives in `extract/` (`prompt.go`: `Prompter`), `scrape/` (`summarize.go`, `diff.go`, `brand.go`), `clean/` (`brand.go` pure + `HarvestMetadata` reuse), `crawl/` (`sitemap.go`); `mcp/agent.go` holds the 7 thin handlers; `mcp/coerce.go` holds `widenToolInput` + flex types; six `cli/*.go` files mirror the tools. Reuse `scrape.Run` for batch/summarize/diff and the `Fetcher` seam for brand/map; item errors are data, batch validation is exit 2; `search` is OUT — do not build it.

### Confirmed API snippets (no research needed — all stdlib/in-repo)

```go
// Fan-out (golang.org/x/sync is a direct dep — no new dependency):
var g errgroup.Group
g.SetLimit(concurrency) // default 8, same as crawl --concurrency
for i, u := range urls {
    g.Go(func() error { res[i], errs[i] = scrape.Run(ctx, deps, u, scrape.Options{/* nil Schema */}); return nil })
}
_ = g.Wait() // item errors are data, never abort the batch

// Sitemap parse (stdlib only):
type urlSet struct { URLs []struct{ Loc string `xml:"loc"` } `xml:"url"` }
type sitemapIndex struct { Maps []struct{ Loc string `xml:"loc"` } `xml:"sitemap"` }
// Phase C: urlset + one index level (≤100 children), ≤10k URLs, truncated flag;
// gzip decoded inline (0x1f8b sniff + compress/gzip). Depth-5 stays Phase D.

// Coercion: widen the inferred schema FIRST (validation precedes unmarshal),
// then coerce post-validation. Widen touches Type only — required[] and
// descriptions are asserted identical pre/post widen.
func widenToolInput(t *sdk.Tool) { // called after every AddTool
	for _, prop := range t.InputSchema.Properties {
		// jsonschema-go: Type for single, Types for union — never both.
		switch prop.Type {
		case "integer":
			prop.Type, prop.Types = "", []string{"integer", "string"}
		case "boolean":
			prop.Type, prop.Types = "", []string{"boolean", "string"}
		case "array":
			prop.Type, prop.Types = "", []string{"array", "string"}
		case "object":
			prop.Type, prop.Types = "", []string{"object", "string"}
		}
	}
}
// Then named flex types consumed by In structs coerce in UnmarshalJSON:
// FlexInt ("3"→3), FlexBool ("true"→true, case-insensitive),
// StringList (string→try JSON-array decode, else single trimmed URL),
// FlexMap (string→JSON-object decode). Loud errors name the param.
// Keep ,omitempty tags — inference marks non-omitempty fields required.
```

### Files to create/edit (per-file guidance)

- `extract/prompt.go`: `Prompter` interface + `PromptText` on the 3 adapter types (closure-lifting, nil-doc branch; Codex first). Compile-time asserts per adapter type. No repair loop, no validator; thread `Purpose`.
- `scrape/summarize.go`: `Summarize` — markdown-only `scrape.Run`, ~4000-word input cap (named const), prompt call with `Purpose "summarize"`, stdlib sentence split, hard truncate to N (clamp 1–20), reuse `checkCostCeiling` (same package). Type-assert the `ExtractorFor` product to `Prompter` (loud error on failure — no new seam).
- `scrape/diff.go`: `DiffWords(prev, cur) (string, error)` — prefix/suffix trim, windowed word-LCS `-old +new` stream; `ponytail:` O(n·m) on the window, >~2k words errors loudly (Hirschberg = upgrade path).
- `clean/brand.go`: pure `Brand(html)` (colors/fonts/logo only); `scrape/brand.go`: `BrandPage` (Fetcher seam → `Brand` + `HarvestMetadata` for favicon/og:image). Never two favicon implementations.
- `crawl/sitemap.go`: `ListSitemapURLs(ctx, Fetcher, site) (urls, truncated, err)` — `Checker.Sitemaps` seeds + xml + inline gzip + ≤100/≤10k caps.
- `mcp/agent.go` (7 handlers) + `mcp/coerce.go` (`widenToolInput` + 4 flex types, applied to all 11 tools) + `mcp/server.go` (register 7, widen after each `AddTool`) + `cli/batch.go|map.go|summarize.go|diff.go|brand.go|vertical.go` + `cli/root.go` (register 6) + `cli/shared.go` (`auto` order, C.8 only).
- Tests as listed in §4 deliverables; goldens in `testdata/brand/`. Update help-text/docs tests for new commands.

### Success criteria

§5 exit criteria, all boxes checked. A keyless agent can batch→map→diff→brand→vertical over MCP with zero LLM calls; `--provider auto` and prompt paths are the only key-requiring additions.

### Expected file structure at end

See §4 Deliverables tree.

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available (scrape.Run/Options, clean.Render/Scope, vertical registry + ErrURLMismatch, MCP Deps/4 tools, Checker.Sitemaps, ProviderHelp/newExtractor — all verified by direct file reads this session)
- [PASS] Every sub-task has a clear, testable completion condition (each task ends with a Sanity check one-liner)
- [PASS] Execution prompt is self-contained: includes (a) what prior phases established, (b) confirmed API snippets (errgroup.SetLimit, xml shapes, FlexInt — all stdlib/in-repo, no web research needed), (c) a Data Model Rules section, (d) per-file guidance, and (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables (one criterion per tool/command/cross-cutting item + gates)
- [PASS] Heavy external dependency strategy noted (LLM calls: hermetic fakes only, key-requiring paths forbidden in default suite; browser/profiles not needed — markdown-only scrape.Run throughout)
- [PASS] New libraries: none — errgroup (x/sync), encoding/xml, regexp are stdlib/already-direct-deps; confirmed via go.mod read. No `web_search` required.

---

## Amendments — validator pass 2026-09-17

Independent validation (repo + pinned go-sdk source + web) found four plan-vs-code breaks, all incorporated above:

- **Coercion redesigned (was a silent broken feature):** go-sdk validates against `InputSchema` before unmarshaling, so `UnmarshalJSON`-only coercion never fires. Widen-first (`widenToolInput` + union `Types`) is now the design, flex types second; extended to arrays/objects (`StringList` for `batch.urls`, `FlexMap` for `schema` params).
- **Brand moved off `scrape.Run`:** `scrape.Result` has no raw-HTML field — `scrape/brand.go` fetches via the `Fetcher` seam; favicon/og:image reuse `clean.HarvestMetadata` (no second copy of the logic).
- **Diff cap fixed:** raw 20k-word cap bounds nothing (20k² cells ≈ 1.6 GB) — now prefix/suffix trim + ~2k-word window cap, Hirschberg as upgrade path.
- **Map hardened early:** gzip decoded inline (`compress/gzip`, ~4 lines) instead of erroring for Phase D; added ≤100/≤10k caps + `truncated` flag (uncapped index = up to 50k fetches).
- **Prompt-mode scoped honestly:** 3 adapter types (not 7 providers), closure-lifting not 5-line additions; `Summarize`→`Prompter` seam decided (type assertion, no new field).
- **Hygiene:** hours corrected 26→24 (task sum); `TestToolCatalog` added alongside `TestMCP_FourTools` (not renamed); C.6/C.8 thinking-out-loud asides resolved into decided checks.

---
