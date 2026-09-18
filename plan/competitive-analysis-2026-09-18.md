# Competitive analysis: magpie vs Firecrawl vs webclaw

Date: 2026-09-18. Sources: our repo at HEAD, github.com/firecrawl/firecrawl (README + docs + repo tree), github.com/0xMassi/webclaw (README + `webclaw-fetch/src/proxy.rs`, `tls.rs`, `cloud.rs`).

## TL;DR

- We are **at or ahead of webclaw** on the core pipeline (cleaning quality, LLM layer, MCP surface, security, distribution). We are **behind both on proxy plumbing, browser/TLS profile breadth, search, and page actions**.
- Firecrawl's moat is *hosted infrastructure* (rotating proxies, async jobs, cache, live browser interact). Chasing that contradicts our local-first/BYOK positioning. The wins to take from them are cheap local features: output formats, actions DSL, geo/locale params, page cache.
- **Proxy verdict: do NOT build a proxy generator.** You cannot create public IPs locally; a "generator" means becoming a proxy provider (ops, cost, abuse liability). Build best-in-class BYO-proxy plumbing instead (~1 new file in `fetch/` + flag wiring), and make third-party pools painless. Optional cheap extra: Tor SOCKS5.

## 1. Three-way feature matrix

| Feature | magpie | webclaw | Firecrawl |
| :-- | :--: | :--: | :--: |
| Static fetch + JS-render escalation | ✅ (detect → rod) | ✅ (needs_js_rendering → cloud) | ✅ (hosted) |
| TLS impersonation | ✅ chrome/firefox/random | ✅✅ chrome/mac, firefox, safari, **safari-iOS-26**, edge, edge-macos (wreq/BoringSSL, ECH GREASE, ALPS, H2 SETTINGS order, verified vs DataDome/indeed WAFs) | ✅ (hosted stealth) |
| Bot-challenge detection (typed) | partial (empty-shell → rod) | ✅✅ public detectors: CF orchestrate/chl variants, `cf-mitigated`, Turnstile, DataDome, AWS WAF, hCaptcha, size-gated | ✅ (hosted) |
| Output formats | markdown, llm, text, json | markdown, llm, text, **json, html** | markdown, html, **rawHtml, screenshot, links, summary, json, attributes** |
| Selector scoping | ✅ include/exclude, only-main-content | ✅ same | ✅ includeTags/excludeTags |
| Structured extraction | ✅✅ 7 providers, schema+prompt, cost caps, exit 6 | ✅ local or hosted LLM (ollama/openai/anthropic/orcarouter) | ✅ /extract + prompt→schema |
| Zero-LLM vertical extractors | ~10 (arxiv, github, shopify, commerce, registries, social, youtube) | **30+** (amazon, ebay, etsy, woocommerce, trustpilot, stackoverflow, hackernews, reddit deep-comments, npm, pypi, dockerhub, huggingface×2, instagram×2, linkedin, substack, crates.io, dev.to, chronopost, og-generic…) | — (schema-based) |
| Crawl | ✅ BFS, scope, robots, bloom, sitemap, exporter | ✅ depth/max-pages | ✅✅ async jobs, webhooks, polling, WS, concurrency |
| Map | ✅ sitemap+BFS, truncation flag | ✅ | ✅ |
| Batch | ✅ ≤100, jsonl | ✅ parallel | ✅✅ async thousands |
| Search (web → scrape results) | ❌ | ✅ (hosted, SerpApi) | ✅✅ first-class endpoint |
| Diff / change tracking | ✅ word-diff vs file | ✅ | ✅ server-side changeTracking w/ history |
| Watch/monitoring (scheduled) | ❌ | ✅ (hosted watches) | — |
| Brand extraction | ✅ | ✅ | ✅ |
| PDF | ✅ paged markdown + quality gate | ✅ | ✅ + DOCX/XLSX/ODT/RTF + multipart /parse |
| Screenshots | ❌ | ❌ | ✅ full-page + actions |
| Browser actions (click/type/scroll/wait/JS) | ❌ (only challenge warmup + cookies) | ❌ | ✅✅ actions array + interact (live session, AI-prompt steered) |
| Location/geo targeting | ❌ | ❌ | ✅ country + languages |
| Mobile emulation | ❌ | ✅ (iOS profile) | ✅ |
| Page cache (maxAge) | ❌ (selector cache only) | ✅ cloud cache | ✅✅ maxAge/minAge, storeInCache |
| MCP server | ✅✅ 11 tools, stdio+HTTP | ✅ (npx, 9 tools) | ✅ |
| REST API server (self-host) | ❌ (MCP-HTTP only) | ✅ | ✅ (hosted or self-host k8s) |
| SDKs | ❌ | ✅ TS/Py/Go | ✅✅ 9 languages |
| Agent / deep research | ❌ | research (hosted) | ✅✅ Agent, deep-research |
| Plugins/sandbox | ✅✅ WASM wazero, custom builds | ❌ | ❌ |
| Selector cache w/ self-heal | ✅✅ unique | ❌ | ❌ |
| Cost governance (max-cost, exit 6) | ✅✅ unique | ❌ | credits (their model) |
| SSRF guard | ✅✅ default-deny, rebind-safe, peer-IP recheck | ✅ blocked-IP resolver + redirect revalidation | n/a hosted |
| Distribution | ✅✅ single pure-Go static binary, 0 CGO | Rust, needs C++/LLVM/NASM (BoringSSL) to build | self-host = redis+postgres+playwright+rust fleet |
| Proxy support | **single** GOMAGPIE_PROXY URL | **pool**: WEBCLAW_PROXY + WEBCLAW_PROXY_FILE (`host:port:user:pass`), per-request clients, credential redaction in logs | hosted rotating ("zero configuration"), `proxy: auto/basic/stealth` |
| Telemetry | run_history (pages/bytes/ms/tokens/cost) | usage tracking (hosted) | credit metering |

## 2. Gap list — what to build to be the better tool

### P0 — cheap, closes visible parity gaps

1. **Proxy pool + rotation** (see §3 for design). This is the single biggest "why can't it do that" gap vs webclaw.
2. **Typed bot-challenge detection.** Port webclaw's public, size-gated signatures (`cf-mitigated` header, `_cf_chl_opt` + `challenge-platform/h/{b,g}/orchestrate/`, Turnstile-on-short-page, DataDome, AWS WAF interstitial, hCaptcha-blocking) into `fetch/` as a `ChallengeError`. Today we only catch empty shells; a 200 challenge page currently flows into cleaning and produces garbage or a false quality-block. We already have the rod escalation + warmup — the detector just makes it fire on the right inputs.
3. **More TLS/browser profiles.** We have chrome/firefox/random. Add safari, safari-ios (mobile profiles are what pass DataDome per webclaw's notes), edge, chrome-macos. Same impersonate-http dep if it carries the profiles; else utls specs. Low risk, imported only in `fetch/`.
4. **Missing output formats.** `html` (cleaned) and raw passthrough are trivial; `screenshot` via rod is ~a day (full-page + viewport flag, saved to `--out` path or base64 in JSON). Firecrawl's `summary` we already have as a command; `links` we already emit.
5. **`search` command with pluggable BYOK backends.** Brave Search API, Serper, SerpApi, SearXNG (local!), https://exa.ai/, DuckDuckGo-lite. Pipeline: query → SERP → (optionally) scrape top N through our normal pipeline → one JSONL record per result. No hosted dependency, fits BYOK; SearXNG gives a zero-key path.

### P1 — bigger swings, real differentiation

6. **Actions DSL before extraction.** A `--actions` step file or inline spec: `click sel`, `type sel text`, `scroll`, `wait ms`, `wait-for sel`, `screenshot path`, `eval-js expr`. We already run rod; this is surface, not engine work. Unlocks paginated lists, cookie-consent dismissal, "load more" — the top reason people reach for Firecrawl.
7. **More verticals from webclaw's proven list.** Highest value for our zero-LLM story: reddit (incl. deep comment trees — they ship HTML fixtures), hackernews, stackoverflow, trustpilot reviews, npm/pypi/dockerhub/huggingface (extend `registries.go`), generic OG/meta as an always-on fallback extractor. Each is JSON-LD/DOM-regex against fixtures — our vertical framework already has the permissive/OptIn pattern.
8. **`watch` command.** `magpie watch <url> --every 1h --webhook …` — store snapshot in SQLite (we have diff + run_history already), emit on change. Matches the competitor-monitoring use case webclaw monetizes and our repricing root use-case.
9. **Async crawl ergonomics.** We're synchronous by design (good for CLI/MCP). Add: resumable runs (visited table exists), NDJSON webhook/exporter (exporter-cmd exists — maybe enough), and a `crawl --status run_id` resume. Don't build a job queue.
10. **Locale/geo knobs.** `--country` / `--lang` mapping to Accept-Language + header-profile selection. Cheap; matters for the Amazon-style verticals.

### P2 — explicitly not chasing (hosted-service parity)

- Credit billing, hosted stealth proxy fleet, live-view interact sessions, Agent/deep-research autonomous endpoints, 9 SDKs, k8s self-host. These are Firecrawl's *business*, not our features. Our monetization plan (Wails GUI + BYOK) actively conflicts with them. Revisit only if the GUI needs them.
- Office formats (DOCX/XLSX): wait for a real user ask (YAGNI).

## 3. Proxies: generate our own, or third-party?

**Recommendation: third-party pools, first-class client support. Do not build a proxy generator.**

Why not a generator:

1. **It's not possible locally.** A proxy is a public IP elsewhere. You cannot mint public IPs on a user's machine. Anything that "generates" egress IPs = renting cloud VMs/serverless per-IP (AWS/GCP), which is: an ops product, a billing product, an abuse/liability magnet (most providers ban scraping egress), and trivially detectable (datacenter ASN ranges that anti-bot vendors score). That's a company, not a feature.
2. **The market already solved it.** Webclaw doesn't run proxies either — they take *sponsorships* from proxy vendors (ColdProxy, NodeMaven, Mango, Thordata) and ship pool-file support. Firecrawl sells managed proxies as hosted margin. Both prove the tool-side job is *client plumbing*.
3. **Our positioning is local-first/BYOK.** BYO proxy keys fit BYO LLM keys perfectly in the GUI story.

What to build instead (small, in priority order):

- **`GOMAGPIE_PROXY_FILE`** (and `--proxy-file`): one proxy URL per line (`http://user:pass@host:port`, `socks5://…`, `host:port:user:pass` accepted like webclaw for vendor-paste compatibility), `#` comments. Parse once, fail loudly if empty/invalid (we already fail loudly on bad `GOMAGPIE_PROXY`).
- **Rotation strategies:** `round-robin` (default) and `sticky-host` (same proxy per target host for a run — crawl-friendly, cookie-jar coherent). Per-request proxy resolution in `proxyForHost`, so static/robots/crawl/utls all inherit it (they already funnel through one place — this is why we win this refactor cheaply).
- **Health + failover:** on proxy connect error, mark cooldown (e.g. 60s), try next; surface `proxy=host:port` (redacted) in run records. Credential redaction in logs/errors is table stakes — webclaw got burned on this and has tests; copy the behavior (strip userinfo, never log it).
- **Sticky-session URL templating** for rotating-gateway vendors: `{{session}}` placeholder in the proxy URL replaced per-host (e.g. `http://cust-{{session}}-cc-us:pw@gw.vendor:7000`) — one line of templating that makes every residential gateway behave sticky.
- **Tor as a pool entry:** `socks5://127.0.0.1:9050` should "just work" (SOCKS5 support is the same code path); optional control-port circuit rotation is a nice-to-have. Caveat loudly in docs: Tor exits are widely blocked; it's a privacy path, not an unblocking path.

Explicitly rejected:

- Spawning local forward proxies as "our proxies" — pointless for unblocking (same IP), only useful someday for GUI header-injection/caching; skip until the Wails GUI asks.
- Bundled proxy-provider accounts/keys — supply chain + billing + abuse liability; never.

## 4. What we already win on (keep, market it)

- Single pure-Go static binary vs webclaw's BoringSSL-heavy Rust build (LLVM/NASM/CMake prereqs) and Firecrawl's k8s self-host.
- WASM plugin sandbox + `magpie build --with` custom binaries — neither has an equivalent.
- Selector cache with null-rate self-healing — unique.
- Cost governance: `--max-cost`, exit 6, provider classes (subscription/flat/credits) — unique.
- Quality gate as a contract (exit 8) — neither has an equivalent.
- Stronger SSRF story than both (default-deny + redirect-hop + connected-peer recheck).
- MCP-first with 11 tools; webclaw has 9, Firecrawl's is an npx wrapper around a hosted key.
