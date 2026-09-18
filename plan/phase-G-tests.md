# Phase G — Testing: Proxy Pool, Typed Challenges, TLS Breadth, Formats, Search

**Scope:** `fetch/proxy.go` (new: `parsePool`/`pool.get`/`pool.reportFailure`/`RedactProxy`/`{{session}}`/NO_PROXY carry-over), `fetch/http.go` (failover retry loop, `proxiedForHost` seam, socks5/socks5h scheme widening), `fetch/challenge.go` (new: `ChallengeError`/`DetectChallenge`), `fetch/utls.go` (7-value browser union, random over 6), `fetch/profiles.go` (header profiles safari/edge/ios/chrome_android), `fetch/rod.go` (`RodFetcher.Screenshot` + `fetch.ScreenshotPage`), `clean/clean.go` (`CleanedPage.HTML`), `clean/llm.go` (`Render` html case + PDF `<article>` fallback), `scrape/scrape.go` (PageFormat union, raw/screenshot, ChallengeError→rod escalation), `scrape/search.go` (new: `SearchHit`/provider registry/6 backends/scrape-top), `store/sqlite.go` (`run_history.proxy` migration), `cli/root.go` (`--proxy-file`), `cli/search.go` (new), `mcp/tools.go` (`search` tool #12), `testdata/challenge/*.html`, `testdata/search/*.json`
**Key Pattern:** **No new fakes — the pool is the SUT.** Tiering does the isolation: internal tables for the unexported pool glue (`ssrf_internal_test.go` precedent, extended by E's `utls_internal_test.go`), behavior tests through the exported `Fetch` seam over `httptest` (reusing `newProxyOrigin`, the reverse-proxy egress stand-in), one ~45-line `fakeSOCKS5` test helper so the Tor matrix row is *proven* rather than skipped, recorded fixture JSONs + package-var endpoint overrides for the six search backends, and `-tags browser` for everything only rod can witness (screenshot bytes, challenge→rod escalation, new-profile JA3s). Hit counters prove every "before/skip/failover" claim; grep assertions prove credential redaction.
**Dependencies:** stdlib `testing`, `net/http/httptest`, `net/http/httputil`, `net`, `strconv`, `io`, `bytes`, `os`, `errors`, `strings`, `sync/atomic`, `encoding/json` only — plus, in fetch tests only, `github.com/North-web-dev/impersonate-http` via the production import (UA cross-check) and in scrape tests `github.com/PuerkitoBio/goquery` for a raw-vs-cleaned discrimination assert. No test frameworks, no new test deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an operator with a vendor proxy pool, I want rotation, failover, sticky sessions, and redacted surfacing, so that blocked targets work without my credentials ever leaking | `fetch/proxy_internal_test.go` parse/strategy/cooldown tables; `fetch/proxy_test.go` failover via `newProxyOrigin` + closed port; `fetch/proxy_security_test.go` matrix; `store` migration test; `cli` `--proxy-file` tests | dead entry skipped 60s then restored; failover serves via entry 2 with entry-1 hits frozen; `run_history.proxy` == `host:port` (no userinfo); every pool error path greps clean of the password; malformed file → exit 2 naming the line |
| US-2 | As a user scraping a bot-blocked site, I want a typed challenge naming the vendor, so that I get "challenge: cloudflare", not garbage markdown or a false quality-block | `fetch/challenge_test.go` fixture table over `testdata/challenge/*.html` + rich-negative + PDF-negative; `fetch/profiles_test.go` warmup-gate rows; `scrape` typed-error surfacing; CLI exit pin | each vendor fixture → correct vendor string; rich article mentioning "Just a moment" → `""` (still cleans); bare-request challenge now warms up + retries (gate widened); `magpie scrape` on a challenge origin → exit 8, stderr names the vendor |
| US-3 | As a user evading fingerprinting breadth checks, I want `--browser safari\|edge\|ios\|chrome_android` with matching header bundles, so that mobile/mac profiles (what passes DataDome) are one flag away | `fetch/utls_internal_test.go` widened `TestResolveBrowser` (EDIT :108/:143) + random-over-6 membership; `TestHeaderProfiles_MatchLibrary` UA cross-check; `echoOrigin` wire-UA for a new profile; MCP/CLI union-message rows | 7-value union (`""`+6+`random`) valid, garbage → exit 2; each `HeaderProfiles[name]["User-Agent"]` == `impersonate.Profiles[name].Headers` UA (drift-proof both ways); `random` stable per process AND ∈ the 6 names |
| US-4 | As a user piping into other tools, I want `--page-format html\|raw\|screenshot`, so that cleaned HTML, untouched bytes, and full-page PNGs come out of the same command | `clean/llm_test.go` Render table (html + PDF wrapper); `clean/clean_test.go` HTML-scoping rows; `scrape` raw bytes-equality + screenshot validation rows; `-tags browser` PNG capture | html returns scoped cleaned HTML (excluded element absent); raw returns the origin's exact bytes (still quality-gated: challenge raw → typed error, not bytes); `screenshot`+`static` → exit 2 pre-I/O; PNG file starts with `\x89PNG` and IHDR dims parse |
| US-5 | As a user doing SERP-driven scraping, I want `magpie search` with six BYOK/no-key backends and `--scrape-top N`, so that query→SERP→scraped records is one JSONL stream | `scrape/search_internal_test.go` per-provider request-shape + hit-mapping tables over `testdata/search/*.json`; missing-key/unknown-provider pins; scrape-top e2e; MCP catalog | 6 providers mapped (brave/serper/serpapi/searxng/exa/duckduckgo) with correct method/path/headers/body per fixture; records in position order, first N embed page records; missing key → `keyHint` + exit 7; `TestToolCatalog` == 12 |

---

## 1. Component Mock Strategy

Phase type: **integration/security-boundary + pure logic** (E's successor on the same seam). Mock strategy in one sentence: **nothing gets a fake — the pool/challenge layer is tested through internal tables plus real HTTP on localhost (the pool must actually dial to be proven), search providers run against recorded fixture JSON with endpoint overrides (the API contract is the fixture), rod-only behavior is fenced in `-tags browser`, and counters/greps carry every negative proof.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `parsePool` | Internal unit table (package `fetch`, `proxy_internal_test.go`) — pure `[]byte → []poolEntry` | URL forms http/https/socks5/socks5h each parse; `host:port:user:pass` → `http://user:pass@host:port`; `#` comments + blank lines skipped; error message contains the **line number** and never the password; empty/all-comment file → loud error; `socks5h` normalizes to `socks5` at parse (pin + drift comment — stdlib treats them alike, docs say so) | US-1 |
| `pool.get` strategies | Internal unit — fixed entries, no network | round-robin: N calls cycle 1→2→3→1 (atomic counter, deterministic); sticky-host: same host twice → same entry (FNV stability pinned), 5 fixed hostnames across 3 entries touch **more than one** entry (deterministic spread, not statistics); dead entries (cooldown fresh) skipped | US-1 |
| cooldown + all-dead | Internal unit — `pool.cooldown` field set to 30ms in-test (package-internal), real clock sleeps ≤100ms | after `reportFailure(e)`: next `get` skips e; after 50ms sleep: e returns; **all** dead → error lists only `host:port` forms — `strings.Contains(err, "user:pass")` false for every entry (the grep belt) | US-1 |
| `{{session}}` templating | Internal unit — two entries with `{{session}}` | same target host twice → identical substituted URL (sticky sessions); different host → different token; token is 8 hex chars | US-1 |
| `RedactProxy` | Pure table | `http://u:p@h:1` → `h:1`; socks5 with userinfo → host:port; nil-safe | US-1 |
| Failover retry loop | Behavior: `GOMAGPIE_PROXY_FILE` with entry1 = `http://127.0.0.1:<closedPort>`, entry2 = `newProxyOrigin` → echo origin; exported `Fetch` | fetch succeeds via entry 2; entry1 connection attempts ≤ 1 (bounded retry, `min(3, len(pool))`); origin hit == 1; **4xx row**: single proxy whose origin returns 500 → NO second proxy attempt (proxyHits frozen at 1 — 4xx/5xx is a page outcome, not egress failure); success records redacted proxy into run_history | US-1 |
| `proxiedForHost` seam + NO_PROXY | Internal unit, `t.Setenv` | pool file set → non-nil for pool host; `NO_PROXY` naming that host → nil (peer check applies — safe default inherited); env-single still works when only `GOMAGPIE_PROXY` set (existing `TestProxy_*` rows stay green) | US-1 |
| Security matrix (G.7 gate) | `fetch/proxy_security_test.go` (external) — `fakeSOCKS5` (§3) + `newProxyOrigin` + `hitOrigin` request counters; six table rows, §7 skeleton | row1 no-proxy+loopback target → error AND origin request-hits==0; row2 env-proxy+private target (AllowPrivate) → allowed, proxyHits==1; row3 pool-http same → allowed; row4 pool `socks5://127.0.0.1:<fakeSOCKS5>` + **public** target URL (TEST-NET-1 `93.184.216.34`, no DNS needed) → served via the NAT'd local origin, socks conns==1 (peer check skipped BECAUSE proxied — the load-bearing trust row); row5 socks pool + loopback **target**, no AllowPrivate → error pre-dial, socks conns==0 (proxy ≠ license); row6 pool creds + forced failure → no error/log text contains the password | US-1 |
| `run_history.proxy` | Store test: hand-create the pre-G schema via raw SQL, `store.Open` → migrate; write a run with proxy | column exists after open (old DB upgrades in place); round-trip persists `127.0.0.1:8888`-shape value; empty default `''` for non-pooled runs | US-1 |
| `--proxy-file` flag | `cli` tests: `resetGlobals`/`captureOutput`/`codeOf` | flag present → env set (fetch-side test proves the env path; CLI asserts exit codes only): malformed file → exit **2** with line number, zero dials (closed port); valid → exit 0 on a `writeFileSite` page | US-1 |
| `DetectChallenge` | `fetch/challenge_test.go` — fixture-FILE table (`os.ReadFile` of `testdata/challenge/{cloudflare,turnstile,datadome,awswaf,hcaptcha}.html`, they are plan deliverables) + inline negatives | each fixture → exact vendor string; `cf-mitigated` header set on a RICH body → still cloudflare (header authoritative, no size gate); rich article mentioning "Just a moment" → `""`; PDF bytes → `""`; empty body → `""`; status 403 + tiny body w/o signatures → `""` at this layer (status stays `IsChallengePage`'s job — assert the split, don't duplicate it) | US-2 |
| Warmup gate widening | `newChallengeOrigin`+`challengeHits` (existing) — EDIT `TestChallenge_NoProfileNoRetry` (:179), flagged §5 | bare request (no Profile/Browser) on a challenge-classified page → homepage hit + retry (NEW contract); bare request on a **clean** page → no warmup (old contract survives for clean pages); retry request recorded with same Profile/Browser/Cookies | US-2 |
| `ChallengeError` surfacing (scrape) | scrape fake Fetcher returning `*ChallengeError`; `errors.As` at the boundary | `render=static` → typed error with vendor in message (not wrapped garbage); `render=auto` hermetic path → same typed error when no browser available (rod launch fails in sandbox → error MUST still name the vendor, never leak the launch noise as primary); exit mapping pinned at CLI (below) | US-2 |
| Challenge CLI/crawl | `cli` on a `newChallengeOrigin`-shaped origin (retry-exhausting variant); crawl mini-run over a challenge page | `magpie scrape` → exit **8**, stderr contains `cloudflare`; crawl page-error record contains the vendor string (Phase A hook) | US-2 |
| `resolveBrowser` 7-union | EDIT existing `TestResolveBrowser` (:102) — flagged §5 | `safari`→`HelloSafari_Auto` profile, `edge`/`ios`/`chrome_android` likewise (assert `ClientHelloID` equality vs `impersonate.Profiles`); garbage/`Chrome` (case) → false; `random` → stable per process AND ∈ 6 names (membership, not distribution) | US-3 |
| `browserClient` new names | Existing `TestBrowserClientCacheAndValidation` (:123) extended | `browserClient("safari")` now returns a cached client sharing the jar; the :143 "safari must fail loud" row inverts (flagged) | US-3 |
| Header profiles ×4 | `TestHeaderProfiles_MatchLibrary` — cross-check, no pinned UA strings | for each of the 4 new names: `HeaderProfiles[n]["User-Agent"]` == `impersonate.Profiles[n].Headers.Get("User-Agent")` (catches drift in either direction); existing 3 profiles untouched (goldens stay green) | US-3 |
| Wire UA for new profile | `echoOrigin` + `Browser:"ios"` on cleartext | echo shows `HeaderProfiles["ios"]["User-Agent"]` (stock-client cleartext rule, E precedent) | US-3 |
| `Render` html + PDF wrapper | Extend the existing `clean/llm_test.go` Render table | `html` → returns `p.HTML` verbatim; `HTML==""` (PDF) → `<article>` wrapper containing the markdown (exact wrapper shape pinned once); all 5 old cases byte-identical (table rows unchanged) | US-4 |
| `CleanedPage.HTML` | `clean_test.go` append: article fixture + include/exclude scope | `HTML` non-empty, contains main content; excluded subtree absent (scoping applied BEFORE serialization); `Markdown`/goldens unchanged (`testdata/clean` drift empty); PDF page → `HTML==""` | US-4 |
| `raw` format | scrape fake Fetcher serving known bytes | `PageFormat:"raw"` → `Result.Content` **bytes-equal** to the served body (not markdown of it); quality still ran: challenge body + raw → typed error, never raw passthrough of a challenge; `raw` survives `ValidateOptions` | US-4 |
| `screenshot` validation | scrape/CLI validation rows, no browser | `screenshot`+`render=static` → OptionsError exit 2; `screenshot`+auto → OK; crawl+screenshot → exit 2; MCP jsonschema string updated | US-4 |
| Screenshot capture | `-tags browser`: local origin page, `fetch.ScreenshotPage` + `--viewport` | bytes start `\x89PNG`; IHDR width/height parse from bytes 16–24 and are non-trivial; viewport flag accepted (pixel-diff assertions banned — flake); `--out` writes a file, absence → base64 in result JSON | US-4 |
| Search providers ×6 | `scrape/search_internal_test.go` (package `scrape` — internal because endpoint package-vars are the override seam) — `httptest` serving `testdata/search/*.json` per provider | per provider: request shape (brave: GET + `X-Subscription-Token`; serper: POST JSON + `X-API-KEY`; serpapi: GET `api_key`+`engine=google`; searxng: GET `{base}/search?format=json` no key; exa: POST JSON + `x-api-key`; duckduckgo: GET html endpoint) asserted against the recorded request; hits mapped from fixture (position/title/url/snippet); zero-result fixture → empty slice, nil error; malformed JSON → error naming the provider | US-5 |
| Search validation/keys | internal table + CLI exits | unknown provider → error naming valid set; searxng without `GOMAGPIE_SEARXNG_URL` → typed error; brave without key → `ErrMissingKey`-class → `keyHint` → CLI exit **7** (pin in `exitfor_test.go`); `t.Setenv` for every key read — no real key ever consulted | US-5 |
| scrape-top e2e | internal: fixture SERP whose hit URLs point at local origins; `scrape.Batch` underneath | 5 hits + `--scrape-top 2` → 5 JSONL records in position order; records 1–2 embed page records (`page` key), 3–5 don't; scrape failures don't drop the SERP record (page key omitted, record survives) | US-5 |
| MCP `search` tool | `dialInMemory`/`callTool` (existing) — hermetic paths only | catalog == **12** (EDIT `TestToolCatalog`, flagged §5 — a tool was ADDED, the count edit is legitimate this phase); `search` without key → typed missing-key error text; unknown provider → error naming valid set; `page_format` union errors name the new values | US-5 |
| CLI `search` command | `resetGlobals`/`captureOutput`/`codeOf` — exits only (endpoint override lives in scrape's internal file; CLI tests never hit network) | `--provider bogus` → exit 2 naming set; brave without key → exit 7 + hint text; `--scrape-top -1` → exit 2; help lists all flags | US-5 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (default `go test ./...`) | Loopback `httptest` (echo/challenge/proxy-stand-in/origins), `fakeSOCKS5` (127.0.0.1-only, §3), fixture files from `testdata/challenge/` + `testdata/search/` (local reads), temp-dir SQLite, in-test sleeps ≤100ms for cooldown — **no external network, no DNS, no browser** | <60s | Every push; the only default gate |
| Smoke (`go test -tags browser ./fetch/ ./scrape/`) | Real network (tls.peet.ws JA3s for safari/edge/ios), real rod (ScreenshotPage PNG, challenge→rod escalation e2e) | ~10s | Manual/pre-release + network-enabled CI job; joins `utls_smoke_test.go`/`rod_smoke_test.go` in the gated file family |
| Manual (not a test file) | Built binary: real vendor pool file, real Tor (`socks5://127.0.0.1:9050`), a genuinely blocked site, a real SERP key | minutes | Pre-release eyeball: fixtures prove contract shape; only the live web proves unblocking — and that was never a CI claim |

Fixture-file note (differs from Phase E on purpose): `testdata/challenge/` and `testdata/search/` are **plan deliverables**, not incidental blobs — so the E-era `git status --porcelain testdata/` empty rule is replaced by: exactly those two dirs may appear; `testdata/clean` drift stays empty.

---

## 3. Fake / Mock Implementations

**No fakes.** The pool and challenge layer are the system under test — faking the transport would re-prove our own wiring (Phase E's stated reason, unchanged). The search "fakes" are recorded JSON fixtures already named as deliverables in phase-G.md §5; the suite serves them from `httptest` and swaps endpoints via package vars. The one new **helper** worth full source is the SOCKS5 stand-in, because matrix row 4 (Tor path) must be *proven*, not skipped.

### `fakeSOCKS5` — the only load-bearing new helper (fetch/proxy_security_test.go)

```go
// fakeSOCKS5 starts a minimal no-auth SOCKS5 CONNECT server. It NATs every
// requested address to the local upstream (test-only egress stand-in: the
// client may ask for any host, the dial always lands on `upstream`), records
// the requested addr + connection count, and refuses to dial anything but
// 127.0.0.1 itself — the test helper must not become the suite's own SSRF hole.
func fakeSOCKS5(t *testing.T, upstream *url.URL) (proxyURL string, conns, requested *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() }) //nolint:errcheck // recorder teardown
	var conns, requested atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close() //nolint:errcheck // test server
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
					return
				}
				methods := make([]byte, int(hdr[1]))
				_, _ = io.ReadFull(c, methods)
				_, _ = c.Write([]byte{5, 0}) // no-auth
				req := make([]byte, 4)
				if _, err := io.ReadFull(c, req); err != nil || req[1] != 1 { // CONNECT only
					return
				}
				var host string
				switch req[3] {
				case 1: // IPv4
					b := make([]byte, 4)
					_, _ = io.ReadFull(c, b)
					host = net.IP(b).String()
				case 3: // domain
					n := make([]byte, 1)
					if _, err := io.ReadFull(c, n); err != nil {
						return
					}
					d := make([]byte, n[0])
					_, _ = io.ReadFull(c, d)
					host = string(d)
				default:
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(c, port); err != nil {
					return
				}
				requested.Add(1)
				conns.Add(1)
				up, err := net.Dial("tcp", upstream.Host) // NAT: always the local origin
				if err != nil {
					_, _ = c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0}) // general failure
					return
				}
				defer up.Close() //nolint:errcheck // test server
				_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}) // success
				go func() { _, _ = io.Copy(up, c) }()                //nolint:errcheck
				_, _ = io.Copy(c, up)                                //nolint:errcheck
			}(c)
		}
	}()
	return "socks5://" + ln.Addr().String(), &conns, &requested
}
```

**Matches real call:** the pool stores `socks5://127.0.0.1:<port>` exactly as a user's Tor entry would read; `http.Transport` (scheme widened in G.1) and `impersonate.ProxyDialer` both dial it with a standard CONNECT — the handshake above is the real protocol, not a stub. `TestFakeSOCKS5_SelfCheck` runs FIRST: one proxied GET through the fake to a local origin (requested addr = the origin's `127.0.0.1:port`) — so a broken helper fails loudly as a helper bug, never as a matrix-row bug.

### In-test helpers (small, at point of use)

```go
// fetch/proxy_internal_test.go (package fetch) — parsePool/pool tables; sets pool.cooldown
//   directly (30ms) for the restore test; calls RedactProxy/substitution internals.
// fetch/proxy_security_test.go (package fetch_test) — matrix (§7) + TestFakeSOCKS5_SelfCheck;
//   fetchers built with the explicit-options constructor (relaxedFetcher where a row needs
//   AllowPrivate, per TestProxy_Hit precedent).
// scrape/search_internal_test.go (package scrape) — setSearchEndpoints(t, base) swaps the
//   package-var endpoints to httptest URLs via t.Cleanup restore; serveFixture(t, name) reads
//   testdata/search/<name>.json and asserts each incoming request's shape before answering.
```

### Reused verbatim (read first, never modify)

- `newProxyOrigin` (fetch/proxy_test.go:25), `hitOrigin`/`relaxedFetcher` (same file, per `TestProxy_Hit`)
- `newChallengeOrigin`/`challengeHits` (fetch/profiles_test.go:22,13), `echoOrigin` (:50), `fetchBody` (:59)
- `acceptRecorder` (fetch/utls_internal_test.go) — dial-vs-request distinctions in the matrix
- `openScrapeDB`/`fakeDeps`/`fakeExtractor` (scrape/scrape_test.go:71,94)
- `dialInMemory`/`callTool`/`TestToolCatalog` (mcp/agent_test.go:153ff)
- `resetGlobals`/`captureOutput` (cli/cmd_test.go:20,25), `codeOf` (cli/scrape_test.go:144)

---

## 4. Test File List

```
gomagpie/
├── fetch/
│   ├── proxy.go                       # DELIVERABLE (impl)
│   ├── proxy_internal_test.go         # NEW (package fetch): parsePool table (forms/comments/line-numbers/
│   │                                  #   socks5h-normalize/redaction), strategies, cooldown restore, {{session}}, RedactProxy
│   ├── proxy_test.go                  # EXTEND (named in phase plan): failover loop (closed port → newProxyOrigin),
│   │                                  #   4xx-not-egress row, NO_PROXY×pool row — TestProxy_Hit/NoProxyBypass/InvalidValue/
│   │                                  #   RobotsViaProxy/DialGuardSkipsProxyPeer stay green untouched
│   ├── proxy_security_test.go         # NEW (package fetch_test): TestFakeSOCKS5_SelfCheck + the 6-row G.7 matrix
│   ├── challenge_test.go              # NEW (package fetch_test): fixture-file vendor table + header-authoritative row
│   │                                  #   + rich-negative + PDF-negative + status-split row
│   ├── utls_internal_test.go          # EDIT :102 TestResolveBrowser (safari/edge/ios/chrome_android → valid;
│   │                                  #   :108 invalid-loop trims them) + EDIT :143 row (safari now cached-valid)
│   │                                  #   + APPEND random-membership-over-6 + TestHeaderProfiles_MatchLibrary
│   ├── utls_test.go                   # APPEND: wire-UA echo for one new profile (cleartext rule)
│   ├── utls_smoke_test.go             # APPEND (//go:build browser): JA3 rows for safari/edge/ios vs recorded constants
│   └── rod_smoke_test.go or rod_test.go # APPEND (browser-gated): ScreenshotPage PNG magic + IHDR dims + viewport
├── clean/
│   ├── llm_test.go                    # APPEND: Render html case + PDF <article> wrapper + old-cases-unchanged rows
│   └── clean_test.go                  # APPEND: CleanedPage.HTML populated, scope-excluded subtree absent, PDF→empty
├── scrape/
│   ├── scrape_test.go                 # APPEND: raw bytes-equality + challenge-raw-still-typed + screenshot
│   │                                  #   validation rows (static-conflict exit 2) + ChallengeError surfacing (errors.As,
│   │                                  #   vendor in message, launch-noise subordinate)
│   └── search_internal_test.go        # NEW (package scrape): 6 provider tables over testdata/search fixtures,
│                                      #   request-shape asserts, endpoint override, zero-result + malformed rows,
│                                      #   key/provider validation, scrape-top e2e (position order, page-embed count)
├── store/
│   └── (existing test file)           # APPEND: run_history.proxy migration (old schema → column) + round-trip
├── cli/
│   ├── cmd_test.go                    # APPEND: --proxy-file malformed → exit 2 line-number (zero dials); valid → exit 0
│   ├── exitfor_test.go                # APPEND: search missing-key → exit 7
│   └── search cmd tests (cmd_test or new search_test.go) # APPEND: --provider bogus → 2, --scrape-top -1 → 2, help rows
├── mcp/
│   └── agent_test.go                  # EDIT TestToolCatalog 11→12 (flagged) + APPEND: search missing-key/unknown-provider
│                                      #   errors, page_format union error text
├── testdata/
│   ├── challenge/*.html               # DELIVERABLE: 5 vendor fixtures (<15KB, under the gate) + rich-negative
│   └── search/*.json (+ .html for DDG)# DELIVERABLE: recorded response shapes, dated header comment each
└── plan/phase-G-tests.md              # this file
```

Every deliverable in `plan/phase-G.md` §5 maps: `fetch/proxy.go`→proxy_internal+proxy_test+security; `fetch/http.go`→proxy_test failover + security matrix + profiles_test gate rows; `fetch/challenge.go`→challenge_test + scrape/mcp/cli surfaces; `fetch/utls.go`→utls_internal edits+appends; `fetch/profiles.go`→MatchLibrary + echo; `fetch/rod.go`→browser-gated rod rows; `clean/clean.go`/`clean/llm.go`→clean_test+llm_test; `scrape/scrape.go`→scrape_test; `scrape/search.go`→search_internal_test; `store/sqlite.go`→store append; `cli/root.go`+`cli/search.go`→cmd_test+exitfor_test; `mcp/tools.go`→agent_test; `README.md`/`spec.md`→no tests (docs).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

Per Phase A–F precedent: per-package test files, hermetic default suite, no integration-tier flag (the `-tags browser` build tag IS the gate). New internal files this phase — both citing precedent in their headers:

- `fetch/proxy_internal_test.go` (`package fetch`) — the pool's unexported glue is where G.1's security-adjacent decisions live (`parsePool` redaction, cooldown, substitution); same justification as `ssrf_internal_test.go`/`utls_internal_test.go`.
- `scrape/search_internal_test.go` (`package scrape`) — the endpoint package-vars are the override seam; exporting them for tests would widen production API. All search tests live in this one file (external `scrape_test` can't reach the vars — that's WHY it's internal, say so in the header).

**Flagged edits — the legitimate categories (implementation-driven contract changes; everything else append-only):**

1. `fetch/utls_internal_test.go:102-122` — `TestResolveBrowser`: safari/edge/ios/chrome_android move from the invalid loop (:108) to valid rows with `ClientHelloID` asserts; `:143-144` "safari must fail loud" row inverts to cache-hit.
2. `fetch/profiles_test.go:179` — `TestChallenge_NoProfileNoRetry`: G.2's gate widening inverts the bare-request-challenge rows (now warmup+retry); bare-request-CLEAN rows keep the old expectation. Split, don't delete.
3. `scrape/validate_test.go:29` — page-format union message widens to `markdown|llm|text|json|html|raw|screenshot`.
4. `scrape/validate_test.go:34` — browser union message widens to the 7-value set; same strings wherever CLI/MCP tests pin them.
5. `mcp/agent_test.go:153` — `TestToolCatalog` 11→12 (a tool was added; same legitimacy rule as Phase C's additions).
6. `fetch/proxy_test.go` — extend in place (named deliverable); existing five tests run unmodified.

Anything ELSE failing to compile against new signatures is a plan gap — flag it, don't refactor silently.

Scope rationale: everything function-scoped (`t.TempDir()` fixture files, per-test `httptest`, `t.Setenv` for `GOMAGPIE_PROXY*`/`GOMAGPIE_SEARXNG_URL`/key vars, per-test atomics). The pool's parse cache MUST be keyed by the `GOMAGPIE_PROXY_FILE` value (distinct tmp path ⇒ distinct pool) — this is the testability-driven clarification to phase-G.md's "parse once": once per env *value*, not once per process, or `t.Setenv` tests poison each other. Cooldown tests set the (internal) cooldown field to 30ms and sleep ≤100ms — no clock abstraction, no long waits.

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| No fakes for pool/challenge | Internal tables + real loopback HTTP + `-tags browser` for rod-only proofs | The pool must actually dial to be proven; a fake transport would re-verify our own wiring while proving nothing about CONNECT/SOCKS handling (Phase E's tiering rationale, unchanged) |
| `fakeSOCKS5` with NAT-to-upstream | Client may request any addr; dial always lands on the local origin; requested-addr + conn counters | Makes matrix row 4 (public target via loopback Tor) fully hermetic — no DNS, no public dial from CI — while exercising the real SOCKS5 handshake; the 127.0.0.1-only dial guard keeps the helper from being its own SSRF hole |
| Pool cache keyed by env value | `GOMAGPIE_PROXY_FILE` value change ⇒ re-parse; `t.Setenv` + `t.TempDir` give natural isolation | A process-once singleton would poison parallel/sequential tests and force a reset hook; keyed-once is the same laziness with testability for free (flagged clarification to phase-G §G.1) |
| Redaction as grep belt, not spot check | One table-driven test iterates every pool error site (parse, all-dead, per-request) asserting the password substring is absent | Webclaw got burned on exactly this; error paths multiply, and one missed `fmt.Errorf("%s", entry.raw)` leaks creds — the grep makes the whole class testable |
| 4xx/5xx ≠ egress failure, proven by frozen counters | Origin returns 500 through proxy 1 → assert proxyHits stays 1 (no second entry tried) | Failover on page outcomes would silently mask site errors as proxy churn and burn vendor bandwidth; the counter distinguishes "tried once" from "cycled the pool" (counters-over-error-text, Phase D lesson) |
| Challenge fixtures as files, negatives inline | `testdata/challenge/*.html` are deliverables loaded by the table; rich-negative + PDF-negative are inline strings | The fixtures double as the `-tags browser` escalation e2e payload and as user-visible examples; inline negatives keep the size-gate boundary visible right next to the assertions |
| Header-profile drift caught by cross-check | Assert `HeaderProfiles[n]["User-Agent"]` == `impersonate.Profiles[n].Headers` UA for the 4 new names | Pinning the UA strings twice guarantees eventual drift between the header bundle and the TLS fingerprint — the mismatch is exactly what DataDome-class WAFs score |
| Screenshot asserts bytes, not pixels | `\x89PNG` magic + IHDR dims (bytes 16–24) parse; viewport row asserts acceptance, not pixel equality | Pixel/size comparisons flake across Chrome versions and CI GPUs; the magic-byte + header parse proves "real PNG of some page" — the honest boundary for a smoke tier |
| Search providers tested at the seam, CLI at the exits | Provider request-shape + mapping in `scrape/search_internal_test.go` (endpoint vars overridable); CLI tests pin only exits/help | Endpoint vars are unexported by design (no prod API widening); CLI e2e through them would need exported knobs — the exit codes + hint text are the CLI's actual contract |
| Catalog count edit is legitimate this phase | `TestToolCatalog` 11→12 in the flagged list | Phase C's stale-count lesson cuts both ways: counts rot when tools are added — and G.5 adds one, so the edit is planned, not silent |
| Matrix file named in the phase plan is the gate | `proxy_security_test.go` runs in the default suite; G.7's go/no-go reads its result | The one HIGH-risk item (egress trust semantics) must be provable without flags, browsers, or network — six loopback rows do that in milliseconds |
| Baseline RUN count, then strictly more | `tee /tmp/phaseG-baseline.log` BEFORE writing (testing.md non-vacuous rule) | Edited files (flagged list) can hide deletions; the RUN delta proves net-new coverage |

---

## 7. Example Test Case

```go
// fetch/proxy_security_test.go — the G.7 gate. External package: drives the
// exported Fetch seam + env + pool files, so it tests what an operator gets.
package fetch_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gomagpie/fetch"
)

func poolFile(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "proxies.txt")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestProxy_SecurityMatrix pins the egress trust semantics across proxy
// sources. Any row that can only pass by weakening dialPeerAllowed or
// ValidateURL is a STOP per phase-G §6 — revert the pool, keep G.2–G.6.
func TestProxy_SecurityMatrix(t *testing.T) {
	pubTarget := "http://93.184.216.34/" // TEST-NET-1: public IP, never dialed directly (row 4 NATs it)

	t.Run("no proxy / loopback target rejected pre-request", func(t *testing.T) {
		var hits atomic.Int64
		origin := hitOrigin(t, &hits, func(w http.ResponseWriter, _ *http.Request) {})
		t.Setenv("GOMAGPIE_PROXY", "")
		t.Setenv("GOMAGPIE_PROXY_FILE", "")
		f := relaxedFetcher(t) // AllowPrivate mirrors the operator opt-in path
		_, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL})
		if err == nil {
			t.Fatal("loopback target without proxy must fail")
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("origin request hits = %d, want 0 (peer check precedes any HTTP byte)", n)
		}
	})

	t.Run("env single proxy / private target allowed", func(t *testing.T) {
		var originHits, proxyHits atomic.Int64
		origin := hitOrigin(t, &originHits, func(w http.ResponseWriter, _ *http.Request) {})
		t.Setenv("GOMAGPIE_PROXY", newProxyOrigin(t, &proxyHits, origin.URL))
		t.Setenv("GOMAGPIE_PROXY_FILE", "")
		f := relaxedFetcher(t)
		if _, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL}); err != nil {
			t.Fatalf("operator-chosen egress must be trusted: %v", err)
		}
		if proxyHits.Load() != 1 || originHits.Load() != 1 {
			t.Errorf("proxy/origin hits = %d/%d, want 1/1", proxyHits.Load(), originHits.Load())
		}
	})

	t.Run("pool tor socks5 / public target served, loopback target still rejected", func(t *testing.T) {
		var originHits atomic.Int64
		origin := hitOrigin(t, &originHits, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("via tor")) //nolint:errcheck // test server
		})
		socks, conns, requested := fakeSOCKS5(t, mustURL(t, origin.URL))
		t.Setenv("GOMAGPIE_PROXY", "")
		t.Setenv("GOMAGPIE_PROXY_FILE", poolFile(t, socks))

		f := relaxedFetcher(t)
		resp, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: pubTarget})
		if err != nil || string(resp.HTML) != "via tor" {
			t.Fatalf("tor-style pool entry must serve public targets: err=%v", err)
		}
		if conns.Load() != 1 || requested.Load() != 1 {
			t.Errorf("socks conns/requested = %d/%d, want 1/1", conns.Load(), requested.Load())
		}

		// Proxy ≠ license: loopback TARGET through the same pool is still
		// ValidateURL-rejected before the proxy is ever contacted.
		_, err = f.Fetch(t.Context(), fetch.FetchRequest{URL: origin.URL})
		if err == nil {
			t.Fatal("loopback target via pool must stay rejected")
		}
		if conns.Load() != 1 {
			t.Errorf("socks conns = %d, want still 1 (rejection is pre-dial)", conns.Load())
		}
	})

	t.Run("credentials never surface", func(t *testing.T) {
		const secret = "hunter2password"
		t.Setenv("GOMAGPIE_PROXY", "")
		t.Setenv("GOMAGPIE_PROXY_FILE", poolFile(t,
			"http://cust:"+secret+"@127.0.0.1:1", // dead port → forced failure path
			"http://127.0.0.1:1",                 // also dead → all-dead error path
		))
		f := relaxedFetcher(t)
		_, err := f.Fetch(t.Context(), fetch.FetchRequest{URL: pubTarget})
		if err == nil {
			t.Fatal("all-dead pool must error")
		}
		for _, surface := range []string{err.Error()} {
			if strings.Contains(surface, secret) {
				t.Errorf("credential %q leaked in: %s", secret, surface)
			}
		}
	})
}
```

Notes for the executor: `hitOrigin`/`relaxedFetcher`/`newProxyOrigin` are the existing helpers in `fetch/proxy_test.go` — reuse, don't restate. `mustURL` is a 3-line wrapper. Rows 2–3 of the phase plan's six collapse into the parameterized env/pool rows shown (same trust decision, different source — keep both if the implementation branches on source). The pubTarget IP must never actually dial: `fakeSOCKS5` NATs it, and if a regression dials it directly the test fails on connection error — which is the correct failure.

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for Phase G of `gomagpie` — proxy pool, typed bot-challenge detection, TLS profile breadth, html/raw/screenshot formats, and the `search` command. Repo: `/home/domidex/projects/gomagpie`. Read `plan/phase-G.md` (design), `plan/phase-G-tests.md` (this suite's contract — §3 helper, §5 flagged edits, §6 decisions, §7 gate), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test. Production code may or may not exist yet — write tests against the frozen names in phase-G §§3–5 and this plan §1; if a name diverges, rename the TEST to the implementation ONLY when the behavior matches the plan, else flag the gap. Package conventions: new `fetch/proxy_internal_test.go` is `package fetch` (ssrf_internal_test.go precedent — say so in its header); new `fetch/proxy_security_test.go` + `fetch/challenge_test.go` are `package fetch_test`; new `scrape/search_internal_test.go` is `package scrape` (endpoint package-vars are the override seam — state that in its header); `cli` stays internal.

### What This Project Is
Go 1.26 CLI web scraper (`magpie`, module `gomagpie`): fetch → clean → extract. Phase G adds a BYO proxy pool (round-robin/sticky-host, 60s cooldown failover, `{{session}}`, credential redaction, `run_history.proxy`), typed bot-challenge detection (`ChallengeError` naming cloudflare/turnstile/datadome/awswaf/hcaptcha), 4 new TLS browser profiles, `html|raw|screenshot` page formats, and a 6-backend BYOK `search` command with `--scrape-top`. One rule above all: the egress trust semantics (`proxiedForHost` → `dialPeerAllowed` skip, `ValidateURL` target governance) must survive the refactor EXACTLY — the security matrix is the phase gate. Tests are hermetic by default: no external network, no DNS, no browser in `go test ./...`.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | pool rotation/failover/sticky/redaction | pool tables + failover rows + security matrix + store migration + CLI flag tests | dead entry skipped 60s; failover via entry 2; `run_history.proxy` redacted; no password in any error; malformed file → exit 2 + line number |
| US-2 | typed challenge, not garbage | fixture table + warmup-gate rows + scrape/cli surfaces | 5 vendors named; rich-negative cleans; bare-request challenge warms up (gate widened); challenge scrape → exit 8 naming vendor |
| US-3 | safari/edge/ios/chrome_android profiles | widened TestResolveBrowser + MatchLibrary cross-check + wire-UA echo | 7-value union; UA bundles == library profiles; random stable ∈ 6 |
| US-4 | html/raw/screenshot | Render table + CleanedPage.HTML scoping + raw bytes-equality + screenshot validation + browser-gated PNG | scoped HTML excludes; raw is exact bytes, still gated; screenshot+static → exit 2; PNG magic + IHDR parse |
| US-5 | search: 6 backends + scrape-top | provider tables over fixtures + validation pins + scrape-top e2e + MCP catalog | request shapes per provider; position-ordered records with page embeds; missing key → exit 7; catalog == 12 |

### Why There Are No Fakes
- The pool and challenge layer are the system under test; faking the transport would verify our wiring while proving nothing about CONNECT/SOCKS/challenge handling. Isolation is structural: loopback httptest for HTTP, `fakeSOCKS5` (§3 verbatim) for the Tor row, `-tags browser` for rod-only proofs.
- Search backends are contract-tested against `testdata/search/*.json` fixtures (plan deliverables) served by httptest; the API "mock" is the recorded shape.
- Everything else reuses: `newProxyOrigin`/`hitOrigin`/`relaxedFetcher` (fetch/proxy_test.go), `newChallengeOrigin`/`challengeHits`/`echoOrigin`/`fetchBody` (fetch/profiles_test.go), `acceptRecorder` (fetch/utls_internal_test.go), `openScrapeDB`/`fakeDeps` (scrape/scrape_test.go), `dialInMemory`/`callTool` (mcp/agent_test.go), `resetGlobals`/`captureOutput` (cli/cmd_test.go), `codeOf` (cli/scrape_test.go:144).

### What NOT to Test
- Don't test impersonate-http/utls internals — new-profile coverage is the ClientHelloID mapping, the UA cross-check, and (smoke tier) JA3 constants; nothing deeper.
- Don't test go-rod internals — the PNG magic-byte + IHDR parse IS the screenshot proof; pixel comparisons are banned.
- Don't test provider APIs beyond the recorded fixture shapes — live SERP behavior is a manual-tier claim, never a CI claim.
- Don't duplicate `IsChallengePage` status logic in `DetectChallenge` tests — assert the SPLIT (header authoritative; body signatures size-gated; statuses stay with the existing function).
- Don't reset the pool cache via hooks — isolation comes from keying the cache on the `GOMAGPIE_PROXY_FILE` value; if the implementation made it process-once, that's a plan deviation to flag, not to work around with sync tricks.
- Don't touch `testdata/clean` (drift must stay empty) and don't add fixture files beyond `testdata/challenge/` + `testdata/search/`.
- Don't modify existing test funcs outside the flagged list (§5 items 1–6). Anything else failing to compile is a PLAN GAP — flag it.
- Don't test README/spec prose; don't add a shared testutil package (10-line helpers at point of use).

### Critical: The One Load-Bearing Helper
`fakeSOCKS5` — full source in this plan §3; copy verbatim into `fetch/proxy_security_test.go`. NATs every CONNECT to the local upstream, records requested-addr + conn counts, dials 127.0.0.1 only. `TestFakeSOCKS5_SelfCheck` (one proxied GET through it) runs before the matrix. Then copy §7's matrix skeleton verbatim — it is the G.7 go/no-go artifact; any row that passes only by weakening `dialPeerAllowed`/`ValidateURL` means STOP (revert the pool, keep the rest).

### Test Files to Create / Edit

```
fetch/proxy_internal_test.go   # NEW (~8): parsePool table (forms, comments, line numbers, socks5h normalize,
                               #   redaction), round-robin/sticky tables, cooldown skip+restore (30ms), {{session}},
                               #   RedactProxy, NO_PROXY×pool, proxiedForHost
fetch/proxy_test.go            # EXTEND (~3): failover loop (closed port → newProxyOrigin, bounded attempts),
                               #   4xx-not-egress row (proxyHits frozen), run_history.proxy end-to-end; 5 existing rows untouched
fetch/proxy_security_test.go   # NEW (~2): TestFakeSOCKS5_SelfCheck + TestProxy_SecurityMatrix (§7 verbatim, 6 rows)
fetch/challenge_test.go        # NEW (~4): fixture-file vendor table, header-authoritative row, rich-negative,
                               #   PDF-negative, status-split row
fetch/utls_internal_test.go    # EDIT :102/:108/:143 (flagged) + APPEND (~2): random-membership-over-6,
                               #   TestHeaderProfiles_MatchLibrary
fetch/utls_test.go             # APPEND (~1): wire-UA echo for ios (cleartext rule)
fetch/utls_smoke_test.go       # APPEND (browser tag, ~1): JA3 rows safari/edge/ios
fetch/rod smoke/test file      # APPEND (browser tag, ~2): ScreenshotPage PNG magic + IHDR + viewport + --out path
clean/llm_test.go              # APPEND (~2): Render html + PDF <article> wrapper + unchanged-legacy rows
clean/clean_test.go            # APPEND (~2): CleanedPage.HTML populated/scope-excluded/PDF-empty
scrape/scrape_test.go          # APPEND (~4): raw bytes-equality, challenge-raw typed, screenshot validation,
                               #   ChallengeError surfacing (errors.As, vendor in message)
scrape/search_internal_test.go # NEW (~10): per-provider request-shape + mapping tables (6 fixtures),
                               #   zero-result, malformed, searxng-no-URL, missing-key, unknown-provider,
                               #   scrape-top e2e (order + embed count + failure-survives)
store tests                    # APPEND (~1): run_history.proxy migration + round-trip
cli/cmd_test.go                # APPEND (~4): --proxy-file malformed→2 (zero dials)/valid→0; search bogus→2,
                               #   scrape-top -1→2, help rows
cli/exitfor_test.go            # APPEND (~1): search missing key → 7
mcp/agent_test.go              # EDIT TestToolCatalog 11→12 (flagged) + APPEND (~2): search missing-key/unknown-provider,
                               #   page_format union error text
```

### Per-File Coverage Guidance (beyond the table)
- **proxy_internal_test.go**: round-robin asserts the exact cycle 1→2→3→1; sticky asserts same-host stability AND that 5 fixed hostnames touch >1 entry (deterministic — no randomness in the hash); cooldown sets the internal field to 30ms, sleeps 50ms for the restore row; the `{{session}}` token regex is `^[0-9a-f]{8}$`.
- **challenge_test.go**: load each fixture with `os.ReadFile(filepath.Join("testdata", "challenge", name+".html"))` — a missing file is a test failure (fixtures are deliverables); the rich-negative string must exceed `clean.ThinPageWords` visible words AND mention "Just a moment" — assert `DetectChallenge` returns `""` AND `clean.Clean` still yields non-empty markdown for it.
- **search_internal_test.go**: `serveFixture` asserts request shape BEFORE writing the fixture body (wrong header/path → t.Fatal with the observed vs expected header map); DDG fixture is HTML — include a zero-result variant; scrape-top failure row: one hit URL → closed port, assert the SERP record survives without a `page` key.
- **scrape_test.go screenshot rows**: validation-only in the default suite (`screenshot`+`static` → `*scrape.OptionsError` via `errors.As`, message names both values); the bytes themselves are browser-tier.
- **Anti-vacuous rule**: every "before X" claim pairs an error/exit assert with a counter assert (dials==0, proxyHits frozen, socks conns unchanged) — error text alone never proves sequencing.

### Data Model Notes (Go)
- `errors.Is`/`errors.As` in-process (`*fetch.ChallengeError`, `*scrape.OptionsError`); `codeOf`+stderr at the CLI edge.
- All env via `t.Setenv` (includes `GOMAGPIE_PROXY`, `GOMAGPIE_PROXY_FILE`, `GOMAGPIE_PROXY_STRATEGY`, `GOMAGPIE_SEARXNG_URL`, provider keys) — never `os.Setenv`.
- Pinned constants (union messages, JA3 hashes, `50<<20`-class numbers) carry drift comments: update in the same commit as the intentional change.
- Fixture JSON files get a one-line `// recorded 2026-09-18 from <provider docs>` header comment.

### Success Criteria
- `go test ./...` exits 0; RUN count strictly exceeds the pre-phase baseline (`tee /tmp/phaseG-baseline.log` FIRST; record the post-F number)
- `go test -tags browser ./fetch/ -run 'TestScreenshot|TestJA3' -v` passes manually with network
- `git status --porcelain testdata/` shows ONLY `testdata/challenge/` + `testdata/search/`; `testdata/clean` drift empty
- `grep -rn "impersonate" --include="*_test.go" . | grep -v fetch/` returns nothing (import locality)
- No test performs a real external DNS lookup or TCP dial outside 127.0.0.1 (matrix + fakeSOCKS5 NAT guarantee it structurally)
- Full gates green: `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, 3-way `CGO_ENABLED=0` cross-builds

---

## 9. Run Commands

```bash
# Baseline BEFORE writing (non-vacuous gate — testing.md)
go test ./... -v 2>&1 | tee /tmp/phaseG-baseline.log; grep -c '^=== RUN' /tmp/phaseG-baseline.log

# Fast hermetic suite (every push — loopback only, no browser, no DNS)
go test ./...

# New-test count vs baseline
go test ./... -v 2>&1 | tee /tmp/phaseG-tests.log; grep -c '^=== RUN' /tmp/phaseG-tests.log

# Focused per task (mirrors phase-G sanity checks)
go test ./fetch/ -run 'TestParsePool|TestPool|TestRedactProxy|TestSession' -v                    # G.1 pool tables
go test ./fetch/ -run 'TestProxy_|TestFailover|TestRunHistory' -v                                # G.1 failover + store
go test ./fetch/ -run 'TestProxy_SecurityMatrix|TestFakeSOCKS5' -v                               # G.7 GATE
go test ./fetch/ -run 'TestDetectChallenge' -v                                                   # G.2 vendor table
go test ./fetch/ -run 'TestChallenge_' -v                                                        # G.2 warmup-gate rows (edited)
go test ./fetch/ -run 'TestResolveBrowser|TestHeaderProfiles|TestBrowserClient' -v               # G.3 union + cross-check
go test ./clean/ -run 'TestRender|TestClean' -v                                                  # G.4 html/PDF wrapper
go test ./scrape/ -run 'TestScrape_Raw|TestScrape_Screenshot|TestScrape_Challenge' -v            # G.4/G.2 scrape rows
go test ./scrape/ -run 'TestSearch' -v                                                           # G.5 providers + scrape-top
go test ./store/ -v; go test ./mcp/ -run 'TestToolCatalog|TestSearch' -v; go test ./cli/ -run 'TestProxyFile|TestSearch' -v  # surfaces

# Browser-gated smoke (network + rod; manual/CI-with-network only)
go test -tags browser ./fetch/ -run 'TestJA3|TestScreenshot' -v

# Discipline checks
git status --porcelain testdata/                                          # ONLY challenge/ + search/ may appear
git status --porcelain testdata/clean                                     # must print nothing
grep -rn "impersonate" --include="*_test.go" . | grep -v fetch/ || echo LOCALITY-OK

# Full gate (mirrors phase-G exit criteria)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run ./... && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
```

---

## Coverage Check

- [x] Phase type identified and mock strategy stated — integration/security-boundary + pure logic; no fakes, tiering + real loopback HTTP + fakeSOCKS5 + fixture JSONs (§ header, §1 opening)
- [x] User stories present with 5 stories from the phase deliverables — US-1 pool, US-2 challenges, US-3 profiles, US-4 formats, US-5 search
- [x] Every user story traces to mock-strategy rows — US tags on all 25 component rows
- [x] Every phase-plan deliverable has a test file — §4 maps all of phase-G §5 (README/spec explicitly no-test; proxy.go/challenge.go/search.go each map to named files)
- [x] Heavy dependencies handled — transport: tiering (behavior/internal/smoke) not a fake, with rationale; rod: `-tags browser`; SERP APIs: recorded fixtures on httptest; no other heavy deps
- [x] Unit tests reference no real network/DNS — all loopback; the Tor row is NAT'd by fakeSOCKS5; pubTarget IP never directly dialed (asserted by construction)
- [x] Integration tier gated — `-tags browser` build tag (rod_smoke precedent), never run by default
- [x] conftest equivalent registered — §5 documents the Go adaptation: two new internal files with precedent citations, six flagged edits enumerated with line numbers
- [x] Execution prompt includes helper implementations inline — fakeSOCKS5 full source (§3) + matrix skeleton (§7), referenced as copy-verbatim
- [x] Run commands present — §9 with baseline, per-task focused, browser smoke, discipline greps, full gate
