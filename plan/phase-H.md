# Phase H — P1 differentiation (actions DSL, verticals breadth, watch, crawl status, locale)

**Duration:** Days 1–5 (~30 hours)
**Depends on:** Phase G (rod `ScreenshotPage` capability, `GuardedTransportWithOptions`/`AllowPrivate` seam, `BrowserHelp` + `ValidateOptions` single validation site, `cli.exitFor` exit map, proxy `{{session}}` gating docs), Phase A (`clean` quality gate, page formats), Phase B (vertical registry + OptIn pattern), Phase C (MCP surface, flex-shadow round-trips)
**Blocks:** nothing planned (P2 items from the competitive analysis are explicitly out of scope; the Wails GUI will consume `--action`/`watch` surfaces but no phase depends on this one)
**Risk Level:** HIGH — four trust/flow decisions: (1) the actions executor rewires `RodFetcher.Fetch` (every existing browser consumer must keep byte-identical behavior); (2) the watch webhook dials operator-chosen URLs with `AllowPrivate: true` — the allowance must be scoped to that one client or it becomes an SSRF hole, and its rejection-proof test must pin `GOMAGPIE_STRICT_SSRF=1` because the test binary auto-relaxes AllowPrivate (`fetch/ssrf.go:138`); (3) `--lang` is a user-controlled header value crossing into HTTP — a CRLF there is header injection; (4) the `screenshot` action is a server-side file-write — CLI-only, rejected at the MCP boundary. Mitigation: Fetch delegates to the actions path with nil actions (one code path), a STRICT_SSRF-pinned webhook test, a control-char validation test, and an MCP rejection test.
**Stack:** go *(per AGENTS.md: `run-phase` hard-blocks on `stack: go` — execute manually via the execution prompt in §7)*
**New libraries:** none. go-rod v0.116.2 (already a dep, `fetch/`-only) is used in a new way — interaction primitives line-checked against the module cache: `page.Element(sel)` (auto-wait, `(*Element, error)`), `el.Click(proto.InputMouseButtonLeft, 1)` (two-arg, clickCount), `el.SelectAllText()` + `el.Input(text)` (Input = InsertText, no select-all — SelectAllText-first for replace), `page.WaitElementsMoreThan(sel, n)`, `page.WaitStable(d)`, `page.Eval(js, args...)` (arrow-fn or expression, `*proto.RuntimeRemoteObject`), `page.Screenshot(fullPage bool, opts)`, `page.SetExtraHeaders(dict []string) (func(), error)`. No dependency ask. **Executor must still re-verify exact signatures via `go doc github.com/go-rod/rod.Page` / `.Element` in the module cache before writing code.**

---

## 1. Objective + What Success Looks Like

Close the P1 "bigger swings" list from `plan/competitive-analysis-2026-09-18.md` §P1, **re-scoped to what HEAD still lacks** (Phase G + earlier phases already landed screenshots, reddit/HN verticals, and crawl `--resume`): (1) an actions DSL executed by rod before capture — paginated lists, cookie-consent dismissal, "load more"; (2) six zero-LLM extractor additions/extensions (reddit comment trees, stackoverflow, trustpilot, dockerhub, huggingface, OptIn generic-OG); (3) a `magpie watch` change-monitor with SQLite snapshots, word-diff, and webhook emit; (4) CLI parity for crawl status polling (`--status run_id`, MCP already has it); (5) `--lang` locale knob wired through header profiles and the rod path. All local-first/BYOK — no hosted anything.

1. `ParseActions` table test passes: every verb (`click|type|scroll|wait|wait-for|screenshot|eval-js`), rest-of-line semantics for `type`/`screenshot`/`eval-js` (spaces allowed without quoting), `#` comments, blank lines, line-numbered errors, unknown verb named in the error, `wait` capped at 30000 ms.
2. `magpie scrape URL --action 'click #consent-accept' --action 'wait-for .item:nth-child(30)'` against an httptest page whose JS renders more rows on click: the cleaned markdown contains items 1–30 (browser-gated test; the default suite pins validation + JSON envelope only). Without actions, the same fixture yields only server-rendered items.
3. `--action 'click #x'` + `--render static` → exit 2 pre-I/O ("actions require browser rendering"); `--action 'frobnicate #x'` → exit 2 naming the verb and line; `--action 'wait 99999'` → exit 2 naming the 30000 ms cap.
4. MCP `scrape_url` accepts `actions` (string list, each element one action line), round-trips through the flex-shadow coercion tests (`mcp/agent_test.go` `TestIn_RoundTrip` pattern), and rejects a `screenshot` action line pre-flight with a typed CLI-only error (the MCP surface must never gain a file-write primitive).
5. `magpie scrape 'https://www.reddit.com/r/golang/comments/abc/x/' --vertical reddit` returns a `comments` array (nested `replies`, depth-capped) from a recorded `.json` fixture; on `.json` failure the HTML fallback still yields the old summary shape (the one order-pin assertion in `TestRedditExtract_FallbackJSON` is rewritten per §4 H.2 — the only pre-existing test this phase may touch). `--vertical stackoverflow|trustpilot|dockerhub|huggingface` each extract their fixture's fields; `--vertical og` extracts OG/meta from arbitrary HTML but `vertical.MatchURL` on that same HTML URL returns **false** (OptIn holds — never auto-fires).
6. `magpie watch URL --every 1h --webhook http://127.0.0.1:PORT/hook --once`: first invocation stores a baseline snapshot and reports `changed=false`; after the fixture mutates, a second invocation prints the word-diff, reports `changed=true` with old/new hashes, and the httptest webhook handler received `POST` JSON `{url, changed, old_hash, new_hash, diff}`. `--every 5s` → exit 2 (floor is 30s). A loopback webhook succeeds while a loopback *scrape target* stays rejected by the SSRF guard (scoped-allowance proof). Ctrl-C exits 0 with no stack noise.
7. `magpie crawl --status <run_id>` prints run row + `pending/inflight/done/errors` counts matching the MCP `crawl_site` `run_id` poll shape; unknown run_id → exit 4.
8. `magpie scrape URL --lang fr-CA,fr;q=0.9` (httptest): the request carries `Accept-Language: fr-CA,fr;q=0.9` overriding the profile default, with and without `--header-profile chrome`; `--lang $'en\r\nX-Evil: 1'` → exit 2. MCP `scrape_url` gains `lang` with the same validation.
9. `go test ./...`, `go test -tags browser ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...`, `go test -race ./fetch/ ./scrape/ ./store/ ./vertical/ ./cli/`, and 3-way `CGO_ENABLED=0` cross-builds pass; no live network in the default suite; `testdata/clean` golden drift empty; suite wall-clock holds at the testing.md ~2 min budget (it sits AT the budget today — the §2 tier-behind-build-tag clause is expected to trigger for watch/loop tests).

**Good:** "`--action 'click .load-more' --action 'wait-for tr:nth-child(25)'` scrapes rows the static page never served"
**Bad:** "Actions work and we monitor pages better"

---

## 2. What Failure Looks Like (and what to do)

- **Refactoring `Fetch` to delegate to the actions path changes non-action browser behavior** (settle order, budget, page teardown) → the delegation must be `Fetch(req) = fetchWithActions(req, nil)` with nav/WaitLoad/2s-settle/`page.HTML()` byte-identical to today; `fetch/rod_smoke_test.go` plus the existing scrape browser tests are the contract. If the smoke test drifts, stop and inline the old body instead of "fixing" the test.
- **Rod auto-wait hangs** (animation, endless spinner) → hangs are bounded by the ctx budget (20s default) — that is the design; never add an implicit network-idle wait. Docs say: slow pages get explicit `wait`/`wait-for` lines. Fixture pages in tests must be deterministic (no animations, no clocks).
- **`og` extractor starts auto-firing** (someone drops `OptIn: true`) → every scrape grows a `Record` and the markdown contract breaks silently. Pinned by a test asserting `MatchURL` returns false on a generic fixture URL. This is the single hardest regression in the phase — the test goes in the same commit as the extractor.
- **Webhook `AllowPrivate` leaks into the scrape path** → the transport with `AllowPrivate: true` is built inline in `scrape.CheckForChange` for the webhook POST only; the scrape/vertical fetch path keeps the default guard. Proof test: loopback webhook delivered + loopback scrape target still rejected — **the rejection half runs under `t.Setenv("GOMAGPIE_STRICT_SSRF", "1")`** (serial, no `t.Parallel`): under plain `go test` the default fetcher auto-relaxes to `AllowPrivate: isTestBinary()` (`fetch/ssrf.go:138`), so the loopback target would *pass* the guard and the test would fail backwards; `GOMAGPIE_STRICT_SSRF=1` is the Phase-G security-matrix precedent (`fetch/proxy_test.go:170`). Never hoist that transport into a shared var a future caller might reuse.
- **`--lang` CRLF injection** → control-char check in `ValidateOptions` (the single validation site) rejects any value containing `unicode.IsControl` runes. A test posts the `\r\n` payload and asserts `OptionsError`, not a sent request.
- **The `screenshot` action becomes an MCP file-write primitive** → an action line would hand an MCP agent a server-side write to an arbitrary path (`screenshot /etc/cron.d/x`). Enforced as a CLI-only verb: `handleScrape` pre-flights the lines and rejects `screenshot` with a typed error; the CLI keeps the verb; a test asserts the MCP rejection. (`eval-js` arbitrary JS is inherent to a browser-action feature — documented, not gated.)
- **Reddit `.json` shape drifts** → fixtures under `testdata/vertical/` recorded with a dated comment are the contract; regenerate from the recorded file, never from a live call inside tests. `TestRedditExtract_FallbackJSON` (`social_test.go:99–101`) pins the OLD HTML-then-`.json` request order — rewriting that one order-pin assertion is explicitly sanctioned (§4 H.2); every other reddit assertion stays untouched.
- **Snapshots table migration breaks existing DBs** → `CREATE TABLE IF NOT EXISTS` inside the shared DDL const (idempotent, zero migration code, same pattern as every prior table). No ALTER anywhere.
- **Suite budget blowout** → browser tests behind `//go:build browser` don't count; watch-loop tests tick at 100 ms and use `--once`/single-check calls, never real sleeps ≥ 1 s. If the default suite crosses 2 min, tier before merging.

---

## 3. Architecture / Key Design Decisions

```
CLI --action (repeatable) ─┐        MCP actions: string[] ─┐
CLI --actions <file> ──────┤                               │
                           ▼                               ▼
                 scrape.Options.Actions []string   (one line per element)
                           │
              fetch.ParseActions(lines) → []Action      (pure, table-tested)
                           │
        render=auto + actions ⇒ browser path (static ⇒ exit 2)
                           ▼
        RodFetcher.fetchWithActions(req, acts)  ← Fetch(req) = same body, acts=nil
          nav → WaitLoad → [click|type|scroll|wait|wait-for|screenshot|eval-js] → 2s settle → HTML
                           ▼
        existing clean → quality gate → extract/vertical pipeline (unchanged)

magpie watch: scrape.Run(markdown, zero-LLM) → sha256 → store.LatestSnapshot
              → changed? DiffWords + webhook POST (AllowPrivate transport, scoped)
              → store.PutSnapshot (always) → WatchResult → CLI loop (ticker + signal)
--lang: Options.Lang → ValidateOptions (control-char check)
        → FetchRequest.Lang → header-profile merge (static) / extra headers (rod)
crawl --status: store.GetRun + store.CrawlStats → same fields MCP poll returns
```

### Data Model Rules (Go — per repo ethos: plain data, no single-impl interfaces)

| Construct | Shape | Why |
|---|---|---|
| Action step | `type Action struct { Verb, Sel, Text string; Ms int }` — one flat struct; field meaning documented per verb (`Text` = type text / screenshot path / eval-js expr / scroll arg; `Ms` = wait ms / scroll px) | one implementation, no tagged-union ceremony |
| DSL parser | `func ParseActions(lines []string) ([]Action, error)` — pure, line numbers in errors | file and inline sources both become `[]string` before parse; one test surface |
| Actions transport | option on existing `scrape.Options` + `fetch.FetchRequest`-adjacent rod method — no new interfaces | rod already behind `Fetcher`; actions are a rod-only capability like `Screenshot` |
| Watch check | `func CheckForChange(ctx, d Deps, rawURL string, o Options) (WatchResult, error)` in `scrape/watch.go`; `WatchResult{URL, Changed, OldHash, NewHash, Diff string, WebhookStatus string, CheckedAt}` | scrape pkg already owns diff + Deps; CLI owns only the loop/ticker/signals |
| Snapshots | two methods on `*store.DB` in a new file: `PutSnapshot(url, hash, markdown string) error`, `LatestSnapshot(url string) (Snapshot, bool, error)`; table via the shared DDL const; `checked_at` stored as **RFC3339Nano** — second-precision RFC3339 + the PK makes same-second inserts (normal at 100 ms test ticks) a guaranteed PK-violation flake; fixed-width Nano keeps lexicographic `ORDER BY checked_at DESC` correct | `store/sqlite.go` is at the "don't grow" ceiling (642 lines) — new file, same receiver |
| Lang | `Options.Lang string` → `FetchRequest.Lang string`; merged in `fetch/http.go` after profile headers; rod path sets extra headers | one knob, both paths, single validation site |

### Decisions with rationale (deviations from the analysis flagged)

- **DSL is a line format, not YAML/JSON** (`click sel` one per line): proxy-pool-file precedent, zero deps, hand-writable, diffable. Quoting is *not needed*: `type`, `screenshot`, and `eval-js` take "rest of line" as their argument (`strings.Cut`); only `click|wait-for` (selector) and `scroll|wait` (token) are single-field. No quoting library, no escape rules.
- **Actions force the browser path** and reject `render=static` — the whole point is DOM interaction; a static fetch cannot honor them, so silent-ignore is not an option (fail loudly, exit 2 pre-I/O).
- **Screenshot mid-flow is a first-class action** (`screenshot path` → viewport PNG written to path) so one browser session can click-then-capture. `page-format screenshot` + actions shares the same session (the capture becomes the final step); this is the Firecrawl "screenshot after actions" shape. **CLI-only:** the MCP surface rejects the `screenshot` verb pre-flight — an action line would hand an agent a server-side file-write to an arbitrary path; `page_format screenshot` stays base64-through-the-envelope on MCP, as it already is.
- **`og` is OptIn, not "always-on fallback"** — *deviation from the analysis, deliberate*: our vertical contract guarantees permissives never auto-fire, and an always-on extractor would attach a `Record` to every scrape, changing the default output shape for all users. Explicit `--vertical og` / MCP `vertical_scrape` covers webclaw's use case honestly. Flagged here so nobody "fixes" it later.
- **Reddit permalinks go `.json`-first** (comment trees are the payload), HTML demoted to fallback — *inversion of today's order, scoped to permalinks only*; subreddit listings stay HTML-first. Caps: depth 10, 200 comments total (`ponytail:` caps named in code; the upgrade path is pagination via `more` objects).
- **Locale = `--lang` only; no `--country` flag** — *deviation*: a country knob would imply IP geo, which we don't control; geo egress is already expressible as a proxy-pool entry (gateway username templates, Phase G `{{session}}` design). A fake `--country` would silently do nothing on direct connections — worse than absent.
- **Webhook client is scoped-`AllowPrivate`** (operator typed the URL on the CLI — same trust tier as `GOMAGPIE_PROXY_FILE` and `GOMAGPIE_SEARXNG_URL`, searxng precedent). Single attempt, no retry loop (`ponytail:` one-shot; cron re-runs are the retry). Failure lands in `WatchResult.WebhookStatus`, never fails the check after the snapshot is stored.
- **Watch snapshots are append-only, no pruning** (`ponytail:` ceiling = unbounded history for long watches; upgrade path is a keep-N prune — add when a user hits it, not before).
- **No MCP `watch` tool this phase** — a ticker loop doesn't fit MCP request/response, and `--once` + cron is the local-first story. `CheckForChange` is importable when the GUI wants it.

---

## 4. Tasks

### Task H.1 — Actions DSL: parser + rod executor + surface (10h)

**`fetch/actions.go`** (new):

```go
// Verbs: click <sel> | type <sel> <text…> | scroll <n|top|bottom>
//        wait <ms> | wait-for <sel> | screenshot <path> | eval-js <expr…>
// Rest-of-line args keep spaces without quoting. '#' comments, blanks skipped.
type Action struct{ Verb, Sel, Text string; Ms int }
func ParseActions(lines []string) ([]Action, error) // line-numbered errors, wait ≤ 30000ms
```

- Parser is pure; table-test verbs, rest-of-line semantics, comments, line numbers, unknown verb, wait cap, empty text/expr/path → error.
- Executor on `RodFetcher` (rod stays in `fetch/`):

```go
// nav → WaitLoad → actions → 2s settle → page.HTML()  (identical prologue/epilogue to Fetch)
func (r *RodFetcher) fetchWithActions(ctx context.Context, req FetchRequest, acts []Action) (*FetchResponse, error)
func (r *RodFetcher) Fetch(ctx, req)   // becomes: return r.fetchWithActions(ctx, req, nil) — behavior contract
func ScreenshotActions(ctx, rawURL string, w, h int, acts []Action) ([]byte, error)
// ScreenshotPage(ctx, url, w, h) becomes: ScreenshotActions(ctx, url, w, h, nil)
```

- Action mapping (signatures line-checked against rod v0.116.2 element.go; still re-verify in module cache before use): `click` → `page.Element(sel)` + `el.Click(proto.InputMouseButtonLeft, 1)` — TWO args, clickCount included (element.go:104); `type` → `el.SelectAllText()` then `el.Input(text)`: rod's `Input` is Focus → WaitEnabled → WaitWritable → InsertText with NO select-all, so bare Input appends to pre-filled fields — SelectAllText-first gives the replace semantics users expect (rod's own doc shows the `SelectAllText().MustInput("")` idiom, element.go:243/260); `wait-for` → `page.Element(sel)` (auto-wait, discard); `wait` → ctx-select over `time.After` (same pattern as the settle); `scroll` → `page.Eval("window.scrollTo(0, document.body.scrollHeight)")` / `scrollTo(0,0)` / `scrollBy(0, n)`; `eval-js` → `page.Eval(expr)`, result discarded; `screenshot path` → `page.Screenshot(false, nil)` + `os.WriteFile` (CLI only — MCP rejects this verb, see below). After `click`/`type`/`scroll`: fixed 500ms settle (`ponytail:` no network-idle heuristic; slower UIs use `wait-for`).
- **`scrape` wiring:** `Options.Actions []string`; `ValidateOptions` gains two checks (both pre-I/O, exit 2): `len(Actions)>0 && render=="static"` and `ParseActions` smoke (parse errors surface here as `OptionsError` — validate early, don't wait for the browser). `Run`: when actions present and render is auto → browser path; `Actions` (and H.5's `Lang`) thread through `fetchURL`'s three call sites into `fetchBrowser`; `page-format screenshot` + actions routes through `fetch.ScreenshotActions` (one session, capture last). Note: the rod path already drops Profile/Cookies today (pre-existing) — leave it, and never assume profile headers reach rod in an H.5 rod test.
- **CLI** `cli/scrape.go`: `--action` (repeatable StringSlice, inline) + `--actions <file>` (one line per action, `#` comments — proxy-file precedent); file lines run first, then inline in flag order.
- **MCP** `mcp/tools.go`: `Actions StringList` on `ScrapeIn` + jsonschema string; `handleScrape` pre-flights the lines and rejects the `screenshot` verb with a typed error ("screenshot action is CLI-only; use page_format screenshot for base64") — the parser is caller-agnostic by design, so the check lives at the MCP boundary; extend the flex-shadow round-trip test in `mcp/agent_test.go` and add the rejection test.
- **Tests:** parser table (default suite); executor behind `//go:build browser` with an httptest page that renders rows on click (the "load more" story, end to end); validation/envelope tests in the default suite; `rod_smoke_test.go` stays green untouched (the delegation contract).

**Sanity check:** `go test ./fetch/ -run TestParseActions -v` then `go test -tags browser ./fetch/ ./scrape/ -run Actions -v`

### Task H.2 — Verticals breadth: six additions/extensions (8h)

All follow the established registry pattern (per-file constructor, `register(...)` in `init`, fixture-driven tests, no live network). New files where the file-per-family precedent allows; `registries.go` (137 lines) takes dockerhub + huggingface.

1. **reddit comment trees** (extend `vertical/social.go`): permalink path fetches `old.reddit.com<uri>.json` **first**, walks `data.children` recursively (`replies.data.children`), emits `comments: [{author, score, body, created, replies:[…]}]`; caps depth 10 / 200 comments (`ponytail:` comment names the `more`-object upgrade path). HTML fallback (existing summary shape) fires when `.json` fails. Subreddit path untouched. **Order-pin sanctioned:** `TestRedditExtract_FallbackJSON` (`social_test.go:99–101`) pins the old HTML-then-`.json` two-request shape — rewrite it to pin the new contract (`.json`-first success = exactly 1 request; fallback = `.json` request then HTML request). This is the ONLY pre-existing assertion this phase may touch.
2. **stackoverflow** (new `vertical/stackoverflow.go`): match `stackoverflow.com/questions/{id}(/slug)?` only (`ponytail:` SE-network sites out of scope). `GET api.stackexchange.com/2.3/questions/{id}?site=stackoverflow&filter=withbody` + `GET …/questions/{id}/answers?site=stackoverflow&filter=withbody&sort=votes&pagesize=10` → `{kind:"question", title, score, tags, body, owner, answers:[{score, body, is_accepted}]}`. Bodies stay raw HTML (consumers clean; record stays flat).
3. **trustpilot** (new `vertical/trustpilot.go`): match `www.trustpilot.com/review/{domain}`. Parse embedded `<script type="application/ld+json">` (goquery — already a dep) → business name, `aggregateRating`, `review[] {author, ratingValue, datePublished, reviewBody}`. No API key, no JS render — JSON-LD is server-rendered.
4. **dockerhub** (`registries.go`): match `hub.docker.com/r/{owner}/{repo}` → `GET hub.docker.com/v2/repositories/{owner}/{repo}/` → `{name, description, star_count, pull_count, last_updated}`.
5. **huggingface** (`registries.go`): match `huggingface.co/{owner}/{name}` and `huggingface.co/datasets/{owner}/{name}` → `GET huggingface.co/api/{models|datasets}/{owner}/{name}` → `{id, likes, downloads, tags, pipeline_tag}` (datasets omit `pipeline_tag`).
6. **og generic** (new `vertical/og.go`, **`OptIn: true`**): any URL → goquery over the fetched HTML: `og:title/description/image/url/type`, `twitter:card/title/description/image`, `<title>`, `meta[name=description]`, `link[rel=canonical]`, first `<h1>`. Flat map. **Same-commit pin: `MatchURL` returns false on a generic fixture URL** (the OptIn regression guard from §2).
- Fixtures under `testdata/vertical/` (recorded shapes, dated comment): `reddit-thread.json`, `stackoverflow.json` (+ answer page), `trustpilot.html` (synthetic JSON-LD page), `dockerhub.json`, `huggingface.json`, `og.html`.
- `magpie vertical --list` renders from the registry — no list-site edits.

**Sanity check:** `go test ./vertical/ -run 'TestReddit|TestStackOverflow|TestTrustpilot|TestDockerHub|TestHuggingFace|TestOG' -v`

### Task H.3 — `magpie watch` (6h)

- **`store/snapshots.go`** (new file, `*store.DB` receiver): DDL const in `sqlite.go` gains

```sql
CREATE TABLE IF NOT EXISTS snapshots (
    url_hash     TEXT NOT NULL,   -- sha256 of canonical URL
    url          TEXT NOT NULL,
    content_hash TEXT NOT NULL,   -- sha256 of markdown
    markdown     TEXT NOT NULL,
    checked_at   TEXT NOT NULL,   -- RFC3339Nano, NOT second precision: the PK + 100ms test ticks make same-second inserts routine and second precision a guaranteed PK-violation flake; fixed-width Nano keeps lexicographic ORDER BY correct
    changed      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (url_hash, checked_at)
);
```

  `PutSnapshot`, `LatestSnapshot` (ORDER BY checked_at DESC LIMIT 1). Idempotent DDL — no migration code.
- **`scrape/watch.go`**: `CheckForChange(ctx, d Deps, rawURL string, o Options) (WatchResult, error)` — runs `Run` with `Schema: nil` (markdown-only, zero LLM), hashes the markdown, compares to `LatestSnapshot`, builds `DiffWords` diff on change, **always** stores the snapshot (first run = baseline), then fires the webhook (only on change, only when `Options.Webhook != ""`). Webhook client: `&http.Client{Transport: fetch.GuardedTransportWithOptions(fetch.SSRFOptions{AllowPrivate: true}), Timeout: 10s}` — **built inline in this function**, comment cites the operator-chosen-endpoint rationale (searxng precedent); POST JSON `{url, changed, old_hash, new_hash, diff}`; single attempt, outcome recorded in `WatchResult.WebhookStatus` (`"sent"` / `"failed: …"` / `""`), never an error after the snapshot is stored.
- **`cli/watch.go`**: `magpie watch <url> --every 1h [--webhook URL] [--once] [--render] [--browser] [--lang]` — loop = immediate first check, then ticker; `signal.NotifyContext` (Ctrl-C → clean exit 0); `--every` via `time.ParseDuration`, floor 30s → exit 2 below; `--once` runs a single check (the cron/systemd-timer mode). Output per check: `changed=true` summary line + the word-diff on change; nothing on no-change (diff-command precedent: silence = unchanged). Exit codes unchanged (0 on no-change *and* on change; failures use the standard map).
- Register in `cli/root.go` `AddCommand`.
- **Tests** (default suite, no loop sleeps > 100ms): baseline → change → diff+webhook sequence against httptest fixtures and an httptest webhook sink; scoped-allowance proof runs under `t.Setenv("GOMAGPIE_STRICT_SSRF", "1")` and stays serial — plain `go test` auto-relaxes AllowPrivate so the loopback target would otherwise PASS the guard (`fetch/ssrf.go:138`), see §2; `--every` floor; snapshot row count grows by one per check, including two checks inside one wall-clock second (RFC3339Nano — no PK collision); `LatestSnapshot` empty-DB shape.

**Sanity check:** `go test ./scrape/ -run TestWatch -v && go run ./cmd/magpie watch --help`

### Task H.4 — `crawl --status` CLI parity (1.5h)

MCP `crawl_site` already polls (`run_id` → `CrawlStats` + run row, `mcp/tools.go:147`). Add the CLI twin: `magpie crawl --status <run_id>` → prints run row (`command`, `started_at`, `finished_at`, `status`, pages) + `pending/inflight/done/errors` from `store.CrawlStats`. Unknown run_id: `GetRun` returns `store: unknown run_id %q` (`store/sqlite.go:252`) → handler-local `fail(4, …)` — **no sentinel, no `exitFor` arm**: exit 4 has no global arm by design (it arrives via `cmdError`), and root.go's own doc keeps command-specific usage errors local via `fail(code, …)`. Deliverables: handler + README row + asserting test. No new store methods (`GetRun`, `CrawlStats` exist).

**Sanity check:** `go run ./cmd/magpie crawl --status nope; echo $?` → 4

### Task H.5 — `--lang` locale knob (2h)

- `scrape.Options.Lang string`; `ValidateOptions`: reject empty-after-trim? no — reject only values containing `unicode.IsControl` runes (header-injection boundary; exit 2 naming the rule). No BCP47 grammar policing — `Accept-Language` is a passthrough.
- `fetch.FetchRequest.Lang string`; `fetch/http.go` applies profile headers then overrides `Accept-Language` when `Lang != ""` (one merge site — profile maps never mutate).
- rod path: when `Lang` set, set the extra header on the page (`page.SetExtraHeaders(dict []string) (func(), error)` — the error-returning form, confirmed at v0.116.2) inside the shared prologue. The rod path drops Profile/Cookies today (pre-existing); the H.5 rod test asserts ONLY that the lang header arrives — never that profile headers did.
- Wire through: `cli/scrape.go` `--lang`, batch + crawl commands (wherever `--browser`/`--header-profile` already appear — mechanical), `mcp/tools.go` `lang` on `ScrapeIn` + jsonschema + round-trip test. Vertical registry fetches stay default-header (locale-free APIs — documented).
- **Tests:** httptest asserts the header overrides the profile default (with and without `--header-profile chrome`); control-char payload → `OptionsError`; rod extra-header behind `//go:build browser`.

**Sanity check:** `go test ./fetch/ ./scrape/ -run 'Lang' -v`

### Task H.6 — Docs + spec delta (1.5h)

- README: `--action/--actions` DSL table (all 7 verbs, rest-of-line rule, 30s wait cap), `watch` command + webhook payload + `--once`/cron pattern + 30s floor, `--lang`, `crawl --status`, new verticals, exit-code table row for `crawl --status` miss.
- `spec.md`: short deltas — actions DSL surface, snapshots table, lang knob, OptIn `og` rationale (one paragraph each). Spec stays source of truth.
- Note the standing caveat: actions need the browser (rod launch on first use), `wait` caps at 30s, no implicit network-idle.

### Task H.7 — Gate: scoped-trust proofs + full checks (1h)

Three assertions form the phase's safety gate, each already written as a test in its task but re-run together here:

1. `TestWatch_WebhookScope` — loopback webhook delivered AND loopback scrape target still SSRF-rejected (H.3).
2. `TestValidateOptions_LangControlChars` — `\r\n` payload rejected pre-I/O (H.5).
3. `TestOG_NeverAutoFires` — `MatchURL` miss on a generic page (H.2).
4. `TestMCP_ScreenshotActionRejected` — the `screenshot` verb never crosses the MCP boundary (H.1).

Then: `go test ./... && go test -tags browser ./... && go vet ./... && gofmt -l .` (empty) `&& golangci-lint run ./...`, `go test -race ./fetch/ ./scrape/ ./store/ ./vertical/ ./cli/`, 3-way `CGO_ENABLED=0` cross-builds (linux/amd64, linux/arm64, darwin/arm64), `testdata/clean` drift empty, wall-clock within the testing.md ~2 min budget (zero headroom today — tier behind a build tag the moment it slips).

---

## 5. Deliverables

```
gomagpie/
├── fetch/
│   ├── actions.go            # Action, ParseActions (pure), rod executor: fetchWithActions, ScreenshotActions
│   ├── actions_test.go       # parser table + validation (default suite)
│   ├── actions_browser_test.go # click/load-more e2e + lang extra-header (//go:build browser)
│   ├── http.go               # FetchRequest.Lang, Accept-Language merge site
│   └── rod.go                # Fetch/ScreenshotPage delegate to the shared prologue (behavior-neutral)
├── scrape/
│   ├── scrape.go             # Options.Actions/.Lang, ValidateOptions checks, browser-path dispatch
│   └── watch.go              # CheckForChange, WatchResult, scoped-AllowPrivate webhook client
├── store/
│   └── snapshots.go          # snapshots DDL (const edit in sqlite.go), PutSnapshot, LatestSnapshot
├── vertical/
│   ├── social.go             # reddit permalinks: .json-first comment trees (caps)
│   ├── stackoverflow.go      # SE API question+answers (filter=withbody)
│   ├── trustpilot.go         # JSON-LD reviews via goquery
│   ├── registries.go         # + dockerhub, huggingface
│   ├── og.go                 # OptIn generic OG/meta extractor
│   └── *_test.go             # fixture-driven per extractor (testdata/vertical/)
├── cli/
│   ├── scrape.go             # --action (repeatable), --actions <file>, --lang
│   ├── watch.go              # magpie watch: loop, --every (floor 30s), --once, --webhook, signals
│   ├── crawl.go              # --status <run_id>
│   └── root.go               # watch registered
├── mcp/tools.go              # ScrapeIn.actions/.lang + jsonschema strings
├── mcp/agent_test.go         # flex-shadow round-trips for the new fields
├── testdata/vertical/        # reddit-thread.json, stackoverflow.json, trustpilot.html,
│                             # dockerhub.json, huggingface.json, og.html (+ watch fixtures)
└── README.md, spec.md        # DSL verbs, watch, lang, verticals, exit-code row
```

---

## 6. Exit Criteria

- [ ] `TestParseActions` table green: 7 verbs, rest-of-line args, comments, line-numbered errors, unknown-verb and wait-cap messages
- [ ] Browser-gated e2e: click-to-load-more fixture yields expanded markdown with actions, static without; `actions + render=static`, unknown verb, and `wait 99999` all exit 2 pre-I/O
- [ ] MCP `scrape_url` accepts `actions` and rejects the `screenshot` verb with the typed CLI-only error; `TestIn_RoundTrip` extended and green; `rod_smoke_test.go` passes untouched (delegation contract holds)
- [ ] Reddit permalink fixture yields nested `comments` within caps; the rewritten `TestRedditExtract_FallbackJSON` pins `.json`-first success (1 request) + `.json`→HTML fallback order; all other reddit assertions untouched-green; stackoverflow/trustpilot/dockerhub/huggingface fixtures extract their fields; `og` extracts via `--vertical og` while `TestOG_NeverAutoFires` proves `MatchURL` misses
- [ ] Watch: baseline store → mutated fixture → diff printed + `changed=true` + webhook JSON observed at httptest sink; snapshot rows = check count (same-second double-insert survives — RFC3339Nano); `--every 5s` exits 2; Ctrl-C clean; `TestWatch_WebhookScope` (under `GOMAGPIE_STRICT_SSRF=1`) proves loopback webhook OK + loopback scrape target rejected
- [ ] `crawl --status <unknown>` exits 4 via the handler-local `fail` (store's `unknown run_id` message, README row); known run_id prints counts matching the MCP poll shape
- [ ] `--lang` header observed at httptest (bare + with profile); control-char payload → `TestValidateOptions_LangControlChars` green; MCP `lang` round-trips
- [ ] **Go/no-go gate:** all four §H.7 gate tests green → proceed. If any gate test can only pass by weakening the SSRF guard, the validation site, or the vertical OptIn contract → stop: ship the unaffected tasks (H.2 without `og`, H.4), revert the offending piece, re-plan it with an explicit design pass
- [ ] Full gates: `go test ./...`, `go test -tags browser ./...`, `go vet ./...`, `gofmt -l .` empty, `golangci-lint run ./...`, `-race` on touched pkgs, 3-way CGO-free cross-builds, `testdata/clean` drift empty, suite ≤ 2 min

---

## 7. Execution Prompt

Copy everything between the `---` lines into a fresh pi session:

---
You are building Phase H of gomagpie (`magpie`) — P1 differentiation: a rod actions DSL, six zero-LLM vertical extractor additions, a `magpie watch` change monitor, `crawl --status` CLI parity, and a `--lang` locale knob.

### What This Project Is
Go 1.26 CLI web scraper (fetch → clean → extract), module `gomagpie`, binary `magpie`. Pure-Go single static binary, zero CGO. Source of truth: `spec.md`; rationale: `plan/competitive-analysis-2026-09-18.md` §P1 (re-scoped by HEAD reality — see `plan/phase-H.md` §1 first). Local-first/BYOK — no hosted anything. Read `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md`, and `plan/phase-H.md` before writing code. House ethos: lazy senior dev — fewest files, no single-implementation interfaces, data registries over plugin systems, fail loudly at trust boundaries, `ponytail:` comments name deliberate ceilings.

### Established in Prior Phases (verified at HEAD)
- **Rod** (go-rod v0.116.2, imported only in `fetch/`): `RodFetcher` in `fetch/rod.go` lazy-launches (`ensureBrowser`), `Fetch` = navigate (`browser.Context(cctx).Page(proto.TargetCreateTarget{URL})`) → `page.WaitLoad()` → fixed 2s settle (ctx-select over `time.After`) → `page.HTML()` → `page.Close()`. `Screenshot(ctx, url, w, h)` + package-level `ScreenshotPage` exist; `screenshotBudget = 45s`. The `Fetcher` interface is NOT extended for rod-only capabilities — follow that precedent (actions are a rod method + package helper).
- **Rod interaction API (element.go@v0.116.2, line-checked; still re-verify in module cache before use):** `page.Element(sel) (*Element, error)` auto-waits under ctx (query.go:142); `el.Click(button proto.InputMouseButton, clickCount int) error` — TWO args, use `Click(proto.InputMouseButtonLeft, 1)` (element.go:104); `el.Input(text) error` = Focus → WaitEnabled → WaitWritable → InsertText, NO select-all — bare Input APPENDS to pre-filled fields, so `type` does `el.SelectAllText()` first for replace semantics (element.go:243; rod's own doc shows the `SelectAllText` → Input idiom at :260); `page.Eval(js string, jsArgs ...any) (*proto.RuntimeRemoteObject, error)` (expression or arrow-fn); `page.Screenshot(fullPage bool, opts *proto.PageCaptureScreenshot) ([]byte, error)`; `page.SetExtraHeaders(dict []string) (func(), error)` (error-returning, confirmed); `page.WaitStable(d)` / `page.WaitElementsMoreThan(sel, n)` available if needed.
- **scrape:** `Options{Render, PageFormat, Profile, Browser, Cookies, Viewport, Vertical, Scope, Schema…}`; `ValidateOptions(o)` is THE single pre-I/O validation site (shared by CLI/batch/MCP/crawl; must stay optional-field-only); `Run` dispatches `render=browser` through package-private `fetchBrowser(ctx, url)` (fresh `RodFetcher`, `defer Close`); page-format `screenshot` short-circuits to `screenshotPage(ctx, url, viewport)` → `fetch.ScreenshotPage`; page-format `raw` bypasses `clean.Render`. `OptionsError` (exit 2) is the typed pre-I/O error; `Result.Rendered` carries output; `ScreenshotPNG []byte json:"-"` carries raw PNG.
- **MCP** (`mcp/tools.go`): `ScrapeIn` struct fields with `jsonschema:` doc strings; `StringList`/`FlexBool`/`FlexMap` flex types for agent-friendly coercion; round-trip tests in `mcp/agent_test.go` `TestIn_RoundTrip` — extend for every new input field. `crawl_site` already polls by `run_id` (status path: `d.DB.GetRun` + `d.DB.CrawlStats`, `mcp/tools.go` ~line 147).
- **store** (`store/sqlite.go`, 642 lines — at the "don't grow" ceiling; new methods go in NEW files with `*store.DB` receivers): tables via one idempotent DDL const (`CREATE TABLE IF NOT EXISTS` — the snapshots table needs NO migration code); existing relevant methods: `GetRun`, `CrawlStats(runID) (pending, inflight, done, errors int, err)`, `BeginRun`/`FinishRun`, `LogFetch`. `sha256Hex` helper exists.
- **vertical** (`vertical/vertical.go`): `Extractor{Info, Match, Extract, OptIn}` registered in per-file `init()`; `MatchURL` skips OptIn extractors (permissives NEVER auto-fire); helpers `fetchBytes`/`fetchJSON`/`child`/`str`/`num`/`hostIs`/`pathSegs`/`lastSegment`/`firstJSONArray`; reddit + hackernews live in `social.go` (reddit permalinks currently HTML-first with `.json` retry); pypi/npm/crates in `registries.go`; goquery available. Extractor errors are hard errors, never LLM fallback.
- **diff:** `scrape.DiffWords(prev, cur)` returns unified-style word diff, "" when identical. `magpie diff` precedent: print diff, silence = unchanged, exit 0 both ways.
- **SSRF/transports:** `fetch.GuardedTransport()` (default-deny private) and `fetch.GuardedTransportWithOptions(fetch.SSRFOptions{AllowPrivate: bool})` exist (`fetch/http.go:41/50`); searxng search uses `AllowPrivate: true` because the operator configured the endpoint — the watch webhook is the same trust tier. CRITICAL for tests: under plain `go test` the default static fetcher auto-relaxes to `AllowPrivate: isTestBinary()` (`fetch/ssrf.go:138`) — any test asserting a loopback REJECTION must `t.Setenv("GOMAGPIE_STRICT_SSRF", "1")` (Phase-G precedent, `fetch/proxy_test.go:170`) and stay serial (no `t.Parallel` beside Setenv). `run_history` migrations use the `migrateRunHistory` ALTER-list pattern (you should NOT need it — snapshots is a new table).
- **Exit codes:** decided ONLY in `cli.exitFor` (`cli/root.go:126`); options=2, quality=8, missing-key=7, partial/error=4; a new user-visible error wires sentinel → exitFor arm → README table → asserting test.
- **Keys/env:** `config.Config.APIKey(provider)` (flag > `GOMAGPIE_<PROVIDER>_API_KEY` > `GOMAGPIE_API_KEY` > keyring). Watch and actions need no new env vars.
- **Fixtures:** `testdata/vertical/` exists; recorded shapes carry a dated comment; golden `testdata/clean` drift must stay empty; browser/rod tests are `//go:build browser` (default suite: no network, no browser, ≤ 2 min); lint has check-blank enabled — no naked `_, _ =` without a nolint comment; touched packages pass `go test -race`.

### Your Goal
Implement tasks H.1–H.7 from `plan/phase-H.md` (read it first — §4 has the exact grammar, endpoints, DDL, and validation rules; §3 has the decisions and their rationale, including two flagged deviations from the competitive analysis: `og` is OptIn and there is no `--country` flag — do not "fix" either).

### Data Model Rules (follow exactly)
- `type Action struct{ Verb, Sel, Text string; Ms int }` — one flat struct, no union types; `ParseActions(lines []string) ([]Action, error)` is pure with line-numbered errors.
- Watch: `CheckForChange(ctx, d scrape.Deps, rawURL string, o scrape.Options) (WatchResult, error)` + `WatchResult{URL string; Changed bool; OldHash, NewHash, Diff, WebhookStatus string; CheckedAt time.Time}` in `scrape/watch.go`; CLI owns only loop/ticker/signals.
- Store: `PutSnapshot` / `LatestSnapshot` on `*store.DB` in new file `store/snapshots.go`; DDL appended to the shared const; `checked_at` is RFC3339Nano (same-second inserts are routine under test ticks — second precision + the PK = guaranteed PK-violation flake); append-only, no pruning (`ponytail:` comment).
- New options ride existing structs (`scrape.Options.Actions []string`, `scrape.Options.Lang` → `fetch.FetchRequest.Lang`); extend `ValidateOptions` — never a second validator.
- The webhook's `AllowPrivate` transport is built inline inside `CheckForChange` — never a package-level shared var.

### Hard Rules
- No new dependencies. No CGO. Never import go-rod outside `fetch/`.
- `RodFetcher.Fetch` and `ScreenshotPage` must become thin delegates to the actions-capable path with byte-identical prologue/epilogue (nav → WaitLoad → settle → capture); `rod_smoke_test.go` must pass untouched.
- Actions + `render=static` → `OptionsError`; malformed DSL lines (unknown verb, empty text/expr/path, `wait` > 30000ms) → `OptionsError` with verb + line number, pre-I/O. The `screenshot` verb is CLI-only: `handleScrape` rejects it pre-flight with a typed error — the MCP surface never gains a file-write primitive (`page_format screenshot` stays base64-through-envelope).
- `--lang` values containing `unicode.IsControl` runes → `OptionsError` (header-injection boundary). No other lang validation.
- `og` extractor ships with `OptIn: true` AND its `MatchURL`-misses test in the same commit.
- Reddit `.json`-first applies to permalinks only; subreddit listings and the HTML fallback keep today's behavior; comment caps: depth 10, 200 total.
- Default suite: no live network, no browser, no sleep > 100ms; rod/interaction tests behind `//go:build browser`; every new fixture carries a dated comment; no `testdata/clean` drift.
- Every goroutine has an owner; `ctx` first param; the watch loop exits cleanly on SIGINT (exit 0).

### Files to Create / Touch
Per `plan/phase-H.md` §5 Deliverables tree; implementation order H.1 → H.5 (independent after H.1's rod refactor), then H.2, H.3, H.4, H.6, gate H.7 last. Verify rod signatures in the module cache before writing `fetch/actions.go`.

### Success Criteria
- All exit criteria in `plan/phase-H.md` §6 check green, including the four H.7 gate tests (webhook scope under STRICT_SSRF, lang control-chars, og never-auto-fires, MCP screenshot rejection).
- `go test ./... && go test -tags browser ./... && go vet ./... && gofmt -l . && golangci-lint run ./...` clean; `go test -race ./fetch/ ./scrape/ ./store/ ./vertical/ ./cli/` clean; 3-way `CGO_ENABLED=0` cross-builds pass; suite ≤ 2 min wall.

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — execution prompt cites verified HEAD facts: `RodFetcher` prologue/epilogue + `ScreenshotPage` (`fetch/rod.go`), `ValidateOptions` single site + `OptionsError` (`scrape/scrape.go`), `GuardedTransportWithOptions`/`SSRFOptions.AllowPrivate` (`fetch/http.go:41/50`, `fetch/ssrf.go:26`), MCP `crawl_site` poll path (`mcp/tools.go:109–153`), vertical registry helpers (`vertical/vertical.go`), reddit/HN extractors (`vertical/social.go`), `DiffWords` (`scrape/diff.go`), `exitFor` (`cli/root.go:126`), idempotent DDL pattern (`store/sqlite.go`), flex-shadow round-trip tests (`mcp/agent_test.go`)
- [PASS] Every sub-task has a clear, testable completion condition — each task ends in a sanity-check command; §6 criteria are observable (exit codes, header bytes at httptest, webhook JSON at a sink, row counts, MatchURL false)
- [PASS] Execution prompt is self-contained: (a) prior-phase facts inline, (b) confirmed API snippets (rod interaction methods, DDL, WatchResult shape, DSL grammar), (c) Data Model Rules section, (d) per-file guidance, (e) observable success criteria — plus explicit don't-fix deviations (og OptIn, no --country)
- [PASS] Exit criteria map 1:1 to deliverables — every file in §5 is exercised by at least one §6 bullet; the four §H.7 gate tests cover the four trust decisions named in the risk statement
- [PASS] Heavy external dependencies have stub strategies — rod executor behind `//go:build browser` with deterministic httptest pages; reddit/stackexchange/dockerhub/huggingface/trustpilot behind recorded fixtures (dated comments, no live calls); webhook verified at an httptest sink; default suite stays hermetic and ≤ 2 min
- [PASS] New libraries — none; go-rod v0.116.2 interaction surface line-checked in the module cache after an external validation pass corrected two drift errors in the first draft (Click takes a clickCount arg; Input is InsertText with no select-all — SelectAllText-first for replace semantics); execution prompt still carries a re-verify-before-use step
