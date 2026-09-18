# Phase 4 — Interactive TUI (`magpie tui`, Bubble Tea)

**Depends on:** Phase 3 (`scrape.Run`, `crawl.Run` + `Progress`/`OnRecord` hooks, `store` accessors)
**Blocks:** Nothing (additive; non-interactive commands unchanged)
**Risk Level:** LOW-MEDIUM — Elm architecture is well-trodden, but three new deps need
explicit approval per `AGENTS.md` ("no new dep without asking"), and mouse hit-testing is
hand-rolled coordinate math that must be golden-tested.
**Stack:** `go`

> Status: SPEC ONLY. No TUI code exists yet (confirmed: no `tui/` dir, no Bubble Tea in
> `go.mod`, no TUI section in `spec.md`). Do not implement until the dep approval below
> is granted.

---

## 1. Objective + What Success Looks Like

One interactive command, `magpie tui`, for the solo dev watching a crawl and inspecting
records without leaving the terminal: a run form (URL + schema + options) → live progress
→ record list + detail viewer. **Keyboard-first**: every action in §3 has a keybinding and
the app is fully usable with mouse disabled. **Mouse as progressive enhancement**: click
selects list rows / focuses panes, wheel scrolls whichever pane is under the cursor.

1. [`magpie tui` opens on a TTY; piped stdout exits 2 with "tui requires a terminal" — never renders garbage into a pipe]
2. [Full keyboard pass: form → run → scroll list → open record → quit, with mouse never enabled, asserted by driving `Update` with synthetic `KeyMsg`s]
3. [Clicking a record row selects it; wheel over list/detail scrolls that pane — asserted with synthetic `MouseMsg`s, no real terminal]
4. [`m` toggles mouse capture at runtime; `--mouse=false` starts disabled]
5. [`go test ./tui/ -v` exits 0 (hermetic: fake `Extractor`, temp DB, synthetic msgs); full suite + vet + fmt + lint clean; three `CGO_ENABLED=0` cross-builds pass]

---

## 2. Scope (minimal — three panes, one window)

```
┌─ magpie tui ─────────────────────────────┐
│ Form: URL │ schema │ pages │ depth │ [run] │  ← focus 1 (text inputs + start)
│ Live: pages ok/err, last URL, cost, bar    │  ← focus 2 (read-only viewport)
│ Records list (n) │ Detail viewport         │  ← focus 3/4 (list + detail)
└─ status: keys hint ───────────────────────┘
```

- **Form pane**: URL input, schema path, max-pages, max-depth, **output folder** (text field + `o` opens a folder browser: arrow-key `cd` through directories, `n` creates a new folder like `mkdir`, `s` selects, `esc` cancels — §2.5). Reuses `extract.LoadSchema` + `scrape.Run` / `crawl.Run` — no new pipeline code, no new store tables.
- **Live pane**: `crawl.Options.Progress` / `OnRecord` hooks forward into the program via `p.Send(...)`; each record also appends to list state. Run executes as a `tea.Cmd` (blocking work off the UI goroutine); cancel via the run ctx on `q`/Ctrl+C (same `signal.NotifyContext` pattern as `crawl`).
- **Results**: record list (one line per record) + detail `viewport` with two modes:
  `record` (pretty JSON of the selected record) and `page` (the source page's cleaned
  markdown, rendered per §2.4). Toggled with `v`.
- **Non-goals**: no mouse drag / multi-select, no hover tooltips (that needs `WithMouseAllMotion`, deliberately rejected in §4), no charts/sparklines, no editing of schemas or cache inside the TUI (use the CLI), no background jobs surviving TUI exit, no syntax highlighting inside code blocks in v1 (styled block only — chroma is deferred), no smart markdown editing aids in v1 (no auto list-continuation, no shortcuts — lists are typed as plain text; `$EDITOR` spawn deferred, inline editing covers it).

---

## 3. Keyboard map (normative — every row must work with mouse off)

| Key | Action (all panes unless noted) |
|---|---|
| `tab` / `shift+tab` | cycle pane focus (form → live → list → detail) |
| `↑`/`k`, `↓`/`j` | move selection (list) / line scroll (detail, form history) |
| `pgup`/`b`, `pgdn`/`f`(`space`) | page scroll (detail viewport via its `DefaultKeyMap`) |
| `g` / `G` (`home`/`end`) | top / bottom |
| `enter` | activate: start run (form) / open record in detail (list) |
| `v` | toggle detail `record` / `page` (rendered markdown) mode |
| `e` | edit page markdown inline (`page` mode; `ctrl+s` save, `esc` discard) |
| `o` | open output-folder browser (form): `↑↓/jk` move, `→/enter` descend, `←/backspace` parent, `n` mkdir, `s` select, `esc` cancel |
| `/` | filter list by substring; `esc` clears |
| `m` | toggle mouse capture at runtime |
| `?` | toggle key-hint overlay |
| `q` | quit (aborts in-flight run via ctx cancel); `ctrl+c` always quits |

Detail scrolling additionally inherits `viewport.DefaultKeyMap` verbatim (do not rebind it).
`esc` closes overlay first, then clears filter — never quits.

---

## 4. Mouse design (normative)

### 2.4 Markdown viewer (normative)

Any markdown shown in the TUI must render styled, never raw. If you open an article
starting with `# H1`, `## H2`, and a fenced code block, you see a big styled title,
a smaller section heading, and a distinct code block — exactly what `glamour` renders
on your terminal, inside the detail `viewport`.

- Renderer: `glamour.NewTermRenderer(glamour.WithAutoStyle(),
  glamour.WithWordWrap(viewportWidth))` — auto dark/light via terminal background
  detection, wrap matched to the pane width. (Re-confirm constructor/option names on
  the pinned v1 — the v0 API is `NewTermRenderer`/`WithAutoStyle`/`WithWordWrap`/`Render`;
  if v1 renamed anything, follow v1 and note it here.)
- Must render correctly: ATX + setext `h1`–`h6`, fenced and indented code blocks
  (language label shown, plain styled block, no chroma in v1), nested lists, GFM
  tables (from the html-to-markdown table plugin upstream), blockquotes, links
  (link text stays readable; URL shown, not hidden), inline code + emphasis,
  `hr`, images degrade to `![alt](src)` text and never break layout.
- Render once per (content, width) pair and cache the string in the model — never
  re-render per keystroke or per frame. Re-render on `WindowSizeMsg` width change.
- Render failure → show the raw source plus a `⚠ markdown render failed` status note.
  Never blank, never crash: display degrades, the app doesn't.
- Data scope (deliberate, bounded memory): scrape runs retain the full cleaned
  markdown in TUI state, so `page` mode always works there. Crawl runs retain records
  only — `page` mode on a crawl row shows a hint (`re-run this URL as a single scrape
  for the full rendered page`) instead of plumbing page bodies through `OnRecord`.
  `ponytail:` ceiling = no full-page archive in crawl mode; upgrade = opt-in
  `--retain-pages` flag writing bodies to the run DB if anyone actually asks.
- Mouse interplay: rendered output is ordinary `viewport` content, so §4.2 wheel
  forwarding and §4.3 click-focus apply unchanged. Glamour's ANSI styling survives
  because the viewport measures width ANSI-aware.

### 2.5 Output folder browser: `cd` + `mkdir` (normative)

The form's output-folder field is backed by a minimal directory browser — the user can
go where they want and create directories without leaving the TUI.

- `o` (form pane, focus on folder field) opens the browser rooted at the field's current
  value (default: process working directory). `~` and relative paths expand on open.
- Browser lists directories only (files hidden — output target is always a folder),
  sorted dirs-first, case-insensitive. Symlinks shown but never auto-followed for the
  select action (select resolves via `filepath.EvalSymlinks` once, then treats it as
  the plain path — no loop chasing).
- Keys (browser modal only): `↑↓/jk` move, `→/enter` descend into highlighted dir,
  `←/backspace/h` parent, `n` new folder (inline name prompt; name must not contain
  separators — reject `a/b` loudly in the status bar), `s` select current directory
  into the form field, `esc` cancel (field keeps its old value).
- `mkdir` uses `os.MkdirAll` (one call — parents included, matching `mkdir -p`) and
  descends into the new folder on success. Permission / exists-as-file errors show in
  the status bar and stay in the browser — never crash, never silently stay put.
- Hand-rolled over `os.ReadDir` (~80 lines). `bubbles/filepicker` rejected: it picks
  files, not folders, and has no mkdir — the adapter would be bigger than the thing.
- Click support: left-click a row descends, matching `enter` (same Y-arithmetic as §4.3).

### 2.6 Markdown editing (normative)

`page` mode is editable — including lists, which are just typed text (this supersedes
the old "per-space" open question: no special list mode, you type `- ` and it renders).

- `e` (detail pane, `page` mode) swaps the viewport for a `bubbles/textarea` holding the
  current markdown, width-matched to the pane. `textarea`/`textinput` live in the
already-pinned `bubbles` module — no new dependency.
- `ctrl+s` saves: writes `<slug>.md` into the form's output folder (slug from record
  title or `domain-YYYYMMDD-HHMMSS` fallback; overwrite asks `y/n` inline — never
  silent-clobber) and returns to rendered `page` mode showing the saved content.
- `esc` discards edits and returns to `page` mode showing the pre-edit content.
- While editing, all keys except `ctrl+s`/`esc` go to the textarea (intercept before
  `textarea.Update`). Pane-focus `tab` is suspended in edit mode — `esc` first.
- Edited state also replaces the in-memory page content, so toggling `v` or resizing
  re-renders the edited text, not the original.
- Crawl rows (no retained markdown, §2.4): `e` shows the same hint as `page` mode —
  nothing to edit until a scrape is run. No special-case, no empty editor.

### 4.1 Enablement — cell motion only, toggleable, TTY-gated

- Program option is `tea.WithMouseCellMotion()` — clicks + wheel + drag-press motion.
  `tea.WithMouseAllMotion()` is **rejected**: it floods `Update` with every hover move
  (render-per-move on large viewports; `ponytail:` ceiling = no hover effects ever;
  upgrade = re-evaluate only if a third hit-test consumer needs hover).
- Runtime toggle without restart: `m` sends `tea.EnableMouseCellMotion` /
  `tea.DisableMouse` (both are `Msg`s in current Bubble Tea — confirmed on pkg.go.dev,
  alongside `ProgramOption` forms `WithMouseCellMotion`/`WithMouseAllMotion`).
- Flag `--mouse=true|false` (config key `tui_mouse`, env `GOMAGPIE_TUI_MOUSE`); default
  **true** on a TTY. Starting disabled must leave zero behavioral delta except no
  `MouseMsg`s arrive (keyboard map §3 covers everything).
- Trust boundary: if `stdout` is not a character device, `magpie tui` exits **2**
  (usage error, same class as bad flags) printing `tui requires a terminal; use
  scrape/crawl for pipes`. Check with a TTY test before constructing the program.
- Documented caveat in `--help`: while mouse capture is on, terminal text selection
  needs the terminal's bypass modifier (typically Shift+select). This is why `m` exists.

### 4.2 Wheel scrolling — delegate to `bubbles/viewport`

- Both scrollable panes (live log, detail) are `viewport.Model`s with defaults kept:
  `MouseWheelEnabled: true`, `MouseWheelDelta: 3` (upstream defaults in bubbles v1.0.0,
  `New()` sets both — do not re-set them, just don't turn them off).
- The app's `Update` forwards **every** `tea.MouseMsg` to the viewport under the cursor;
  `viewport.Update` already handles `Action == Press` + `WheelUp/WheelDown(/Left/Right)`
  → `ScrollUp/Down(MouseWheelDelta)` (verified against bubbles v1.0.0 source). No custom
  wheel code — forwarding + focus-by-position is the whole implementation.
- Which pane is "under the cursor": compare `msg.Y` against pane boundary offsets the
  app already computes in `View` (header height + pane heights). Wheel over the record
  list scrolls the list selection instead (list is app state, not a viewport: wheel =
  ±3 rows, same delta as viewports for consistent feel).

### 4.3 Click selection — Y-coordinate arithmetic, no hit-test dep

- `tea.MouseMsg` (`MouseEvent`): absolute cell coords `X, Y`; `Action` Press/Release/Motion;
  `Button` Left/Right/Middle/Wheel*; `msg.IsWheel()` discriminates wheel. Click = `Action
  == Press && Button == MouseButtonLeft`.
- Record-list hit test is subtraction the app can already do: `index = msg.Y -
  listTopOffset - viewport.YOffset`, bounds-checked against visible rows. `listTopOffset`
  is the same constant `View` uses to place the list (single source of truth — define
  layout offsets once as pure funcs of `WindowSizeMsg`, unit-test them directly).
- Click semantics: left-click a row = select + open in detail (same as `enter`); left-click
  a pane border/header = move focus there; right-click = no-op (reserved; do not invent
  context menus in v1).
- Third-party zone libs (`bubblezone` et al.) are **rejected for v1** per the ladder:
  one subtraction per click needs no dependency. Revisit only when a third distinct
  hit-test region appears (rule of three).

### 4.4 Resize

- `tea.WindowSizeMsg` re-runs the layout funcs (§4.3) and sets `viewport.Width/Height`.
  Zero-size dimension clamps to 1 (never pass 0 to `viewport.New` — it renders empty and
  `maxYOffset` math divides by nothing but shows nothing; fail loud in dev via assert,
  clamp in prod).

---

## 5. Architecture (fits existing seams, no refactors)

```
tea.Program (alt-screen, cell-motion opt-in)
 └─ tui/Model{ form, live viewport, list state, detail viewport, layout offsets }
     ├─ RunCrawlCmd ──► crawl.Run (Progress/OnRecord → p.Send → TUI msgs)
     ├─ RunScrapeCmd ─► scrape.Run (single result → detail)
     └─ Update: KeyMsg → §3 map │ MouseMsg → §4.2/4.3 │ WindowSizeMsg → §4.4
```

- New `tui/` package + `cli/tui_cmd.go` (`tui [--mouse]`). `tui` never imports `cli`
  internals beyond what `serve` already injects (DB, extractor-for, key-for) — same
  `Deps` func-field pattern as `mcp`/`scrape` (no import cycle by construction).
- Long work runs as `tea.Cmd`s returning result `Msg`s; nothing blocks `Update`.
- Styling is `lipgloss` only where `viewport.Style`/borders need it — one shared
  `styles.go` (~30 lines), no theme system.

---

## 6. Dependencies (APPROVAL GATE — do not `go get` until granted)

| Module | Pin at implementation | Why |
|---|---|---|
| `github.com/charmbracelet/bubbletea` | v1.3.x exact (latest v1.3.10 as of 2026-09-16) | Elm runtime; `WithMouseCellMotion`, `Enable/DisableMouse` Msgs, alt-screen |
| `github.com/charmbracelet/bubbles` | v1.0.0 exact | `viewport` (wheel handling + keymap verified in §4.2) |
| `github.com/charmbracelet/lipgloss` | v1.1.x exact | borders/padding only |
| `github.com/charmbracelet/glamour` | v1.0.0 exact (latest as of 2026-09-16) | markdown → styled ANSI for `page` mode (§2.4) |

No new modules for §2.5/§2.6: `textarea` (editing) would come from the already-pinned
`bubbles`; the folder browser is stdlib `os`/`path/filepath` only.

All three are pure Go (no CGO — re-verify with `go list -deps` CgoFiles check after
`go get`; if anything shows CgoFiles, stop and flag). Charm majors move together
(bubbletea v1 ↔ bubbles v1 ↔ lipgloss v1); re-confirm the trio's compatibility with
`go mod tidy && go build ./...` plus the three `CGO_ENABLED=0` cross-builds.

---

## 7. Testing (hermetic — no real terminal, no network, no keys)

- `tui/model_test.go`: drive `Update` with synthetic `tea.KeyMsg` (keyboard map §3 —
  full flow: fill form → start (fake extractor via `scrape.Deps` injection) → select →
  quit) and synthetic `tea.MouseMsg{Action: Press, Button: WheelDown/Left, X, Y}` (wheel
  changes `YOffset` by `MouseWheelDelta`; click at row N selects record N; click outside
  rows is a no-op). `tea.WindowSizeMsg{Width, Height}` → layout offsets recomputed.
- `tui/layout_test.go`: offset funcs — row Y → index mapping incl. scrolled-list case
  (`YOffset > 0`) and boundary rows (first/last visible, one past each end).
- `tui/view_test.go`: golden `View()` strings at a fixed 80×24 size (catches pane-offset
  drift that would silently break §4.3 hit-testing).
- Non-TTY guard: TTY check factored as `isTerminal(w io.Writer) bool` (injectable —
  test passes a pipe/buffer, asserts exit-2 path without a real TTY).
- `tui/folders_test.go`: synthetic msgs drive the browser over a `t.TempDir()` fixture
  (nested dirs + a file + a symlink loop): descend/parent/select return the resolved
  path; `n` creates a real dir and descends into it; separator-in-name and
  permission errors surface in status without leaving the browser; `esc` keeps the
  old field value. Click-row-descends asserted with a synthetic `MouseMsg`.
- `tui/edit_test.go`: synthetic `KeyMsg` stream types a `- list` + `# heading` into the
  editor; `ctrl+s` writes `<slug>.md` into a temp output dir (overwrite prompts `y/n`);
  `esc` discards (state byte-identical to pre-edit); `e` on a crawl row shows the hint
  and opens nothing.
- `tui/markdown_test.go`: fixture doc with h1, h2, fenced code block (tagged + untagged),
  nested list, GFM table, blockquote, link, image → render at width 80 → assert every
  source text line is present in output (ANSI-stripped) AND output contains escape
  codes (i.e. actually styled, not echoed raw); corrupt-input case asserts the raw
  fallback path. `v` key toggles `record`/`page` in the model test.
- Existing suites must pass unmodified (TUI is purely additive).

---

## 8. Exit criteria

- [ ] Dep approval recorded (AGENTS.md "no new dep" gate) + exact pins in `go.mod`
- [ ] `magpie tui` on a TTY: form → live run (fake-provider manual smoke) → list → detail
- [ ] Keyboard-only session completes the §1 flow with mouse disabled from the start
- [ ] Fixture article (h1/h2/codeblock/table/list/quote/link/image) renders styled in
  `page` mode; toggle `v` works keyboard-only; render failure shows raw + warning
- [ ] Folder browser: cd/descend/mkdir/select/cancel proven over temp dirs; edited
  markdown (typed list + heading) saves to `<slug>.md` in the chosen folder via `ctrl+s`
- [ ] Synthetic-msg tests prove click-select + wheel-scroll + resize (§7)
- [ ] `m` toggles capture live; non-TTY exits 2 with the documented message
- [ ] `go test ./...`, `go vet`, `gofmt -l .` (empty), `golangci-lint` clean; 3× cross-builds green; zero CgoFiles in new closure
