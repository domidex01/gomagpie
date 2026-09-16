# Go rules — gomagpie (`magpie`)

- Go 1.26+ required (go-trafilatura v2). Toolchain: `go build`, `go test`, `go vet`, `gofmt`.
- Stdlib first: `net/http`, `net/http/cookiejar`, `context`, `encoding/json`.
  Concurrency: `golang.org/x/sync/errgroup` + per-host token buckets (`golang.org/x/time/rate`).
- Pure-Go deps only — no CGO, ever. SQLite via `modernc.org/sqlite` (pin `modernc.org/libc`
  to the exact version from its go.mod). WASM via wazero. Must build with `CGO_ENABLED=0`
  for windows/amd64, linux/amd64, darwin/arm64.
- Pinned CLI/core deps (spec §13): `spf13/cobra`, `go-rod/rod` v0.116.x, `markusmobius/go-trafilatura/v2`,
  `JohannesKaufmann/html-to-markdown/v2` v2.5.2, `santhosh-tekuri/jsonschema/v6`,
  `zalando/go-keyring` v0.2.8, `PuerkitoBio/goquery`, `cenkalti/backoff/v5`,
  `jimsmart/grobotstxt`, `bits-and-blooms/bloom/v3`, `modelcontextprotocol/go-sdk`.
- No new dependency without asking — each one is a permanent supply-chain cost.
- Browser (go-rod) sits behind the `Fetcher` plugin interface — never import rod outside `fetch/`.
  Static fetch first; escalate to browser only when the detect score is ≥ 2 (spec §1.2).
- Layout: `cmd/magpie/` entrypoint, `core/` registry + pipeline, one package per stage
  (`fetch clean extract selector crawl store mcp plugin config`), fixtures in `testdata/`.
- External input validated at the boundary; fail loudly, never swallow errors.
  Mark deliberate shortcuts with a `ponytail:` comment naming the ceiling.
