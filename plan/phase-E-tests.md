# Phase E — Testing: TLS Impersonation + PDF Extraction

**Scope:** `fetch/utls.go` (new: `resolveBrowser`/`browserClients`/`clientFor`/`browserDial`), `fetch/http.go` (`s.jar` promotion, `FetchRequest.Browser`, scheme split, effective-headers rule, browser-only `decodeBody`, warmup-retry threading, `redirectGuard` extraction, `proxyForHost` split, `guardedDialFunc` extraction), `fetch/ssrf.go` (`redirectGuard`), `fetch/detect.go` (`ScoreJSRequired` PDF guard), `fetch/decode.go` (`decodeBody`), `fetch/utls_smoke_test.go` (`-tags browser`), `clean/pdf.go` (new: `isPDFBody`/`IsPDFContentType`/`pdfToMarkdown`/`ErrPDFEncrypted`), `clean/clean.go` (`RawPage.ContentType`, PDF branch), `clean/quality.go` (`Classify` `isPDF` param + `pdf-empty` rule), `scrape/scrape.go` (`Options.Browser` + validation + ContentType plumbing), `crawl/crawl.go` (`Options.Browser`), `mcp/tools.go` (`ScrapeIn.Browser`/`CrawlIn.Browser`), `cli/{scrape,crawl,batch}.go` (`--browser` + CLI-side `fail(2)`)
**Key Pattern:** **Tiering replaces fakes for the TLS wrapper** — the impersonation transport is unfakeable at the TLS layer by design (hardcoded `utls.Config{ServerName}`, no cleartext support), so: behavior tests through the exported `Fetch` seam over `httptest` cleartext (routing/headers/cookies), internal tests (`package fetch`, `ssrf_internal_test.go` precedent) for the unexported security glue (`clientFor` split, `redirectGuard`, `decodeBody`, guarded-dial path), and a network-gated `-tags browser` smoke for the actual JA3/JA4 proof. PDF gets the real `ledongthuc/pdf` parser on **code-generated fixtures** (computed xref offsets — no binary blobs). Hit counters prove pre-dial rejection and pre-I/O exit 2, per Phase D precedent.
**Dependencies:** stdlib `testing`, `net/http/httptest`, `compress/gzip`, `compress/flate`, `bytes`, `net`, `sync/atomic`, `os`, `strings`, `errors` only — plus, in the smoke file only, `github.com/North-web-dev/impersonate-http` via the production import and `encoding/json` for the tls.peet.ws body. No test frameworks, no new test deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an operator scraping a bot-blocked site, I want `--browser chrome\|firefox\|random` to send a browser-class TLS fingerprint with matching headers, so that JA3/JA4 + h2 fingerprinting stops blocking me | `fetch/utls_smoke_test.go` (`-tags browser`): tls.peet.ws `ja3_hash` per profile; `fetch/utls_internal_test.go`: `resolveBrowser` table + random-once; `echoOrigin` header asserts for the cleartext fallback | smoke prints chrome `1d03c132ce29d0d7936acc72f12dd7a7`-class hash (constants recorded at impl); `random` resolves to the same profile for the process lifetime; `safari` → exit 2 |
| US-2 | As a security-conscious operator, I want every Phase D guarantee to hold on the browser path — pre-dial SSRF, redirect re-validation, proxy policy, 50 MB decoded cap — so that a new transport never widens egress | `fetch/utls_internal_test.go`: `redirectGuard` table + `browserDial` strict-reject + `proxyForHost` table + `decodeBody` cap; `fetch/utls_test.go`: routing + jar sharing | strict dial to private addr errors before TLS with origin hits == 0; redirect to private → `errors.Is(ErrPrivateAddress)`; decoded output capped at exactly `50<<20`; browser client has `CheckRedirect != nil` and the shared jar |
| US-3 | As a user scraping ordinary sites, I want `--browser` to never disturb cleartext traffic, cookies, or challenge-warmup, so that the default path is byte-identical when the flag is absent | `fetch/utls_test.go`: `Browser:""` requests unchanged (existing suite green); `Browser:"chrome"` on `http://` → stock client, our matching `HeaderProfiles["chrome"]` UA on the wire; cookies + warmup carry `Browser` | stock-client routing for all `http://` + `Browser:""`; echoOrigin sees `HeaderProfiles["chrome"]["User-Agent"]` for browser+cleartext; warmup fires for browser-only requests (gate widened) and retry carries the same `Browser` |
| US-4 | As a user handed a `report.pdf` URL, I want `magpie scrape` to return markdown with `## Page N` structure and a URL-stem title, so that PDFs flow through the same formats and outputs as HTML | `clean/pdf_test.go`: `pdfToMarkdown` tables on generated fixtures; `clean/clean_test.go`: `Clean` end-to-end (magic + Content-Type detection); `scrape/scrape_test.go`: PDF e2e incl. `--format json` | `## Page 1`…`## Page N` present in order; title = URL stem minus `.pdf`; `Render(json)` carries the same markdown; HTML pages' existing goldens untouched |
| US-5 | As an operator who must never cache garbage, I want PDF edge cases classified, not silently swallowed: scan/zero-text → `empty` (exit 8), encrypted → loud error, malformed → typed error never a panic, and PDF bytes never escalate to rod | `clean/quality_test.go`: pdf rule rows incl. the captcha-negative; `clean/pdf_test.go`: encrypted/malformed/blank tables; `fetch/fetch_test.go`: `ScoreJSRequired` PDF rows; CLI: zero-text PDF → exit 8 | `words==0 && isPDF` → `IssueEmpty`; `errors.Is(err, clean.ErrPDFEncrypted)` wrapping `pdf.ErrInvalidPassword`; `%PDF-`+garbage → error (recovered); rich PDF containing "captcha" → `IssueNone`; `ScoreJSRequired(pdfBytes)` → `(0, false)` |

---

## 1. Component Mock Strategy

Phase type: **integration/security-boundary + pure logic** (Phase D successor on the same fetch seam). Mock strategy in one sentence: **no fake for the TLS wrapper — it is the thing under test and cannot be faked at its own layer, so behavior tests drive the exported `Fetch` seam over cleartext `httptest`, internal tests lock the unexported glue (the `ssrf_internal_test.go` precedent), a `-tags browser` smoke proves the real handshake, and the PDF path uses the real parser on generated fixtures; hit counters prove every "before" claim (pre-dial, pre-I/O, pre-escalation).**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `resolveBrowser` | Internal unit (package `fetch`) — pure table | `chrome`→Chrome profile, `firefox`→Firefox, `""`→not-browser (ok=false); `safari`/`edge`/`ios`/`chrome_android`/garbage → false (spec surface is exactly 3 names); `random` → ok=true AND two calls return the **same** name (per-process `sync.OnceValue` pin — a per-call re-roll fails this) | US-1 |
| `browserClients` cache + `clientFor` split | Internal unit: fetcher built via `NewStaticFetcherWithOptions`, requests probed for client identity (pointer compare via a `clientFor`-shaped accessor or map inspection — prefer asserting *behavioral* identity: browser client `Timeout==30s`, `Jar==stock jar`, `CheckRedirect!=nil`) | `Browser:""` + https-URL string → stock client identity; `Browser:"chrome"` + `http://` URL → **stock** client (the load-bearing cleartext rule); `Browser:"chrome"` + `https://` URL → cached browser client; second call returns the SAME pointer (cache hit); browser client shares `s.jar` (cookie warmup parity) | US-2, US-3 |
| Routing behavior (exported seam) | `echoOrigin` (existing, echoes request headers) + `fetchBody`; `Browser:"chrome"` against cleartext | Succeeds (would TLS-handshake-port-80 and die if misrouted); wire UA == `fetch.HeaderProfiles["chrome"]["User-Agent"]` (cleartext+browser carries OUR matching profile — the wrapper is never on this path); `Browser:""` body/headers byte-identical to today (existing suite IS this test) | US-3 |
| `redirectGuard` (extracted) | Internal unit table (req + via slices, no server) + existing `TestStaticRedirectCap` stays green (behavior proof on the stock client) | 11th hop → "10 redirects" error; private hop → `errors.Is(ErrPrivateAddress)` naming the host; public hop → nil; non-http scheme hop → rejected | US-2 |
| `browserDial` guarded path | Internal unit: call the returned `DialFunc` directly against an `httptest` addr (127.0.0.1) with strict `SSRFOptions{}` + atomic hit counter | Error (private peer) AND zero origin hits — the peer check fires before any TLS/HTTP byte; relaxed `AllowPrivate` → dial succeeds (TCP connect to the test origin is fine) | US-2 |
| `proxyForHost` (extracted) | Internal unit table with `t.Setenv` | `GOMAGPIE_PROXY` set → parsed URL; garbage → loud error naming `GOMAGPIE_PROXY`; `NO_PROXY` exact + dot-suffix → nil; unset → `http.ProxyFromEnvironment` shape (set `HTTP_PROXY`, expect it) — locks the extraction against behavior drift from `proxyFunc` | US-2 |
| `decodeBody` | Internal unit — pure table on bytes | gzip/deflate/br/zstd each round-trip; `""`/`identity` → passthrough; unknown token → error; chained `"gzip, br"` decodes in order; **exact cap**: 60 MB of gzipped zeros → exactly `50<<20` bytes out (hardcode the constant + drift comment, Phase D bomb precedent); corrupt stream → error, never panic | US-2 |
| Warmup retry + `Browser` threading | `newChallengeOrigin` + `challengeHits` (existing) + scrape-level fake Fetcher recording `FetchRequest` fields | Warmup gate widened: `Browser:"chrome"` (Profile `""`) on a challenge page → homepage hit + retry; retry + warmup requests both carry `Browser:"chrome"` (assert via recorded `FetchRequest`s at the scrape fake, or extend `challengeHits` with a browser log — pick ONE, note it); existing profile-warmup tests untouched and green | US-3 |
| `ScoreJSRequired` PDF guard | Append to the existing table harness (fetch_test.go:141 area) | `%PDF-1.5\n…` body → `(0, false)` regardless of script spam appended after the magic; HTML body unchanged rows still pass | US-5 |
| `isPDFBody` / `IsPDFContentType` | Pure tables | Magic: `%PDF-1.4` prefix true; ` `%PDF` without dash`/empty/HTML → false; CT: `application/pdf`, `application/pdf; charset=binary` true; `text/html`, `APPLICATION/PDF` (case) decided per implementation and pinned in a comment | US-4 |
| `pdfToMarkdown` | Real `ledongthuc/pdf` on `buildPDF`-generated fixtures (§3 full source) — no mock, no fixture files | 1-page → `## Page 1` + text; 2-page → `## Page 1` then `## Page 2`, in order, blank pages skipped; title = `report` for `…/report.pdf`, `document` for stemless; encrypted fixture → `errors.Is(ErrPDFEncrypted)` AND `errors.Is(pdf.ErrInvalidPassword)` through the wrap; truncated `%PDF-1.4` + garbage → error (not panic — the recover belt); texts containing `\n` preserved through the page builder | US-4, US-5 |
| `Classify` pdf rules | Append to the existing table harness (quality_test.go) — **call sites updated for the `isPDF` param** (flagged exception, §5) | `words==0, isPDF, 200` → `IssueEmpty`; `words==0, isPDF=false, 200` → `IssueNone` (rule must not leak to HTML); rich PDF text containing "captcha"/"just a moment" + `isPDF` → `IssueNone` (the binary-noise negative); `isPDF` + 404 → still `IssueEmpty` (status rules outrank); `isPDF` + 503 → `IssueUnavailable` (ordering) | US-5 |
| `Clean` PDF branch | `clean_test.go` append: `RawPage{HTML: pdfBytes, ContentType: "application/pdf", …}` and magic-only variant | `CleanedPage.Markdown` contains `## Page 1`; `Title=="report"`; `Quality` set per Classify; HTML-with-pdf-CT edge: a real HTML page mislabeled `application/pdf` → parses as PDF → likely error (pin actual behavior + comment); existing HTML goldens untouched | US-4 |
| `scrape.Run` Browser + PDF e2e | `openScrapeDB`/`fakeDeps` (existing) + a fake Fetcher recording `FetchRequest` + `scrapeOrigin` serving generated PDF bytes with `Content-Type: application/pdf` | `Options.Browser:"safari"` → validation error BEFORE any fetch (recorder hits == 0); `Browser:"chrome"` → recorder saw `FetchRequest.Browser=="chrome"`; PDF URL → `Result.Markdown` contains `## Page 1`, `Result.Title=="report"`; `Render(json)` path returns the markdown (existing Render tests cover formats — one presence assert here) | US-1, US-4 |
| crawl `Options.Browser` + skip-list | Mini-crawl over `newSiteOrigin` (existing) with `Browser:"chrome"`; existing scope tests cover `.pdf` skip | Crawl completes over cleartext with browser set (routing sanity at the crawl layer); origin saw our chrome UA; `.pdf` links still never enqueued (existing `TestLinks_`/scope rows green — no new test if one already asserts it; add one only if absent) | US-3 |
| MCP `ScrapeIn.Browser` / `CrawlIn.Browser` | `dialInMemory`/`callTool` (existing) with string args | `scrape_url` with `"browser":"chrome"` → no validation error; `"browser":"safari"` → tool error naming allowed values; `crawl_site` same; `TestToolCatalog` STILL exactly 11 names (fields ≠ tools — do not touch the count) | US-1 |
| CLI `--browser` | `resetGlobals`/`captureOutput`/`codeOf` (existing) + closed-port origin + hit counter | Help text lists `--browser` on scrape/crawl/batch; `--browser safari` → exit **2** (the CLI-side `fail(2)`, not Run's exit-1 default) with zero dial attempts (closed port + counter == 0 proves pre-I/O); `--browser chrome` on a `file://` fixture site → exit 0 markdown (cleartext routing e2e) | US-1, US-3 |
| Smoke: real handshake | `fetch/utls_smoke_test.go`, `//go:build browser` (rod-suite precedent) — real network, real wrapper | GET `https://tls.peet.ws/api/all` with chrome then firefox: `ja3_hash` equals the recorded constant (chrome `1d03c132ce29d0d7936acc72f12dd7a7`, firefox `b5001237acdf006056b409cc433726b0` — verify at impl; Auto-template drift → update constant IN THE SAME COMMIT with a note); body parses as JSON with non-empty `user_agent` (proves the decode path end-to-end through the real wrapper) | US-1 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (default `go test ./...`) | Generated PDF fixtures (in-code), localhost `httptest` (echo/challenge/origins), internal glue tests, temp-dir SQLite — **no TLS handshake, no external network, no browser** (the wrapper cannot do cleartext, and nothing in the default suite needs it to) | <60s | Every push; the only default gate |
| Smoke (`go test -tags browser ./fetch/`) | Real network + the real impersonate transport against tls.peet.ws | ~5s | Manual/pre-release and network-enabled CI job; alongside the existing rod smoke |
| Manual (not a test file) | Built binary: `magpie scrape <known-blocked-site> --browser chrome`; a real PDF URL; `--format json` on both | minutes | Pre-release eyeball: the JA3 smoke proves the fingerprint; only a real blocked page proves the outcome |

No golden tier (PDF fixtures are generated in-code; no `testdata/` additions — `git status --porcelain testdata/` must stay empty). No new integration flag: Phase A–D precedent is that localhost `httptest` IS the hermetic stand-in; the only genuinely-external behavior (the TLS handshake) is fenced in the `-tags browser` file where `rod_smoke_test.go` already lives.

---

## 3. Fake / Mock Implementations

**No new fakes.** The two heavy dependencies are handled by structure, not stubs: the impersonate transport is the system under test (faking it would test nothing — tiering does the isolation), and `ledongthuc/pdf` is deterministic, stdlib-only, and fast enough to run for real on generated input. What the suite needs instead is **one generator** (the PDF builder — the only non-trivial new helper, full source below) and small in-test probes otherwise.

### `buildPDF` — generates minimal valid PDFs with computed xref offsets (replaces fixture files)

```go
// clean/pdf_test.go (package clean_test) — the suite's one load-bearing helper.
// Builds a valid PDF (header, object offsets, xref table, startxref, %%EOF) with
// one page per text. Offsets are COMPUTED, never hardcoded — rsc/pdf-lineage
// validation (%PDF-1. header + %%EOF tail + startxref + exact /Length) passes
// because every byte is accounted for. Texts must avoid ( ) \ — content-stream
// escaping is deliberately out of scope for tests.
func buildPDF(t *testing.T, pageTexts ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var offsets []int
	writeObj := func(s string) int {
		offsets = append(offsets, buf.Len())
		n, _ := buf.WriteString(s) //nolint:errcheck // bytes.Buffer never errors
		return n
	}
	buf.WriteString("%PDF-1.4\n")
	kids := make([]string, len(pageTexts))
	for i := range pageTexts {
		kids[i] = fmt.Sprintf("%d 0 R", 3+i*2)
	}
	writeObj("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")
	writeObj(fmt.Sprintf("2 0 obj << /Type /Pages /Kids [%s] /Count %d >> endobj\n",
		strings.Join(kids, " "), len(pageTexts)))
	for i, txt := range pageTexts {
		content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", txt)
		pg := 3 + i*2
		writeObj(fmt.Sprintf("%d 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
			"/Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >> endobj\n", pg, pg+1, pg+2))
		writeObj(fmt.Sprintf("%d 0 obj << /Length %d >>\nstream\n%sendstream\nendobj\n",
			pg+1, len(content), content))
		writeObj(fmt.Sprintf("%d 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj\n", pg+2))
	}
	xref := buf.Len() // byte offset of the xref table
	total := len(offsets) + 1
	fmt.Fprintf(&buf, "xref\n0 %d\n", total)
	buf.WriteString("0000000000 65535 f \n") // free entry — exactly 20 bytes
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total, xref)
	return buf.Bytes()
}
```

**Matches real call:** `pdf.NewReader(bytes.NewReader(buildPDF(t, "Hello page")), n)` — the same bytes `clean.pdfToMarkdown` will receive in production. A wrong offset/Length fails loudly inside the parser (that failure is the generator's own test: `TestBuildPDF_SelfCheck` round-trips one page through `pdf.NewReader` + `GetPlainText` BEFORE any clean-level test uses it, so generator bugs are never misdiagnosed as extractor bugs).

### In-test helpers (small, at point of use)

```go
// fetch/utls_test.go (package fetch_test)
// browserFetch(t, url, browser) — NewStaticFetcherWithOptions (explicit options, never the
//   bare constructor for NEW security tests) + FetchRequest{URL, Browser} + fetchBody-shape.
// recordingFetcher — scrape-side fake Fetcher (existing seam vertical.Fetcher) that appends
//   every FetchRequest to a slice; asserts Browser/Profile threading at scrape level.

// fetch/utls_internal_test.go (package fetch — ssrf_internal_test.go precedent)
// browserFetcherInternal(t, o SSRFOptions) *StaticFetcher — same explicit-options rule,
//   internal so clientFor/redirectGuard/decodeBody/browserDial/proxyForHost are reachable.
// hitTCP(t, hits *atomic.Int64) (addr string) — tiny TCP listener counting connections;
//   browserDial's guarded path is asserted against its 127.0.0.1 addr (reject) with hits==0.
```

### Reused verbatim (read first, never modify)

- `newFakeOrigin` (fetch/fetch_test.go:16), `newChallengeOrigin` + `challengeHits` + `echoOrigin` + `fetchBody` (fetch/profiles_test.go:22,50,59), `TestStaticRedirectCap`/`TestStaticGzip` patterns (fetch_test.go)
- `ScoreJSRequired` table harness (fetch/fetch_test.go:141 area — append PDF rows to the pattern, don't restructure)
- `openScrapeDB`, `fakeDeps`, `scrapeOrigin`, `fakeExtractor` (scrape/scrape_test.go:61,71,94)
- `openCrawlDB`, `newSiteOrigin`, `TestLinks_*` scope rows (crawl/crawl_test.go)
- `openMCPDB`, `dialInMemory`, `callTool`, `decodeOut`, `TestToolCatalog` (mcp/agent_test.go:153ff)
- `resetGlobals`, `captureOutput` (cli/cmd_test.go:21,26), `codeOf` (cli/scrape_test.go:144), `writeFileSite` (cli)
- `Classify` table harness (clean/quality_test.go — gains the `isPDF` field on its case struct; the ONE flagged edit, below)

---

## 4. Test File List

```
gomagpie/
├── fetch/
│   ├── utls_test.go               # NEW (package fetch_test): cleartext routing + wire-UA + warmup-with-browser + cookies-through-browser
│   ├── utls_internal_test.go      # NEW (package fetch): resolveBrowser/random-once, clientFor split + cache + jar, redirectGuard table, browserDial guarded-reject (hits==0), proxyForHost table, decodeBody table incl. exact cap
│   ├── utls_smoke_test.go         # NEW (//go:build browser): JA3 constants vs tls.peet.ws + JSON-parseable body (decode sanity through the real wrapper)
│   ├── detect_test.go or fetch_test.go  # APPEND: ScoreJSRequired PDF rows (magic-first, script-spam-after-magic still (0,false)) — whichever file holds the existing table
│   └── fetch_test.go              # APPEND only if detect rows land here; existing tests untouched
├── clean/
│   ├── pdf_test.go                # NEW: TestBuildPDF_SelfCheck + buildPDF (§3) + pdfToMarkdown tables (1p/2p/blank-skip/title/encrypted/truncated) + isPDFBody/IsPDFContentType tables
│   ├── quality_test.go            # EDIT ONE LINE (Classify call at :57 gains isPDF — flagged exception) + APPEND pdf rule rows (pdf-empty, captcha-negative, ordering vs 404/503)
│   └── clean_test.go              # APPEND: Clean end-to-end PDF (CT-detected + magic-detected), title stem, HTML-goldens untouched
├── scrape/
│   └── scrape_test.go             # APPEND: Browser validation pre-I/O (recorder hits==0), Browser→FetchRequest threading, PDF e2e (markdown + title + json Render presence)
├── crawl/
│   └── crawl_test.go              # APPEND: Browser-over-cleartext mini-crawl (routing + UA seen); .pdf skip assert only if no existing row covers it
├── mcp/
│   └── agent_test.go              # APPEND: ScrapeIn/CrawlIn browser passthrough + invalid-name tool error; TestToolCatalog untouched (still 11)
├── cli/
│   └── cmd_test.go                # APPEND: --browser help ×3 commands, safari → exit 2 pre-I/O (counter==0), chrome-on-file e2e exit 0
└── plan/phase-E-tests.md          # this file
```

Every deliverable in `plan/phase-E.md` §5 maps: `fetch/utls.go`→utls_internal/utls_test; `fetch/decode.go`→utls_internal table; `fetch/http.go`→utls_test (routing/headers/warmup) + utls_internal (jar, split extractions); `fetch/ssrf.go`→utls_internal redirectGuard (+ existing ssrf tests prove no drift); `fetch/detect.go`→fetch/detect rows; `fetch/utls_smoke_test.go`→smoke; `clean/pdf.go`→pdf_test; `clean/clean.go`→clean_test; `clean/quality.go`→quality_test rows; `scrape/scrape.go`→scrape_test; `crawl/crawl.go`→crawl_test; `mcp/tools.go`→agent_test; `cli/*`→cmd_test; `README.md`→no tests (docs).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

Per Phase A–D precedent: per-package test files, hermetic default suite, no integration-tier flag. **Append-only on existing files, with exactly ONE flagged exception:** `clean/quality_test.go:57` — `clean.Classify(clean.CleanedPage{Markdown: c.md}, c.status, c.body)` gains the `isPDF` argument (add `isPDF: false` to the case struct + pass it). This is the implementation's signature change surfacing in tests, same category as Phase D's `ExtractLinks` call sites; every OTHER existing test must run unmodified.

Package conventions (verified this session): `fetch`/`clean`/`scrape`/`mcp` tests are **external** (`package *_test`) — the ONE new internal file is `fetch/utls_internal_test.go` (`package fetch`), justified exactly like `ssrf_internal_test.go`: the unexported security glue (`clientFor`, `redirectGuard`, `decodeBody`, `browserDial`, `proxyForHost`, `resolveBrowser`) is where E's security decisions live, and exporting test-only API for them would widen production surface for tests' convenience. `cli` tests are internal (`package cli`).

```go
// fetch/utls_internal_test.go — package fetch; ADD: browserFetcherInternal + hitTCP +
// the six internal tables. Compile locks:
//   var _ = (*StaticFetcher).clientFor      // fails compile if the split method vanishes
//   (mirror any named type the impl introduces — e.g. DialFunc aliases — with var _ assignments)

// fetch/utls_test.go — package fetch_test; ADD: browserFetch + (if chosen) recordingFetcher.
// REUSE echoOrigin/fetchBody/newChallengeOrigin AS-IS.

// clean/pdf_test.go — package clean_test; ADD: buildPDF (§3 verbatim) + TestBuildPDF_SelfCheck
// (round-trip one page through the real parser FIRST — generator bugs must never masquerade
// as extractor bugs) + the pdfToMarkdown/detection tables.

// clean/quality_test.go — EDIT the :57 call (isPDF) + extend the case struct + APPEND rows.

// scrape/scrape_test.go — APPEND: recordingFetcher (fake vertical.Fetcher capturing
// FetchRequest) + the three test funcs. REUSE openScrapeDB/fakeDeps.
```

Scope rationale: everything function-scoped (`t.TempDir()`, per-test `httptest`, per-test atomics, `t.Setenv` for every env read in `proxyForHost`/hatch-adjacent code) — parallel-safe. The only process-global the suite introduces is `resolveBrowser`'s `sync.OnceValue` for `random` — pinned AS the contract (random-once test), never reset.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| No fake for the impersonation transport | Tier instead: cleartext behavior tests (default) + internal glue tests (default) + real-handshake smoke (`-tags browser`) | A fake RoundTripper would verify our wiring while proving nothing about uTLS; the wrapper also can't serve cleartext, so the default suite *can't* reach it — the tier boundary is forced by the dependency, and the smoke file (rod precedent) is where the real proof lives |
| Internal test file for unexported glue | `fetch/utls_internal_test.go`, `package fetch`, citing `ssrf_internal_test.go` in its header | `clientFor`'s scheme split, `redirectGuard`'s table, and `decodeBody`'s cap are the security decisions; exporting them for tests widens production API. One new internal file, same justification pattern the repo already accepted |
| `TestBuildPDF_SelfCheck` gates all PDF tests | The generator round-trips through the real parser before any clean-level test runs | A malformed generated fixture would fail inside `pdfToMarkdown` and be misread as an extractor bug; the self-check localizes the failure to the helper |
| Generated fixtures, zero `testdata/` additions | `buildPDF` computes xref offsets; `git status --porcelain testdata/` must stay empty | Binary blobs in git rot silently and can't express the encrypted/truncated variants; code-generated input is parametric (1p/2p/Np, any text) and self-documenting |
| Classify signature change = ONE flagged edit | `quality_test.go:57` gains `isPDF` (case struct + pass-through); everything else append-only | Same contract as Phase D's `ExtractLinks` call sites — the implementation's signature change legitimately touches its own test harness; silent edits elsewhere are banned |
| Exact decoded cap: `len(out) == 50<<20` | Hardcode the constant + drift comment (Phase D bomb precedent) | `<=` passes if decode simply truncated early for the wrong reason; exactness proves the LimitReader sat on the *decoded* stream |
| Random-once is pinned, not worked around | Two `resolveBrowser("random")` calls → identical name | The per-process choice is the documented contract (`ponytail:` note in the plan); a test that tolerates re-rolling would bless per-request churn — a fingerprinting tell |
| Hit counters for every "before" claim | Pre-dial reject, pre-I/O exit 2, pre-escalation all assert a counter == 0 alongside the error | Phase D's lesson: error text can't distinguish "rejected before" from "failed after"; the counter is the proof (US-2, US-5) |
| Warmup gate widened for `Browser` — pinned as DECIDED | Tests assert `Browser:"chrome"` (Profile `""`) triggers warmup+retry; `challengeHits` records the field | Browser-only requests are THE blocked-page case; leaving the gate on `Profile != ""` would make `--browser` useless against exactly the sites it exists for (amended into phase-E.md §3/E.4) |
| Cleartext+browser carries OUR HeaderProfiles | `echoOrigin` asserts UA == `HeaderProfiles[resolvedBrowser]["User-Agent"]` | The wrapper (and its matching headers) is never on the `http://` path; sending NO UA would be a worse fingerprint than our stock one (amended into phase-E.md §3) |
| JA3 constants are expected-drift artifacts | Smoke compares against recorded constants; drift → update constant in the same commit, never `t.Skip` | uTLS `*_Auto` intentionally tracks browsers; skipping would rot the smoke into decoration, while hard-fail-with-update keeps it a living fingerprint check |
| MCP: fields never touch the tool count | New-field tests ignore catalog size; `TestToolCatalog` (11) runs untouched | Phase C's stale-count lesson — double-pinning counts rots on the next tool addition |
| `.pdf` crawl skip: reuse, don't duplicate | Existing scope rows assert the skip; add a crawl-side row only if none exists | The skip-list is Phase D behavior, unchanged by E; a duplicate assert would double-pin a constant |
| Smoke body JSON-parse assert | `json.Valid`/decode on the tls.peet.ws response in the smoke | Proves the AE/decode story end-to-end through the real wrapper (h2 sends br/zstd-encoded → our decoder ran) — the one thing the unit table can't witness |

---

## 7. Example Test Case

```go
// fetch/utls_internal_test.go
package fetch

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestClientFor_SchemeSplit is the load-bearing routing test: the impersonation
// transport TLS-handshakes EVERYTHING it sees, so a cleartext URL that reaches it
// dies on a port-80 handshake. The split is the feature — this pins it.
func TestClientFor_SchemeSplit(t *testing.T) {
	f, err := NewStaticFetcherWithOptions(SSRFOptions{AllowPrivate: true})
	if err != nil {
		t.Fatalf("fetcher: %v", err)
	}
	httpsURL := "https://blocked.example/article"
	httpURL := "http://plain.example/article"

	stock1 := f.clientFor(FetchRequest{URL: httpsURL})
	stock2 := f.clientFor(FetchRequest{URL: httpURL, Browser: "chrome"}) // cleartext NEVER impersonates
	browser1 := f.clientFor(FetchRequest{URL: httpsURL, Browser: "chrome"})
	browser2 := f.clientFor(FetchRequest{URL: httpsURL, Browser: "chrome"}) // cache hit

	if stock1 != stock2 {
		t.Error("cleartext requests must take the stock client regardless of Browser")
	}
	if browser1 == stock1 {
		t.Error("https + Browser must take a browser client, got the stock one")
	}
	if browser1 != browser2 {
		t.Error("browser clients must be cached per resolved profile (conn reuse)")
	}
	if browser1.Jar == nil || browser1.Jar != f.jar {
		t.Error("browser client must share the fetcher cookiejar (warmup parity)")
	}
	if browser1.Timeout != 30*time.Second {
		t.Errorf("browser client timeout = %v, want 30s (stock parity)", browser1.Timeout)
	}
	if browser1.CheckRedirect == nil {
		t.Fatal("browser client CheckRedirect is nil — redirects would bypass the SSRF guard")
	}
}

// TestRedirectGuard_Table pins the extracted guard both clients share.
func TestRedirectGuard_Table(t *testing.T) {
	guard := redirectGuard(SSRFOptions{})
	priv := "http://169.254.169.254/latest/meta-data/"
	pub := "http://93.184.216.34/"
	mk := func(raw string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, raw, nil) //nolint:errcheck // fixed URLs
		return req
	}
	if err := guard(mk(priv), []*http.Request{mk("http://93.184.216.34/")}); err == nil {
		t.Error("redirect to link-local metadata must abort")
	} else if !isPrivateErr(err) { // errors.Is(err, ErrPrivateAddress) — helper in this file
		t.Errorf("guard err = %v, want ErrPrivateAddress wrap", err)
	}
	if err := guard(mk(pub), make([]*http.Request, 0, 10)); err != nil {
		t.Errorf("public redirect rejected: %v", err)
	}
	eleven := make([]*http.Request, 11)
	if err := guard(mk(pub), eleven); err == nil {
		t.Error("11 hops must trip the redirect cap")
	}
}

// TestDecodeBody_CapExact proves the bomb guard sits on the DECODED stream:
// 60 MB of gzip zeros must come back as exactly the cap, not a byte less/more.
func TestDecodeBody_CapExact(t *testing.T) {
	var raw bytes.Buffer
	zw := gzip.NewWriter(&raw)
	_, _ = zw.Write(bytes.Repeat([]byte{0}, 60<<20)) //nolint:errcheck // in-memory
	_ = zw.Close()
	hdr := http.Header{}
	hdr.Set("Content-Encoding", "gzip")
	got, err := decodeBody(raw.Bytes(), hdr)
	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if len(got) != 50<<20 {
		t.Fatalf("decoded %d bytes, want exactly %d (cap must truncate, hard-coded with intent)", len(got), 50<<20)
	}
}
```

Notes for the executor: `f.jar`/`clientFor`/`redirectGuard`/`decodeBody` are the implementation's names per phase-E §3/E.4 — if the implementation names them differently, rename HERE (tests follow the plan's frozen API; flag any divergence rather than adapting silently). `isPrivateErr` is a 2-line `errors.Is` helper defined once in this file. The `browser1.Jar != f.jar` compare requires the jar promotion from E.4.1 — if it fails to compile, the implementation skipped the promotion, which is a plan violation, not a test bug.

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase E of `gomagpie` — TLS impersonation (`--browser`) + PDF extraction. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-E.md` (full design AS AMENDED — jar promotion, warmup gate widened to `Profile == "" && Browser == ""`, cleartext+browser carries our HeaderProfiles, `Classify` gains `isPDF bool`), `plan/phase-E-tests.md` (this suite's contract — §3 generator, §6 decisions, §7 load-bearing tests), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test. Production code may or may not exist yet — write tests against the frozen names in phase-E §§3–4 and this plan's §1/§3/§7; if a name diverges, rename the TEST to the implementation ONLY when the behavior matches the plan, else flag the gap. Package conventions: new files `fetch/utls_test.go` + `fetch/utls_smoke_test.go` are `package fetch_test`; `fetch/utls_internal_test.go` is `package fetch` (ssrf_internal_test.go precedent — say so in its header comment); `clean/pdf_test.go` is `package clean_test`; `cli` stays internal.

### What This Project Is
Go 1.26 CLI web scraper (`magpie`, module `gomagpie`): fetch → clean → extract. Phase E adds a uTLS-impersonating fetch path (`--browser chrome|firefox|random`, byte-exact JA3/JA4 + h2 fingerprint via `North-web-dev/impersonate-http`) that MUST preserve every Phase D guarantee (pre-dial SSRF, redirect re-validation, proxy policy, 50 MB cap — now on the decoded stream), and PDF extraction (`ledongthuc/pdf` → `## Page N` markdown → existing quality gate). The impersonation transport cannot do cleartext and cannot be faked at its own layer — hence the three-tier structure. Tests are hermetic by default: no TLS handshake, no external network, no browser in `go test ./...`.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | browser-class fingerprint behind `--browser` | smoke ja3_hash + resolveBrowser/random-once tables + CLI safari→2 | recorded constants match; random stable per process; safari exits 2 |
| US-2 | Phase D guarantees hold on the browser path | §7 verbatim + browserDial reject + proxyForHost + decodeBody cap + redirectGuard | private dial → error + hits==0; redirect→private → sentinel; decoded cap == `50<<20` exact; browser client has CheckRedirect + shared jar |
| US-3 | cleartext/cookies/warmup untouched by `--browser` | routing + wire-UA + warmup-with-browser tests; existing suite green | http://+browser → stock client with our chrome UA; warmup fires browser-only; `Browser:""` byte-identical today |
| US-4 | PDFs scrape to paged markdown, same formats as HTML | buildPDF tables + Clean e2e + scrape PDF e2e | `## Page 1..N` in order; title = stem; json Render carries it; HTML goldens untouched |
| US-5 | PDF edges classified, never swallowed | pdf rule rows + encrypted/malformed tables + ScoreJSRequired rows + CLI exit 8 | 0-words→IssueEmpty; ErrPDFEncrypted wraps pdf.ErrInvalidPassword; %PDF+garbage → error not panic; captcha-word rich PDF → IssueNone; ScoreJSRequired(pdf)→(0,false) |

### Why There Are No New Fakes
- The impersonation transport is the system under test; a fake RoundTripper would verify wiring while proving nothing about TLS. Isolation is structural: cleartext behavior tests (default suite) + internal glue tests + `-tags browser` real-handshake smoke (the ONLY file allowed to touch the network, mirroring rod_smoke_test.go).
- `ledongthuc/pdf` is deterministic, stdlib-only, and fast — tested for real on `buildPDF`-generated bytes (§3 full source; self-check round-trip FIRST so generator bugs never masquerade as extractor bugs).
- Everything else reuses existing helpers: `echoOrigin`/`fetchBody`/`newChallengeOrigin`/`challengeHits` (fetch/profiles_test.go), `newFakeOrigin` (fetch_test.go:16), `openScrapeDB`/`fakeDeps`/`scrapeOrigin` (scrape_test.go), `openCrawlDB`/`newSiteOrigin` (crawl_test.go), `dialInMemory`/`callTool`/`TestToolCatalog` (mcp/agent_test.go:153ff), `resetGlobals`/`captureOutput` (cli/cmd_test.go), `codeOf` (cli/scrape_test.go:144).

### What NOT to Test
- Don't test uTLS/impersonate-http internals (ClientHello byte layout, h2 framing) — the smoke's ja3_hash IS our coverage of the wrapper; the unit suite covers only OUR glue.
- Don't test ledongthuc/pdf beyond what `pdfToMarkdown` exercises (its own repo's job); don't hand-build encrypted-PDF fixtures if the /Encrypt dict proves disproportionate — cover the sentinel via the wrapper's error mapping and say so in a comment (honest-gap rule).
- Don't touch `TestToolCatalog`'s count (fields ≠ tools; still 11) and don't write new MCP coercion harnesses — plain string fields pass through.
- Don't duplicate the `.pdf` crawl skip (Phase D scope rows already pin it — add a row only if genuinely absent).
- Don't reset `resolveBrowser`'s OnceValue between tests — random-once is the pinned contract.
- Don't add `testdata/` files (generated fixtures only; `git status --porcelain testdata/` must stay empty) and don't create a shared testutil package (10-line helpers at point of use; Phase D rule).
- Don't modify existing test funcs — the ONE flagged exception is `clean/quality_test.go:57` (Classify gains `isPDF`; extend the case struct, pass it through). Anything else failing to compile against the new signatures is a PLAN GAP — flag it, don't refactor the old test.
- Don't test `README.md` or help-text prose beyond the `--browser` presence greps.

### Critical: The One Load-Bearing Helper

`buildPDF` — full source in test-plan §3; copy verbatim into `clean/pdf_test.go`. Computes xref offsets (never hardcoded), forbids `( ) \` in page texts (content-stream escaping out of scope), and MUST be proven by `TestBuildPDF_SelfCheck` (1-page round-trip through `pdf.NewReader` + `Page(1).GetPlainText(nil)`) before any clean-level test consumes it. For the encrypted-path test: attempt a minimal `/Encrypt` dict ONLY if it falls out naturally; otherwise test `pdfToMarkdown`'s sentinel mapping by feeding the error shape the parser produces for `NewReaderEncrypted(f, size, nil)` on an encrypted doc — if even that needs a fixture you can't generate, mark the row `t.Skip("encrypted fixture: honest gap — see phase-E-tests §6")` and say why.

Also copy §7's three tests (`TestClientFor_SchemeSplit`, `TestRedirectGuard_Table`, `TestDecodeBody_CapExact`) verbatim into `fetch/utls_internal_test.go` — they compile-lock the scheme split, the shared guard, and the decoded cap.

### Test Files to Create

```
fetch/utls_test.go           # NEW (~6): cleartext routing + wire-UA (echoOrigin) + warmup browser-only
                             #   + cookies via browser fetch + retry-carries-Browser + Browser:"" unchanged
fetch/utls_internal_test.go  # NEW (~9): §7 verbatim ×3 + resolveBrowser table + random-once +
                             #   browserDial guarded reject (hitTCP, hits==0) + proxyForHost table (t.Setenv)
fetch/utls_smoke_test.go     # NEW (//go:build browser, ~2): ja3_hash chrome+firefox vs recorded constants
                             #   + body json.Valid (decode sanity through the real wrapper)
fetch/fetch_test.go          # APPEND (~1): ScoreJSRequired PDF rows in the existing table style
clean/pdf_test.go            # NEW (~8): TestBuildPDF_SelfCheck + buildPDF + pdfToMarkdown (1p/2p/
                             #   blank-skip/title-stem/truncated-recover/encrypted-sentinel) + detection tables
clean/quality_test.go        # EDIT :57 call site (isPDF) + APPEND (~4): pdf-empty, HTML-no-leak,
                             #   captcha-negative, ordering (404/503 outrank)
clean/clean_test.go          # APPEND (~2): Clean end-to-end PDF (CT + magic variants), title/stem asserts
scrape/scrape_test.go        # APPEND (~3): Browser validation pre-I/O (recorder hits==0),
                             #   Browser threading to FetchRequest, PDF e2e (markdown/title/json presence)
crawl/crawl_test.go          # APPEND (~1): browser mini-crawl over cleartext (routing + UA seen)
mcp/agent_test.go            # APPEND (~2): scrape_url/crawl_site browser passthrough + invalid-name error
cli/cmd_test.go              # APPEND (~4): --browser help ×3, safari→exit 2 pre-I/O (counter==0),
                             #   chrome-on-file e2e exit 0
```

### Per-File Coverage Guidance

#### fetch/utls_test.go
`TestRouting_CleartextUsesStock`: `browserFetch` (explicit-options constructor) with `Browser:"chrome"` against `echoOrigin` → 200 AND echoed UA == `fetch.HeaderProfiles["chrome"]["User-Agent"]` (the wrapper never sees this request — if the echo shows the impersonate lib's UA instead, the implementation leaked browser headers onto the stock path: flag it). `TestWarmup_BrowserOnlyTriggersGate`: `newChallengeOrigin` + `Browser:"chrome"`, Profile `""` → homepage hit + retry (gate widened per amended plan; if the implementation kept `Profile == ""` as the gate, this test FAILS BY DESIGN — that's the amended contract, do not weaken it). `TestCookies_SurviveBrowserPath`: cookie header echo through a browser-tagged cleartext fetch. `TestRetryCarriesBrowser`: challenge origin whose retry is recorded → both FetchRequests show `Browser:"chrome"` (via `challengeHits` extension OR scrape-level recordingFetcher — pick one, say which in a comment).

#### fetch/utls_internal_test.go
§7 verbatim first. Then `TestResolveBrowser_Table` (3 ok names, 4 known-but-unsurfaced names → false, garbage → false) + `TestResolveBrowser_RandomOnce` (two calls, identical). `TestBrowserDial_GuardedReject`: `browserDial(SSRFOptions{})` against `hitTCP`'s 127.0.0.1 addr → error AND hits==0; with `AllowPrivate:true` → connects. `TestProxyForHost_Table`: GOMAGPIE_PROXY set/garbage/NO_PROXY-exact/NO_PROXY-suffix/unset-with-HTTP_PROXY (all `t.Setenv`) — locks the extraction against drift from `proxyFunc`. Anti-vacuous: every test that asserts a client property must have gotten that client from `clientFor` with a URL whose scheme drives the split (no hardcoded client literals).

#### clean/pdf_test.go
`TestBuildPDF_SelfCheck` FIRST. Then `TestPdfToMarkdown_Pages` (1p/2p: exact `## Page N` sequence + text; 3p with middle-blank: blank skipped, numbering still ordinal), `TestPdfToMarkdown_Title` (`report.pdf`→`report`; extensionless→`document`), `TestPdfToMarkdown_TruncatedRecover` (`%PDF-1.4\nrandom bytes` → error, message names malformed; assert test did NOT panic by reaching the error check), `TestPdfToMarkdown_Encrypted` (generated `/Encrypt` dict if trivial, else the documented honest-gap skip), `TestIsPDFBody_Table` + `TestIsPDFContentType_Table` (incl. the case-sensitivity row pinned to whatever the implementation does, commented).

#### clean/quality_test.go + clean_test.go + scrape + crawl + mcp + cli additions
Quality rows: `{"pdf zero words", isPDF: true, want: IssueEmpty}`, `{"html zero words no leak", isPDF: false, want: IssueNone}`, `{"pdf rich with captcha", isPDF: true, md: rich text containing "captcha", want: IssueNone}`, ordering rows (pdf+404→IssueEmpty, pdf+503→IssueUnavailable). `Clean` e2e: same generated bytes via CT-header path AND magic-only path (empty CT) → both produce `## Page 1`; one HTML page mislabeled `application/pdf` → pin actual behavior in a comment. Scrape: `recordingFetcher` hits==0 on `Browser:"safari"` (validation precedes fetch), threading assert, PDF e2e via `scrapeOrigin` serving `buildPDF(t, "Quarterly report body")` with the CT header → `Result.Markdown` contains `## Page 1`, `Result.Title=="…"`, `Render("json")` non-empty. Crawl: 2-page site + `Browser:"chrome"` → completes; UA echoed matches our chrome profile. MCP: string `"browser":"chrome"` → no error; `"browser":"safari"` → error text contains `chrome|firefox|random`. CLI: help greps ×3; `--browser safari` against a closed-port URL → `codeOf(err)==2` AND stderr names values; `--browser chrome` on `writeFileSite` → exit 0.

### Data Model Notes (Go)
- `errors.Is` in-process (`clean.ErrPDFEncrypted`, `fetch.ErrPrivateAddress`); `codeOf`+stderr at the CLI edge — never assert sentinels through fail().
- `Classify`'s new param: extend the EXISTING case struct with `isPDF bool` — do not build a second harness.
- All env mutation via `t.Setenv`; all servers `t.Cleanup`-closed; hit counters read AFTER the fetch returns (no sleep-polling).
- Constants pinned BY tests (`50<<20`, ja3 hashes, profile names) carry a drift comment: the test's job is pinning the magic; update in the same commit as the intentional change.

### Success Criteria
- `go test ./...` exits 0; RUN count strictly exceeds the pre-phase baseline (`tee /tmp/phaseE-baseline.log` BEFORE writing — non-vacuous rule; record the actual post-D number)
- `git status --porcelain testdata/` empty throughout (generated fixtures only)
- `go test -tags browser ./fetch/ -run TestUTLS -v` prints both ja3_hash values matching the recorded constants (network-gated, run once manually)
- `grep -rn "impersonate" --include="*_test.go" . | grep -v utls` returns nothing (import locality: the wrapper appears in fetch tests only; `ledongthuc/pdf` in clean tests only)
- Every deliverable from `plan/phase-E.md` §5 maps to §4 above; full gates green (`go vet`, `gofmt -l` empty, `golangci-lint`, 3-way `CGO_ENABLED=0` builds)

### Expected File Structure at End
Same tree as §4 — 4 new test files (utls_test, utls_internal_test, utls_smoke_test, pdf_test), 7 appended (fetch_test, quality_test, clean_test, scrape_test, crawl_test, agent_test, cmd_test), zero fixture files, zero new packages.
---

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -v 2>&1 | tee /tmp/phaseE-baseline.log; grep -c '^=== RUN' /tmp/phaseE-baseline.log  # record actual post-D count

# Fast hermetic suite (every push — no TLS handshake, no network, no browser)
go test ./...

# New-test count vs baseline
go test ./... -v 2>&1 | tee /tmp/phaseE-tests.log; grep -c '^=== RUN' /tmp/phaseE-tests.log

# Focused per task (mirrors phase-E sanity checks)
go test ./clean/ -run 'TestBuildPDF|TestPdfToMarkdown|TestIsPDF' -v                      # E.2 extractor + detection
go test ./clean/ -run 'TestQuality|TestClassify' -v                                      # E.2 pdf rules (incl. call-site edit)
go test ./fetch/ -run 'TestClientFor|TestRedirectGuard|TestDecodeBody|TestBrowserDial|TestProxyForHost|TestResolveBrowser' -v  # E.3/E.4 glue
go test ./fetch/ -run 'TestRouting_|TestWarmup_|TestCookies_|TestRetryCarries' -v        # E.4 behavior
go test ./fetch/ -run 'TestScoreJSRequired' -v                                           # E.2 escalation guard
go test ./scrape/ -run 'TestScrape_Browser|TestScrape_PDF' -v; go test ./crawl/ -run 'TestCrawl_Browser' -v  # E.5
go test ./mcp/ -run 'TestToolCatalog|TestScrapeURL_Browser|TestCrawlSite_Browser' -v; go test ./cli/ -run 'TestBrowser' -v  # surfaces

# Real-handshake smoke (network; manual/CI-with-network only)
go test -tags browser ./fetch/ -run TestUTLS -v

# Discipline checks
git status --porcelain testdata/                                                          # must print nothing
grep -rn "impersonate" --include="*_test.go" . | grep -v fetch/ || echo LOCALITY-OK
grep -rn "ledongthuc" --include="*_test.go" . | grep -v clean/ || echo LOCALITY-OK

# Full gate (mirrors phase-E exit criterion)
go test ./... && go test -tags browser ./fetch/ -run TestUTLS && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — integration/security-boundary + pure logic; tiering replaces fakes for the unfakeable TLS wrapper (§1 first paragraph)
- [x] User stories block present with 5 stories from the phase deliverables — US-1 (E1 fingerprint), US-2 (security preservation), US-3 (cleartext unchanged), US-4 (PDF markdown), US-5 (PDF quality edges)
- [x] Every user story traces to mock-strategy rows — US tags on all 17 component rows
- [x] Every phase-plan deliverable has a test file — §4 maps all of phase-E §5 (README explicitly no-test)
- [x] Heavy dependencies handled — impersonate transport: tiering (behavior/internal/smoke), not a fake, with the rationale; ledongthuc/pdf: real parser on generated fixtures; no other heavy deps
- [x] Unit tests reference no real network/TLS — default suite is cleartext httptest + internal glue; the ONLY network file is fenced behind `//go:build browser`
- [x] Integration tier gated — `-tags browser` build tag (Go equivalent of the `--with-X` flag; rod_smoke precedent), never run by default
- [x] conftest equivalent registered — §5 documents the Go adaptation (no conftest.py; helper structure + the one flagged call-site edit)
- [x] Execution prompt includes helper implementations inline — buildPDF full source (§3, referenced as "copy verbatim" with the source IN this file) and §7's three tests verbatim
- [x] Run commands present — §9 with baseline, focused, smoke, discipline, and full-gate invocations
