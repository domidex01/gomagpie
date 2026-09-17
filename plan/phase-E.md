# Phase E — TLS Impersonation + PDF Extraction (dep-gated)

**Duration:** Days 1–3 (~21 hours)
**Depends on:** Phase A (header profiles, per-request cookies, challenge-warmup retry, quality gate `clean.Classify`) and Phase D (`GuardedTransport` + SSRF `ValidateURL` + redirect guard, `proxyFunc`, crawl binary-ext skip-list)
**Blocks:** Nothing. Phase E3 (office docs, QuickJS islands, REST/Firecrawl-compat) stays **on demand only** per `plan/webclaw-gap-spec.md` §5/§3 — nothing in this phase builds it.
**Risk Level:** HIGH — two new dependencies (supply chain), a transport swap on the security boundary (SSRF guard + proxy must survive), and an untrusted-input parser (PDF). 7 sections (failure section included per HIGH risk).
**Stack:** go *(per AGENTS.md: `run-phase` hard-blocks on `stack: go` — execute manually via the execution prompt in §7)*
**Approval gate:** ⛔ **Do not start E.0 without explicit approval of both dependencies (§ "Dependency approval ask").** The gap spec marks this whole phase dep-gated; this plan is the ask.

---

## Dependency approval ask (decide before E.0)

| # | Module | Why | License | Direct deps added | CGO |
|---|--------|-----|---------|-------------------|-----|
| 1 | `github.com/North-web-dev/impersonate-http` (wraps `refraction-networking/utls` v1.8.2) | E1: byte-exact Chrome/Firefox TLS ClientHello (JA3/JA4) **plus** byte-exact HTTP/2 fingerprint (SETTINGS/WINDOW_UPDATE/pseudo-header order) — stock `crypto/tls` has a distinctive JA3 and this is why we get blocked where webclaw passes. Verified against tls.peet.ws in the project README. | MIT | `utls`, `golang.org/x/net` (already direct), `andybalholm/brotli`, `klauspost/compress`, `golang.org/x/time` — all pure Go, all reputable | none |
| 2 | `github.com/ledongthuc/pdf` | E2: pure-Go PDF text extraction (rsc/pdf lineage, stdlib-only). Spec §4 names it as the option class. Stable, dormant fork — no churn, which for a stdlib-only parser is a feature, not a liability. | BSD-3-Clause | none (stdlib only) | none |

Both keep `CGO_ENABLED=0` intact (3-way cross-build gate proves it). If approval is declined for either item, the phase ships the other item alone — they are independent.

---

## 1. Objective + What Success Looks Like

Close the two dep-gated gaps from `plan/webclaw-gap-spec.md` §5-Phase E: **E1** — a TLS-impersonating fetch path (`--browser chrome|firefox|random`) that survives JA3/JA4 + HTTP/2 fingerprinting while preserving every Phase D security property (pre-dial SSRF check, redirect re-validation, peer-IP check, proxy policy, 50 MB cap); **E2** — PDF documents fetched by `magpie scrape` are extracted page-by-page into markdown and flow through the existing quality gate like any HTML page.

1. `magpie scrape <https-url> --browser chrome` completes the request through a uTLS handshake (JA4 `t13d…`, browser-class) with the wrapper's matching Chrome header set; `--browser firefox` likewise. Verified by the network-gated smoke test: `go test -tags browser ./fetch/ -run TestUTLS` asserts `ja3_hash` equals the constant recorded from tls.peet.ws at implementation time.
2. `magpie scrape <url> --browser safari` (or any name outside `chrome|firefox|random`) exits 2 **before any I/O** with a message naming the allowed values.
3. A `--browser chrome` request to a cleartext `http://` URL (httptest) succeeds through the **stock** client with Phase A cookies/profiles intact — the impersonation transport is never fed a non-TLS URL (its `RoundTrip` unconditionally TLS-handshakes).
4. Security matrix holds on the browser path: a strict `StaticFetcher` (`NewStaticFetcherWithOptions(SSRFOptions{})`) with `--browser chrome` rejects a private URL **pre-dial** (origin hit counter stays 0); a redirect chain landing on a private host aborts at the redirect; `GOMAGPIE_PROXY` tunnels browser connections via CONNECT (proxy-side counter proves it); NO_PROXY bypasses.
5. `magpie scrape https://host/report.pdf` returns markdown with `## Page 1`…`## Page N` sections and a title derived from the URL path stem; `--format json` carries the same content; word counts feed `clean.Classify` — a text-less/scan PDF exits 8 (`empty`), a rich PDF that happens to contain the word "captcha" stays clean, an encrypted PDF fails loudly (never empty markdown).
6. PDF bytes never escalate to go-rod: `fetch.ScoreJSRequired(%PDF…)` returns `(0, false)` (unit-proven; covers scrape and crawl callers).
7. Crawl behavior unchanged: `.pdf` links stay in the skip-list (documented non-goal to crawl PDFs this phase).
8. `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, and 3-way `CGO_ENABLED=0` cross-builds all pass; the default suite stays hermetic (no TLS handshake, no live network — the JA4 proof lives behind the existing `-tags browser` suite next to `rod_smoke_test.go`).

**Good:** "`go test -tags browser ./fetch/ -run TestUTLS -v` prints `ja3_hash=1d03c132ce29d0d7936acc72f12dd7a7 (chrome)` matching the recorded constant"
**Bad:** "TLS impersonation works"

---

## 2. What Failure Looks Like (and what to do)

- **`go get impersonate-http` fails or the module is yanked** (small-author supply-chain risk) → fall back to `refraction-networking/utls` directly: implement `DialTLSContext` (raw guarded dial → `utls.UClient` + `HandshakeContext`) + ALPN split to `golang.org/x/net/http2` (~150 lines in `fetch/utls.go`, same seams). Lose byte-exact h2 framing; keep JA3/JA4. Worst case: revert `--browser` to an alias of `--header-profile` and keep only E2 — E2 is unaffected either way.
- **JA4 smoke test mismatches the recorded hash** after a `go get -u` bump → uTLS `*_Auto` templates intentionally track current browsers; update the recorded constant *with the new value in the commit message*. Hard-fail only on handshake errors or a JA4 that is no longer browser-class (`t13d…` prefix).
- **h2 transport leaks connections or serializes crawl throughput** (wrapper keeps one h2 conn per host, one stream at a time, no `CloseIdleConnections`) → acceptable for CLI lifetime (ponytail-noted); if it stalls real crawls, document `--browser` as scrape-first and run crawls with `--render static` + header profiles.
- **Compressed bodies arrive encoded** (wrapper's h2 transport never decompresses; its h1 fallback sees the profile's `Accept-Encoding: gzip, deflate, br, zstd`, which disables stock auto-decode) → this is a known fact, not a risk: `do()` decodes by `Content-Encoding` itself (E.4). If a charset/encoding edge corrupts output, fall back to forcing `Accept-Encoding: gzip` on browser requests (caller-set wins over the wrapper's header injection) and decode gzip only — document the realism dip.
- **Stalled h2 body read is uninterruptible** (the wrapper's `h2Conn.roundTrip`/`readResponse` never select on ctx — neither the 30s client timeout nor `do()`'s per-attempt budget can abort a wedged frame read; the stock path aborts at 30s) → acceptable for scrape-first usage; upgrade path if it bites: a `time.AfterFunc` conn-closer wrapped around `browserDial`'s returned conn.
- **PDF parser panics on hostile/truncated input** → the lib recovers panics inside `Page.GetPlainText` (converts to error) and inside `NewReaderEncrypted`; our extraction wrapper adds one more `defer recover()` belt. Malformed-fixture table test proves typed errors, never a crash, never empty-markdown success.
- **Quality gate misfires on PDFs** (binary body counted as "richer text" → false `empty`; challenge markers found in binary noise → false `access-denied`) → PDF bodies are excluded from the two body-bytes rules in `Classify` before any real page hits it; negative tests (rich PDF mentioning "captcha") pin this.
- **User declines one dependency** → ship the other half; the plan's tasks split cleanly at E.0.

---

## 3. Architecture / Key Design Decisions

```
scrape/crawl/batch Options.Browser ("chrome"|"firefox"|"random"|"")
  → FetchRequest.Browser ── validated pre-I/O in scrape.Run (exit 2), threaded through warmup retry

fetch.StaticFetcher.do(ctx, req)
  ├─ ValidateURL (SSRF pre-dial gate — shared, unchanged)
  ├─ client = clientFor(req): Browser=="" or scheme http:// → stock client (GuardedTransport,
  │    Jar, redirectGuard)   |   Browser!="" and https → browser client (below)
  ├─ headers: browser && Profile=="" → let wrapper inject its matching browser headers
  │            (our HeaderProfiles stay out — their UA/headers match the uTLS hello era)
  │            browser && Profile!="" → caller's explicit override, ours win (documented combo)
  │            cleartext http:// + browser → OUR matching HeaderProfiles (wrapper is never
  │              on that path; sending no UA would be worse than our stock fingerprint)
  ├─ read raw body (50 MB LimitReader, unchanged)
  └─ browser-only: decodeBody by Content-Encoding (gzip/deflate stdlib, br→brotli,
       zstd→klauspost — promoted transitive deps, zero new modules), output capped 50 MB

browser client (fetch/utls.go, one per resolved profile name, cached on the fetcher):
  http.Client{ Transport: impersonate.NewTransport(profile, WithDialer(browserDial)),
               Jar: same cookiejar (warmup parity), Timeout 30s,
               CheckRedirect: redirectGuard(o)  ← extracted from ssrf.go, also used by stock client }
  browserDial(o): proxyForHost(host) → impersonate.ProxyDialer (trusted-peer CONNECT, no IP check)
                  else guarded dial + post-connect peer-IP check (extracted from GuardedTransport)
  random → resolved ONCE per process (sync.OnceValue, crypto/rand pick chrome/firefox)

clean.Clean(RawPage{ContentType NEW})
  ├─ isPDF? (magic "%PDF-" prefix OR Content-Type application/pdf) → cleanPDF:
  │    pdf.NewReader(bytes.NewReader(body), len) → per page GetPlainText(nil)
  │    → markdown "## Page N" sections, Title = URL path stem, recover() → typed error
  │    (encrypted → error wrapping pdf.ErrInvalidPassword; malformed → error; 0 words → IssueEmpty)
  └─ else existing trafilatura path; Classify(+isPDF flag from Clean's pre-scope branch):
       pdf → q.body = nil (kills empty-shell/challenge-marker binary-noise rules;
       word-count rules unchanged; pdf-empty rule fires IssueEmpty at 0 words)
fetch.ScoreJSRequired: "%PDF-" magic → (0, false) — no rod escalation for PDF bytes
```

### Data model strategy (stack: go)

| Layer | Type | Why |
|-------|------|-----|
| Wire surfaces | `FetchRequest.Browser`, `scrape.Options.Browser`, `crawl.Options.Browser`, `batch` options, MCP `ScrapeIn.Browser`/`CrawlIn.Browser` — plain `string` fields | Same pattern as `Profile`/`Cookies` (Phase A); validated by one switch at the scrape edge; no coercion type needed |
| Fetch internals | unexported `browserClients struct{ mu sync.Mutex; m map[string]*http.Client }` on `StaticFetcher` | One implementation, no interface; keyed by resolved profile name so conns pool per run |
| Guard funcs | extracted `redirectGuard(o SSRFOptions) func(*http.Request, []*http.Request) error` and `guardedDial(...)` helpers in `fetch` | Pure extraction of existing logic — shared by stock + browser clients, no new abstraction |
| PDF errors | `clean.ErrPDFEncrypted` sentinel wrapping `pdf.ErrInvalidPassword`; malformed → wrapped error naming the URL | Fail loud, `errors.Is`-able at the CLI edge; no new Issue enum value |

**Rules for this phase (follow exactly):**
- **Import locality, mirroring the go-rod rule:** `impersonate-http` is imported only in `fetch/`; `ledongthuc/pdf` only in `clean/`. Nothing else sees them.
- **Phase D's invariant holds:** the uTLS transport preserves the pre-dial `ValidateURL`, the post-connect peer-IP check, redirect re-validation, the proxy policy (incl. proxied-connections-skip-the-IP-check semantics), and the 50 MB cap — now on the *decoded* stream for browser responses. This was the note Phase D left on `GuardedTransport`.
- **Cleartext never enters the impersonation transport** (its RoundTrip TLS-handshakes unconditionally): scheme split inside `clientFor`.
- **Browser headers come from the wrapper, not from our Phase A profiles** unless the user sets `--header-profile` explicitly (stale-UA-vs-fresh-hello mismatch is a fingerprint tell).
- **Fail loud on PDFs:** encrypted/malformed → error; zero extracted words → `IssueEmpty` (exit 8 path). Never cache, never return empty-markdown success.
- **Tests stay hermetic:** no TLS handshake and no live network in the default suite (the wrapper hardcodes `utls.Config{ServerName}` — no `InsecureSkipVerify` hook — so self-signed https is unreachable anyway). Routing, guards, decode, and PDF logic are all proven offline; the JA4 proof lives behind `-tags browser`.

---

## 4. Tasks

### Task E.0 — Dependency approval + `go get` (0.5h)

**⛔ Gate task.** Get explicit user approval for the two modules in the ask table. Then:

```bash
go get github.com/North-web-dev/impersonate-http@latest   # MIT; pulls utls v1.8.2, brotli, klauspost/compress
go get github.com/ledongthuc/pdf@latest                    # BSD-3; stdlib-only
go mod tidy
```

Record the pinned pseudo-versions in this file's Readiness section when done. `x/net` already direct at v0.59.0 (wrapper wants ≥v0.56.0 — no conflict). Reject anything requiring CGO.

**Sanity check:** `go build ./...` green; `grep cgo`-free dep tree: `go list -deps ./... | grep -i cgo` prints nothing.

### Task E.2 — PDF extraction in `clean` (spec E2) (5h)

Build the lower-risk, fully independent half first.

```go
// Confirmed API (ledongthuc/pdf, read from source 2026-09-17):
// pdf.NewReader(f io.ReaderAt, size int64) (*Reader, error)
//   - validates "%PDF-1." header AND trailing "%%EOF"; recovers panics → "malformed PDF: …"
//   - encrypted docs: NewReader → NewReaderEncrypted(f, size, nil) → err wraps pdf.ErrInvalidPassword
r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
n := r.NumPage()                       // 1-based pages
for i := 1; i <= n; i++ {
    p := r.Page(i)
    if p.V.IsNull() { continue }
    text, err := p.GetPlainText(nil)   // panics recovered internally → error; nil fonts = build own cache
    // text = whole page text with "\n" line breaks (BT → "\n", T*/Tj/TJ handled)
}
```

1. `clean/pdf.go`: `isPDFBody(b []byte) bool` (`bytes.HasPrefix(b, []byte("%PDF-"))`); `IsPDFContentType(ct string) bool` (`strings.HasPrefix(strings.ToLower(ct), "application/pdf")`); `pdfToMarkdown(body []byte, sourceURL string) (md, title string, err error)` — `defer recover()` → typed error; per-page loop skipping null pages, joining non-empty pages as `## Page N\n\n<text>`; title = `path.Base(url)` minus `.pdf` extension (fallback `"document"`); encrypted (`errors.Is(err, pdf.ErrInvalidPassword)`) → `fmt.Errorf("clean: encrypted PDF (password required): %s: %w", sourceURL, ErrPDFEncrypted)`.
2. `clean.RawPage` gains `ContentType string`; `Clean` branches to the PDF path when `isPDFBody || IsPDFContentType(raw.ContentType)` **before** trafilatura, producing a normal `CleanedPage` (no `StructuredData`), then `Classify(out, status, body, true /* isPDF */)` (see 3.).
3. `clean/quality.go`: `Classify` gains an `isPDF bool` param (update its test call sites). Clean learns it's a PDF **before** goquery scoping, so this flag — never a byte re-test — drives both guards (Classify receives the *scoped* HTML, not the raw body; `isPDFBody(q.body)` there would be fragile): when `isPDF`, carry it in `qualityCtx`, set `q.body = nil` before rule evaluation (`bodyIsRicher` naturally returns false on a nil body; challenge markers stop matching binary noise), and add one ordered rule `{"pdf-empty", …}`: `q.words == 0 && isPDF → IssueEmpty`, placed after the status rules.
4. `fetch/detect.go`: first line of `ScoreJSRequired` — `if bytes.HasPrefix(page, pdfMagic) { return 0, false }` (kills rod escalation for every caller).
5. Call sites: set `ContentType` from `page.Headers.Get("Content-Type")` in `scrape.Run` (scrape/scrape.go:138 area) and crawl's main clean call (crawl/crawl.go:724). Crawl's `.pdf` skip-list entry stays (non-goal, note in code).

**Sanity check:** `go test ./clean/ -run TestPDF -v` — page-markdown golden, encrypted → `ErrPDFEncrypted`, blank-scan → `IssueEmpty`, rich-PDF-mentions-"captcha" → `IssueNone`, `%PDF-`-prefixed garbage → error not crash.

### Task E.3 — Guard extraction in `fetch` (refactor, no behavior change) (3h)

**Depends on:** E.0. Pure move-and-call so E.4 shares the logic instead of duplicating it:

1. `fetch/ssrf.go`: extract the `NewStaticFetcherWithOptions` `CheckRedirect` body (ssrf.go:161) into `func redirectGuard(o SSRFOptions) func(*http.Request, []*http.Request) error`; the stock client calls it unchanged.
2. `fetch/http.go`: extract the `guardedTransport` `DialContext` closure (dial + `dialPeerAllowed` peer check) into a reusable `guardedDialFunc(o SSRFOptions, dialer *net.Dialer) func(ctx, network, addr) (net.Conn, error)`; `guardedTransport` calls it.
3. `fetch/http.go`: split `proxyFunc(req)` into `proxyForHost(hostname string) (*url.URL, error)` (all current env/NO_PROXY logic) + a thin `proxyFunc` wrapper calling `proxyForHost(req.URL.Hostname())`.

**Sanity check:** `go test ./fetch/ ./crawl/` — the entire existing SSRF/proxy/redirect test matrix passes untouched (proves the extraction is behavior-preserving).

### Task E.4 — Browser clients + decode in `fetch/utls.go` (spec E1 core) (4h)

**Depends on:** E.3.

```go
// Confirmed API (impersonate-http, read from source 2026-09-17):
impersonate.Profiles                  // map[string]Profile — "chrome","chrome_android","firefox","safari","edge","ios"
p, ok := impersonate.Profiles[name]
rt := impersonate.NewTransport(p, impersonate.WithDialer(dial)) // headerRT injects p.Headers
                                      // ONLY for headers the caller left empty
impersonate.ProxyDialer(rawurl) (DialFunc, error)               // http(s)/socks5 CONNECT tunnel
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) // raw TCP under uTLS
// KNOWN LIMITS (from transport.go/http2.go): RoundTrip ALWAYS TLS-handshakes (no cleartext);
// h2 responses are NOT decompressed; h1 fallback sees caller's Accept-Encoding so stock
// auto-decode is bypassed too; one h2 stream at a time; no CloseIdleConnections on h2.
```

1. `fetch/utls.go`: `resolveBrowser(name string) (impersonate.Profile, bool)` — accepts exactly `chrome|firefox|random` (`random` → `sync.OnceValue` + `crypto/rand` pick between chrome/firefox, **per process** — `ponytail:` per-request rotation is the upgrade path if ever needed); `browserClients` cache on `StaticFetcher` keyed by resolved name; `clientFor(req) *http.Client` implementing the scheme split (https+browser → cached browser client; everything else → stock client). **First** promote the cookiejar from a `NewStaticFetcherWithOptions` local (ssrf.go:153) to an `s.jar` field on `StaticFetcher` (the Deliverables tree lists this under fetch/http.go) — the browser client needs it: `&http.Client{Transport: impersonate.NewTransport(p, WithDialer(browserDial(s.ssrf))), Jar: s.jar, Timeout: 30*time.Second, CheckRedirect: redirectGuard(s.ssrf)}` — same jar as stock so challenge-warmup cookies carry over.
2. `browserDial(o SSRFOptions) DialFunc`: `u, err := proxyForHost(hostOf(addr))` — `u != nil` → `impersonate.ProxyDialer(u.String())` dial (trusted peer, no IP check — same semantics as the stock transport under `GOMAGPIE_PROXY`); else `guardedDialFunc` (peer check before any TLS byte).
3. `fetch/http.go`: `FetchRequest` gains `Browser string`; `do()` uses `clientFor(req)`, resolves the effective header profile (browser && `Profile==""` → set **no** profile headers — wrapper injects matching ones; explicit `Profile` wins; cleartext+browser → our `HeaderProfiles[resolvedBrowser]` fallback), threads `Browser` through the warmup-retry `FetchRequest` literals in `Fetch`, and **widens the warmup gate** to fire unless `req.Profile == "" && req.Browser == ""` (browser-only requests are exactly the blocked-page case — leaving the gate on `Profile` alone would make `--browser` useless against the sites it exists for); `Close()` calls `CloseIdleConnections()` on cached stock clients (h2 conns die with the process — ponytail comment).
4. `fetch/decode.go`: `decodeBody(b []byte, hdr http.Header) ([]byte, error)` — split `Content-Encoding` on commas, chain gzip (`compress/gzip`), deflate (`compress/flate`), br (`github.com/andybalholm/brotli`), zstd (`github.com/klauspost/compress`); final output capped at 50 MB via `io.LimitReader` (bomb guard on the decoded stream); applied in `do()` **only** when the browser client served the response.

**Sanity check:** `go test ./fetch/ -run TestBrowser -v` — httptest cleartext through `Browser:"chrome"` returns 200 via the stock client (routing); `decodeBody` table (gzip/deflate/br/zstd/identity/50 MB cap).

### Task E.5 — Surfaces: CLI, MCP, crawl, batch (3h)

**Depends on:** E.4 (fetch field exists).

1. `scrape.Options.Browser` + validation switch in `Run` beside the `render` switch (`""|chrome|firefox|random`, else `scrape: browser %q must be chrome|firefox|random`) — pre-I/O. **`Run`'s switch alone is not enough:** `--render` reaches exit 2 via a duplicate pre-`Run` check in cli/scrape.go:88-ish → `fail(2, …)`; without the CLI-side check an unknown `--browser` surfaces as exit 1 through `scrapeExit`'s default arm. Add the `fail(2, …)` in cli/scrape.go and in the crawl/batch flag paths.
2. `crawl.Options.Browser` → `fetchFn`'s `FetchRequest`; `batch` options gain the same field (one flag, same plumbing).
3. CLI flags `--browser` on `scrape`, `crawl`, `batch` (long-help: "TLS-impersonating browser fingerprint: chrome|firefox|random"); MCP `ScrapeIn.Browser` + `CrawlIn.Browser` plain strings (no coercion needed — string passthrough); update the tool-catalog test if it pins input docs.
4. README: `--browser` section (what it does, JA3/JA4 + h2 fingerprint, proxy interplay, `--header-profile` override semantics), PDF extraction paragraph (formats, encrypted/scan behavior, crawl skip note), dependency credits.

**Sanity check:** `magpie scrape --help` lists `--browser`; `magpie scrape <url> --browser safari` exits 2 pre-I/O; `curl -s` against the MCP server's catalog shows `browser` on scrape_url/crawl_site.

### Task E.6 — Hermetic test matrix + smoke + full gates (5.5h)

**Depends on:** E.2–E.5.

1. PDF fixtures as **code, not blobs**: a ~40-line test helper builds minimal valid PDFs (objects + programmatic xref offsets) so tests stay hermetic and drift-free — one-page text, two-page, zero-text, `%PDF-`+garbage truncated, encrypted-shaped (skippable if hand-building the /Encrypt dict proves disproportionate — cover the sentinel via a unit test on the wrapper's error mapping instead).
2. Security matrix on the browser path (hermetic, httptest + strict `SSRFOptions{}`): private URL rejected pre-dial (hit counter 0), `redirectGuard` aborts redirect-to-private, `browserDial` picks `ProxyDialer` vs guarded dial per `proxyForHost` (assert via dial-target recording), `random` resolves once (two calls, same name).
3. `fetch/utls_smoke_test.go` behind `//go:build browser` (alongside `rod_smoke_test.go`): GET `https://tls.peet.ws/api/all` with chrome then firefox; assert `ja3_hash` equals the constant recorded at implementation time (wrapper README's measured table: chrome `1d03c132ce29d0d7936acc72f12dd7a7`, firefox `b5001237acdf006056b409cc433726b0` — verify and update at E.6, note in the test that uTLS Auto bumps intentionally change it). Run once manually: `go test -tags browser ./fetch/ -run TestUTLS -v`.
4. Full gates: `go test ./...` (hermetic), `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` cross-builds, plus `go test -tags browser ./...` for the record.

**Sanity check:** all gates green; `-tags browser` smoke prints both `ja3_hash` values.

---

## 5. Deliverables

```
plan/phase-E.md              # this file
go.mod / go.sum              # +impersonate-http (→utls, brotli, klauspost), +ledongthuc/pdf
fetch/utls.go                # resolveBrowser (incl. per-process random), browserClients cache,
                             #   clientFor scheme split, browserDial (proxy tunnel | guarded dial)
fetch/utls_smoke_test.go     # -tags browser JA3/JA4 verification vs tls.peet.ws
fetch/decode.go              # decodeBody: Content-Encoding chain, 50 MB decoded cap
fetch/http.go                # (edit) StaticFetcher += s.jar field (promoted from NewStaticFetcherWithOptions
                             #   local, ssrf.go:153); FetchRequest.Browser; do(): clientFor + effective headers +
                             #   browser-only decode; guardedDialFunc extraction; proxyForHost split;
                             #   warmup retry threads Browser; Close drains stock clients
fetch/ssrf.go                # (edit) redirectGuard extraction (shared stock+browser)
fetch/detect.go              # (edit) ScoreJSRequired: %PDF- → (0,false)
fetch/*_test.go              # browser routing/guard/dial/decode tables (hermetic)
clean/pdf.go                 # isPDFBody, IsPDFContentType, pdfToMarkdown, ErrPDFEncrypted
clean/clean.go               # (edit) RawPage.ContentType; PDF branch before trafilatura
clean/quality.go             # (edit) Classify: pdf body guard + pdf-empty rule
clean/pdf_test.go            # generated fixtures, quality negatives, encrypted/malformed paths
scrape/scrape.go             # (edit) Options.Browser + pre-I/O validation; ContentType plumbing
crawl/crawl.go               # (edit) Options.Browser → fetchFn; ContentType at :724
cli/scrape.go, cli/crawl.go, cli/batch.go  # (edit) --browser flag + help
mcp/tools.go                 # (edit) ScrapeIn.Browser, CrawlIn.Browser (+catalog test if pinned)
README.md                    # --browser, PDF, dependency credits
```

No interface changes: `Fetcher` unchanged, `scrape.Run`/`crawl.Run` signatures unchanged, `CleanedPage` unchanged.

---

## 6. Exit Criteria

- [x] `--browser chrome|firefox|random` requests complete; `-tags browser` smoke asserts recorded `ja3_hash` per profile (E.6) — **go/no-go gate: if the smoke proves the handshake AND the hermetic security matrix (below) passes → keep E1; if impersonate-http is unusable → implement the utls-direct fallback or revert `--browser` to a header-profile alias and keep E2; PDF work never reverts**
- [x] `--browser safari` (unknown name) exits 2 pre-I/O naming allowed values; `scrape.Run` validation switch in place (E.5)
- [x] Browser-path security matrix hermetic-green: strict fetcher rejects private URL pre-dial (0 origin hits), redirect-to-private aborts via the extracted `redirectGuard`, proxied browser conns use the tunnel while direct conns keep the peer-IP check (E.4/E.6)
- [x] Cleartext routing: `Browser:"chrome"` on an `http://` httptest target succeeds via the stock client with cookies/profiles intact (E.4)
- [x] `decodeBody` table green (gzip/deflate/br/zstd/identity/cap); browser responses arrive as plain text (E.4)
- [x] `scrape <pdf-url>` → markdown with `## Page N` + URL-stem title; `--format json` carries it; encrypted → loud error; zero-text → exit 8 `empty`; rich-PDF-mentions-"captcha" stays clean; malformed → typed error, no crash (E.2)
- [x] `ScoreJSRequired` returns `(0,false)` on PDF bytes; crawl still skips `.pdf` links (E.2)
- [x] `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` builds, `go test -tags browser ./fetch/ -run TestUTLS` — all pass (E.6)

---

## 7. Execution Prompt

---
You are implementing Phase E of gomagpie (`magpie`, module `gomagpie`, Go 1.26) — TLS impersonation (E1) + PDF extraction (E2), both **previously approved** dependencies (if not approved, stop and ask).

### What this project is

Go CLI web scraper: fetch → clean → extract. Single static binary (`CGO_ENABLED=0`, 3-way cross-builds), pure Go, no CGO ever. Source of truth: `plan/webclaw-gap-spec.md` §5-Phase E; this plan. Rules: `AGENTS.md` + `.pi/rules/go.md` + `.pi/rules/testing.md`. Lazy senior dev: reuse existing helpers, no new abstractions for single callers, hermetic tests only in the default suite. **Import locality:** `impersonate-http` only in `fetch/`, `ledongthuc/pdf` only in `clean/` (mirrors the go-rod rule).

### Established in prior phases (reuse, don't reinvent)

- `fetch.StaticFetcher` (fetch/http.go): `client *http.Client` built in `NewStaticFetcherWithOptions(o SSRFOptions)` — `GuardedTransport` (DialContext with post-connect `dialPeerAllowed` peer check + `Proxy: proxyFunc` + file:// handler), shared `cookiejar` (`s.jar`… store the jar on the struct if not already), `CheckRedirect` validating every hop via `ValidateURL` (ssrf.go:161), 50 MB `LimitReader` on the decoded body, challenge-warmup retry in `Fetch` (rebuilds `FetchRequest{URL, Timeout, Profile, Cookies}` — add `Browser`), `FetchRequest{URL, Timeout, Profile, Cookies}` / `FetchResponse{URL, FinalURL, StatusCode, HTML, Headers}`.
- `fetch/proxyFunc(req)` (fetch/http.go): `GOMAGPIE_PROXY` (validated loudly) > `http.ProxyFromEnvironment`, minimal `NO_PROXY` exact/dot-suffix match.
- `fetch.HeaderProfiles` + `profileHeaders(profile)` (fetch/profiles.go): default/chrome/firefox header maps — stock path only.
- `fetch.ScoreJSRequired(page, headers) (score, hasEmbeddedData)` (fetch/detect.go): rod escalation when score ≥ 2; called by scrape AND crawl.
- `clean.Clean(ctx, RawPage{HTML, URL, FinalURL, Scope, StatusCode})` (clean/clean.go:45): trafilatura path, ends with `out.Quality = Classify(out, raw.StatusCode, body)` (:78). `Classify` (clean/quality.go): ordered `qualityRules`, `qualityCtx{status, words, thin, md, title, body, lower}`, `bodyIsRicher(md, body)` for the empty-shell rule, challenge markers via shared vocabulary, `ThinPageWords = 200`, `QualityError{Issue, URL}` → exit 8.
- `scrape.Run` (scrape/scrape.go): pre-I/O validation switches (render at :88, page-format) → `fail(2)` equivalents; clean call at :138; `crawl.go` cleans at :376/:425/:724 (main path :724). CLI duplicate-check pattern for exit 2: cli/scrape.go:88-ish (`--render`) — unknown `--browser` must `fail(2, …)` there too, not only in `Run` (else `scrapeExit`'s default arm makes it exit 1).
- MCP coercion (mcp/coerce.go) already widens scalars — plain string fields need nothing.
- Gates: `go test ./...`, `go vet`, `gofmt -l` empty, `golangci-lint`, 3-way `CGO_ENABLED=0` builds. `-tags browser` suite exists (`fetch/rod_smoke_test.go`) — network tests go there.

### Confirmed library APIs (pinned by research 2026-09-17 — do not re-derive)

```go
// github.com/North-web-dev/impersonate-http (MIT; deps: utls v1.8.2, brotli, klauspost/compress, x/net)
impersonate.Profiles            // map[string]Profile: "chrome","chrome_android","firefox","safari","edge","ios"
p := impersonate.Profiles["chrome"]   // p.ClientHello = utls.HelloChrome_Auto; p.Headers = browser-accurate set
rt := impersonate.NewTransport(p, impersonate.WithDialer(dial)) // headerRT: injects p.Headers ONLY where caller left empty
impersonate.ProxyDialer(rawurl) (DialFunc, error) // http(s)/socks5 CONNECT
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) // raw TCP under uTLS
// LIMITS: RoundTrip ALWAYS TLS-handshakes (never feed it http://); h2 responses NOT decompressed;
// h1 fallback sees caller's Accept-Encoding (gzip,deflate,br,zstd) → stock auto-decode bypassed;
// one h2 stream at a time per host; no CloseIdleConnections on h2 (dies with process).

// github.com/ledongthuc/pdf (BSD-3, stdlib-only)
r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
// validates "%PDF-1." header + "%%EOF" tail; encrypted → err wraps pdf.ErrInvalidPassword;
// construction panics recovered → "malformed PDF: …"
for i := 1; i <= r.NumPage(); i++ {
    p := r.Page(i)               // 1-based; missing page → p.V.IsNull()
    text, err := p.GetPlainText(nil) // fonts nil = internal cache; panics recovered → error
}
```

### Data model rules (follow exactly)

- Plain `string` fields end to end: `FetchRequest.Browser`, `scrape/crawl/batch Options.Browser`, MCP `ScrapeIn.Browser`/`CrawlIn.Browser`. One validation switch in `scrape.Run` (`""|chrome|firefox|random` → else error, CLI exit 2) + the same literal set in `resolveBrowser`.
- Unexported `browserClients{mu, map[string]*http.Client}` on `StaticFetcher` — no interface, one impl.
- Guard helpers are extractions, not abstractions: `redirectGuard(o)`, `guardedDialFunc(o, dialer)`, `proxyForHost(hostname)`.
- PDF: no new `Issue` value; sentinel `clean.ErrPDFEncrypted` wrapping `pdf.ErrInvalidPassword`; 0 extracted words → `IssueEmpty` via a `qualityRules` entry.

### Architecture (implement as drawn in §3)

Scheme split in `clientFor` (impersonation transport never sees cleartext). Browser headers: wrapper's own unless explicit `--header-profile`; cleartext+browser falls back to our matching `HeaderProfiles` (wrapper never on that path). Warmup gate widened: fires when `Profile != "" || Browser != ""`. Browser client = stock-grade security: same jar, same `redirectGuard`, guarded dial or proxy tunnel via `proxyForHost` (proxied → trusted peer, no IP check — same semantics as stock). Decode browser responses by `Content-Encoding` (gzip/flate stdlib, br, zstd) capped at 50 MB. PDF: magic-or-Content-Type branch at the top of `Clean`, page loop → `## Page N` markdown, `Classify` gains an `isPDF bool` param threaded from that branch (drives `q.body = nil` + the `pdf-empty` rule; never re-test bytes inside Classify — it sees scoped HTML), `ScoreJSRequired` short-circuits on the magic. Crawl `.pdf` skip-list unchanged.

### Files to create/edit

Per the Deliverables tree in §5 — each entry has its one-line contract there. Key non-obvious points: thread `Browser` through the warmup-retry `FetchRequest` literals; apply `decodeBody` only when the browser client served the response; set `ContentType` at scrape.go:138 and crawl.go:724; build PDF fixtures programmatically in the test (compute xref offsets in code — no binary blobs in git); put the JA3/JA4 live check in `fetch/utls_smoke_test.go` behind `//go:build browser`, asserting `ja3_hash` against constants recorded during this phase (wrapper README measured chrome `1d03c132ce29d0d7936acc72f12dd7a7`, firefox `b5001237acdf006056b409cc433726b0` — verify at tls.peet.ws and update the constant in the same commit if uTLS Auto drifted, with a comment saying exactly that).

### Success criteria

All boxes in §6 Exit Criteria, including the go/no-go gate. Observable proof beats prose: exit codes, hit counters, `ja3_hash` strings, `## Page N` markdown.

---

## Execution record (2026-09-17)

Pinned versions: `github.com/North-web-dev/impersonate-http v0.4.0` (→ `refraction-networking/utls v1.8.2`, `andybalholm/brotli v1.2.2`, `klauspost/compress v1.19.0`), `github.com/ledongthuc/pdf v0.0.0-20260907135840-6c8c28e0e8a0`.

Live tls.peet.ws results (E.6.3):
- firefox: `ja3_hash=b5001237acdf006056b409cc433726b0`, `ja4=t13d1715h2_5b57614c22b0_5c2c66f702b0` — both stable, match the README table.
- chrome: `ja4=t13d1516h2_8daaf6152771_d8a2da3f94cd` stable; **`ja3_hash` is randomized per handshake** (`6910c584…`, `a1f0143b…`, `cd7e1703…` across runs) — `HelloChrome_Auto` faithfully reproduces Chrome's extension-order randomization, which is exactly why JA4 exists (sorted extensions). The README table's chrome JA3 was therefore never pinnable; the smoke pins JA4 for chrome and both hashes for firefox.

Deviations from the letter of the plan (behavior intact):
- `Classify` nils the PDF body *before* computing `qualityCtx.lower` (a post-hoc nil would have left binary noise in the marker haystack).
- crawl `Clean` call sites are at :376 (main cleanFn) and :724 (qualityErr) in the current tree — both thread `ContentType`; the heal path (:425) only has an HTML string, no headers, so nothing to pass.
- E.5 validation uses one exported `fetch.ValidBrowser` (4 call sites: scrape.Run + 3 CLI fail(2) edges) instead of 4 copies of the switch — the literal set is still owned next to `resolveBrowser`.
- End-to-end PDF proof ran over `file://` with `GOMAGPIE_ALLOW_FILE=1` (same clean/fetch path as https; the https fetch path itself is proven by the -tags browser smoke).

Exit criteria: all boxes below verified 2026-09-17 — hermetic suite 15 pkgs ok, vet/gofmt/golangci-lint clean, 3-way CGO_ENABLED=0 builds ok, `-tags browser` suite ok incl. `TestUTLSImpersonation` (go/no-go gate: **GO**, impersonate-http kept).

## Readiness Check

- [PASS] All inputs from prior phases are listed and available (verified by direct file reads this session: `NewStaticFetcherWithOptions` + CheckRedirect at ssrf.go:161, `GuardedTransport` DialContext + `proxyFunc` + warmup retry in http.go, `HeaderProfiles`/`profileHeaders`, `ScoreJSRequired` in detect.go, `Clean`/`RawPage`/`Classify`/`qualityRules` in clean, clean-call sites scrape.go:138 / crawl.go:376,425,724, MCP coercion conventions, `-tags browser` precedent)
- [PASS] Every sub-task has a clear, testable completion condition (each task ends with a Sanity check one-liner)
- [PASS] Execution prompt is self-contained: (a) prior-phase facts with file:line anchors, (b) confirmed API snippets for both new libraries (read from source this session, incl. their known limits), (c) data-model rules, (d) per-file guidance, (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables (one criterion per deliverable group + gates; nothing untested)
- [PASS] Heavy external dependency strategy noted: impersonate-http failure → utls-direct fallback (~150 lines, same seams) → worst case header-profile alias; PDF parser panics → recovered at lib level (`GetPlainText`, `NewReaderEncrypted`) + one wrapper-level `defer recover()`; live-TLS verification isolated behind `-tags browser` so the default suite stays hermetic
- [PASS] New libraries have confirmed usage snippets in the execution prompt (both pinned from primary source, dep footprints + licenses listed in the approval ask; `go get` commands in E.0)
- [FAIL→resolved] Phase-plan skill's `stack:` field supports only python|nextjs|react|typescript — this Go repo writes `stack: go` per AGENTS.md, so `run-phase` will hard-block by design; execution is manual via the §7 prompt (documented in the metadata block)
