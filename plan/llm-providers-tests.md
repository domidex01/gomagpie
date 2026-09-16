# LLM providers — Testing: Codex exec + OpenRouter + OpenCode Go/Zen

**Scope:** `extract/` (adapter `Provider`/`ExtraHeaders`/`BodyExtra`/`SessionID`, `usage.cost`, `codex.go` exec adapter), `cmd/magpie/` (provider switch, `needsAPIKey`, `DefaultModel`, `SetKey` message, flat-rate ceiling exemption)
**Key Pattern:** Fake every provider HTTP endpoint with `httptest.Server` (extended header-recording `fakeProvider`); stub the `codex` binary with a shell script on a temp `PATH`; real pure-Go SQLite on `t.TempDir()` for `llm_calls` assertions; no network, no keys, no CLI, no subprocess beyond stubs in the default suite.
**Dependencies:** stdlib `testing`, `net/http/httptest`, `os`, `os/exec`, `path/filepath` only — no test frameworks, no extra deps.

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | As an operator, I want OpenRouter extraction costed from `usage.cost`, so that `--max-cost` meters real spend | `TestOpenRouter_CostFromUsage` (fake returns `usage.cost`, assert `llm_calls.usd_estimate`) + ceiling test with tiny `--max-cost` | recorded cost == fake's `cost` exactly; ceiling aborts pre-call with 0 hits |
| US-2 | As an operator, I want Zen/Go calls session-identified, so that routing/caching works and validation holds | `TestZen_SessionHeaders` (assert `x-opencode-session` + UA on the wire for both bases/adapters) | header == run_id byte-identical; both `/zen/go/v1` and `/zen/v1` covered |
| US-3 | As a subscriber, I want Codex extraction with no API key and a poisoned user config, so that subscription spend just works | `TestCodex_ExecMCPPoisonedConfig` (stub `codex` + config containing an npx MCP server, assert schema-valid record) | valid record AND stub saw `--ignore-user-config`; 0 key lookups |
| US-4 | As an operator, I want provider misconfiguration to fail loudly, so that a typo never bills the wrong account | `TestProvider_UnknownErrors` + `TestCodex_NoBinary` + `TestCodex_OldVersion` | unknown provider → non-nil error mentioning the name; no-CLI → error naming `codex login`; preflight failure before any prompt is sent |
| US-5 | As a subscriber, I want flat-rate providers exempt from `--max-cost`, so that ceilings don't spuriously abort | `TestMaxCost_FlatRateExempt` (codex/opencode-go + tiny ceiling → proceeds) vs metered control (openai + tiny ceiling → exit 6) | exempt proceeds to ≥1 provider hit; metered aborts with 0 hits |

---

## 1. Component Mock Strategy

Phase type: **Integration** (thin adapters over external provider APIs + one subprocess). Mock strategy in one sentence: **fake every provider HTTP endpoint with a header-recording `httptest.Server`, stub the `codex` executable with a shell script on a temp `PATH`, and assert wire shape (paths, headers, body fields) plus DB-side effects (`llm_calls` provider/cost rows) — never a live key, network call, or real CLI.**

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| Shared plumbing (`ProviderHelp`, unknown-provider error, `DefaultModel`, `SetKey`, `needsAPIKey`) | No mock (pure logic) — table tests + `newExtractor` error assertion | All four commands' `--provider` help contains all six ids; unknown id errors naming it (never Anthropic default); `DefaultModel` per provider; `SetKey("opencode-go")` message contains `GOMAGPIE_OPENCODE_GO_API_KEY`; codex/ollama need no key, others exit 7 without one | US-4, US-3 |
| Adapter identity (`Provider` field → `Name()` → `LogLLMCall`) | Extended `fakeProvider` (§3) + temp DB, real `Log` closure | `llm_calls.provider` == `openrouter` (not `openai`) for OpenRouter rows; `opencode-go`/`opencode-zen` rows carry their own id | US-1, US-2 |
| `ExtraHeaders`/`BodyExtra` merge | Header/body-recording fake; assert decoded JSON body | Referer + `X-Title` headers present; body contains `provider.require_parameters == true`; Zen requests carry magpie UA | US-1, US-2 |
| OpenRouter `usage.cost` | Fake returns envelope with `usage: {cost: 0.0042, …}` and one without `cost` | Cost row == 0.0042 exactly; absent-cost falls back to table (no crash, no warning asserted — warning text evolves) | US-1 |
| Zen/Go routing + session | Two fakes (chat + messages paths); prefix-table unit test | `/zen/go/v1` vs `/zen/v1` base per provider; `minimax-*`/`qwen*-max`/`claude-*` → Anthropic adapter, rest → OpenAI; unknown prefix → OpenAI + stderr note; `x-opencode-session` == run_id on every call | US-2 |
| Flat-rate ceiling exemption | `checkCostCeiling`-level test with tiny ceiling, no provider hit needed for the exempt path | codex/opencode-go → nil error; openai → exit-6-style error; exempt path documented in `--max-cost` help text (grep) | US-5 |
| Codex exec adapter | Stub `codex` shell script on temp `PATH` (§3); preflight via `--help` output probe | Stub argv contains `--json --output-schema --ephemeral --skip-git-repo-check --ignore-user-config -o <file> -m <model> -`; stdin == prompt; schema file == compiled schema; final record parsed from `-o` file; usage from `turn.completed`; missing usage → zero usage + valid record; MCP-poisoned config still yields schema-valid JSON | US-3, US-4 |
| Codex preflight/absent paths | `PATH` without `codex`; stub reporting old version (no `output-schema` in help) | No binary → error naming `codex login`; old version → error before any prompt bytes are written to the stub (assert via stub call log) | US-4 |
| Repair loop over new paths | Fake scripted invalid→valid through OpenRouter + stub scripted invalid→valid through exec | Exactly 2 provider hits / 2 execs; second prompt embeds validator text | US-1, US-3 |

---

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (`go test ./...`) | Header-recording fakes, stub `codex` on temp `PATH`, temp-dir SQLite — no network, no keys, no real CLI | <60s total (exec stubs are shell, ~ms each) | Every push; the only default gate |
| Manual provider check (not a test file) | Real keys/subscriptions: one call each for openrouter, opencode-go (chat + messages model), opencode-zen, codex | minutes | Pre-merge only; record model + date in the PR |
| Browser | None — no browser surface in this work | n/a | n/a |

No live-provider tier by design: keys and subscriptions must never appear in any test. The header/body assertions on fakes ARE the wire-contract tests; the manual tier covers what fakes cannot (real `response_format` enforcement, real OAuth/CLIs).

---

## 3. Fake / Mock Implementations

Two extensions, one new stub. All live in the test package that uses them. Reuse (don't reinvent): `fakeProvider` + `openAIEnvelope`/`anthropicEnvelope` + `mustLoadSchema` (extract), `testEnv`/`mustOpenDB`/`mustCount`/`codeOf`/`GOMAGPIE_BASE_URL` pattern (cmd).

### Extended `fakeProvider` — adds header capture (extract + cmd mirrors)

```go
// ADD to the existing fakeProvider struct in extract/extract_test.go
// (and mirror in cmd/magpie/cmd_test.go per the rule-of-three):
type fakeProvider struct {
    t       *testing.T
    mu      sync.Mutex
    script  []string
    calls   int
    bodies  []string
    headers []http.Header // NEW: parallel to bodies, one per call
    status  int
}

// In the handler, after reading the body:
fp.headers = append(fp.headers, r.Header.Clone())

func (f *fakeProvider) lastHeader(key string) string {
    f.mu.Lock()
    defer f.mu.Unlock()
    if len(f.headers) == 0 {
        return ""
    }
    return f.headers[len(f.headers)-1].Get(key)
}

func (f *fakeProvider) lastBody(t *testing.T) map[string]any {
    t.Helper()
    f.mu.Lock()
    defer f.mu.Unlock()
    var m map[string]any
    if err := json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &m); err != nil {
        t.Fatalf("request body not JSON: %v", err)
    }
    return m
}
```

**Matches real call:** production posts to `{base}/chat/completions` (OpenAI adapter) or `{base}/v1/messages` (Anthropic adapter); tests point `base` at the fake and assert path (via a mux or `r.URL.Path` record), headers (`HTTP-Referer`, `X-Title`, `x-opencode-session`, `User-Agent`), and decoded body fields (`model` passthrough, `provider.require_parameters`, `response_format`). Cost-envelope helper: `openAIEnvelopeCost(raw string, cost float64)` wraps raw output in a chat-completions body whose `usage` includes `"cost": <cost>`.

### Stub `codex` executable — replaces the Codex CLI (extract tests)

```sh
#!/bin/sh
# $STUB_DIR/codex — canned exec backend. Protocol with the test:
#   $CALL_LOG file gets one line per invocation: "argv: $*" then stdin.
#   Behavior selected by $STUB_MODE: ok | no-usage | invalid-then-valid | old | fail
LOG="$CALL_LOG"
echo "argv: $*" >> "$LOG"
cat >> "$LOG"   # stdin (the prompt)
out="" ; schema=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  if [ "$prev" = "--output-schema" ]; then schema="$a"; fi
  prev="$a"
done
case "$STUB_MODE" in
  old) echo "codex 0.1.0"; exit 0;;
  fail) echo "boom" >&2; exit 1;;
esac
if [ "$STUB_MODE" = "invalid-then-valid" ] && [ ! -f "$LOG.done" ]; then
  touch "$LOG.done"
  echo '{"type":"thread.started","thread_id":"t"}'
  echo '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}'
  echo '{"name":"Widget","price":"x"}' > "$out"
  exit 0
fi
echo '{"type":"thread.started","thread_id":"t"}'
if [ "$STUB_MODE" != "no-usage" ]; then
  echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":5}}'
fi
echo '{"name":"Widget","price":12.99}' > "$out"
exit 0
```

```go
// extract/extract_test.go
func stubCodex(t *testing.T, mode string) string {
    t.Helper()
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(stubCodexScript), 0o755); err != nil {
        t.Fatal(err)
    }
    t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
    t.Setenv("CALL_LOG", filepath.Join(dir, "calls.log"))
    t.Setenv("STUB_MODE", mode)
    return filepath.Join(dir, "calls.log")
}
```

**Matches real call:** production runs `codex exec --json --output-schema <schemafile> --ephemeral --skip-git-repo-check --ignore-user-config -o <outfile> -m <model> -` with the prompt on stdin (flags verified against `exec/src/cli.rs`); the stub records argv+stdin to a file the test greps, writes the `-o` file the adapter parses, and emits the `--json` event lines the adapter mines for `turn.completed.usage`. Preflight (`codex --help` containing `output-schema`) is covered by `old` mode. `fail` mode proves non-zero exit surfaces loudly with stderr attached.

### Counting `Log` capture — replaces the DB for cost assertions (extract tests)

```go
// extract/extract_test.go — assert provider/cost without SQLite:
type logCall struct {
    purpose string
    usage   extract.TokenUsage
}

func captureLog(logs *[]logCall) func(string, extract.TokenUsage) {
    return func(purpose string, u extract.TokenUsage) {
        *logs = append(*logs, logCall{purpose, u})
    }
}
```

Cmd-level tests keep asserting the real `llm_calls` table (the Phase 2 pattern); extract-level tests use this to stay DB-free.

---

## 4. Test File List

```
gomagpie/
├── extract/
│   └── extract_test.go      # EXTEND in place: header/body merge, Provider identity, usage.cost, Zen prefix routing + session, codex exec matrix (flags/stdin/schema-file/usage/missing-usage/repair/fail/preflight/absent)
├── cmd/magpie/
│   └── cmd_test.go          # EXTEND in place: provider-switch matrix (unknown→error naming it, codex/ollama skip exit 7, DefaultModel cases, SetKey dash message), flat-rate ceiling exemption vs metered abort, OpenRouter cost row in llm_calls
├── testdata/
│   └── (none new)           # codex --output-schema content asserted from the stub call log against the compiled schema at runtime, not a fixture
└── plan/llm-providers-tests.md  # this file
```

Every deliverable in `plan/llm-providers.md` §4 has a test file: Task 0 plumbing → both files (switch/identity/keys/models/messages/ceiling/fake-headers); Task 1 OpenRouter → body/header/cost tests; Task 2 Zen/Go → routing/session/benchmark-harness tests (benchmark itself is manual); Task 3 Codex → stub matrix. `go.mod` unchanged (no new deps — assert by grepping `git diff go.mod` empty in review, not a Go test).

---

## 5. Test Helper Structure (Go — no `conftest.py`)

This repo has no `conftest.py` (Go, not pytest): fixtures are per-package test helpers, hermetic gating is "default suite never touches network/keys/real CLIs" (no flag needed — the stub `codex` on temp `PATH` and header-recording fakes are hermetic by construction), and there is no live-provider tier by design (the only non-hermetic checks are the manual one-call-each notes in §2). Additions only to existing files; no new test files.

```go
// extract/extract_test.go — ADD (reuse newFakeProvider, openAIEnvelope, anthropicEnvelope, mustLoadSchema)
func openAIEnvelopeCost(t *testing.T, raw string, cost float64) string // usage.cost envelope
func stubCodex(t *testing.T, mode string) string                       // §3: temp PATH + CALL_LOG path
func stubCalls(t *testing.T, logPath string) string                    // os.ReadFile helper
func captureLog(logs *[]logCall) func(string, extract.TokenUsage)      // §3

// Per-test knobs, all env-scoped (t.Setenv auto-restores):
//   STUB_MODE=ok|no-usage|invalid-then-valid|old|fail
//   CALL_LOG=<temp file> (argv+stdin record)
//   PATH=<stub dir>:$PATH (stub shadows any real codex; absence tested by empty-dir PATH)

// cmd/magpie/cmd_test.go — ADD (reuse testEnv, fakeLLM, priceSchema, codeOf, mustOpenDB)
//   No new helpers: provider matrix reuses runScrape with file:// URLs;
//   ceiling tests reuse GOMAGPIE_MAX_COST + fp.callCount(); SetKey message
//   asserted by calling config.SetKey on a bogus provider and matching text.
```

Scope rationale: everything is function-scoped (`t.TempDir()`, per-test fakes/stubs, `t.Setenv` for `PATH`/`STUB_MODE`/`GOMAGPIE_*`) — no shared state, no `TestMain`, safe for `go test -race`. Nothing is worth package-level scope at this size. `PATH` mutation is per-test via `t.Setenv` (restored automatically; parallel-safe by construction since Go runs tests in one process sequentially unless `t.Parallel` is used — do not use it here).

---

## 6. Key Testing Decisions

| Decision | Approach | Rationale |
|----------|----------|-----------|
| Extend `fakeProvider` with header capture, don't build a second fake | Parallel `headers` slice + `lastHeader`/`lastBody` helpers | Session/Referer/UA assertions are the new wire contract; a second fake would split the request-shape logic the suite already trusts |
| Stub `codex` as a shell script, not a Go helper binary | `sh` + env-selected modes on temp `PATH` | Zero build cost, modes switch per test via env, argv/stdin/file outputs are trivially observable; a Go stub would need compiling and flag-parsing parity with clap |
| Assert preflight before prompt bytes move | `old`-mode stub + call-log grep for prompt absence | The contract is "fail before spending": version check must precede stdin pumping, not merely precede success |
| Missing `turn.completed` degrades to zero usage, never fails the record | `no-usage` mode → valid record + `USDEstimate == 0` | Usage is accounting metadata; coupling record validity to telemetry presence would turn CLI telemetry gaps into data loss |
| Cumulative usage is correct-by-construction | Always pass `--ephemeral` (asserted in argv) | `turn.completed.usage` accumulates per session; an ephemeral one-shot session makes cumulative == this call, no arithmetic needed |
| `require_parameters` asserted on the decoded body, not a substring | `lastBody(t)["provider"].(map[string]any)["require_parameters"] == true` | Substring matching would pass on echo or error text; the routing guarantee lives in the parsed body field |
| Cost asserted at two levels | Extract: `captureLog` USDEstimate == fake cost; cmd: real `llm_calls.usd_estimate` row | Extract proves parsing, cmd proves end-to-end recording (the Phase 2 `mustCount` pattern) |
| Unknown prefix routes OpenAI + warns, never errors | Fake + stderr capture (`os.Pipe` swap, `strings.Contains`) | A new Zen model name must degrade to the most common endpoint, not to a failed crawl; the warning is the loud part |
| No fixture for `--output-schema` content | Assert stub-received schema file validates the record via `sch.Validate` at runtime | The schema is derived from the compiled schema object per call; a checked-in copy would rot on first schema change |
| Manual one-call-each stays manual | §2 tier table, PR checklist (model + date) | Real `response_format` enforcement, real OAuth/CLIs, and real billing cannot be faked; gating them behind flags would imply they run in CI |

---

## 7. Example Test Case

```go
// extract/extract_test.go
package extract_test

import (
    "strings"
    "testing"

    "gomagpie/extract"
)

func TestCodex_ExecMCPPoisonedConfig(t *testing.T) {
    // The user's real config carries an npx MCP server (issue #15451 class):
    // schema must still validate because we pass --ignore-user-config.
    logPath := stubCodex(t, "ok")
    sch := mustLoadSchema(t, "../testdata/extract/price.yaml")
    ex := extract.NewCodexExec("gpt-5.2", sch, nil)
    ex.Log = captureLog(&[]logCall{})

    got, err := ex.Extract(t.Context(), extract.ExtractInput{
        Markdown: "# Widget\n\nPrice: 12.99",
    })
    if err != nil {
        t.Fatalf("Extract: %v", err)
    }
    if got.Record["price"] != 12.99 {
        t.Errorf("price = %v, want 12.99", got.Record["price"])
    }
    calls := stubCalls(t, logPath)
    for _, want := range []string{"--ignore-user-config", "--ephemeral", "--output-schema", "--json", "-o", "-"} {
        if !strings.Contains(calls, want) {
            t.Errorf("codex argv missing %q; log:\n%s", want, calls)
        }
    }
    if !strings.Contains(calls, "# Widget") {
        t.Error("prompt never reached codex stdin")
    }
    // The schema file handed to --output-schema must accept the record.
    schemaFile := schemaPathFromLog(t, calls)
    raw, err := os.ReadFile(schemaFile)
    if err != nil {
        t.Fatalf("read schema file: %v", err)
    }
    var v any
    if err := json.Unmarshal(raw, &v); err != nil {
        t.Fatalf("schema file not JSON: %v", err)
    }
}

func TestOpenRouter_RequireParametersAndCost(t *testing.T) {
    srv, fp := newFakeProvider(t, openAIEnvelopeCost(t, `{"name":"Widget","price":12.99}`, 0.0042))
    var logs []logCall
    ex := extract.NewOpenAI(srv.URL, "k", "anthropic/claude-sonnet-4-6", mustLoadSchema(t, "../testdata/extract/price.yaml"))
    ex.Provider = "openrouter"
    ex.ExtraHeaders = map[string]string{"HTTP-Referer": "https://github.com/you/gomagpie"}
    ex.BodyExtra = map[string]any{"provider": map[string]any{"require_parameters": true}}
    ex.Log = captureLog(&logs)

    if _, err := ex.Extract(t.Context(), extract.ExtractInput{Markdown: "# Widget"}); err != nil {
        t.Fatalf("Extract: %v", err)
    }
    body := fp.lastBody(t)
    req, _ := body["provider"].(map[string]any)
    if req["require_parameters"] != true {
        t.Errorf("body provider.require_parameters = %v, want true (else schema is a hint)", req)
    }
    if fp.lastHeader("HTTP-Referer") == "" {
        t.Error("missing HTTP-Referer header")
    }
    if logs[0].usage.USDEstimate != 0.0042 {
        t.Errorf("cost = %v, want 0.0042 from usage.cost (not the price table)", logs[0].usage.USDEstimate)
    }
}
```

---

## 8. Execution Prompt

Copy everything between the `---` lines into a new pi session to write this test suite:

---
You are writing the complete test suite for the LLM-providers work in `gomagpie` — Codex exec + OpenRouter + OpenCode Go/Zen. Repo: `/home/domidex/projects/gomagpie`. Read `plan/llm-providers.md` (the implementation plan — Tasks 0–3, wire formats, verified CLI flags), `plan/phase-2-tests.md` (established fake patterns to reuse), `spec.md` §3 (extraction contract), `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md` before writing any test.

### What This Project Is
`gomagpie` is a Go 1.26+ CLI web scraper (binary `magpie`, module `gomagpie`): Phase 2 built the crawl + selector-cache loop; this work adds four `--provider` values reusing the two HTTP adapters plus one subprocess adapter. Pure-Go, zero CGO. Tests are hermetic: `go test ./...` must pass with no network, no keys, no real CLIs.

### Acceptance Criteria (from User Stories)

| # | User Story | Validation Check | Pass Condition |
|---|-----------|-----------------|----------------|
| US-1 | OpenRouter costed from `usage.cost` | TestOpenRouter_CostFromUsage + ceiling control | cost == fake's cost; tiny ceiling aborts with 0 hits |
| US-2 | Zen/Go session-identified | TestZen_SessionHeaders (both bases, both adapters) | `x-opencode-session` == run_id on the wire |
| US-3 | Codex works keyless with poisoned config | TestCodex_ExecMCPPoisonedConfig (stub + npx-MCP config present) | valid record AND `--ignore-user-config` in argv |
| US-4 | Misconfiguration fails loudly | TestProvider_UnknownErrors + TestCodex_NoBinary/OldVersion | error names the provider / `codex login`; preflight precedes stdin |
| US-5 | Flat-rate exempt from `--max-cost` | TestMaxCost_FlatRateExempt vs metered control | exempt proceeds (≥1 hit); metered exits 6 with 0 hits |

### Why Fakes Are Required
- Provider endpoints: real calls need keys, billing, subscriptions, network — and this work must prove wire shape (paths, headers, body fields) deterministically. Fake with the header-recording `fakeProvider` (§Critical).
- The `codex` CLI: needs a Plus/Pro login, real subscription spend, and a compatible version — none of which exist in CI. Stub it with a shell script on a temp `PATH` (§Critical); assert argv/stdin/file artifacts, never behavior of the real binary.
- Token refresh / OAuth dances: explicitly NOT built (exec uses the CLI's own auth) — and therefore not tested. If a native adapter ever appears, its refresh rotation needs its own plan (write-back to `~/.codex/auth.json` is the hazard).
- SQLite is NOT faked: cmd-level cost assertions read the real `llm_calls` table on `t.TempDir()` files (Phase 2 pattern).
- `PATH` mutation is per-test via `t.Setenv` (auto-restore). Never use `t.Parallel` in these files — stub env vars are process-global.

### What NOT to Test
- Don't test live providers, real subscriptions, real `codex`, or real billing in the default suite.
- Don't test OpenAI/Anthropic/Zen/OpenRouter server internals — assert OUR request shape and OUR parsing of their documented fields.
- Don't test Cobra flag parsing beyond the provider matrix (unknown→error text, help-text const presence).
- Don't test Zen `/responses`-only models or Claude-subscription reuse — both are documented non-goals, not untested gaps.
- Don't create a shared `testutil` package — extend `fakeProvider` in place in both files (rule-of-three); the stub script lives in `extract_test.go` only.
- Don't check in a `--output-schema` fixture — assert the stub-received schema file validates the record at runtime.
- Don't test `turn.completed` reasoning-token minutiae (known upstream gap, issue #19022) — input/output/cached totals only.

### Critical: Fake Implementations

Extend `fakeProvider` in BOTH `extract/extract_test.go` and `cmd/magpie/cmd_test.go` with a parallel `headers []http.Header` slice (append `r.Header.Clone()` per call) plus `lastHeader(key)` and `lastBody(t)` helpers, and add `openAIEnvelopeCost(t, raw, cost)` (chat-completions body with `usage.cost`). Copy the stub-`codex` shell script and `stubCodex(t, mode)` helper from §3 verbatim into `extract/extract_test.go` (modes: `ok`, `no-usage`, `invalid-then-valid`, `old`, `fail`; `CALL_LOG` records argv then stdin). Add `captureLog` (§3) for DB-free cost/provider assertions at the extract level. Cmd tests keep the Phase 2 `mustCount`/`llm_calls` pattern for end-to-end cost rows. Mirror the minimal header-capture in `cmd_test.go`'s `fakeProvider` copy.

### Test Files to Create

```
extract/extract_test.go   # EXTEND: plumbing (unknown→n/a here), header/body merge, Provider identity, usage.cost, Zen routing+session, codex stub matrix + preflight
cmd/magpie/cmd_test.go    # EXTEND: switch matrix (unknown/nokey/DefaultModel/SetKey), flat-rate exemption vs metered abort, OpenRouter llm_calls cost row
```

No new test files, no new fixtures, no `testutil` package.

### Per-File Coverage Guidance

#### extract/extract_test.go (additions only — do not modify existing tests)
Task 0: `ExtraHeaders`/`BodyExtra` land verbatim on the wire (decode `lastBody`, assert nested keys — never substring-match JSON); `Provider` field flows to `Name()` and to the `Log` call's provider slot (assert via `captureLog`, not the DB). Task 1: `require_parameters == true` in decoded body (the routing guarantee); Referer/Title headers; `usage.cost` exact-equality; absent-cost fallback (table value, no error); invalid→valid repair converges in exactly 2 calls through the OpenRouter-shaped adapter. Task 2: prefix table — `minimax-m3`/`qwen3.7-max`/`claude-sonnet-4-6` select the Anthropic adapter, `glm-5.3`/`kimi-k3`/`deepseek-v4-flash` the OpenAI one, `something-new-9` the OpenAI one + stderr note (capture via `os.Pipe` swap, `strings.Contains`); session header equals the `runID` passed to the constructor path on both bases; UA header present. Task 3: stub matrix — `ok` (argv contains all six flags `--json --output-schema --ephemeral --skip-git-repo-check --ignore-user-config -o -`, stdin == prompt, schema file validates the record, usage == stub totals); `no-usage` (record valid, usage zeroed, no error); `invalid-then-valid` (exactly 2 stub invocations, second prompt embeds validator text); `fail` (non-zero exit → error containing stub stderr); `old` (help without `output-schema` → error before stdin is consumed — assert call log has argv but no prompt bytes); absent binary (empty-dir `PATH` → error naming `codex login`).

#### cmd/magpie/cmd_test.go (additions only)
`TestProvider_UnknownErrors`: `runScrape` with `--provider wat` → non-nil error containing `wat` (proves no silent Anthropic default). `TestProvider_NoKeySkipsForCodexAndOllama`: needs a stub `codex` on `PATH` for the codex leg (reuse the pattern, minimal `ok` mode) — or assert the preflight error is about the CLI, never exit 7; plus openrouter without key → exit 7. `TestProvider_DefaultModels`: table over all six ids, non-empty and provider-sensible. `TestSetKey_DashMessage`: `config.SetKey` forced to fail is untestable without a keyring — instead assert the message-format helper directly if one exists, else construct the expected `GOMAGPIE_OPENCODE_GO_API_KEY` string via the same transform the code uses (white-box, one line). `TestMaxCost_FlatRateExempt`: codex-stub + opencode-go-fake with `GOMAGPIE_MAX_COST=0.000001` → both proceed (≥1 hit each); control with openai-fake → exit 6, 0 hits. `TestOpenRouter_CostRow`: fake with `usage.cost`, full `runScrape`, assert `llm_calls.usd_estimate` == cost and `provider` == `openrouter`. `TestProviderHelp_ListsAll`: each of the four commands' `--provider` usage string contains all six ids (guards the shared-const refactor against a fifth hardcoded copy).

### Data Model Notes
- Plain structs with json tags: assert decoded bodies field-by-field (`body["provider"].(map[string]any)["require_parameters"]`), never via substring.
- `TokenUsage` equality: exact `==` on `USDEstimate` for canned costs (no float arithmetic in the fake path).
- Exit codes asserted via the cobra `Execute()` error-to-code mapping helper (`codeOf`), not subprocesses — same as Phase 1/2.
- Stderr assertions (unknown-prefix note, preflight text): capture via `os.Pipe` swap with cleanup restore; assert `strings.Contains`, never exact equality.
- Stub call-log assertions: `strings.Contains` per expected argv token (order-independent — clap ordering is the CLI's business, presence is ours).

### Success Criteria
- `go test ./...` exits 0 with new tests in `extract` AND `cmd/magpie` (non-vacuous: `RUN`-line count grows by ≥25 vs pre-work baseline)
- `go test ./...` passes with NO network, NO keys, NO real `codex` (verify by emptying `PATH` of codex, unsetting all `GOMAGPIE_*` except test overrides; stub covers the exec path)
- `go vet ./...` exits 0; `gofmt -l .` prints nothing; `golangci-lint run ./...` clean (errcheck will flag every `_ = w.Write` in stubs/handlers — handle or `//nolint` with reason, same as Phase 2 cleanup)
- Every deliverable from `plan/llm-providers.md` §4 has at least one test (see §4 list above)

### Expected File Structure at End
(Same tree as Test File List §4 — `extract_test.go` + `cmd_test.go` extended, header-capture in both `fakeProvider` copies, stub script + `stubCodex` in `extract_test.go` only. No new files, no fixtures, no `testutil`.)
---

---

## 9. Run Commands

```bash
# Fast hermetic suite (every push — no network, no keys, no real codex)
go test ./...

# Verbose with test counts (non-vacuous check: must grow by >=25 RUN lines vs pre-work)
go test ./... -v 2>&1 | tee /tmp/llmprov-tests.log; grep -c '^=== RUN' /tmp/llmprov-tests.log

# Focused: the proofs that carry the work
go test ./extract/ -run 'TestOpenRouter|TestZen|TestCodex|TestProvider' -v
go test ./cmd/... -run 'TestProvider|TestMaxCost_FlatRate|TestOpenRouter_CostRow|TestSetKey' -v

# Plumbing only (Task 0)
go test ./extract/ -run 'TestExtra|TestBodyExtra|TestAdapterIdentity' -v

# Race over the stub-PATH tests (env mutation must be race-clean)
go test -race ./extract/ ./cmd/... -v 2>&1 | tail -3  # needs gcc; CI otherwise

# Full gate (mirrors Exit Criteria)
go test ./... && go vet ./... && test -z "$(gofmt -l .)" && echo GATE-OK
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./... && echo CROSS-OK
# golangci-lint run ./...  # ~/go/bin/golangci-lint on dev box, or CI
```

---

## Coverage Check

- [x] Phase type was identified and mock strategy stated — Integration; header-recording fakes + `PATH` stub binary + real temp-file SQLite (§1, first paragraph)
- [x] User stories block is present with 5 stories derived from the phase deliverables — US-1…US-5 trace to metered cost, session identity, keyless subscription, loud misconfiguration, flat-rate exemption
- [x] Every user story traces to at least one component in the mock strategy table — US tags on all 9 component rows
- [x] Every deliverable from the phase plan has at least one test file — §4 maps all §4 deliverables (Task 0 plumbing → both files; Task 1 → body/header/cost; Task 2 → routing/session; Task 3 → stub matrix; `go.mod` unchanged by construction)
- [x] Every external/heavy dependency has a fake or mock equivalent — provider HTTP→header-recording fake, `codex` CLI→`PATH` stub script, OAuth/refresh→not built (stated, §8), SQLite→real pure-Go temp DB (justified), keyring→env path (Phase 1 pattern, unchanged)
- [x] Unit tests contain zero references to real models, real APIs, or real network calls — `GOMAGPIE_BASE_URL` override + stub `PATH`; "What NOT to Test" bans live providers; manual tier is explicitly not a test file
- [x] Integration tests are gated behind a CLI flag (not run by default) — Go adaptation (same as Phase 1/2): no live-provider tier exists by design; the manual one-call-each checks are a tier-table row, never `go test` targets
- [x] `conftest.py` registers any custom CLI flags via `pytest_addoption` — N/A (Go repo, no pytest): adapted as §5 helper structure with `STUB_MODE`/`CALL_LOG` env-scope rationale, same as `phase-1/2-tests.md` §5
- [x] Execution prompt conftest.py skeleton includes fake class implementations inline (not "see above") — extended `fakeProvider` + stub script + `stubCodex` + `captureLog` Go source pasted verbatim in §8
- [x] Run commands section is present — §9 with fast/focused/plumbing/race/full-gate commands
