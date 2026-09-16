# LLM providers: Codex exec + OpenRouter + OpenCode Go/Zen

**Scope:** four new `--provider` values (`codex`, `openrouter`, `opencode-go`,
`opencode-zen`) plus shared provider plumbing. No new dependencies, no changes
to fetch/clean/crawl/selector/store.
**Depends on:** Phase 2 (`newExtractor` single provider switch, `checkCostCeiling`,
`GOMAGPIE_BASE_URL` override, hermetic `fakeProvider` test pattern).
**Stack:** `go` — execute manually, same as Phase 2.

> Naming note: this is NOT spec Milestone 3 (MCP + plugins). It lives in
> `plan/llm-providers.md` so phase numbering stays intact.

---

## 1. Objective + What Success Looks Like

1. `magpie scrape --provider openrouter --model anthropic/claude-sonnet-4-6`
   extracts valid records, costed from `usage.cost` (hand-verified once).
2. `magpie scrape --provider opencode-go --model glm-5.3` and
   `--provider opencode-zen --model claude-sonnet-4-6` extract valid records
   with `x-opencode-session` == run_id on the wire (hand-verified once each).
3. `magpie scrape --provider codex --model gpt-5.2` works on a machine with a
   logged-in Codex CLI and no other auth configured.
4. `go test ./...` still hermetic; `golangci-lint run ./...` clean.

**Non-goals:** Claude Pro/Max subscription reuse (banned by Anthropic —
§2 Policy), Zen `/responses`-only models (different API shape), a native
Codex OAuth adapter (dropped deliberately — Task 3), multi-user OAuth flows.

---

## 2. Policy box (read before building)

- **Anthropic: subscription reuse is banned.** Since Jan 2026 enforcement +
  Feb 2026 docs update, Free/Pro/Max OAuth tokens are for Claude Code and
  Claude.ai only; any other product/service (incl. Agent SDK) violates the
  Consumer ToS, with reported bans. gomagpie talks to Claude models only via
  API key, OpenRouter, or Zen — never via subscription tokens.
- **OpenAI/Codex: tolerated, not guaranteed.** OpenAI runs a Codex-for-OSS
  program and has not acted against third-party OAuth harnesses (OpenCode,
  pi), but nothing in the terms guarantees Codex-subscription use outside
  the CLI. Mitigation: the Codex path shells out to the user's own CLI
  (their login, their ToS relationship with OpenAI) — gomagpie never touches
  subscription tokens at all.
- **OpenCode Go: ambiguous by design.** The docs say Go is "designed for
  OpenCode and other coding agents that produce similar types of requests.
  Traffic is monitored for abuse." A per-page JSON extractor on a $10 flat
  plan is arguably not coding-agent traffic. Mitigation: offer `opencode-zen`
  (pay-as-you-go credits, same endpoints under `/zen/v1`, same adapters —
  a one-line sibling) alongside `opencode-go`, and document the distinction.
  Extraction volumes are tiny anyway (steady state ≈ 0 LLM calls), but the
  plan must not pretend the ambiguity away.

## 3. Key Design Decisions

- **Task 0 first: shared provider plumbing.** The four code gaps below block
  all three providers; fix once, use thrice:
  1. `--provider` help text is hardcoded in **four** files
     (`scrape.go`, `crawl.go`, `extract.go`, `cache_cmd.go`) → one shared
     `ProviderHelp` const. Same for the provider list anywhere else it appears.
  2. `newExtractor` defaults unknown providers to Anthropic → return an
     error instead (a typo must never silently bill the wrong provider).
  3. Adapter identity: `Name()` hardcodes `"openai"`/`"anthropic"`, so reused
     adapters would leak `usage.provider="openai"` for OpenRouter/Zen rows.
     Add a `Provider string` field (defaults preserve current names);
     `Name()` returns it, and it flows into `LogLLMCall`.
  4. `isFreeProvider` only knows `ollama` → replace with `needsAPIKey(p)`:
     false for `ollama` (local) and `codex` (CLI creds). Without this,
     `codex` exits 7 in scrape/crawl/extract. Add `DefaultModel` cases per
     new provider (no more `claude-sonnet-5` fallback for everyone), and fix
     the `SetKey` error message to dash-to-underscore the env name
     (`GOMAGPIE_OPENCODE_GO_API_KEY`, matching `APIKey()`).
- **Adapters gain `ExtraHeaders` + `BodyExtra`, not per-provider subclasses.**
  Headers alone are insufficient: OpenRouter needs the **body** field
  `provider: {require_parameters: true}`, or it may route to an upstream
  that treats the schema as a hint. Both maps merge into the existing
  `postJSON` call sites; default empty.
- **End the model-name guessing in cost code.** `EstimateCost` warns on
  stderr for every unknown model (up to 3×/page through the repair loop).
  OpenRouter returns `usage.cost` in every response — parse it into
  `TokenUsage.USDEstimate` instead of the price table (canonical,
  provider-computed, no warning). Zen/Go stay on table-lookup → 0, quietly:
  suppress the unknown-model warning for subscription providers (bill is
  flat; noise is not signal).
- **`--max-cost` must not meter flat rates (validator correction).**
  `checkCostCeiling` projects a $2/1M floor for unknown prices, so any
  flat-rate provider with `--max-cost` set aborts spuriously. Fix:
  `isFlatRateProvider(p)` (`codex`, `opencode-go`) → ceiling check returns
  nil immediately + `--max-cost` help notes the exemption. Metered
  providers (incl. OpenRouter via `usage.cost`, Zen via table) unchanged.
- **Zen routing by model prefix.** `/chat/completions` models → OpenAI
  adapter; `/messages` models (`minimax-*`, `qwen*-max/flash`, `claude-*`)
  → Anthropic adapter; base `https://opencode.ai/zen/go/v1` vs
  `https://opencode.ai/zen/v1` is the only Go/Zen difference. Unknown
  prefix → OpenAI adapter + stderr note. Session header (`x-opencode-session:
  <run_id>`, from the `runID` `newExtractor` already receives) + magpie UA
  on all Zen/Go calls — session-less requests are now rejected, not optional.
- **Codex is exec-only. No native OAuth adapter, no token code, no header
  impersonation.** Rationale, all verified: the local CLI (current builds)
  already exposes `--output-schema` (native strict schema file),
  `--json` (JSONL events incl. `turn.completed.usage`),
  `-o/--output-last-message` (final JSON only), `--ephemeral` (no session
  files; also scopes the cumulative usage counters to this call), stdin
  prompts via `-`, `--skip-git-repo-check`, and `--ignore-user-config`
  ("auth still uses CODEX_HOME" — config ignored, login kept). That last
  flag is load-bearing, not hygiene: issue #15451 proves the backend
  silently drops strict schema when MCP servers/tools are active, and real
  user configs carry an npx MCP server. Dropping the native adapter also
  deletes the refresh-rotation write-back hazard (rotating refresh tokens
  must be written back to `~/.codex/auth.json`, or magpie silently breaks
  the user's CLI login) and the `chatgpt-account-id` /
  `OpenAI-Beta: responses=experimental` / `originator: codex_cli_rs`
  impersonation surface. This cuts Task 3 from ~6–8h to ~2h.
- **Test fakes record headers, not just bodies.** `fakeProvider` keeps
  `bodies` only — extend with a headers capture (parallel slice) so the
  session/Referer/UA assertions are possible. No new harness.

## 4. Tasks

### Task 0 — Shared plumbing (2h)

Goal: the four gaps in §3 fixed, existing tests green.

- Shared `ProviderHelp` const; `newExtractor` errors on unknown provider;
  adapter `Provider` field + `Name()`; `ExtraHeaders`/`BodyExtra` merge in
  both adapters; `needsAPIKey`; `DefaultModel` cases
  (`openrouter→anthropic/claude-sonnet-4-6`? No — default to a cheap sane
  model per provider and document; `codex→gpt-5.2`, `opencode-go→glm-5.3`,
  `opencode-zen→claude-sonnet-4-6`); `SetKey` env-name transform;
  `isFlatRateProvider` ceiling exemption + help note; fake header capture.
- Sanity: `go test ./extract/ ./cmd/...` green, lint clean. No behavior
  change for existing providers (adapter `Provider` defaults keep names).

### Task 1 — OpenRouter (2h)

Goal: `--provider openrouter --model <any>` with hard schema routing + real cost.

- `newExtractor`: `case "openrouter"` → OpenAI adapter, base
  `https://openrouter.ai/api/v1`, `BodyExtra{"provider":
  {"require_parameters": true}}`, headers `HTTP-Referer:
  https://github.com/you/gomagpie` + `X-Title: magpie`.
- Parse `usage.cost` → `USDEstimate` (fall back to table when absent, so
  `--max-cost` meters correctly either way).
- Tests: fake asserts path `/chat/completions`, verbatim model passthrough,
  body contains `require_parameters`, Referer present; canned `usage.cost`
  lands in `LogLLMCall` (`llm_calls.usd_estimate`).
- Sanity: one real call on a $2 key before merge.

### Task 2 — OpenCode Go + Zen (3h)

Goal: `--provider opencode-go|opencode-zen`, correct adapter per model,
session headers, benchmarked defaults.

- `newExtractor`: both cases set base (`…/zen/go/v1`, `…/zen/v1`),
  `SessionID: runID`, magpie UA; prefix table picks the adapter.
- Tests: fake asserts `x-opencode-session` == runID + UA on both bases;
  prefix-table unit test (chat vs messages vs unknown→OpenAI+note).
- Sanity (manual, 30 min): real calls — 1 chat/completions model (cheapest
  Flash) + 1 `/messages` model per base — asserting strict `json_schema`
  actually constrains output. If Zen ignores `response_format`, record
  per-model results and rely on the repair loop; no Zen-specific workaround.
- Benchmark (manual, 30 min): price.yaml extraction across 2–3 models;
  note first-attempt validity; set the documented default.
- Docs note the Go-vs-Zen policy distinction (§2).

### Task 3 — Codex via CLI exec (2h)

Goal: `--provider codex`, zero token code.

- `extract/codex.go`: `CodexExecAdapter{Model, Log, LookPath}` implementing
  `Extractor`: preflight (`codex --version` + `--help` contains
  `output-schema`; absent/old → loud error telling the user to upgrade);
  per call, write prompt + schema to temp files and run
  `codex exec --json --output-schema <f> --ephemeral --skip-git-repo-check
  --ignore-user-config -o <final> -m <model> -` with stdin = prompt;
  parse final message from `-o` file; parse `turn.completed.usage` from
  the `--json` stream (fall back to zero usage, never fail the record on
  missing usage); feed through the normal validate path (schema.Validate +
  coerce — the CLI enforces shape, gomagpie still verifies).
  Timeouts: exec inherits the caller's ctx (crawl worker ctx); kill on
  cancel. Temp files under `t.TempDir`-equivalent (`os.MkdirTemp`, removed).
- `newExtractor`: `case "codex"` → adapter (no key needed —
  `needsAPIKey` false); missing CLI → error naming `codex login`.
- Tests: stub `codex` shell script on PATH emitting canned `--json`
  events + `-o` file (assert flags passed, stdin content, schema file
  content); usage parse incl. absent-`turn.completed` fallback; old-version
  preflight failure; no-binary loud error. No real CLI in CI.
- Sanity: one real `scrape` on a logged-in machine.
- Explicitly NOT built: native OAuth adapter, token refresh, header
  impersonation (see §3 rationale).

## 5. Test plan

Extend `extract/extract_test.go` (headers/body merge, prefix routing,
`usage.cost`, exec stub) + `cmd/magpie/cmd_test.go` (provider switch:
unknown → error, `codex` without key → proceeds to preflight). No live
keys in tests. One real call per provider by hand before merge (record
model + date in the PR).

## 6. Exit criteria

- [ ] `--provider openrouter|opencode-go|opencode-zen|codex` all extract
      schema-valid records (hand-verified once each; fakes cover CI)
- [ ] Zen/Go calls carry `x-opencode-session` == run_id + magpie UA (asserted)
- [ ] OpenRouter `require_parameters` in body (asserted); `usage.cost`
      lands in `llm_calls` (asserted)
- [ ] Codex works with an npx MCP server in user config (the
      `--ignore-user-config` regression case); usage parsed from events
- [ ] Unknown `--provider` errors loudly; `codex` needs no API key (no exit 7)
- [ ] `go test ./... && go vet ./...`, `gofmt -l` empty,
      `golangci-lint run ./...` clean, 3× cross-builds pass
- [ ] Docs note: no Claude-subscription reuse (banned), `--max-cost`
      exempts flat-rate providers, Zen `/responses` models unsupported,
      Go-vs-Zen policy distinction

## 7. Execution prompt

> Implement `plan/llm-providers.md` in `/home/domidex/projects/gomagpie`.
> Read `AGENTS.md`, `.pi/rules/go.md`, `.pi/rules/testing.md`, `spec.md` §3
> first. Order: Task 0 → 1 → 2 → 3. Do not build a native Codex OAuth
> adapter — Task 3 is exec-only per §3. Reuse `fakeProvider` (+ the new
> header capture) and `openAIEnvelope`; no new deps. Verify with
> `go test ./... && go vet ./...`, `gofmt -l .`,
> `golangci-lint run ./...`, and the three `CGO_ENABLED=0` builds.

## 8. Sources

- OpenRouter API reference (OpenAI-compatible schema, Referer/Title headers)
- OpenRouter structured outputs (`require_parameters`, strict-mode variance)
- OpenRouter usage accounting (`usage.cost`, no params needed)
- OpenCode Go docs (endpoints, model list, session-header + UA requirements)
- OpenCode Zen docs (pay-as-you-go `/zen/v1`, Claude/GPT/Gemini catalog)
- OpenCode ToS (subscription scope; Go abuse-monitoring language)
- Codex `exec/src/cli.rs` (flags: `--output-schema`, `--json`, `-o`,
  `--ephemeral`, `--skip-git-repo-check`, `--ignore-user-config`,
  stdin `-`; "auth still uses CODEX_HOME")
- Codex issue #15451 (strict schema silently dropped with MCP/tools active)
- Codex `--json` event shape (`turn.completed.usage`, cumulative ⇒ pair
  with `--ephemeral`)
- OpenAI Codex-for-OSS program (third-party-harness tolerance, no guarantee)
- Anthropic legal/compliance docs (Jan–Feb 2026 subscription-OAuth ban)
