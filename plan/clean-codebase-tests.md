# Clean-Codebase Plan — Testing: Refactor S1–S3 (validation collapse, registry note, crawl split)

**Stack:** go (per AGENTS.md — `run-phase` hard-blocks on this stack; execute steps manually)
**Scope:** `scrape` (ValidateOptions extraction), `cli` (exit-code consolidation), `crawl` (Run split, moves only), `core` (doc-only)
**Key Pattern:** Behavior-preserving refactor → characterization tests pin current behavior BEFORE each step; moves-only commits are verified by an identical test pass-set, not new tests. No new fakes — reuse existing ones verbatim.
**Dependencies:** stdlib `testing` only (repo rule: no testify, no new deps)

---

## User Stories

| # | User Story | Validation Check | Pass Condition |
|---|-----------|------------------|----------------|
| US-1 | As a CLI user, I want identical exit codes (2/7/8) after validation is consolidated, so that scripts relying on exit codes keep working | `cli/exitfor_test.go` table over err→code + one httpd smoke per code | exit 2 for bad `--render`, 7 for missing key, 8 for quality — unchanged |
| US-2 | As an MCP client, I want identical validation errors from `scrape_url`/`crawl_site`, so that agent error handling doesn't break | `mcp/tools_test.go` cases calling the same `scrape.ValidateOptions` | error strings match the pinned characterization set exactly |
| US-3 | As a crawl user, I want byte-identical behavior after `crawl.Run` is split into phases, so that resumes/ratelimits/robots behave the same | existing `crawl` suite green *untouched* + goldens byte-identical | `go test ./crawl/` pass-set identical to pre-refactor capture; `testdata` drift EMPTY |
| US-4 | As a maintainer, I want the option switches to exist in exactly one function, so that adding an option edits 1 file not 4 | grep: `must be auto\|static\|browser` appears once in non-test source (scrape pkg) | grep count == 1 outside `_test.go` |

---

## 1. Component Mock Strategy

| Component | Mock Strategy | What to Assert | User Story |
|-----------|--------------|----------------|------------|
| `scrape.ValidateOptions` | None — pure function, table-driven | Exact error string per invalid input (characterization: strings pinned from current `scrape.Run` pre-I/O checks); nil for all valid combos | US-1, US-2 |
| `cli exitFor(err)` | None — pure errors.Is switch, table-driven | 2/7/8/1 mapping for `scrape.ErrMissingKey`, `clean.ErrQuality`, validation errors, unknown errors | US-1 |
| CLI commands | Existing cli test patterns (httptest `httpd` smoke where already present) | `--render bogus` → exit 2 before any network I/O; help text unchanged | US-1 |
| MCP tools | Existing `fakeExtractor` (`mcp/server_test.go:24`) + existing fake fetchers | Same validation errors as CLI path, same strings | US-2 |
| `crawl.Run` phases | Existing crawl fakes (`crawl/crawl_test.go:41` fakeExtractor, httptest servers) — **zero new tests for moves** | Pass-set identity: pre/post `go test ./crawl/ -v` diff shows same test names, same outcomes | US-3 |
| `crawlContext` (new struct) | Existing heal tests exercise it via moves; no dedicated tests | Existing `healField` tests green unchanged | US-3 |
| `core/registry.go` doc | Not testable — comment-only change | None (S2 is verified by review, not tests) | — |

## 2. Test Tier Table

| Tier | Dependencies | Speed | When to Run |
|------|-------------|-------|-------------|
| Unit (table-driven) | None — pure funcs + existing in-package fakes | <5s | Every commit |
| Integration (existing suites) | httptest servers, sqlite temp files | ~30s | Every commit (`go test ./...`) |
| Browser-gated | Chrome via rod (`//go:build browser`) | minutes | Before S3 merge only — proves the crawl split didn't disturb escalation paths; pin `render:static` per the Phase-A lesson |

## 3. Fake / Mock Implementations

**None new. Reuse verbatim (read first, never modify):**
- `mcp/server_test.go:24` `fakeExtractor` — MCP validation-error parity cases
- `crawl/crawl_test.go:41` `fakeExtractor` — crawl pass-set identity runs
- `scrape` tests' existing static fetcher fake — ValidateOptions needs no fetch at all
- `vertical/vertical_test.go:20` `fakeVerticalFetcher` — only if vertical validation cases move

Rationale: this phase adds no new seams, so inventing a second fake of anything would violate the repo's duplication rule and give zero additional coverage.

## 4. Test File List

```
scrape/validate_test.go        NEW   Table-driven ValidateOptions: render(4 cases), pageFormat(5),
                                     browser(valid+1 invalid), vertical("", auto, known, unknown),
                                     nil-DB error; EXACT error strings (characterization).
cli/exitfor_test.go            NEW   Table: typed error → exit code (2/7/8, default 1).
cli/scrape_test.go             EDIT  Replace inline switch expectations with ValidateOptions-backed
                                     expectations; keep one end-to-end exit-2 smoke.
cli/crawl_test.go              EDIT  Same consolidation; assert `crawl: --schema is required` etc.
                                     still exit 2 via exitFor.
mcp/tools_test.go              EDIT  Validation cases now assert unified strings (see KD-2).
crawl/*_test.go                UNTOUCHED  Moves-only proof: pass-set identity (capture procedure, §6 KD-1).
core/                          NONE  S2 is a comment; nothing to test.
```

## 5. Test Helper Structure (Go — no `conftest.py`)

No shared fixtures needed. If `exitfor_test.go` and `validate_test.go` both need the pinned string set, declare it once in `scrape/validate_test.go` as exported-to-package `var wantErrs = map[string]string{...}` and have the CLI tests reference the *scenarios*, not the strings (CLI asserts exit codes, not message text — message text is owned by scrape's own tests).

## 6. Key Testing Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| KD-1 | **Capture before, diff after:** run `go test ./crawl/ -v > /tmp/before.txt` (and `./...`) before each step; after the moves commit, diff test-name+outcome lines only (strip timings). Must be identical. | For moves-only refactors, pass-set identity is a stronger and cheaper guarantee than any new test |
| KD-2 | **One intentional text change, declared:** CLI validation messages currently lack the `scrape:` prefix and say `page-format` vs scrape's `page format`. S1 unifies on scrape's strings (the trust boundary owns the message). Exit codes do NOT change. Update the 3 CLI/MCP test expectations in the same commit; call this out in the commit message. | Honest behavior-change ledger; everything else is pinned |
| KD-3 | Assert exact error strings, not `strings.Contains` — these strings cross the MCP boundary to agents and the exit-map boundary to CLI; drift in either direction is a regression | Error text is API surface in this codebase |
| KD-4 | Structural claims (single switch site) are enforced by grep in Definition-of-Done, not as Go tests | Greps are CI-cheap and can't rot the way a meta-test can |
| KD-5 | No new test deps; no testify; no new fixtures in `testdata/` | Repo rules; refactor must not touch goldens at all |

## 7. Example Test Case

`scrape/validate_test.go` (strings must be re-captured from `scrape.go` at implementation time — these are the current pre-I/O checks):

```go
package scrape

import "testing"

// Characterization: these strings are API surface (CLI exit map + MCP clients).
// Captured from Run()'s pre-I/O validation on 2026-09-17. Change only with a
// declared message-text change (KD-2).
func TestValidateOptions(t *testing.T) {
	valid := Options{DB: nil} // ValidateOptions does not touch DB; Run still checks nil DB
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string // "" = must pass
	}{
		{"defaults pass", func(o *Options) {}, ""},
		{"render auto", func(o *Options) { o.Render = "auto" }, ""},
		{"render browser", func(o *Options) { o.Render = "browser" }, ""},
		{"render bogus", func(o *Options) { o.Render = "nope" },
			`scrape: render "nope" must be auto|static|browser`},
		{"format llm", func(o *Options) { o.PageFormat = "llm" }, ""},
		{"format bogus", func(o *Options) { o.PageFormat = "xml" },
			`scrape: page format "xml" must be markdown|llm|text|json`},
		{"browser firefox", func(o *Options) { o.Browser = "firefox" }, ""},
		{"browser bogus", func(o *Options) { o.Browser = "safari" },
			`scrape: browser "safari" must be chrome|firefox|random`},
		{"vertical off", func(o *Options) {}, ""},
		{"vertical auto", func(o *Options) { o.Vertical = "auto" }, ""},
		{"vertical bogus", func(o *Options) { o.Vertical = "nope" },
			`scrape: vertical "nope" unknown (see ` + "`magpie vertical --list`" + `)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := valid
			tt.mutate(&o)
			err := ValidateOptions(o)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("want %q, got %v", tt.wantErr, err)
			}
		})
	}
}
```

## 8. Execution Prompt

```
You are implementing the test + refactor steps of plan/clean-codebase-spec.md (rev 2)
in the gomagpie repo (Go 1.26 CLI web scraper; module `gomagpie`; stdlib tests only).
Read AGENTS.md, plan/clean-codebase-spec.md, and this file first. stack: go — do NOT
use run-phase; execute steps manually.

ACCEPTANCE CRITERIA (from User Stories):
US-1 exit codes 2/7/8 unchanged after consolidation (cli/exitfor_test.go + smoke).
US-2 MCP validation errors identical strings (mcp/tools_test.go updated expectations).
US-3 crawl pass-set identity: diff of `go test ./crawl/ -v` test-name+outcome lines
     before vs after the moves commit is EMPTY; testdata drift EMPTY.
US-4 grep "must be auto|static|browser" in non-test source returns exactly 1 hit (scrape pkg).

WHY NO NEW FAKES: this refactor adds no seams. Existing fakes (mcp/server_test.go:24,
crawl/crawl_test.go:41) already cover every path; a second fake is duplication.

ORDER (each step = separate commit, green before commit):
0. Capture: `go test ./... -v > /tmp/before-full.txt` and per-package for crawl.
1. S1a: add scrape.ValidateOptions (extract switches verbatim from Run; Run calls it).
   Add scrape/validate_test.go (characterization — re-capture exact strings from source).
2. S1b: cli: add exitFor in cli/root.go consolidating shared.go:137 mapping + the
   fail(2/7,...) validation calls; update cli tests. THE ONE declared text change:
   CLI messages unify to scrape's strings (KD-2). Exit codes unchanged.
3. S2: core/registry.go doc comment only (capability-interface upgrade path).
4. S3: crawl moves-only split (crawlContext struct; named phases in Run). Zero logic
   edits. Diff /tmp/before crawl capture — must be identical.
5. Gates: go build ./... && go test ./... && go vet ./... && gofmt -l . (empty) &&
   golangci-lint run ./... ; then go test -tags browser ./fetch/ ./crawl/ (pin
   render:static fixtures — Phase-A lesson: auto-render escalates to real rod).

WHAT NOT TO TEST:
- Do not add tests for core/registry.go (comment-only).
- Do not touch testdata/ goldens or add fixtures.
- Do not test crawl's internal phase functions directly (moves-only: existing suite is the contract).
- Do not add testify, new deps, or new fake types.
- Do not "improve" error messages beyond KD-2's declared unification.

SUCCESS: all five gates green; US-1..US-4 checks pass; 3 commits (S1, S2+S3 may merge
only if both are trivially small — prefer 3).
```

## 9. Run Commands

```bash
# Fast unit + integration (every commit)
go test ./...
go vet ./... && gofmt -l . && golangci-lint run ./....

# Focused
go test ./scrape/ -run TestValidateOptions -v
go test ./cli/ -run TestExitFor -v

# Crawl pass-set identity (KD-1)
go test ./crawl/ -v > /tmp/after.txt
diff <(grep -E "^(=== RUN|--- (PASS|FAIL))" /tmp/before-crawl.txt) \
     <(grep -E "^(=== RUN|--- (PASS|FAIL))" /tmp/after.txt)   # must be empty

# Browser-gated (before S3 merge only)
go test -tags browser ./fetch/ ./crawl/
```
