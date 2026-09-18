# Testing rules — gomagpie (`magpie`)

- Default suite is fast and hermetic: `go test ./...` — no live network, no browser.
  **Budget: ≤ 2 min wall.** Currently ~2 min, dominated by the two `go build` integration
  tests in `build/` (`TestBuild_Hermetic`, `TestBuild_FixtureMarker` — they compile the
  real binary, ~100 s combined, and earn their keep as hermetic-build proof). When new
  tests push the suite past budget, tier the slow ones behind a build tag or CI job —
  never let the default suite quietly rot.
- Golden-file tests for clean/extract: input HTML fixtures + expected markdown/JSON under
  `testdata/`. `go test -update` regenerates goldens — review the diff hunk by hunk; a
  golden regen is a contract change, not a chore. Locks the cleaning contract across
  go-trafilatura/html-to-markdown upgrades.
- `httptest.Server` for fetch: canned HTML (static, SPA-shell, redirect chains, 429/503,
  gzip) to test the auto-detect heuristic, redirect policy, retry taxonomy, and rate
  limiter deterministically.
- Selector self-healing: mock the LLM with a fake `Extractor` returning fixed ground truth.
  Serve page version A (selectors valid) → assert cache hit + 0 LLM calls; swap to version B
  (price selector broken), feed pages past the null-rate threshold → assert re-synthesis fires
  for the broken field only.
- Parallelism: pure tests (parsers, validators, coercion, goldens) take `t.Parallel()` —
  the suite has 380+ tests and package-level parallelism alone won't hold the budget as
  packages grow. Tests using `t.Setenv`/`os.Chdir`/shared ports stay serial (the stdlib
  already panics on the Setenv+Parallel combo). Don't force-parallel state-sharing
  httptest suites.
- **Race detector:** touched packages pass `go test -race`; CI runs the full suite with
  `-race`. Crawl workers, core registry, selector heal, and scrape batch all spawn real
  goroutines — races there ship to users, not just CI.
- **Flakes are bugs:** a test that fails on a clean tree gets fixed or deleted the same
  day (precedent: TestWASM_CanceledCtx). No retry wrappers, no skip-on-flake, no
  "known flaky" lists.
- Browser tests need Chrome: gate with `//go:build browser`, run via
  `go test -tags browser ./...` as a separate CI job (never in the default suite).
- WASM plugin tests: compile a tiny fixture plugin, assert denied host functions trap.
- One runnable check per package with logic (`*_test.go`, no frameworks). The default suite
  must never pass vacuously — `go test ./...` exiting 0 with zero tests is a failure.
- Verify with: `go test ./... && go vet ./... && golangci-lint run ./...` and `gofmt -l .`
  printing nothing.
