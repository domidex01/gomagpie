# WebClaw gap analysis → gomagpie improvement spec

Source of truth for *what to build next*. Competes with `spec.md` nowhere:
`spec.md` defines the pipeline; this file defines the parity-plus deltas
inspired by [webclaw](https://github.com/0xMassi/webclaw) (Rust, OSS CLI +
MCP + REST + hosted cloud). Researched 2026-09-17 from the webclaw tree
(`crates/webclaw-{core,fetch,llm,mcp,server,pdf}`, `benchmarks/`, README)
plus two web searches (Go TLS-impersonation options; Firecrawl-class
positioning). All webclaw claims below were read in source, not snippets.

Rule for this whole spec, per AGENTS.md: **no new dependency without asking.**
Every item is tagged `stdlib` (build now) or `dep-gated` (needs approval).

---

## 1. Feature matrix (what each product has today)

| Capability | webclaw | gomagpie | Verdict |
|---|---|---|---|
| Single-URL scrape → markdown | ✅ formats: `markdown\|llm\|text\|json` | ✅ GFM markdown only | **Gap: `llm`/`text`/`json` formats** |
| Token-optimized LLM text (`to_llm_text`: strip bold/italic/images, logo-collapse, stat-merge, CSS-class strip, link-dedup → footer, structured-data gating 16 KB + body-field scrub) | ✅ `webclaw-core/src/llm/` | ❌ | **Gap (high value, stdlib)** |
| User scoping: `--include/--exclude/--only-main-content` | ✅ CLI + MCP + crawl | ❌ (only `x-gomagpie.css_hint` in schema) | **Gap (stdlib)** |
| Batch (N URLs, bounded concurrency, per-URL errors) | ✅ `batch` tool + streaming `fetch_and_extract_batch_stream` | ❌ (crawl only) | **Gap (stdlib)** |
| Map (sitemap discovery as first-class output) | ✅ `map` tool + route | ❌ (sitemaps only seed crawl internally) | **Gap (stdlib)** |
| Search (Serper key, `country/lang/scrape` opts) | ✅ `search` tool | ❌ | Gap (stdlib, needs user key) |
| Summarize (`max_sentences`) | ✅ `summarize` tool + route | ❌ | **Gap (stdlib, reuses providers)** |
| Diff (snapshot vs current) | ✅ `diff` tool + route | ❌ | Gap (stdlib) |
| Brand (colors/fonts/logo/favicon) | ✅ `brand` tool + route | ❌ | Gap (stdlib+goquery) |
| Vertical typed extractors (29: reddit, hn, github×4, pypi, npm, crates_io, hf×2, arxiv, docker_hub, dev_to, so, substack, youtube, linkedin, instagram×2, shopify×2, ecommerce, woo, amazon, ebay, etsy, trustpilot, chronopost; JSON-API-first, `list` + `dispatch_by_url/by_name`) | ✅ `list_extractors` + `vertical_scrape` | ❌ (generic JSON-LD only) | **Biggest functional gap (stdlib)** |
| Site fast paths: reddit→old.reddit, YouTube `ytInitialPlayerResponse`, LinkedIn embedded JSON, `__NEXT_DATA__`/SvelteKit islands, QuickJS-evaluated blobs | ✅ | 🟡 `__NEXT_DATA__`/`__NUXT__`/JSON-LD only | **Gap (stdlib, pick top 3)** |
| Content recovery after scoring: H1-prepend, hero paragraph, announcement banners, stripped-h2 re-insert, footer CTA + footer sitemap, noscript decode, body-fallback when <200 words | ✅ `extractor.rs` | ❌ (trafilatura single-shot + dumb `fallbackMarkdown`) | **Gap (stdlib, keep trafilatura, add recovery)** |
| Quality taxonomy (`empty/access-denied/unavailable/login-required`, <200-word guard so articles mentioning "Just a moment" don't false-positive) | ✅ `quality.rs`, enforced in fetch | ❌ | **Gap (stdlib)** |
| Bot-challenge detect + homepage cookie-warmup retry (Akamai `_abck` etc.) | ✅ | ❌ | **Gap (stdlib)** |
| TLS/browser fingerprint (Chrome/Firefox/Random/Safari-iOS via wreq/BoringSSL) + per-call cookies | ✅ | ❌ (stock `net/http`, one static UA) | Gap, **dep-gated** (see §4) |
| Proxy: single `WEBCLAW_PROXY` + `proxies.txt` pool, host-pinned rotation | ✅ | ❌ (only a `ProxyProvider` interface, no impl) | Gap (stdlib for env-single; pool later) |
| Cloud fallback for blocked pages (`WEBCLAW_API_KEY`, `smart_fetch`) | ✅ (hosted upsell) | ❌, and correctly so | **Not a gap** — no hosted backend; fail loudly instead (§5) |
| PDF + office-document extraction | ✅ `webclaw-pdf` + `document.rs` | ❌ | Gap, **dep-gated** |
| Crawl scope: `path_prefix`, include/exclude globs (validated vs ReDoS), `allow_subdomains`, `allow_external_links`, `use_sitemap`, `stream_only`, frontier cap + trim, uncapped `max_pages=None` | ✅ | 🟡 `sameHost` bool only; SQLite frontier already durable | **Gap: globs + subdomain flag (stdlib)**; stream_only/frontier-cap unneeded (writer already streams, §5) |
| Sitemap robustness: `.xml.gz` via raw-bytes fetch, index recursion depth 5, case-insensitive `Sitemap:` + inline-comment parsing, time-budget partial results | ✅ | 🟡 robots+Sitemaps via `grobotstxt` (`crawl/robots.go`) — verify gzip + index depth before claiming parity | **Gap iff verification fails (stdlib)** |
| SSRF guard (`validate_public_http_url`: no localhost/link-local/cloud-metadata) | ✅ enforced on every fetch incl. batch | ❌ (no guard found; `file://` transport registered globally) | **Gap, security (stdlib)** |
| Body limits: 50 MB cap + streaming decompression-bomb guard | ✅ | 🟡 50 MB `LimitReader` only (no decompression guard; default transport auto-decodes gzip) | Small gap (stdlib) |
| MCP hardening: `"3"`→`3` / `"true"`→`true` coercion per param, tool-catalog test, cached Firefox client | ✅ | ❌ (strict schema; fails on stringy clients) | **Gap (stdlib)** |
| MCP surface | 12 tools: scrape, crawl, map, batch, extract, summarize, diff, brand, research, search, list_extractors, vertical_scrape | 4 tools: scrape_url, crawl_site, extract_structured, get_cached_selectors | **Gap: +7 local tools** (research stays out, §6) |
| REST server + Firecrawl-compat API + SDKs/`npx create-webclaw` installer + agent skill | ✅ `webclaw-server`, examples/ | ❌ (`serve` is MCP-only) | Defer (see §6); installer/skill is docs work |
| Extraction benchmarks (offline harness + fixtures + compare script) | ✅ `benchmarks/` | 🟡 golden tests (`clean -update`) only | Small gap: add word-count + timing assertions later |
| Transfer observability (per-request proxy/status/bytes/complete) | ✅ `transfer.rs` observer | 🟡 `llm_calls` table only (LLM side) | Small gap: fetch-side counters in `run_history` |
| Metadata richness | title/desc/author/date/lang/site/image/favicon/word_count + domain_type | title + sidecar only | **Gap (stdlib, goquery)** |
| Selector synthesis + cache + per-field self-heal | ❌ (pays LLM every page) | ✅ our differentiator | **Keep + extend (see §5)** |
| Strict structured output + repair loop + cost ceiling + keyring | 🟡 `json_object` default, `json_schema` opt-in via env | ✅ native per-provider strict + 3-attempt repair + `--max-cost` | **Keep; we're ahead** |
| Durable crawl (SQLite frontier, resume, run history) | 🟡 JSON state file, in-memory visited | ✅ `crawl_state`/`dedup`/`run_history` | **Keep; we're ahead** |
| Plugin system (WASM sandbox + exec exporters + `build --with`) | ❌ | ✅ | Keep |
| Single static binary, pure-Go, Windows-first | ❌ (needs pkg-config/openssl/cmake/clang; Windows = MSVC+LLVM+NASM, x64-only ZIP) | ✅ `CGO_ENABLED=0`, 3-way cross-build | **Keep; competitive moat** |
| Prompt-mode extraction (schema OR free prompt) | ✅ `extract` takes either | ❌ (schema required) | **Gap (stdlib, trivial)** |
| Provider chain w/ fallback (openai→anthropic→gemini→ollama→…) | ✅ `ProviderChain` | ❌ (one `--provider`, fail hard) | Gap (stdlib, Phase C) |
| Cookies per request / locale header | ✅ | ❌ | Gap (stdlib, with scoping flags) |

---

## 2. Things we do differently but should do like webclaw

1. **Cleaning: trafilatura-only → trafilatura + recovery layer.** Their
   recall comes from post-scoring recovery (H1/hero/announcement/h2/footer/
   noscript/body-fallback), not from a better scorer. Keep
   `go-trafilatura` (our precision story + benchmark F1 0.960) and port the
   recovery steps into `clean/` behind the existing `Clean` signature.
   Add the `<200-word + body-has-more-text` retry and the `quality`
   taxonomy so we never cache garbage.
2. **Fetch: stock TLS → profiles + challenge handling, rod stays.**
   Stock `crypto/tls` has a distinctive JA3; that plus one static UA is why
   we'd get blocked where webclaw passes. Stdlib-first step now (header
   profiles, cookies, challenge-detect + cookie-warmup retry, Reddit/YT
   fast paths); byte-exact TLS impersonation later via an approved dep
   (§4). go-rod escalation stays for truly-JS pages — capability webclaw
   OSS notably lacks (they escalate to paid cloud instead).
3. **Extraction input: schema-only → schema OR prompt.** `extract` and
   `extract_structured` should accept a free prompt; implement as
   schema-less LLM call returning markdown/JSON text. Trivial, unlocks
   summarize too.
4. **Crawl scope: bool → filters.** Replace/augment `--same-host` with
   `--path-prefix`, `--include/--exclude` globs (with webclaw's ReDoS
   validation: max 4 `**`, max 1024 chars), `--allow-subdomains`,
   `--no-sitemap`. Keep SQLite frontier — strictly better than their JSON
   state file.
5. **MCP inputs: strict → coercing.** Accept `"3"`/`"true"` for numeric/
   bool params (webclaw `tools.rs` pattern), else Claude Desktop and friends
   error on our tools. Add a tool-catalog test like theirs.
6. **Sitemap handling: verify, then fix.** Confirm `.xml.gz` (raw-bytes +
   gunzip, never through lossy UTF-8), index recursion ≥5, and that
   truncated/partial results still return. Their `discover_within` budget
   pattern (return partial, don't `timeout`-drop) is the right shape.

## 3. Things we should NOT copy

- **Hosted cloud fallback / research backend.** Requires a SaaS we don't
  have. Our answer: precise `quality` errors + exit codes telling the user
  exactly what happened (blocked/challenge/login/empty), not a vendor key.
- **REST + Firecrawl-compat + SDKs.** MCP covers agents; REST doubles the
  surface for no local-first win. Revisit only on user demand.
- **QuickJS evaluation.** CGO-free QuickJS in Go means a new dep/runtime;
  static island parsing covers most of the value. Revisit if islands fail
  on real targets.
- **`stream_only` / frontier-cap machinery.** Their problem (all pages held
  in RAM) doesn't exist here: our writer streams records and the frontier
  lives in SQLite. Don't port complexity we don't need.

## 4. New-dependency decisions (approval required, none taken here)

| Need | Option (pure-Go, no CGO) | Stdlib-first alternative (do this now) |
|---|---|---|
| TLS impersonation | `github.com/refraction-networking/utls`-based transport (`North-web-dev/impersonate-http`, `aarock1234/mimic`) — researched 2026-09-17, both wrap `http.Client` | Header profiles + cookies + challenge retry (§5 A3) |
| PDF text | `ledongthuc/pdf`-class pure-Go extractor | Defer; detect + clean error first |
| Office docs (docx/xlsx/pptx) | `unidoc`-style libs (license/cost risk) | Defer |

## 5. Improvement spec (phased, smallest-diff order)

### Phase A — output + scoping + quality (stdlib, no API breaks)

- **A1. `clean`: `ToLLMText` + `ToText`.** Port `to_llm_text` sections:
  metadata header → body cleanup (strip bold/italic/decorative images,
  collapse logo runs, merge stat lines, strip CSS-class lines, dedup
  paragraphs/headings, drop pagination/comment links) → deduped `## Links`
  → gated `## Structured Data` (≤16 KB, drop `WebSite`/`WebPage` chrome,
  scrub `articleBody`/long-text dupes). `ToText` = `strip_markdown`.
  Wire `--format markdown|llm|text|json` through `scrape`, `crawl` writer,
  `scrape_url`. Acceptance: golden tests on 3 fixtures proving token
  reduction + link preservation; `json` returns the full extraction
  envelope (metadata/content/links/images/structured).
- **A2. Scoping flags.** `--include/--exclude/--only-main-content` on
  `scrape` (+ `ScrapeIn`), applied post-trafilatura via goquery
  (exclude wins; invalid selectors warn-and-skip, cap 100). Acceptance:
  hermetic tests: include-only returns scoped markdown; exclude strips
  nav; main-only on shell returns article.
- **A3. Fetch hardening.** Per-request `--cookies` + header profiles
  (`chrome`/`firefox` header bundles, still stock TLS); challenge-page
  detect (<15 KB + title/marker match) → homepage cookie-warmup → one
  retry; `Retry-After` already exists — verify. Acceptance: httptest
  challenge→warmup→success; cookie header asserted server-side.
- **A4. Quality gate.** New `clean/quality.go`: `Issue` enum
  (`empty/access-denied/unavailable/login-required`) + <200-word guard;
  `scrape`/`crawl` map it to errors/exit codes, crawl counts (not caches)
  affected pages. Acceptance: table tests incl. the "article mentioning
  Just a moment" negative.
- **A5. Metadata.** Extend `CleanedPage`: description/author/date/lang/
  site_name/image/favicon/word_count via goquery `metadata.rs` equivalent.
  Surfaced in `llm` header + `json` format. Acceptance: golden fixture.

### Phase B — zero-LLM structured data: vertical extractors (stdlib)

- **B1. `vertical/` package.** Registry: `INFO{name,label,desc,patterns}` +
  `matches(url)` + `extract(ctx, fetch, url) (map, error)`, stdlib JSON +
  goquery only. Auto-dispatch tries verticals before LLM; explicit
  `--vertical name` / `vertical_scrape` tool validates `matches` first
  (clear URL-mismatch error). Ship first 8 by ROI: `reddit` (old.reddit
  HTML + `.json` where open), `github_repo/issue/pr/release`
  (api.github.com), `pypi`/`npm`/`crates_io` (registry JSON),
  `hackernews` (Algolia API), `arxiv` (export API), `youtube`
  (`ytInitialPlayerResponse` + oembed), `shopify_product` (`/products/*.js`),
  generic `ecommerce_product` (JSON-LD/Product + microdata — mostly exists).
  Acceptance: httptest fixture per extractor (record real response shape
  once, replay); `list_extractors` test; dispatch-order test proving
  permissive matchers (shopify/ecommerce) never steal generic URLs
  (opt-in only, mirroring webclaw).
- **B2. Fast paths in `clean`.** Reddit/YT/LinkedIn-equivalent extraction
  + Next/Svelte island append when scored words <200 (unscoped only).
  Acceptance: fixture tests with island-bearing SPA shells.

### Phase C — agent surface: tools + commands (stdlib)

- **C1. New MCP tools (7):** `batch` (≤100 URLs, bounded concurrency,
  per-URL ok/error — reuse `scrape.Run`), `map` (sitemap list),
  `summarize` (`max_sentences`, prompt-mode LLM), `diff`
  (word-diff current vs `previous_snapshot`), `brand` (colors/fonts/logo
  from CSS/DOM), `list_extractors`, `vertical_scrape`. Mirror as CLI:
  `magpie batch|map|summarize|diff|brand|vertical`. `search` stays OUT
  until a key story exists — or ship thin Serper client (stdlib HTTP,
  10s timeout, `scrape` opt-in fan-out ≤5) if users ask; keep it behind
  `SERPER_API_KEY`, never a hosted dependency.
- **C2. Prompt-mode extract.** `extract`/`extract_structured` accept
  `prompt` xor `schema`. Acceptance: prompt path returns text, no
  validator involvement.
- **C3. MCP coercion + catalog test.** String→number/bool coercion for all
  numeric/bool inputs; `TestToolCatalog` asserting the exact tool list
  (catches accidental renames).
- **C4. Provider chain (optional, after C2).** `--provider auto` tries
  configured providers in order; only for prompt/summarize paths where
  strict-schema differences don't matter.

### Phase D — crawl + sitemap + security (stdlib)

- **D1. Scope filters.** `--path-prefix/--include/--exclude/--allow-subdomains/--no-sitemap`
  in `crawl.Options` + `qualify_link` (+ glob validation caps). Skip-list
  for binary extensions (pdf/png/zip/mp4/woff…). Acceptance: frontier unit
  tests (subdomain on/off, glob include/exclude, ext skip).
- **D2. Sitemap verification + `map`.** Prove/then-fix: gzip bodies via
  raw bytes + gunzip, index recursion ≥5, entity decoding, partial-on-budget
  results, `Sitemap:` comment/case tolerance. Expose `magpie map <site>`.
- **D3. SSRF guard.** `fetch` rejects non-public hosts (loopback,
  link-local, cloud metadata IP, non-http(s)) *before* dialing — including
  `file://` (keep `file://` only behind an explicit flag/test build tag so
  hermetic tests keep working). Acceptance: table test incl. DNS-rebind
  shape (resolve + re-check) if cheap, else IP-literal + hostname-blocklist.
- **D4. Fetch telemetry.** `run_history` gains fetch-side counters
  (pages, bytes, fetch_ms) alongside LLM usage; per-page elapsed already
  exists in pipeline — persist the aggregates.
- **D5. Single-proxy env (`GOMAGPIE_PROXY`) via `ProxyFromEnvironment`.**
  Pool rotation deferred (needs per-host pinning design).

### Phase E — dep-gated (only with approval)

- **E1.** TLS-impersonating transport (uTLS-based) behind `--browser chrome|firefox|random`.
- **E2.** PDF text extraction (detect `application/pdf` → pages→markdown,
  word counts flow into quality gate).
- **E3.** Office docs, QuickJS islands, REST/Firecrawl-compat — on demand only.

## 6. Explicitly out of scope

Hosted cloud fallback, hosted research jobs, login-wall/CAPTCHA/stealth
evasion in core (plugin-only per spec §11), REST parity, SDKs/installer
(until MCP tools land — then `npx`-style skill doc is cheap and worth it).

## 7. Test + DoD per phase

Hermetic-first (httptest/goldens/fake Extractor), same gates as today:
`go test ./...`, `go vet`, `gofmt -l` empty, `golangci-lint`, 3-way
`CGO_ENABLED=0` builds. New goldens: `testdata/{llm,vertical,quality}/`.
DoD for the whole spec: an agent with only MCP access can
scrape-bounded→batch→map→summarize→diff→brand→vertical without an LLM key
for vertical-eligible URLs, with `--format llm` halving prompt tokens vs
markdown on the fixture corpus, and every blocked page returning a typed
error instead of empty markdown.
