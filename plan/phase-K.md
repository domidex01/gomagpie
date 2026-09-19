# Phase K — Desktop Bootstrap (Wails v3 skeleton: two-repo split, Service layer, 3 screens)

**Duration:** 2 days (~16h)
**Depends on:** master @ 19d5cd3 (Phase J merged). Ideally PR #14 (corpus mode) merges before the v0.1.0 tag so the GUI ships with it — not blocking the skeleton.
**Blocks:** Phase L (dashboard depth: watch/diff/corpus UI, settings depth), Phase M (licensing + MoR + per-OS CI matrix), `magpie serve` daemon (same Service, second transport).
**Risk Level:** HIGH — new framework (Wails v3 **Beta**, not GA), first CGO-enabled build in the repo's history (sanctioned exception), a cross-repo bootstrap, and a module-path rename touching ~103 files. Every risk has a named fallback in §2.
**Stack:** go (+ React 19 / TypeScript / Vite / TanStack Router+Query for `frontend/`)
**Note (AGENTS.md):** `run-phase` expects `stack: python|nextjs|react|typescript` and will hard-block on `stack: go`. Execute manually via the execution prompt.

**Repos:** Tasks K.1–K.2 land in **this** repo + a new **private** repo (`magpie-desktop`). Phases L+ are planned *in the private repo* — this is the last phase planned here.

---

## 1. Objective + What Success Looks Like

Two repos, one core. The public repo becomes an importable Go module (`github.com/motherlodelab/magpie`, MIT, tagged `v0.1.0`); a new private repo (`magpie-desktop`) consumes it behind a **Service layer** and wraps it in a Wails v3 desktop shell with three working screens (Scrape, Crawl, History). The GUI never imports `cli/` — it calls the same package seams the CLI calls, which is exactly the thinness the scrape.Deps-era prep bought.

**What Success Looks Like:**

1. `go build ./...` in the public repo passes after the module rename with **zero test changes and zero golden changes** (`git diff testdata/` empty); `magpie` binary behaves identically (`magpie scrape --help` unchanged).
2. `go doc github.com/motherlodelab/magpie` resolves from a *second* module: in `magpie-desktop`, `go mod tidy && go build ./...` succeeds with `require github.com/motherlodelab/magpie v0.1.0` (no replace).
3. `wails3 dev` in `magpie-desktop` opens a desktop window; the **Scrape screen** fetches `https://example.com` end-to-end through the bridge and renders cleaned markdown.
4. The **Crawl screen** starts a 5-page crawl against a local test site and shows live progress (progress bar moving via Wails events, not polling-only), ending with `pages_ok=5`.
5. The **History screen** lists past runs from the store (command, status, pages_ok/err, started), including the run just executed in (4).
6. `wails3 build` produces a self-contained executable in `bin/`.
7. Public-repo gates stay green: `go test ./... && go vet ./... && gofmt -l . && golangci-lint run ./...`.

## 2. What Failure Looks Like (and what to do)

- **`wails3 dev` can't open a window (WSL2/WSLg, missing webkit deps)** → `sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev` (wails doctor names them). If WSLg still fails: develop the frontend hot-reload-only against **mocked bindings** (thin `serviceMock.ts` behind one `import.meta.env.PROD` check), and validate the real bridge by building the Windows exe and running it on the host. Do not block on in-WSL windowing.
- **`wails3 generate bindings` rejects a Service method type** (channels/funcs are unsupported; `map[string]any` works but is untyped) → flatten to DTO structs with JSON tags; worst case serialize a field to a JSON `string`. Never smuggle a `chan` across the bridge — progress rides `app.Event.Emit`.
- **Module rename breaks a fixture or golden** → the only known import-path-in-data is the `buildmod` fixture's replace directive (rename lesson, commit 8baef0e) — include it in the scripted sed. If anything else breaks, fix the script, not the fixture.
- **CGO/CCO problems on cross-compile** → Phase K targets **Linux-local only**; per-OS CI matrix is Phase M. `wails3 build` locally is the only build claim.
- **`go mod tidy` in magpie-desktop can't find the public module** → the tag is wrong or private-repo GOPROXY can't see it; verify `git tag -l` on origin and `GOPRIVATE`/`GONOSUMCHECK` not needed (public repo). With `replace ../magpie` in place during dev this cannot block.
- **Wails v3 Beta API drifts from the confirmed snippets below** → pin the wails version in `go.mod` at whatever the snippets were verified against; do not chase the latest. The snippets come from the Beta docs (fetched 2026-09-19).

## 3. Architecture / Key Design Decisions

```
PUBLIC repo (magpie, MIT, tag v0.1.0)          PRIVATE repo (magpie-desktop, closed)
┌────────────────────────────────┐             ┌─────────────────────────────────────┐
│ scrape.Run(ctx,Deps,url,Opts)  │◄────────────│ internal/service                    │
│ crawl.Run(ctx,Options)         │   import    │  ├ ScrapeService.Scrape(...)        │
│ store.Open/GetRun/CrawlStats/  │  (tagged)   │  ├ CrawlService.Start/Status(...)   │
│   ListRuns(NEW)                │             │  └ HistoryService.Runs(...)         │
│ config.Load/DefaultConfigPath  │             │ main.go ─ application.New(services) │
│ clean/fetch/vertical/mcp       │             │ frontend/ (React+TanStack SPA)      │
└────────────────────────────────┘             │  └ bindings/ (generated, -ts)       │
   unchanged behavior; +ListRuns               └─────────────────────────────────────┘
                                                 wails3 dev / wails3 build → bin/
```

### Decisions

1. **Two repos, no fork.** Public repo never learns the GUI exists. Desktop `go.mod` requires the tagged public module; dev uses `replace ../magpie`. Core fixes flow back as public PRs.
2. **The Service layer is the only Go the frontend sees, and it imports `scrape/`, `crawl/`, `store/`, `config/` — never `cli/`.** The cobra tree is terminal presentation; the seams beneath it are the product. If the Service layer needs a core change, that change is a public PR.
3. **One service instance per screen concern, registered via `application.NewService`** (confirmed API). Services are singletons with shared state across windows — mutex-guard anything mutable; long crawls run in a goroutine with `crawl.Options.Progress` hooked to `app.Event.Emit("crawl:progress", dto)`.
4. **No HTTP API in K.** The Service layer is transport-agnostic plain Go; `magpie serve` (farm/CI daemon) becomes a thin second transport in a later phase if a consumer appears. YAGNI now.
5. **Frontend: Vite + React 19 + TypeScript + TanStack Router + TanStack Query.** Router owns the 3-screen navigation; Query owns Service-call caching/invalidation (invalidate `['run', id]` on progress events). Generated bindings come from `wails3 generate bindings -ts`. **No TanStack Start** (server-shaped; wrong for an embedded webview) and no state library beyond Query in K.
6. **CGO is the sanctioned exception** (AGENTS.md), confined to the private repo. The public module stays CGO-free and `GOOS`-portable; verify with the existing 3-way cross-build gate after the rename.
7. **Data directory:** the Service opens the same store/config the CLI uses (`config.DefaultConfigPath()` / `config.DefaultDBPath()`) so CLI and GUI share history, selector caches, and keys. One user, one data dir — not two parallel universes.
8. **Licensing/MoR/per-OS CI are Phase M.** K ships a Linux-local build and a private repo. Nothing in K should depend on license state.

### Data Model Strategy

| Layer | Type | Why |
|-------|------|-----|
| Bridge DTOs (Service returns) | Plain Go structs, exported fields, `json:"..."` tags, **flat** | Wails bindings map structs→TS interfaces; only exported fields cross; chan/func can't cross at all |
| Crawl progress payload | `CrawlProgress{RunID string; Done, PagesOK, PagesErr, Records int; Finished bool}` | Emitted via `app.Event.Emit("crawl:progress", p)`; flat + tiny |
| Frontend types | **Generated only** (`wails3 generate bindings -ts`) — never hand-written | Generator owns the contract; hand-copies drift |
| Client state | TanStack Query cache only | No Zustand/Redux in K; screens are query+form, not app-state machines |

**Bridge type gotchas (confirmed):** `int64`→`number` (2^53 ceiling — run IDs are strings, not ints, by design), `[]byte`→base64 `string`, `time.Time`→RFC3339 string, errors reject as `RuntimeError` with `.cause` = JSON of custom error types (set `MarshalError` if a typed error needs structure).

---

## 4. Tasks

### Task K.1 — Public repo: LICENSE, module rename, `ListRuns`, tag v0.1.0 (3h)

**Depends on:** nothing.

- Add `LICENSE` (MIT, copyright 2026 DomiDex) and replace the TBD badge in `README.md`.
- Rename the module `magpie` → `github.com/motherlodelab/magpie`: scripted sed across `go.mod` + all `magpie/...` imports (~103 files) **plus the `buildmod` fixture's replace directive** (the one place an import path lives in testdata — rename lesson 8baef0e). Gates must stay green with **zero fixture churn beyond buildmod**: `go test ./... && go vet ./... && gofmt -l . && golangci-lint run ./...`, `git diff testdata/` shows only the buildmod line.
- Add `store.ListRuns(limit int) ([]RunInfo, error)` (newest first) + one test — the History screen needs it and only `GetRun`/`CrawlStats` exist today. Additive, mirrors `GetRun`'s row mapping.
- Commit, merge via PR, then `git tag v0.1.0 && git push origin v0.1.0`.

**Sanity check:** `go list -m github.com/motherlodelab/magpie@v0.1.0` resolves from outside the repo.

### Task K.2 — Private repo bootstrap (1.5h)

**Depends on:** K.1.

- Create private GitHub repo `motherlodelab/magpie-desktop` (closed). Locally: `magpie-desktop/` with `go.mod` (`module github.com/motherlodelab/magpie-desktop`, `require github.com/motherlodelab/magpie v0.1.0`, plus `replace github.com/motherlodelab/magpie => ../magpie` during K), `main.go`, `internal/service/`, `frontend/`, `Taskfile.yml`, `.gitignore` (`bin/`, `frontend/bindings/`, `frontend/node_modules/`).
- Confirm the toolchain: `go install github.com/wailsapp/wails/v3/cmd/wails3@latest`, `wails3 doctor` (installs/verifies webkit deps on Linux). Pin whatever version resolves now in `go.mod` (tools.go pattern for the CLI).
- Minimal `main.go` that compiles and opens an empty window (snippets in §7).

**Sanity check:** `wails3 dev` opens a blank window on the desktop; `go build ./...` passes.

### Task K.3 — Service layer (3h)

**Depends on:** K.2.

`internal/service/` — three services, plain Go, bridge-safe DTOs only:

- `ScrapeService`: `Scrape(url string, opts ScrapeOptions) (ScrapeResultDTO, error)` — maps a flat `ScrapeOptions` DTO → `scrape.Options`, wires `scrape.Deps` from `config.Load(config.DefaultConfigPath())` + `store.Open(config.DefaultDBPath())` (open ONCE in `ServiceStartup`, share), calls `scrape.Run`. Returns markdown/content/extracted/usage. Respect the exit-code semantics: errors come back as Go errors (bridge → `RuntimeError.cause`).
- `CrawlService`: `StartCrawl(opts CrawlOptions) (string, error)` (returns runID) + `Status(runID string) (CrawlStatusDTO, error)` (`db.CrawlStats` + `db.GetRun`). StartCrawl validates via `crawl`-level rules, spawns `crawl.Run` in a goroutine with `Options.Progress` → `app.Event.Emit("crawl:progress", CrawlProgress{...})`; never block the bridge call.
- `HistoryService`: `Runs(limit int) ([]RunSummaryDTO, error)` → `db.ListRuns`; `LLMCalls(runID)` for a cost drawer if cheap.

**Rules:** flat DTOs with json tags; no `cli/` imports; no channels; mutex around the in-flight-run map (`runID → cancel func` for a future Stop button — add `Stop(runID)` now, it's 10 lines with `context.WithCancel`).

**Sanity check:** `wails3 generate bindings -ts` emits `frontend/bindings/github.com/motherlodelab/magpie-desktop/internal/service/*.ts` without warnings; a temporary button calling `ScrapeService.Scrape("https://example.com", …)` returns markdown.

### Task K.4 — App shell + frontend scaffold (2h)

**Depends on:** K.3.

- `frontend/`: Vite + React 19 + TS. `npm i @tanstack/react-router @tanstack/react-query @wailsio/runtime`. Alias `$bindings` → `./bindings` in `vite.config.ts`. Left nav (Scrape / Crawl / History) via Router; `QueryClientProvider` at root.
- One `useCrawlProgress(runID)` hook: `Events.On("crawl:progress", …)` → Query cache update.
- Delete the template's demo service/page.

**Sanity check:** `wails3 dev` shows the 3-route shell; hot reload works on a text change.

### Task K.5 — Scrape screen (2.5h)

**Depends on:** K.4.

Form (URL, render auto/browser/static, page-format select, optional vertical select — populate from a small `ListVerticals()` Service method over `vertical.Lookups()`), submit → `useMutation(ScrapeService.Scrape)`. Result tabs: Markdown (rendered), Content (page-format output as text), Extracted (JSON viewer), Usage. Errors surface in a toast (show `error.cause` when present).

**Sanity check:** scrape `https://example.com` renders markdown in-window; a bad URL shows the Go error message.

### Task K.6 — Crawl screen + live progress (2.5h)

**Depends on:** K.4.

Form (seed URL, max pages, depth, same-host, schema file path optional, **corpus toggle** if PR #14 is in the tag). Start → `StartCrawl` → navigate to a run panel keyed by runID: progress bar + counters from the `crawl:progress` event stream, `Status()` polled once on mount for late-joining. Terminal state shows pages ok/err + records; link "View in History".

**Sanity check:** `wails3 dev`, crawl a 5-page local file:// or httptest-style local site → progress animates, terminal counts correct. (For a local target: `python3 -m http.server` a temp dir of pages.)

### Task K.7 — History screen (1h)

**Depends on:** K.3.

`useQuery(HistoryService.Runs(100))` → table (started, command, status, pages_ok/err, cost) newest-first; row click → existing `GetRun`-based detail (defer LLM-call drawer to L unless trivial).

**Sanity check:** runs from K.5/K.6 appear; refresh after a new crawl shows it.

### Task K.8 — Build + wrap (1h)

**Depends on:** K.5–K.7.

`wails3 build` → `bin/magpie-desktop`; run the exe (not dev mode) and smoke the three screens. Private-repo `README.md`: what it is (closed), build prereqs, `replace` workflow. Tag `v0.0.1`. Public repo: nothing further.

**Sanity check:** double-clicking `bin/magpie-desktop` works without `wails3 dev`.

---

## 5. Deliverables

```
magpie/  (PUBLIC — this repo)
├── LICENSE                     # MIT (replaces TBD badge)
├── go.mod                      # module github.com/motherlodelab/magpie
├── (~103 .go files)            # scripted import-path rename; zero behavior change
├── store/sqlite.go             # + ListRuns(limit) (+ test) — additive
└── plan/phase-K.md             # this file (last phase planned publicly)

magpie-desktop/  (PRIVATE — new repo)
├── go.mod                      # requires github.com/motherlodelab/magpie v0.1.0 (+ replace ../magpie in dev)
├── main.go                     # application.New + services + window + app.Run
├── Taskfile.yml                # wails3 build/dev tasks (from wails3 init)
├── internal/service/
│   ├── service.go              # shared Deps wiring: config.Load + store.Open (once, ServiceStartup)
│   ├── scrape.go               # ScrapeService + DTOs
│   ├── crawl.go                # CrawlService: Start/Status/Stop + event emission
│   ├── history.go              # HistoryService: Runs
│   └── dto.go                  # flat bridge DTOs (json tags)
└── frontend/
    ├── package.json            # react, @tanstack/react-router, @tanstack/react-query, @wailsio/runtime
    ├── vite.config.ts          # $bindings alias
    ├── bindings/               # GENERATED (wails3 generate bindings -ts) — gitignored
    └── src/
        ├── main.tsx            # QueryClientProvider + Router
        ├── routes/             # scrape.tsx, crawl.tsx, history.tsx
        ├── hooks/useCrawlProgress.ts
        └── components/         # ResultTabs, JsonView, ProgressPanel
```

## 6. Exit Criteria

- [ ] Public repo: module renamed to `github.com/motherlodelab/magpie`; all gates green; `git diff testdata/` shows only the buildmod replace line; `LICENSE` = MIT
- [ ] `store.ListRuns` exists with a test; `GetRun`/`CrawlStats` behavior unchanged
- [ ] `v0.1.0` tagged and pushed; `go list -m github.com/motherlodelab/magpie@v0.1.0` resolves
- [ ] `magpie-desktop` builds with `require v0.1.0` (replace removed for the check, restored for dev)
- [ ] `wails3 dev` shell renders 3 routes
- [ ] Scrape screen end-to-end on `https://example.com` (markdown + error path)
- [ ] Crawl screen: live progress via events; terminal counts match `Status()`
- [ ] History screen lists the runs from the two screens above
- [ ] `wails3 build` → executable in `bin/` that runs standalone; private repo tagged `v0.0.1`
- [ ] Public module still CGO-free: existing cross-build gate (or `GOOS=windows go build ./...` locally) passes in the public repo

## 7. Execution Prompt

Copy everything between the `---` lines into a new pi session to implement this phase:

---
You are building **Phase K of magpie — Desktop Bootstrap**: split the opensource Go scraper into an importable module and build a closed Wails v3 desktop skeleton (`magpie-desktop`) over it with three screens.

### What This Project Is
magpie is a Go CLI web scraper (module currently `magpie`, Go 1.26+, fetch → clean → extract; zero CGO) at `/home/domidex/projects/magpie`. Read `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md`, and `plan/phase-K.md` first. The CLI is and stays opensource; the desktop app is a separate **private** repo (`github.com/motherlodelab/magpie-desktop`) that consumes the public module. **Wails is the sanctioned CGO exception — CGO must never leak into the public module.**

### Established (master @ 19d5cd3, Phase J merged)
- Seams to consume (never import `cli/`): `scrape.Run(ctx, scrape.Deps, url, scrape.Options) (Result, error)`; `crawl.Run(ctx, crawl.Options) (Result, error)` with `Options.Progress func(done int)` and `Options.OnRecord`; `store.Open(path) (*DB, error)` with `GetRun/CrawlStats/LLMCalls/RunCost` (+ `ListRuns` you add in K.1); `config.Load(path)`, `config.DefaultConfigPath()`, `config.DefaultDBPath()`, `config.DefaultModel(provider)`.
- One data dir for CLI+GUI: open the store once (`config.DefaultDBPath()`), share across services in `ServiceStartup`.
- The public repo's gates: `go build ./... && go test ./... && go vet ./... && gofmt -l . && golangci-lint run ./...`; goldens in `testdata/` must not drift (only known import-path-in-data: the `buildmod` fixture replace directive).

### Your Goal
K.1 public bootstrap (MIT LICENSE; rename module `magpie` → `github.com/motherlodelab/magpie` via scripted sed incl. the buildmod fixture; add `store.ListRuns(limit int) ([]RunInfo, error)` + test; tag `v0.1.0`) → K.2 private repo + Wails scaffold → K.3 Service layer → K.4–K.7 three screens (Scrape, Crawl with live progress, History) → K.8 `wails3 build` + tag `v0.0.1`.

### Confirmed Library APIs (Wails v3 **Beta**, docs fetched 2026-09-19 — pin the version you install; do not chase latest)

```go
// main.go — app with services (confirmed):
app := application.New(application.Options{
    Name: "magpie",
    Services: []application.Service{
        application.NewService(&service.ScrapeService{}),
        application.NewService(&service.CrawlService{}),
        application.NewService(&service.HistoryService{}),
    },
})
// lifecycle: func (s *Svc) ServiceStartup(ctx context.Context, opts application.ServiceOptions) error
// window:    app.Window.New() (no error return; options struct has Name/Title/Width/Height/URL)
if err := app.Run(); err != nil { log.Fatal(err) }
```

```bash
wails3 init -n myapp            # scaffold (Vanilla+Vite template; swap frontend for React+Vite)
wails3 generate bindings -ts    # → frontend/bindings/<go-import-path>/*.ts ; -d to redirect
wails3 dev                      # hot reload both sides; regenerates bindings on Go change
wails3 build                    # → bin/<app>
```

```ts
// frontend calls (generated; import path mirrors the Go package path):
import { ScrapeService } from '../bindings/github.com/motherlodelab/magpie-desktop/internal/service';
const res = await ScrapeService.Scrape('https://example.com', opts); // Promise; rejects on Go error
import { Events } from '@wailsio/runtime';
Events.On('crawl:progress', (e) => updateCache(e.data));            // events for streaming progress
```

Bridge rules (confirmed): only exported methods bind; `(value, error)` → Promise resolve/reject (`RuntimeError`, `.cause` = JSON of custom error types); `chan`/`func` params CANNOT cross — long work = goroutine + `app.Event.Emit`; `int64`→JS `number` (2^53 ceiling); `[]byte`→base64 string; `time.Time`→RFC3339 string; unexported fields don't cross. Services are singletons — mutex mutable state.

### Data Model Rules
- Service DTOs: plain Go structs, exported fields, `json` tags, **flat** (no nested chan/func/map-of-func). Forms on the frontend send flat DTOs too.
- Frontend types come ONLY from generated bindings — never hand-write a Go mirror.
- Client state = TanStack Query cache only. No state library.
- Progress: `crawl.Options.Progress` goroutine → `app.Event.Emit("crawl:progress", CrawlProgress{RunID, Done, PagesOK, PagesErr, Records, Finished})`.

### Hard rules
- Public module stays CGO-free; CGO only inside magpie-desktop (wails). Verify: `GOOS=windows go build ./...` in the public repo still passes.
- No new public-repo dependencies. Frontend deps limited to: react, react-dom, @tanstack/react-router, @tanstack/react-query, @wailsio/runtime, vite, typescript.
- Errors: Service methods return `error` (message crosses the bridge — keep the `pkg: message` convention). Never swallow.
- No HTTP API, no licensing, no CI matrix — explicitly Phase L/M.

### Per-file guidance
- **public/store/sqlite.go**: `ListRuns(limit int) ([]RunInfo, error)` — newest first, same row mapping as `GetRun`; one table-driven test.
- **public/LICENSE + README badge**: MIT, 2026 DomiDex.
- **desktop/main.go**: `application.New` with the 3 services + one window (Title "magpie", 1280×800); `ServiceStartup` opens store/config once into a shared `*service.App` struct injected into all services.
- **desktop/internal/service/*.go**: one file per service + shared `service.go` (Deps wiring) + `dto.go`. `CrawlService.StartCrawl` = validate → `context.WithCancel` → goroutine `crawl.Run` → map `Progress` to events; store cancel in a mutex-guarded map; `Stop(runID)` cancels.
- **frontend/src/routes/**: scrape (form → mutation → ResultTabs: Markdown/Content/Extracted/Usage), crawl (form → StartCrawl → ProgressPanel fed by `useCrawlProgress`), history (`Runs(100)` table). Keep components dumb; Query owns fetching.
- **frontend/vite.config.ts**: alias `$bindings` → `./bindings`; dev proxy not needed (wails serves the runtime).

### Success Criteria
- All Phase-K exit criteria in `plan/phase-K.md` §6 pass (three screens live against the real bridge, `wails3 build` artifact runs, public gates green, v0.1.0 + v0.0.1 tagged)
- Public repo diff is exactly: LICENSE + module rename + `ListRuns` (+test) + this plan file

---

## Readiness Check

- [PASS] All inputs from prior phases are listed and available — master @ 19d5cd3 verified this session; every seam cited by exact name (`scrape.Run`, `crawl.Run` + `Progress`/`OnRecord`, `store.Open/GetRun/CrawlStats/LLMCalls`, `config.Load/DefaultConfigPath/DefaultDBPath` all grep-confirmed on master)
- [PASS] Every sub-task has a clear, testable completion condition — each of K.1–K.8 ends in a named sanity check
- [PASS] Execution prompt is self-contained: (a) prior-phase facts inline (seams, one-data-dir rule, gates), (b) confirmed Wails v3 **Beta** snippets from official docs fetched 2026-09-19 (application.New/Services, ServiceStartup, generate bindings -ts, dev/build, events, bridge type gotchas), (c) Data Model Rules (flat DTOs, generated TS only, Query-only state), (d) per-file guidance, (e) observable success criteria
- [PASS] Exit criteria map 1:1 to deliverables — LICENSE/rename/ListRuns/tags/shell/3 screens/build/CGO-free each have a criterion
- [PASS] Heavy external dependency strategy — Wails v3 Beta is the one external risk: named fallbacks in §2 (webkit deps via `wails3 doctor`; WSLg failure → mocked-bindings frontend dev + Windows exe validation; API drift → pin the installed version), CGO confined to the private repo with a public-repo cross-build check as the tripwire
- [PASS] New libraries have confirmed usage snippets — Wails v3 (this session, official docs); React 19/TanStack Router+Query assumed-known to the operator (established stack), deps list bounded in the hard rules
