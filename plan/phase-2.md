# Phase 2 — Selector cache + concurrency + crawl

**Duration:** Weeks 2–4 (~50 hours)
**Depends on:** Phase 1 (straight-line fetch→clean→extract pipeline, full 5-table §9.2 DDL in `store`, `extract` package with repair loop + coerce + cost, `fetch` with static/detect/rod, Cobra `scrape`/`extract`/`config` commands)
**Blocks:** Phase 3 (MCP + plugins — `serve` reuses the crawl pipeline and selector cache)
**Risk Level:** MEDIUM — five brand-new pinned deps plus the first concurrency and LLM-synthesis logic, but every DoD item is buildable with confirmed APIs; synthesis-quality risk carries a specified fallback (per-page LLM), not a plan-changing gate.
**Stack:** `go` — NOTE (per `AGENTS.md`): phase-plan/run-phase skills only accept `python|nextjs|react|typescript`, so `run-phase` will hard-block on this file. Execute this phase manually via the Execution Prompt at the end.

---

## 1. Objective + What Success Looks Like

Build spec Milestone 2: synthesize CSS selectors once from N=3 sample pages (validated field-by-field against direct-LLM ground truth), cache them by `(domain, schema_hash)` in SQLite, serve steady-state pages with **0 LLM calls**, self-heal broken fields when their null rate crosses 0.30, and crawl whole sites with bounded-concurrency errgroup stages, per-host rate limiting, backoff retries, bloom+SQLite dedup, robots.txt enforcement, and checkpoint/resume. This is the phase that makes gomagpie cheap — Phase 1 proved extraction works; Phase 2 proves it works without paying per page.

1. [`magpie crawl https://site.example --schema testdata/extract/price.yaml --max-pages 20 --format jsonl --out /tmp/r.jsonl` exits 0 and `/tmp/r.jsonl` has ≥1 schema-valid record per successfully fetched page]
2. [Second crawl of the same site+schema makes **0 LLM calls** (`select count(*) from llm_calls` unchanged) — all records served from cache]
3. [`magpie crawl` against an httptest origin that breaks the price selector mid-run triggers re-synthesis for `price` only; `title` keeps serving from cache (assert via deterministic test, not manually)]
4. [`go test ./...` exits 0 with >0 tests in `selector` and `crawl` — no network, no browser, no real LLM keys in the default suite]
5. [`magpie cache inspect --domain <d>` prints cached selectors + null rates; `cache clear` evicts; `cache heal --domain d --schema s.yaml --seed-url u` re-synthesizes]
6. [Killing a crawl with SIGINT then `magpie crawl --resume <run_id>` finishes without re-fetching done pages (assert `pages_ok` total equals a single uninterrupted run)]
7. [`magpie crawl` against a robots-denying httptest origin fetches 0 pages and exits 5; `--ignore-robots` prints a warning and proceeds]
8. [`CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64; `go vet` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean (repo enforces it via `.golangci.yml`; the binary is NOT on this WSL box — install it or let CI run it)]

> **Validation 2026-09-16:** every pin, API snippet, and "established in Phase 1" claim below was re-checked against proxy.golang.org, `go doc`, library source, a `CGO_ENABLED=0` probe build on all three targets, a modernc SQLite probe, RFC 9309, and the Phase 1 code at commit `182b4eb`. Corrections from that pass are marked **[validated 2026-09-16]** inline.

---

## 2. Key Design Decisions

### Pipeline (this phase adds the fan-out/fan-in around the Phase 1 straight line)

```
seed URL (+ robots Sitemap: seeds) ──► frontier (SQLite crawl_state + bloom front) ◄── links from CLEAN
                                            │ pump goroutine: Claim(n) → chan; closes chan when
                                            │ Claim is empty AND outstanding==0 (or --max-pages hit)
  --(chan buf=1000)--> FETCH stage (8 workers: robots? → rate-limit → backoff-retry fetch)
  --(chan buf=100)--> CLEAN stage (GOMAXPROCS: sidecar harvest + link extraction→frontier ALWAYS;
                                    trafilatura→markdown ONLY when the page will need an LLM)
  --(chan buf=100)--> EXTRACT stage (4 workers: cache-apply (0 LLM)
                                    | cold domain → direct-LLM purpose=synth, retain validated sample,
                                      Synthesize once N=3 samples exist → PutSelectors → cache flips
                                    | non-cacheable fields → direct-LLM purpose=extract) + heal accounting
  --> writer (single goroutine: jsonl|json|csv|sqlite) + per-page MarkDone/MarkError + outstanding--
```

Backpressure is structural: the extract stage is the bottleneck (LLM latency on miss); its input buffer fills, fetchers block on send, the frontier stops being drained. No explicit throttling code — the bounded channels ARE the throttle (spec §5.1). **[validated 2026-09-16]** There is no synthesis preamble any more — see the "lazy synthesis" and "termination" decisions below.

### Decisions (with spec section refs)

- **CSS-only selectors via goquery, no XPath dep (spec §4 override).** Spec §4.2 says "CSS (preferred) or XPath" — XPath needs a new dependency (`antchfx/xpath` or similar) and the repo rule is "no new dep without asking." goquery is already pinned and Phase-1-proven. Fields for which no validating CSS exists stay non-cacheable → per-page LLM extraction. If real-site agreement (§3 Task 2.6 fallback) shows CSS systematically insufficient, XPath is a Phase-2-follow-up decision with evidence, not a speculative dep now.
- **`jsonld_path` fields bypass synthesis entirely (spec §3.7).** If a field has `jsonld_path`, steady-state extraction evaluates the already-written `JSONLDWalk` against the page sidecar — no selector needed, never null unless the sidecar lacks the path. These fields still count in null-rate accounting (a missing path is a null).
- **Synthesis is lazy and lives inside the extract stage — no separate preamble (spec §4.2 + §4.3). [validated 2026-09-16]** Spec §4.2 says "collect N sample pages" but never says which. The earlier draft's "seed + first two BFS links" picks a category page and a contact page on most sites → null ground truth → every field non-cacheable forever. Instead: while `(domain, schema_hash)` has no cached doc, extract workers run direct LLM (`Purpose:"synth"`) and `Healer.Retain` keeps each page whose record validated with every `required` field non-null. At N=3 retained samples, `Synthesize` runs once, `PutSelectors` flips the domain to cache-apply. This is the SAME retention `Healer` needs for re-synthesis (spec §4.3 "most recent pages that still produced valid values"), so cold-start and heal share one code path and the preamble is deleted. `ponytail:` with 4 extract workers a few extra pages go direct-LLM before the cache flips (ceiling ≈ N + workers LLM calls per domain, not exactly 3; upgrade = per-domain `sync.Once` gate on the workers). `cache heal --seed-url` still BFS-fetches ≤3 pages itself because it runs outside a crawl.
- **All frontier mutations serialize through the single SQLite writer (spec §9.1). [validated 2026-09-16]** `Claim` is ONE statement — `UPDATE crawl_state SET status='inflight', updated_at=? WHERE run_id=? AND url_hash IN (SELECT url_hash FROM crawl_state WHERE run_id=? AND status='pending' LIMIT ?) RETURNING url, url_hash, depth` — atomic by itself, no txn plumbing. Spec §5.6's `UPDATE … LIMIT n RETURNING` is a **syntax error** on modernc.org/sqlite v1.59.0 (SQLite 3.53.4 is built without `SQLITE_ENABLE_UPDATE_DELETE_LIMIT`; probed). `SetMaxOpenConns(1)` (already in Phase 1 `store.Open`) serializes workers with no `worker` column and no DDL drift from spec §9.2. **Footgun:** with one connection, any `*sql.Rows` left open blocks every other store call in the process — every method drains and `Close`s before returning; never hold rows across another query.
- **Crawl termination = an outstanding counter + a pump goroutine (spec §5.1 leaves it unsaid). [validated 2026-09-16]** The pipeline is a cycle (CLEAN enqueues links back into the frontier), so no stage can close the fetch channel without knowing nothing more will arrive. `crawl.Run` keeps an `atomic.Int64 outstanding`: `+1` per row `Enqueue` actually inserted (and per row `ResetInflight` re-queues on resume), `-1` when the writer finishes a page (`MarkDone`/`MarkError`). The pump loops `Claim(n)` → send on the fetch chan; when `Claim` returns empty it checks `outstanding.Load()==0` → close the chan and return, else sleep 50 ms and retry. `--max-pages N` = pump stops claiming after N rows claimed (pages that fed synthesis count — they were fetched); remaining `pending` rows stay in SQLite so `--resume` can continue them. `Enqueue` always precedes the writer for the same URL, so the counter never goes negative.
- **`--resume` = three store calls before the pipeline starts. [validated 2026-09-16]** (1) `ResumeRun(runID)` flips `run_history.status` back to `running` — a second `BeginRun` violates the PRIMARY KEY; (2) `ResetInflight(runID)` sets `inflight → pending` — rows stranded by SIGKILL or an expired 30 s drain would otherwise never be claimed again and Exit Criterion 6 cannot hold; (3) `LoadHashes` → bloom. `FinishRun` on ANY run derives `pages_ok/pages_err` from `CrawlStats(runID)` (done/error counts), never from in-memory counters that reset on restart.
- **Bloom filter stores `url_hash` hex strings, not URLs (spec §5.6).** `Add`/`Test` take `[]byte` — the SHA-256 hex of the canonical URL is a fixed-size, uniform key. This is what makes resume-rebuild possible: `dedup.LoadHashes(runID)` replays hashes into a fresh filter without storing 100k URL strings anywhere.
- **Bloom filter is mutex-guarded, never shared lock-free.** The library documents filters as "unsynchronized for performance" — one `sync.Mutex` around `Add`/`Test` in our wrapper; contention is negligible (in-memory bit ops).
- **Crawl-delay via `grobotstxt.Parse` + custom handler (spec §11). [validated 2026-09-16]** The one-shot `AgentAllowed`/`Sitemaps` cover allow + sitemaps; crawl-delay has no accessor, but `Parse(body, handler)` delivers every directive and `HandleUnknownAction(lineNum, action, value)` receives the **raw key exactly as written** (`"Crawl-Delay"`, `"Crawl-delay"`, … — verified in `robots_cc.go`), so match with `strings.EqualFold(action, "crawl-delay")`, never `==`. Track the current group in `HandleUserAgent` (`*` or our bare token) and take the **max** delay over matching groups → `SetFloor`. `ponytail:` no Google "specific group overrides `*`" precedence for the delay — max of both is the politest reading and two lines (ceiling = a site giving `*` a huge delay but our token a small one; upgrade = replicate the matcher's group selection).
- **Robots matching uses the bare product token, not the UA header. [validated 2026-09-16]** `AgentAllowed(body, "magpie", url)`. Passing Phase 1's full header `magpie/1.0 (+https://github.com/you/gomagpie)` makes a `User-agent: magpie` block match nothing and silently allows everything (probed). Derive the token once from the UA (`strings.FieldsFunc` up to the first `/` or space) and keep it in `Checker.token`.
- **Robots fetch failure follows RFC 9309 §2.3.1, not "allow when in doubt". [validated 2026-09-16]** 4xx (incl. 404) = "unavailable" → crawler MAY access → **allow**. 5xx or transport error = "unreachable" → crawler **MUST assume complete disallow** → treat like an explicit deny: seed host → exit 5 with `robots.txt unreachable (HTTP 503 / dial error); retry later or pass --ignore-robots`; non-seed host → its URLs get `MarkError`. The earlier draft said unreachable → allow "per Google's convention" — that was wrong; Google also treats 5xx as full disallow.
- **Robots bodies cached in memory per host per run, re-fetched each run.** No DB table for robots — one extra request per host per run is the cost, and staleness bugs are impossible. `ponytail:` no `Expires`/`max-age` handling (ceiling = re-fetch per run even for 10-minute crawls of one host — one request, irrelevant).
- **Retry-After honored natively via `backoff.RetryAfter` (spec §5.4). [validated 2026-09-16]** backoff/v5 resets the backoff policy on `RetryAfterError` and waits the parsed seconds — no hand-rolled sleep (probed: 1 s wait, then policy reset). Parse failure on the header value → fall through to normal exponential wait (fail polite, never crash). `*ExponentialBackOff` is **stateful** (`currentInterval`) — `FetchWithRetry` constructs a fresh one per call; a package-level or shared instance across the 8 fetch workers is a data race. `Retry` calls `BackOff.Reset()` on entry, so no manual `Reset()` is needed.
- **`core/pipeline.go` holds the concrete three-stage runner; `core/registry.go` + `module.go` wait for Phase 3.** Spec §13 lists all three under `core/`, but the Caddy-style registry + semver gate serve plugins (explicitly deferred per spec Recommendation 4). Building them now is speculative scaffolding — pipeline wiring is the only `core` code this phase needs.
- **CSV column order = schema `required[]` then remaining sorted; nested values are a loud error (spec §10.1).** Go map iteration is random — without a fixed order, CSV output is nondeterministic and untestable.
- **`--format json` buffers records in memory; `jsonl` streams (spec §10.1).** `ponytail:` a 100k-page `--format json` crawl holds all records in RAM (ceiling = large crawls must use `jsonl`; document it in `--help` text, don't engineer around it).

### Data Model Strategy (Go — same rules as Phase 1)

| Layer | Pattern | Why |
|---|---|---|
| Domain types (`FieldSelector`, `SelectorDoc`, `ClaimedURL`, `CrawlStats`, job structs) | Plain structs with `json`/`db` tags | Zero-cost, `encoding/json` native, matches Phase 1 style |
| Stage boundaries (`Synthesizer`, `Applier`, `Frontier`) | Small interfaces declared in the consuming package | `crawl` accepts a fake `Extractor` in tests; Phase 3 `serve` reuses `core/pipeline.go` untouched |
| Config additions (concurrency, rate, thresholds) | Same plain `Config` struct + `GOMAGPIE_` env overlay, new keys only | No new config mechanism; precedence unchanged |
| Hot path (dedup check per URL, selector apply per field) | Bloom `Test` + `sync.Mutex`; goquery per page (parse once, apply all fields) | Parse the DOM once per page and run every field selector against the same `goquery.Document` — N parses per page is the obvious waste |
| Errors | `fmt.Errorf("crawl: %w", err)` + `backoff.Permanent` for the non-retryable set | Retry taxonomy lives in ONE function (`crawl/backoff.go:classify`), not scattered per call site |

**Other critical rules:** no CGO ever (re-verify the gate after `go get` — bloom pulls `bits-and-blooms/bitset` + `twmb/murmur3`, both pure Go; **[validated 2026-09-16]** a probe importing all five modules cross-built `CGO_ENABLED=0` on all three targets with zero `CgoFiles` in `go list -deps`); no live-network/browser/LLM in default `go test ./...` (browser tag + fake `Extractor` as in Phase 1); golden/deterministic fixtures under `testdata/crawl/` + `testdata/selector/`.

---

## 3. Tasks

### Task 2.1 — Module setup + pinned deps + cross-compile gate (2h)

Goal: `go.mod` gains exactly five modules; all three target platforms still build with `CGO_ENABLED=0`.

```bash
# Pins resolved on proxy.golang.org 2026-09-16 — use EXACTLY these
go get github.com/cenkalti/backoff/v5@v5.0.3 \
  github.com/bits-and-blooms/bloom/v3@v3.7.1 \
  github.com/jimsmart/grobotstxt@v1.0.3 \
  golang.org/x/sync@v0.23.0 \
  golang.org/x/time@v0.16.0
go mod tidy && go build ./...
# (bloom pulls bits-and-blooms/bitset + twmb/murmur3 — pure Go, no CGO; verified
# 2026-09-16 on all three targets. If anything shows up in
#   go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./...
# stop and flag.)
# NOTE [validated 2026-09-16]: Phase 1's go.mod marks EVERY dep `// indirect`;
# `go mod tidy` rewrites the whole require block into direct/indirect groups.
# Expected noise — commit it, don't "fix" it by hand.
```

**Sanity check:** `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK`

### Task 2.2 — `store`: selector_cache + frontier + dedup accessors (4h)

Goal: all Phase 2 persistence behind methods on the existing `*store.DB`; zero DDL drift from spec §9.2 (plus one `records` table for SQLite output, created on demand by the writer, not in the migration).

```go
// Selector cache — fields_json is the §4.4 doc verbatim.
func (d *DB) GetSelectors(domain, schemaHash string) (selectorDoc string, ok bool, err error)
func (d *DB) PutSelectors(domain, schemaHash, fieldsJSON string, samplesUsed int) error // INSERT ... ON CONFLICT(domain,schema_hash) DO UPDATE
func (d *DB) DeleteSelectors(domain, schemaHash string) (int64, error) // hash="" deletes all for domain; returns rows evicted

// Frontier — each method is ONE statement or one short db.Begin() txn (single writer => atomic).
func (d *DB) Enqueue(runID string, urls []string, depth int) (inserted int, err error) // INSERT OR IGNORE into crawl_state(pending)+dedup; returns rows actually inserted (feeds the outstanding counter); canonicalize BEFORE calling (crawl pkg owns that)
func (d *DB) Claim(runID string, n int) ([]ClaimedURL, error)       // [validated 2026-09-16] UPDATE … SET status='inflight' WHERE run_id=? AND url_hash IN (SELECT url_hash … status='pending' LIMIT ?) RETURNING url,url_hash,depth — drain + Close rows before returning
func (d *DB) MarkDone(runID, urlHash string) error
func (d *DB) MarkError(runID, urlHash, msg string) error
func (d *DB) CrawlStats(runID string) (pending, inflight, done, errors int, err error)

// Resume — both called before the pipeline starts. [validated 2026-09-16]
func (d *DB) ResumeRun(runID string) error              // UPDATE run_history SET status='running', finished_at=NULL WHERE run_id=?; RowsAffected()==0 → error "unknown run_id"
func (d *DB) ResetInflight(runID string) (int64, error) // UPDATE crawl_state SET status='pending' WHERE run_id=? AND status='inflight'; returns rows re-queued

// Dedup — Seen returns true if the hash was already present (INSERT OR IGNORE + RowsAffected()==0).
func (d *DB) Seen(runID, urlHash string) (bool, error)
func (d *DB) LoadHashes(runID string) ([]string, error) // resume: replay into bloom
```

Implementation notes: `ClaimedURL{URL, URLHash, Depth}`. `Enqueue` writes BOTH tables (crawl_state pending + dedup seen) so a URL is never double-enqueued even across resume; wrap its per-URL inserts in one `db.Begin()` txn for batch speed and commit before returning. **[validated 2026-09-16]** `Claim` uses the `IN (SELECT … LIMIT ?)` form because `UPDATE … LIMIT` is a syntax error on modernc (SQLite 3.53.4, probed); `RETURNING` works. No method may return open `*sql.Rows` (single connection → deadlock). Timestamps RFC3339 (`time.Now().UTC().Format(time.RFC3339)`). Unit test on temp-file DB: enqueue 5 (inserted==5) → re-enqueue same 5 (inserted==0) → claim 2 → `CrawlStats` = 3/2/0/0 → `ResetInflight` == 2 → stats 5/0/0/0 → claim 2 → mark done/error → recount; `ResumeRun` on unknown id → error, on a finished run → status back to `running`; `Seen` twice → true only second time; `Put`+`Get` selector round-trip; reopen file → data survives (resume relies on this).

**Sanity check:** `go test ./store/ -v` passes (extend `sqlite_test.go`, no new test file needed for store)

### Task 2.3 — `crawl/robots.go` + `crawl/ratelimit.go`: politeness (5h)

Goal: robots enforced before every fetch; per-host token buckets with crawl-delay floor; `--ignore-robots` warns loudly and skips.

```go
// Confirmed via go doc on grobotstxt v1.0.3 (2026-09-16) — pure one-shot funcs, safe for concurrent use:
import "github.com/jimsmart/grobotstxt"
allowed := grobotstxt.AgentAllowed(robotsBody, "magpie", rawURL) // [validated 2026-09-16] userAgent = BARE product token, NOT the UA header string; false also on unparseable URL
sitemaps := grobotstxt.Sitemaps(robotsBody)                      // []string, seed the frontier (spec §11)
// handler.HandleUnknownAction(line, action, value) gets action as written ("Crawl-Delay") → strings.EqualFold

// Confirmed via go doc on x/time v0.16.0:
import "golang.org/x/time/rate"
lim := rate.NewLimiter(rate.Every(time.Second), 3) // 1 req/s per host, burst 3 (spec §5.3)
lim.Wait(ctx)                                       // blocks until token (call before EVERY request)
lim.SetLimit(rate.Every(d))                         // crawl-delay floor: d = parsed seconds (spec §11)
```

Implementation notes: `robots.go` — `Checker{client StaticFetcher-ish, mu, bodies map[host]robotsEntry, token string}`; `Allowed(ctx, rawURL) (bool, error)`: fetch `scheme://host/robots.txt` once per host (static fetch, 10s timeout, no retry). **[validated 2026-09-16] Status handling per RFC 9309 §2.3.1:** 2xx → parse; 4xx → allow all; 5xx or transport error → **disallow all** for that host and return a sentinel `ErrRobotsUnreachable` (the crawl maps it to exit 5 when it is the seed host, `MarkError` otherwise). Only `--ignore-robots` overrides. Parse crawl-delay with a ~30-line `ParseHandler` impl: `HandleUserAgent` sets `inGroup = value=="*" || EqualFold(value, token)`; `HandleUnknownAction` with `EqualFold(action,"crawl-delay")` and `inGroup` → `max(delay, parsed)` (see the ponytail in §2). Feed `Sitemaps()` output back to the frontier as depth-0 seeds. `ratelimit.go` — `HostLimiters{mu sync.Mutex, m map[string]*rate.Limiter, rps float64, burst int}`; `Wait(ctx, host)` lazy-creates bucket; `SetFloor(host, d)` only ever LOWERS the rate (max of current delay and crawl-delay). Tests: httptest origins — `/robots.txt` with `User-agent: *` + `Disallow: /private` → `Allowed("/private/x")==false`, `Allowed("/public")==true`; a `User-agent: magpie` group with `Disallow: /x` → false (regression test for the header-vs-token bug); `Crawl-Delay: 2` (capital D, as sites write it) → bucket rate becomes ≤0.5/s; robots.txt returning 503 → `Allowed==false` + `ErrRobotsUnreachable`; 404 → allow; closed port → disallow + `ErrRobotsUnreachable`. `--ignore-robots` prints `WARNING: --ignore-robots set; fetching despite robots.txt` to stderr (assert in cmd test).

**Sanity check:** `go test ./crawl/ -run 'TestRobots|TestRateLimit' -v` — zero network (all `httptest`)

### Task 2.4 — `crawl/frontier.go` + `bloom.go`: canonicalize + dedup + links (5h)

Goal: URL canonicalization, two-tier dedup (bloom front + SQLite truth), BFS claim loop, same-host link extraction.

```go
// Confirmed via go doc on bloom/v3 v3.7.1 (2026-09-16):
import "github.com/bits-and-blooms/bloom/v3"
f := bloom.NewWithEstimates(1000000, 0.01) // ~1.2MB for 1M URLs @ 1% FP (spec §5.6)
f.Add([]byte(urlHashHex))                  // we store url_hash hex, NOT the URL (resume-replayable)
if f.Test([]byte(urlHashHex)) { /* possible hit -> confirm via store.Seen */ }
```

Implementation notes: `Canonicalize(rawURL) string` (stdlib `net/url` only): lowercase host, strip default ports (:80/:443), sort query params, drop `utm_*`/`gclid`/`fbclid`/`msclkid`, drop fragment. Empty/invalid → error (fail loud, never enqueue garbage). `bloom.go` — `Filter{mu sync.Mutex, f *bloom.BloomFilter}` (the type is `BloomFilter`, not `Filter` — **[validated 2026-09-16]**) with `Add/Test` locking (library README: "unsynchronized for performance"). 1M @ 1% FP measured at 1,198,132 bytes — the ~1.2 MB claim holds. `frontier.go` — `Frontier{db, filter, runID}`: `Add(urls, depth)` = canonicalize → hash → bloom.Test? confirm `store.Seen` : `store.Enqueue`; `Claim(n)` delegates to `store.Claim`. Link extraction (in `frontier.go`, ~40 lines): goquery on the RAW html (pre-trafilatura — nav links live in boilerplate), `a[href]` → resolve against page URL → `Add` at depth+1 iff `sameHost` (exact host equality; `ponytail:` subdomains excluded) and depth+1 ≤ maxDepth. Tests: canonicalization vectors (`HTTP://EX.com:80/a?utm_x=1&b=2` ≡ `http://ex.com/a?b=2`); dedup: add 3 URLs, re-add → 0 new enqueued; bloom false-positive path: pre-seed filter with a hash NOT in DB → `Add` still enqueues (DB is truth); link test: page with 2 same-host + 1 external link, sameHost=true → 2 enqueued at depth+1.

**Sanity check:** `go test ./crawl/ -run 'TestCanonical|TestDedup|TestLinks' -v`

### Task 2.5 — `crawl/backoff.go`: retry taxonomy (4h)

Goal: ONE classifier + ONE retry wrapper used by every fetch in the crawl (and later by synthesis); `Retry-After` honored; permanent errors fail fast.

```go
// Confirmed via go doc on backoff/v5 v5.0.3 (2026-09-16):
// Operation takes NO ctx — close over it. Retry guarantees ≥1 attempt.
import "github.com/cenkalti/backoff/v5"
type Operation[T any] func() (T, error) // library type; shown for clarity

b := backoff.NewExponentialBackOff()
b.InitialInterval = 500 * time.Millisecond
b.Multiplier = 2.0
b.RandomizationFactor = 0.5 // built-in jitter (spec §5.4)
b.MaxInterval = 30 * time.Second
// no b.Reset() needed — Retry() calls BackOff.Reset() on entry [validated 2026-09-16, retry.go:84]
// BUILD b INSIDE FetchWithRetry, per call: *ExponentialBackOff is stateful (currentInterval).
resp, err := backoff.Retry(ctx, op,
    backoff.WithBackOff(b),
    backoff.WithMaxTries(4),             // uint; total attempts, not retries
    backoff.WithMaxElapsedTime(2*time.Minute),
)

// Inside op: permanent errors stop immediately; 429 Retry-After resets the backoff clock:
if status==400 || status==401 || status==403 || status==404 || status==410 { return nil, backoff.Permanent(fmt.Errorf("fetch: %w", errFetch)) }
if status==429 || status==503 {
    if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
        return nil, backoff.RetryAfter(secs) // native: waits secs, resets policy
    }
}
// Retryable: reset/timeout/temp-DNS, 408/429/500/502/503/504, TLS timeout.
// Permanent: the 400-class above + schema failure + robots disallow + ctx canceled.
```

Implementation notes: `classify(resp, err) error` returns nil (success), `backoff.Permanent(...)`, `backoff.RetryAfter(...)`, or the raw error (retryable). `FetchWithRetry(ctx, do func() (*fetch.FetchResponse, error))` wires classifier into `backoff.Retry` and builds a fresh `ExponentialBackOff` per call (shared use across workers is a data race). Unit test with a counting fake (no httptest needed — classify is pure; retry timing tested with `MaxInterval: time.Millisecond` + `MaxTries: 3` asserting exactly 3 calls and permanent asserting exactly 1). NOTE: `RetryAfter(0)`/unparseable → normal exponential wait (documented above). **[validated 2026-09-16, probed on v5.0.3]:** transient → exactly `MaxTries` calls; `Permanent` → exactly 1; `RetryAfter(1)` → 1 s wait then policy reset; canceled ctx → returns `context.Canceled` after 1 call. The loop checks `MaxTries` BEFORE `Permanent`, so a permanent error on the final attempt surfaces as the raw error, not unwrapped — don't assert error type on the last attempt.

**Sanity check:** `go test ./crawl/ -run 'TestClassify|TestRetry' -v` runs in <2s (millisecond backoff in tests)

### Task 2.6 — `selector/synth.go` + `validate.go`: synthesis (10h)

Goal: from N=3 validated sample pages (collected lazily by the extract stage — Tasks 2.7/2.8 — or BFS-fetched by `cache heal`), produce per-field CSS selectors that reproduce direct-LLM ground truth; fields that don't validate stay non-cacheable (per-page LLM forever, flagged).

Synthesis (cheapest candidate first, validation disposes) **[validated 2026-09-16 — reordered]**:
1. Samples arrive as `SynthSample{URL, HTML, Sidecar, Truth}` already carrying ground truth — the direct-LLM call that produced `Truth` was logged with `Purpose:"synth"` (see the `extract` change below). `Synthesize` itself makes NO ground-truth calls.
2. **`css_hint` from `x-gomagpie` first.** Phase 1 already parses it into `Schema.Hints.CSSHint` and nothing uses it yet — a user-supplied selector that validates costs zero work. If it fails validation, fall through (and say so on stderr).
3. **Heuristic generator second** (BardeenAgent-style, ~100 lines, no LLM): for each field value, find text nodes containing it (goquery), walk up ≤4 ancestors, emit candidates in order — `#id`, `tag#id`, `.class1.class2` (skip obvious layout classes matching `^(col|row|container|wrapper)-`), `tag.class:nth-of-type(n)`, `[attr="value"]` for distinctive attrs — and keep the first candidate that exact-matches on ALL samples. Heuristic wins ties (free, deterministic).
4. **LLM proposal last, only for fields 2+3 missed.** The `Extractor` interface is schema-bound, so build an ad-hoc schema (`extract.ParseSchema` on a generated JSON object with one `string` property per missing field, `additionalProperties:false`) and call the same adapter with `Markdown` = a **trimmed** HTML payload: scripts/styles/comments stripped, then only the subtrees ≤4 ancestors above each text node containing a truth value, capped at `clean.MaxTokens` (8k) — never the raw page. `Purpose:"synth"`. No temperature is sent (adapters send none); validation is the safety net.
5. `validate.go`: run winning selectors on all N samples via goquery (ONE parse per page, all fields applied to the same document), apply `regex` + `coerce` + `trim` from `x-gomagpie` (reuse `extract` pkg funcs — no copies), compare to ground truth as canonical JSON strings. Require ≥ (N-1)/N agreement per field (N=3: 3/3 clean, 2/3 caches WITH a stderr warning naming the field). Store the §4.4 doc via `PutSelectors` (`engine_version: 1`, `samples_used: N`).
6. `schema_hash` = hex(SHA-256(`json.Marshal(sch.Raw)`)) — **[validated 2026-09-16]** `Schema.Raw` is already the `yamlToJSON`-round-tripped value and `json.Marshal` sorts map keys, so this is one line; do not add a canonicalizer.
7. `xpath_hint` present in the schema → one stderr warning per schema, `xpath_hint ignored (CSS-only engine)`. Phase 1 silently drops it today.

**`extract` package change (3 lines, extend in place) [validated 2026-09-16]:** `ExtractInput` gains `Purpose string`; `runRepairLoop` uses `in.Purpose` (default `"extract"` when empty) as the first-attempt purpose, `"repair"` on retries unchanged. Without this there is NO way to log `synth` — the loop hardcodes `extract`, and a "wrapper around `newExtractor`" cannot change it.

**Fallback (spec Recommendation 1, the benchmark that changes tactics):** if field-level agreement across the 3 samples is < 80% for price/EAN-class fields, do NOT over-engineer the cache — mark those fields non-cacheable (per-page LLM) and note it in the run summary. css_hint-then-heuristic ordering already implements the "invest in non-LLM" half for free. Build steps 2–3 and 5–7 first and get them green; step 4 (LLM proposal) is the last sub-step so it can be cut if the benchmark says the heuristic alone clears 80%.

Implementation notes: `Synthesize(ctx, samples []SynthSample, schema *extract.Schema, propose func(ctx, fields []string, trimmedHTML string) (map[string]string, error), only []string) (SelectorDoc, error)`. `propose` is the LLM-proposal closure — tests inject a canned map; production wraps the ad-hoc-schema adapter. `only` filters fields (heal re-synthesizes one field; nil = all). Min samples: 1 sample → synthesize but warn (agreement vacuous); 0 samples → loud error (never cache nothing). Tests (fixtures + fake-propose closure, NO real keys): product-page fixture ×3 variants; `css_hint` present and valid → chosen with 0 heuristic/LLM work; `css_hint` present but wrong → falls through; assert heuristic finds `#productTitle`-style selector with 0 `propose` calls; `propose` path tested with a canned map; validation test: 2/3-agreeing field caches with warning, 1/3 field → non-cacheable; `only=["price"]` touches no other field.

**Sanity check:** `go test ./selector/ -run 'TestSynth|TestValidate' -v` — 0 external calls (fake closure counts invocations)

### Task 2.7 — `selector/cache.go` + `heal.go`: apply + self-heal (8h)

Goal: steady-state pages extract with 0 LLM calls; broken fields re-synthesize alone while healthy fields keep serving.

```go
// Apply path (per page, per schema):
doc, ok := store.GetSelectors(domain, schemaHash)
if !ok → direct LLM (non-cacheable page; logged purpose=extract)
else {
    parse HTML once (goquery); for each field:
      jsonld_path → extract.JSONLDWalk(sidecar, path)          // no selector, never "breaks"
      selector    → doc.Find(expr).First().Text() → regex → coerce → trim
    record nulls per field into the sliding window (see below)
}
```

Healing (spec §4.5, defaults window=50 / threshold=0.30 / N=3): `Healer` holds in-memory `map[field][]bool` rings (last 50 outcomes) + a ring of the last 3 `SynthSample`s per domain — **[validated 2026-09-16]** this ring is ALSO the cold-start sample collector (§2 "lazy synthesis"): on a cache miss the extract worker runs direct LLM with `Purpose:"synth"`, and if the record validated with every `required` field non-null it calls `Retain(sample)`; when `Samples()` reaches N the worker runs `Synthesize` → `PutSelectors` and the domain flips to cache-apply. After each cached page: `nullRate = nulls/len(window)`; crossing 0.30 → re-synthesize ONLY that field: run direct-LLM ground truth on the retained samples (`Purpose:"synth"`), `Synthesize(..., only=[field])` (css_hint → heuristic → LLM), validate ≥2/3, `PutSelectors` merged doc (healthy fields untouched, serving throughout). If ≥50% of fields are broken simultaneously → full re-synthesis (treat as redesign). Null-rate counters flush to `fields_json.null_rate` every 25 pages + at run end (`ponytail:` a crash loses ≤25 pages of stats; ceiling = slightly stale trigger after `--resume` — self-corrects within 25 pages).

Implementation notes: `cache.go` — `Applier{db, schema, doc}` + `Apply(html, sidecar) (record, nullFields[])`. `heal.go` — `Healer{...}` + `Observe(field, wasNull) (triggered []string)` + `Retain(SynthSample)` + `Samples() []SynthSample`. Both cold-start and heal call the same `Synthesize` from Task 2.6 (heal with a field filter — don't rebuild the world per broken field). Tests (the deterministic healing test from spec §12, fake `Extractor`, NO real LLM): serve page version A → assert cache hit + 0 LLM calls; swap handler to version B (price node id renamed) → feed 20 pages → assert heal fires for `price` ONLY (`title`/`ean` uninterrupted, fake-LLM call log shows `synth` purpose only after crossing); full-redesign test: break 2/3 fields → whole-template re-synthesis.

**Sanity check:** `go test ./selector/ -run TestHeal -v` — asserts per-field isolation via fake-LLM call counts

### Task 2.8 — `core/pipeline.go` + `crawl/crawl.go` + `cmd` wiring (10h)

Goal: `magpie crawl` + `magpie cache …` + csv/sqlite outputs + exit codes 3/4/5/6, all wired through the pipeline with SIGINT drain and `--resume`.

```go
// Confirmed idioms (x/sync v0.23.0, x/time v0.16.0 — stable APIs, verified by build in Task 2.1):
import "golang.org/x/sync/errgroup"
g, gctx := errgroup.WithContext(ctx)
g.SetLimit(8) // bounded concurrency per stage; first error cancels gctx (clean shutdown free)
g.Go(func() error { /* worker loop: select on gctx.Done() + job chan */ })
err := g.Wait() // first non-nil error; context.Canceled on SIGINT drain is NOT a failure
```

`core/pipeline.go` (~120 lines): concrete job types `FetchTask{URL, URLHash, Depth}` → `FetchedPage{Task, Resp}` → `PageResult{Task, Record, Links, Err}`; `Run(ctx, cfg PipelineConfig, source <-chan FetchTask, fetchFn, cleanFn, extractFn, sink func(PageResult))` wires `source→fetch(buf 1000) →clean(buf 100) →extract(buf 100) →sink` with per-stage `SetLimit` (fetch 8 / clean GOMAXPROCS / extract 4) + a 2-slot browser semaphore around `fetchFn` when escalation fires (browser fetches construct lazily via the Phase 1 `fetchBrowser` helper pattern; max 2 live `RodFetcher`s, `Close`d at stage end — no zombie class). Each stage closes its output chan when its errgroup `Wait` returns, so closing `source` drains the whole pipeline in order. The pipeline knows nothing about the frontier — `crawl` owns the pump and the counter (§2 "termination").

`crawl/crawl.go` orchestration **[validated 2026-09-16 — steps 1–3 and 7 rewritten]**:
1. New run: `runID = uuidNew()` + `BeginRun(runID, "crawl")`. `--resume <id>`: `ResumeRun` (a second `BeginRun` hits the PRIMARY KEY) → `ResetInflight` (stranded `inflight` rows → `pending`, returned count seeds `outstanding`) → `LoadHashes` → bloom; skip seeding.
2. Seed host robots check FIRST: explicit deny OR unreachable (5xx/network, RFC 9309) → exit 5 unless `--ignore-robots`. Then seed frontier (URL + robots `Sitemap:` seeds, depth 0); `outstanding += inserted`.
3. Pump goroutine: `Claim(n)` → send `FetchTask` on `source`; empty claim + `outstanding==0` → close `source`; `--max-pages` reached → stop claiming, close `source` (pending rows remain for `--resume`). No synthesis preamble — cold domains synthesize lazily inside step 4.
4. `pipeline.Run` with fns: fetch = robots-check → `HostLimiters.Wait` → `FetchWithRetry` (static; detect ≥2 → browser-semaphore path); clean = `HarvestSidecar` + link extraction → `Frontier.Add` (`outstanding += inserted`) ALWAYS, `clean.Clean` (trafilatura→markdown) ONLY when the page will need an LLM — `GetSelectors` miss or the doc has non-cacheable fields (`ponytail:` one SQLite point read per page to decide; ceiling = negligible, WAL read on the single conn); extract = cache-apply (0 LLM) | cold domain → direct-LLM `Purpose:"synth"` + `Healer.Retain` → `Synthesize` at N samples → `PutSelectors` | non-cacheable fields → direct-LLM `Purpose:"extract"`; every LLM call preceded by `checkCostCeiling` → exit 6; `Healer.Observe` on every cached page.
5. Writer goroutine (`sink`): `jsonl` (stream line/record), `json` (buffer, marshal array at end), `csv` (required-order header, nested → loud error), `sqlite` (`records(run_id,url,record_json,ts)` table, created on demand). Every page → `MarkDone`/`MarkError` (checkpoint continuous) then `outstanding.Add(-1)`.
6. Root ctx = `signal.NotifyContext(SIGINT, SIGTERM)`; on cancel: 30s drain (`context.WithTimeout`), then `FinishRun(status=interrupted)` — resume continues exactly (done rows are never re-fetched; inflight rows are reset by step 1).
7. `FinishRun` counts come from `CrawlStats(runID)` (done/error), never in-memory. Exit code: 0 all pages ok; 4 some pages errored but ≥1 record; 3 zero records; 5 robots; 6 cost ceiling.

`cmd/magpie/crawl.go` flags per spec §10 (`--max-pages 100 --max-depth 3 --same-host=true --schema --concurrency(→fetch workers) --rate(per-host rps) --resume --format --ignore-robots`; `--help` states that `--max-pages` counts claimed/fetched pages, including the ones that fed synthesis); `cache_cmd.go` (`inspect [--domain] [--schema-hash]`, `clear [--domain]`, `heal --domain --schema --seed-url` = BFS ≤3 pages from seed, min 2 samples else loud error). Exit codes: reuse Phase 1 `fail()`/`exitFor` — **[validated 2026-09-16]** Phase 1 emits only 1/2/6/7 (3 is in the spec but never used) — ADD 3 (no records), 4 (partial: some pages errored) and 5 (robots disallowed or unreachable target — also when the seed itself is denied and `--ignore-robots` absent). `scrape` gains cache-apply: schema + cached (domain,hash) + no `--no-cache` → apply selectors, 0 LLM (log `from_cache=true`); miss → Phase 1 path unchanged.

**Sanity check:** full matrix against httptest origins — `go test ./crawl/ ./cmd/... -run 'TestCrawl|TestCache|TestResume' -v`; manual: `go run ./cmd/magpie crawl file://…` (file transport from Phase 1 Task 1.4 makes even the crawl e2e hermetic)

---

## 4. Deliverables

```
gomagpie/
├── core/
│   └── pipeline.go          # FetchTask/FetchedPage/PageResult types + Run(source, …, sink): 3 errgroup stages, bounded chans, browser semaphore
├── extract/
│   └── extractor.go         # +ExtractInput.Purpose → runRepairLoop first-attempt purpose (3 lines, extend in place)
├── selector/
│   ├── synth.go             # Synthesize(samples, schema, propose, only): css_hint → heuristic → LLM-proposal candidates; ground truth arrives in samples
│   ├── validate.go          # field-level ≥(N-1)/N agreement vs ground truth; schema_hash = sha256(json.Marshal(sch.Raw))
│   ├── cache.go             # Applier: one goquery parse/page, jsonld_path fast path, regex+coerce via extract pkg
│   ├── heal.go              # Healer: 50-page rings, 0.30 trigger, Retain/Samples ring (cold-start + heal), per-field re-synth, ≥50% → full re-synth
│   └── selector_test.go     # synth + validate + deterministic A/B heal test (fake propose closure, 0 real calls)
├── crawl/
│   ├── crawl.go             # Run(): seed/resume(ResumeRun+ResetInflight) → pump + outstanding counter → pipeline.Run → writer → FinishRun(CrawlStats); SIGINT drain
│   ├── frontier.go          # Canonicalize + Frontier{Add,Claim} + same-host link extraction (goquery on raw HTML)
│   ├── bloom.go             # mutex-guarded NewWithEstimates(1M, 0.01) wrapper over url_hash hex
│   ├── robots.go            # Checker: fetch+cache robots.txt, bare-token AgentAllowed gate, RFC 9309 4xx=allow/5xx=deny, Sitemaps seeds, EqualFold crawl-delay
│   ├── ratelimit.go         # HostLimiters: per-host token buckets, 1/s burst 3, SetFloor for crawl-delay
│   ├── backoff.go           # classify() taxonomy + FetchWithRetry (Permanent / RetryAfter / exponential+jitter)
│   └── crawl_test.go        # httptest: robots gate, canonical vectors, dedup truth, classify/retry counts, resume round-trip
├── store/
│   └── sqlite.go            # +Get/Put/DeleteSelectors, Enqueue/Claim/MarkDone/MarkError/CrawlStats, ResumeRun/ResetInflight, Seen/LoadHashes (extend in place, extend sqlite_test.go)
├── cmd/magpie/
│   ├── crawl.go             # crawl command: all §10 flags, exit 3/4/5/6, --resume, --ignore-robots
│   ├── cache_cmd.go         # cache inspect|clear|heal
│   ├── scrape.go            # +cache-apply on hit (from_cache), --no-cache bypass (extend in place)
│   ├── shared.go            # +csv/sqlite writers, records-table DDL (extend in place)
│   └── cmd_test.go          # crawl e2e matrix + cache commands + exit-code paths (httptest + file://, fake LLM)
├── testdata/
│   ├── selector/            # 3 product-page variants (A/B/C: B renames price node) + expected SelectorDoc JSON
│   └── crawl/               # robots.txt fixtures, canonical-URL vectors, spa-vs-static link pages
├── go.mod / go.sum          # +backoff/v5 v5.0.3, bloom/v3 v3.7.1, grobotstxt v1.0.3, x/sync v0.23.0, x/time v0.16.0
└── plan/phase-2.md          # this file
```

---

## 5. Exit Criteria

- [ ] `magpie crawl <site> --schema price.yaml --max-pages 20 --format jsonl` exits 0 with ≥1 schema-valid record per fetched page (crawl/crawl.go, cmd/crawl.go, core/pipeline.go)
- [ ] Repeat crawl shows 0 new `llm_calls` rows — steady state fully cached (selector/cache.go, store selector accessors)
- [ ] Deterministic A/B test proves price-only re-synthesis fires past the null threshold while other fields serve uninterrupted (selector/heal.go, selector/selector_test.go)
- [ ] Synthesis validates ≥(N-1)/N per field; <80%-agreement fields degrade to per-page LLM with a run-summary note, never silently cached (selector/synth.go, validate.go)
- [ ] SIGINT mid-crawl + `--resume` completes with combined `pages_ok` equal to one uninterrupted run; done pages never re-fetched; stranded `inflight` rows are re-queued; a second `BeginRun` is never issued (store Claim/Mark*/ResumeRun/ResetInflight/LoadHashes, frontier rebuild)
- [ ] Robots-denying origin → 0 fetches, exit 5; robots.txt 503 or unreachable → 0 fetches, exit 5 (RFC 9309); 404 → crawl proceeds; `--ignore-robots` warns on stderr and proceeds (crawl/robots.go, cmd/crawl.go)
- [ ] `cache inspect/clear/heal` all work; `heal` with <2 obtainable samples errors loudly (cmd/cache_cmd.go)
- [ ] `go test ./...` exits 0 with tests in `selector` + `crawl`; no network/browser/keys in default suite (selector_test.go, crawl_test.go, cmd_test.go)
- [ ] Retry taxonomy test: permanent errors = exactly 1 attempt, transient = ≤4 attempts, unparseable Retry-After falls back to exponential (crawl/backoff.go)
- [ ] `CGO_ENABLED=0` builds pass for windows/amd64 + linux/amd64 + darwin/arm64; `go vet` clean; `gofmt -l .` empty; `golangci-lint run ./...` clean (go.mod, .golangci.yml)
- [ ] Crawl terminates on its own when the frontier drains (no hang, no premature close): httptest site of 7 interlinked pages → exactly 7 `done` rows and the process exits (crawl/crawl.go pump + outstanding counter)
- [ ] `magpie scrape` with warm cache prints `from_cache=true` and 0 LLM calls; `--no-cache` forces direct LLM (cmd/scrape.go extension)

---

## 6. Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are implementing Phase 2 of `gomagpie` (module `gomagpie`) — selector cache + concurrency + crawl. Repo: `/home/domidex/projects/gomagpie`. Source of truth: `spec.md` (§4 selector lifecycle, §5 concurrency, §9 storage, §10 CLI, §11 politeness — re-read all five) plus `plan/phase-1.md` (what exists). Conventions: `AGENTS.md` + `.pi/rules/go.md` + `.pi/rules/testing.md` (read all three before writing code).

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`): static fetch → JS detection → optional go-rod browser → trafilatura → markdown + JSON-LD sidecar → LLM structured extraction. Pure-Go, zero CGO, `CGO_ENABLED=0` cross-builds for windows/amd64, linux/amd64, darwin/arm64. Phase 1 built the single-URL pipeline; this phase (spec Milestone 2, weeks 2–4) adds the cost-saving loop: synthesize CSS selectors once, cache by (domain, schema_hash), serve pages with 0 LLM calls, self-heal on null-rate, and crawl sites with bounded concurrency. Phase 3 (MCP + plugins) reuses this phase's pipeline and cache — do NOT build any registry/module/plugin scaffolding.

### Established in Prior Phases
- `fetch`: `NewStaticFetcher()(*StaticFetcher,error)`, `Fetch(ctx,FetchRequest{URL,Timeout})`, `CanHandle`, `Close`; `ScoreJSRequired(html,headers)(score int, embedded bool)` + `NeedsBrowser(score)=score>=2`; `NewRodFetcher()` lazy (import rod ONLY in `fetch/`); `file://` transport registered so `file:///abs/path` scrapes hermetically.
- `clean`: `Clean(ctx,RawPage)(CleanedPage{Markdown,StructuredData,...},error)`; `HarvestSidecar(page []byte) json.RawMessage` (JSON-LD + `__NEXT_DATA__` + `__NUXT__`); 8k-token cap.
- `extract`: `Extractor` interface (`Extract(ctx, ExtractInput) (ExtractResult, error)` + `Name()`); `NewOpenAI(baseURL,key,model,schema)` (also serves Ollama) + `NewAnthropic(...)`, both with a `Log func(purpose string, usage TokenUsage)` field; `Schema{Raw any, Validator, Hints}` via `LoadSchema(path)`/`ParseSchema(bytes)` with `x-gomagpie` hints parsed into `Hints.{CSSHint,Regex,Coerce,JSONLDPath,Trim,Multiple}` (`css_hint` is parsed but UNUSED today; `xpath_hint` is silently dropped); `(*Schema).Validate(doc []byte)`; `JSONLDWalk(sidecar, "$.a.b.0") (any, bool)`; `Coerce/ApplyRegex/ApplyCoercions` exported, kinds `int|float|eur_decimal|iso_date|bool|trim|regex`; `ProjectedCost`/`EstimateCost`/`EstimatePromptTokens`; `runRepairLoop` hardcodes `purpose` = `extract` (first attempt) / `repair` — **`synth` is NOT reachable today; you add `ExtractInput.Purpose`** (see Files).
- `store`: `Open(path)` (single writer `SetMaxOpenConns(1)`, WAL, `busy_timeout=5000`), full §9.2 DDL already migrated; `BeginRun(runID, command)` (INSERT — fails on a duplicate run_id) / `FinishRun(runID, ok, err, status)` (overwrites counters) / `LogLLMCall` (txn) / `RunCost` / `LLMCallCount(runID)` (`""` = all rows — use it for Exit Criterion 2; there is no sqlite3 CLI on the dev box); `LLMCall{Provider,Model,PromptTokens,CompletionTokens,USDEstimate,Purpose}`. modernc.org/sqlite v1.59.0 = SQLite 3.53.4: `RETURNING` works, `UPDATE … LIMIT` does NOT.
- `cmd/magpie`: `newExtractor(provider,key,model,sch,db,runID)` (single provider switch), `checkCostCeiling(db,runID,model,prompt,maxCost)` (fail-closed, exit 6), `fetchBrowser(ctx,url,msg)` (rod lifecycle), `fail(code,fmt,args)` + `exitFor(err)` (codes 1/2/6/7 emitted today — 3 is defined in spec §10.2 but never used; ADD 3 no-records, 4 partial, 5 robots); `uuidNew()`; `--no-cache` flag + `Config.NoCache` already exist but are unused; test-only `GOMAGPIE_BASE_URL` env override for the provider endpoint. Static fetcher UA is `magpie/1.0 (+https://github.com/you/gomagpie)` — robots matching needs the bare token `magpie`.
- Tests are hermetic: `httptest` fake origins/providers, temp-file real SQLite, `//go:build browser` for rod, fake-LLM closures (never real keys).

### Data Model Rules (follow exactly)
- Domain types (`FieldSelector`, `SelectorDoc`, `ClaimedURL`, job structs) are plain structs with `json` tags. No ORM, no codegen.
- Stage boundaries are small interfaces in the consuming package (`Synthesizer`, `Applier`, `Frontier`). Accept interfaces, return structs.
- `Config` gains keys only (concurrency, per-host rps, null-threshold/window, samples N) — same struct, same flags>env>file>defaults precedence.
- Hot path: ONE goquery parse per page, all field selectors applied to the same document; bloom `Test` under `sync.Mutex` (library is unsynchronized).
- Errors wrap with context and fail loudly (`fmt.Errorf("crawl: %w", err)`); the retry taxonomy lives in ONE `classify()` function.
- Deliberate shortcuts get a `ponytail:` comment (json-format buffering, exact-host matching, max-crawl-delay-across-groups instead of Google group precedence, robots re-fetch per run, 25-page null-rate flush, json-format RAM ceiling, a few extra direct-LLM pages before a cold domain's cache flips, one `GetSelectors` point read per page in the clean stage).

### Architecture
Seed (robots-checked first: explicit deny OR 5xx/unreachable per RFC 9309 → exit 5) → frontier (SQLite `crawl_state` + bloom) → `crawl.Run` pump goroutine (`Claim(n)` → `source` chan; closes `source` when a claim is empty AND the `atomic.Int64 outstanding` counter is 0, or when `--max-pages` claimed) → `core/pipeline.go` three errgroup stages over bounded channels (1000/100/100; fetch 8 workers / clean GOMAXPROCS / extract 4; 2-slot browser semaphore; backpressure is structural, no throttle code; each stage closes its output when its group finishes) → single-goroutine writer (jsonl streams, json buffers, csv uses required-then-sorted header + rejects nested loudly, sqlite writes `records` table) with per-page `MarkDone/MarkError` checkpointing then `outstanding.Add(-1)`. `outstanding` goes `+inserted` on every `Enqueue`/`Frontier.Add` and on `ResetInflight`. **No synthesis preamble:** a domain with no cached doc runs direct LLM (`Purpose:"synth"`) in the extract stage, `Healer.Retain`s each page whose record validated with all required fields non-null, and at N=3 samples runs `Synthesize` (css_hint → heuristic → LLM proposal) → `PutSelectors`; heal reuses the same ring with `only=[field]`. Clean stage always harvests the sidecar + extracts links, but runs trafilatura only when the page will need an LLM. Root ctx is `signal.NotifyContext`; 30s drain then `FinishRun(interrupted)` with counts from `CrawlStats`; `--resume` = `ResumeRun` (never a second `BeginRun`) → `ResetInflight` → `LoadHashes` → bloom, and never re-fetches `done` rows. Fetch worker order: robots `Checker` (bare token `magpie`) → `HostLimiters.Wait` → `FetchWithRetry` (fresh `ExponentialBackOff` per call; static; detect ≥2 → browser path). `scrape` checks `GetSelectors(domain,hash)` first (hit → apply, `from_cache=true`, 0 LLM) unless `--no-cache`.

### Confirmed Library APIs (verified 2026-09-16 via `go doc`, library source, and a probe binary on the pinned versions — use exactly these)
```go
// backoff/v5 v5.0.3 — Operation takes NO ctx (close over it); Retry guarantees ≥1 attempt.
// Retry() calls BackOff.Reset() on entry (no manual Reset). *ExponentialBackOff is STATEFUL:
// build one per FetchWithRetry call, never share across workers.
b := backoff.NewExponentialBackOff()
b.InitialInterval = 500 * time.Millisecond // spec §5.4
b.Multiplier = 2.0
b.RandomizationFactor = 0.5               // built-in jitter
b.MaxInterval = 30 * time.Second
resp, err := backoff.Retry(ctx, op, backoff.WithBackOff(b),
    backoff.WithMaxTries(uint(4)), backoff.WithMaxElapsedTime(2*time.Minute))
return nil, backoff.Permanent(err)        // 400/401/403/404/410, schema fail, robots deny, ctx canceled → exactly 1 attempt
return nil, backoff.RetryAfter(secs)      // 429/503 with parseable Retry-After: waits secs + resets policy (probed)
// Loop order: MaxTries check → Permanent → ctx.Cause → NextBackOff → RetryAfter override → MaxElapsedTime.
// Canceled ctx returns context.Cause(ctx) (== context.Canceled on SIGINT).
// bloom/v3 v3.7.1 — NewWithEstimates(n uint, fp float64) *BloomFilter; Add/Test on []byte; NOT goroutine-safe → our mutex.
f := bloom.NewWithEstimates(1000000, 0.01) // 1,198,132 bytes
f.Add([]byte(urlHashHex)); hit := f.Test([]byte(urlHashHex)) // false = definitely unseen; true = confirm in SQLite
// grobotstxt v1.0.3 — pure one-shot funcs, concurrency-safe.
grobotstxt.AgentAllowed(robotsBody, "magpie", rawURL) // bool; userAgent = BARE token (full UA header matches nothing); false on unparseable URL too
grobotstxt.Sitemaps(robotsBody)                       // []string → frontier seeds
grobotstxt.Parse(robotsBody, handler)                 // ParseHandler = 7 methods; HandleUnknownAction(line, action, value) gets action AS WRITTEN ("Crawl-Delay") → strings.EqualFold
// x/sync v0.23.0 + x/time v0.16.0:
g, gctx := errgroup.WithContext(ctx); g.SetLimit(8); g.Go(func() error {...}); g.Wait()
lim := rate.NewLimiter(rate.Every(time.Second), 3); lim.Wait(ctx); lim.SetLimit(rate.Every(d)) // SetLimit(Every(2s)) → Limit()==0.5
// modernc.org/sqlite v1.59.0 (SQLite 3.53.4) — Claim is ONE statement; `UPDATE … LIMIT` is a syntax error here:
UPDATE crawl_state SET status='inflight', updated_at=? WHERE run_id=? AND url_hash IN
  (SELECT url_hash FROM crawl_state WHERE run_id=? AND status='pending' LIMIT ?)
RETURNING url, url_hash, depth
```
Pins (exact): `backoff/v5 v5.0.3`, `bloom/v3 v3.7.1`, `grobotstxt v1.0.3`, `x/sync v0.23.0`, `x/time v0.16.0` (all are the latest tags as of 2026-09-16; bloom also pulls `bitset` + `twmb/murmur3`, pure Go).

### Files to Create
`core/pipeline.go` (job types + 3-stage `Run(ctx, cfg, source, fetchFn, cleanFn, extractFn, sink)` with bounds above, browser semaphore inside fetch stage, each stage closes its output when done — no frontier knowledge); **extend `extract/extractor.go`** (`ExtractInput.Purpose string`; `runRepairLoop` uses it for the first attempt, default `"extract"`, `"repair"` unchanged — 3 lines); `selector/synth.go` (`Synthesize(ctx, samples, schema, propose, only)`: candidates in order `css_hint` → heuristic → LLM-proposal (ad-hoc string-per-field schema via `ParseSchema`, trimmed HTML ≤ `clean.MaxTokens`, `Purpose:"synth"`); ground truth arrives IN the samples; `schema_hash`=`sha256(json.Marshal(sch.Raw))`; min-1-sample-warn / 0-samples-error; `xpath_hint` → one stderr warning); `selector/validate.go` (≥(N-1)/N per-field agreement on canonical-JSON comparison, 2/3 caches with named-field stderr warning, <80% fields → non-cacheable + run-summary note); `selector/cache.go` (`Applier`: one parse/page, `jsonld_path` via existing `extract.JSONLDWalk`, selector→regex→coerce via existing `extract` funcs); `selector/heal.go` (`Healer`: 50-outcome rings, 0.30 trigger, `Retain`/`Samples` ring of 3 validated samples per domain used by BOTH cold-start and heal, per-field re-synth via `Synthesize(only=…)`, ≥50%-broken → full re-synth, flush null_rates every 25 pages + run end); `crawl/crawl.go` (orchestration per Architecture: seed robots gate, pump + `outstanding` counter, lazy synthesis in the extract fn, `FinishRun` from `CrawlStats`); `crawl/frontier.go` (`Canonicalize`: lowercase host, strip :80/:443, sort query, drop utm_*/gclid/fbclid/msclkid + fragment; `Frontier{Add,Claim}` — `Add` returns inserted count; link extraction from RAW html, exact-host + depth gate); `crawl/bloom.go` (mutex wrapper over `*bloom.BloomFilter`); `crawl/robots.go` (`Checker`: per-host fetch-once, bare token, RFC 9309 4xx→allow / 5xx+network→deny with `ErrRobotsUnreachable`, sitemap seeds, `EqualFold` crawl-delay with max over matching groups → `SetFloor`); `crawl/ratelimit.go` (`HostLimiters`, default 1/s burst 3, `--rate` override, floor only lowers); `crawl/backoff.go` (`classify` + `FetchWithRetry` with a fresh `ExponentialBackOff` per call); extend `store/sqlite.go` (12 methods per Task 2.2 incl. `ResumeRun`/`ResetInflight`; `Claim` = single `UPDATE … IN (SELECT … LIMIT ?) RETURNING`; never return open rows; `records` table NOT in migration — writer creates on demand); `cmd/magpie/crawl.go` (all §10 crawl flags + `--ignore-robots` with stderr WARNING + codes 3/4/5/6) + `cache_cmd.go` (inspect/clear/heal; heal BFS ≤3 pages from `--seed-url`, min 2 samples); extend `scrape.go` (cache-apply + `--no-cache`) and `shared.go` (csv/sqlite writers). Tests: `selector/selector_test.go` (fake `propose` closure; css_hint-first; A/B heal isolation via call counts), `crawl/crawl_test.go` (httptest robots incl. token-vs-header regression + 503/404/closed-port, `Crawl-Delay` capitalised, canonical, dedup, classify/retry counts, resume with stranded inflight rows, termination on a 7-page interlinked site), `cmd/magpie/cmd_test.go` (crawl matrix + cache cmds + exit codes 3/4/5/6, `file://` + fake provider). Fixtures: `testdata/selector/` (3 product variants, B renames price node) + `testdata/crawl/` (robots, URL vectors, link pages). Do NOT create `core/registry.go`, `core/module.go`, or anything WASM/exec/MCP — that is Phase 3.

### Success Criteria
- All 12 Exit Criteria (Section 5) check out, in order.
- `go test ./... && go vet ./...` exit 0; `gofmt -l .` prints nothing; `golangci-lint run ./...` clean; all three `CGO_ENABLED=0` cross-builds succeed.
- A capable engineer pastes this prompt into a fresh session with no other context and ships the phase with zero follow-up questions.

### Expected File Structure at End
(See Section 4 Deliverables tree — reproduce it exactly: new `core/pipeline.go`, `selector/` (4 src + 1 test), `crawl/` (6 src + 1 test), extended `store/sqlite.go` + `extract/extractor.go`, `cmd` +2 new / +2 extended, `testdata/selector/` + `testdata/crawl/`, pinned `go.mod`.)
---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — Phase 1 deliverables verified on disk (`fetch/`, `clean/`, `extract/`, `config/`, `store/`, `cmd/magpie/`, `testdata/clean|extract/`); concrete constructor/method names in the execution prompt were read from the actual source (fetcher.go, sqlite.go, extractor.go, schema.go, shared.go, main.go), not assumed from the Phase 1 plan
- [PASS] Every sub-task has a clear, testable completion condition — Tasks 2.1–2.8 each end with a one-liner `Sanity check`
- [PASS] Execution prompt is self-contained: (a) prior-phase facts enumerate exact function/type names and behaviors, (b) five confirmed API snippets with exact pins, (c) Go Data Model Rules table, (d) per-file guidance for all ~20 files, and (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables — 11 criteria cover pipeline, cache/apply, heal, synth/validate, resume, robots, cache cmds, tests, retry taxonomy, cross-builds, scrape cache integration; `plan/phase-2.md` maps to this artifact itself
- [PASS] Heavy external dependencies have fake/stub strategies noted — LLM via fake closure/`httptest` provider (never keys), browser via existing `//go:build browser` gate + 2-slot semaphore (no new browser tests), Chrome/LLM never in default suite; SQLite via real pure-Go temp DB (justified: millisecond-fast, DSN behavior must be real)
- [PASS] New libraries have confirmed usage snippets in the execution prompt — backoff/v5 (Retry signature, struct fields, Permanent/RetryAfter), bloom/v3 (NewWithEstimates/Add/Test + mutex requirement), grobotstxt (AgentAllowed/Sitemaps/Parse), errgroup + rate idioms; versions resolved live on proxy.golang.org 2026-09-16 (v5.0.3 / v3.7.1 / v1.0.3 / v0.23.0 / v0.16.0) and struct fields confirmed via `go doc` (not just pkg.go.dev prose)
- [PASS] Second validation pass 2026-09-16 (library source + probe binaries + RFC 9309 + Phase 1 code at `182b4eb`): fixed robots 5xx semantics, bare-token matching, raw-key crawl-delay, stateful backoff, modernc `UPDATE … LIMIT` rejection, missing `synth` purpose, unspecified sample selection (→ lazy synthesis), missing termination condition, three `--resume` holes, unused `css_hint`, exit-code inventory, and the lint gate. Facts recorded in the project memory note `phase2-plan-validation-2026-09-16`.
