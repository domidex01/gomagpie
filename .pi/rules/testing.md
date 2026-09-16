# Testing rules — gomagpie (`magpie`)

- Default suite is fast and hermetic: `go test ./...` — no live network, no browser.
- Golden-file tests for clean/extract: input HTML fixtures + expected markdown/JSON under
  `testdata/`. `go test -update` regenerates goldens. Locks the cleaning contract across
  go-trafilatura/html-to-markdown upgrades.
- `httptest.Server` for fetch: canned HTML (static, SPA-shell, redirect chains, 429/503,
  gzip) to test the auto-detect heuristic, redirect policy, retry taxonomy, and rate
  limiter deterministically.
- Selector self-healing: mock the LLM with a fake `Extractor` returning fixed ground truth.
  Serve page version A (selectors valid) → assert cache hit + 0 LLM calls; swap to version B
  (price selector broken), feed pages past the null-rate threshold → assert re-synthesis fires
  for the broken field only.
- Browser tests need Chrome: gate with `//go:build browser`, run via
  `go test -tags browser ./...` as a separate CI job (never in the default suite).
- WASM plugin tests: compile a tiny fixture plugin, assert denied host functions trap.
- One runnable check per package with logic (`*_test.go`, no frameworks). The default suite
  must never pass vacuously — `go test ./...` exiting 0 with zero tests is a failure.
- Verify with: `go test ./... && go vet ./...` and `gofmt -l .` printing nothing.
