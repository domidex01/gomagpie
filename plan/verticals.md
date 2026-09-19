# Vertical extractor proposals — next phases (2026-09-19)

Grounded in the 15 existing extractors (arxiv, shopify_product, ecommerce_product,
github_repo, og, pypi, npm, crates_io, dockerhub, huggingface, reddit, hackernews,
stackoverflow, trustpilot, youtube). The pattern that wins in this codebase:
**ride standards, not sites** — `ecommerce_product` (JSON-LD) and `trustpilot`
(JSON-LD) each cover thousands of sites with one extractor.

## Tier 1 — highest leverage (each ≈ a day, all zero-key)

| Vertical | Source | Why it matters |
| :-- | :-- | :-- |
| `job_posting` | schema.org JobPosting JSON-LD — Greenhouse/Lever/Ashby career pages + job boards embed it | Hiring-market maps, job-search agents, lead-gen for recruiters. Biggest unclaimed demand. Clone of `ecommerce_product`. |
| `event` | schema.org Event JSON-LD — Eventbrite, Meetup, venue & ticket sites | "What's on in Bangkok this weekend" is a canonical agent query |
| `rss` | Atom/RSS via stdlib `encoding/xml` — the `arxiv` extractor already proves the pattern | One vertical = millions of blogs, podcasts, news feeds, changelogs. Fuel for `watch`/`diff` monitoring. |
| `osm` | Overpass API | "Every gym in a city with phone numbers" — the lead-gen killer demo. Only wrinkle: input is a query+bbox, not a URL. |
| `local_business` | schema.org LocalBusiness JSON-LD on company homepages | Completes the stack: OSM finds businesses → their sites yield hours/phone/founded. |

## Tier 2 — solid second wave

- **`article`** — NewsArticle JSON-LD normalized (author, published/updated): news & PR monitoring. `og` half-covers it; this types it.
- **`wikidata`** — no-key REST: company enrichment (HQ, founders, subsidiaries) for B2B lists.
- **`sec_edgar`** — free US filings API, UA-required like `crates_io`: finance/regulatory monitoring.
- **`wikipedia`** — no-key REST summaries: knowledge cards for agents.

## Skip for now

Recipes/real-estate (JSON-LD, easy but thin demand for this product), app stores
(brittle, ToS-gray), anything needing a key (violates the zero-credit positioning).

## Finance verticals (2026-09-19)

Strong family: the best sources are public infrastructure — regulators and
central banks publish this data by law.

### Tier A — zero-key, do first

| Vertical | Source | What you get |
| :-- | :-- | :-- |
| `sec_filing` | EDGAR submissions API (`data.sec.gov`, no key, UA required like `crates_io`) | Every US-listed company's filing history: 10-K, 10-Q, 8-K, S-1 — dates, links, types |
| `sec_financials` | EDGAR **XBRL companyfacts** API | The crown jewel: standardized revenue/net-income/assets/equity series for every US ticker, as-filed, free. One extractor = the entire US market's financials |
| `insider_13f` | EDGAR Forms 4 + 13F | Insider buys/sells; which funds hold what — "what are smart funds buying" reports, fintech lead-gen |
| `crypto_market` | CoinGecko public API / Kraken public endpoints (no key) | Price, market cap, volume, history for 10k+ coins |
| `fx_rates` | Frankfurter/ECB (no key) | Daily reference rates, 30+ currencies, history |
| `macro_indicators` | World Bank API (no key) | GDP, CPI, unemployment, population — every country, every year |

### Tier B — free-key (BYOK fits, breaks zero-key purity)

- **`fred_series`** — FRED (free key): 800k macro series, the gold standard for US economic data
- **`companies_house`** — UK registry (free key): UK company officer/filing data, B2B enrichment
- **`stock_prices`** — Alpha Vantage/Twelve Data (free keys): OHLCV history when Stooq's EOD CSV isn't enough

### Skip (gray or hostile)

Yahoo Finance (unofficial API, crumb-walled, blocks bots), SeekingAlpha
transcripts, TradingView. Paywalled data is paywalled for a reason.

### Killer use cases

1. **Filing monitors** — `magpie watch` + `sec_filing`: "alert me when anything
   in my watchlist files a 10-K" — exists as paid products today
2. **Research agents over MCP** — "pull ACME's revenue history, latest 10-K, and
   insider trades, then summarize" — filings + XBRL + LLM summarize in one pipeline
3. **Quant feeds** — `batch` over tickers → JSONL of financials/prices for
   backtesting, zero credits
4. **Fund-copying reports** — 13F deltas → "what did Renaissance buy this
   quarter" content

**Finance pick:** `sec_filing` + `sec_financials` as one phase (same API family,
one auth pattern; companyfacts alone is a wow-demo: "every US company's full
financials, no key, one command"). Crypto+FX as the follow-up phase.

## Market-analysis verticals (2026-09-19)

Theme: demand signals + competitor intelligence + historical context.

### Tier A — zero-key, high leverage

| Vertical | Source | Market-analysis angle |
| :-- | :-- | :-- |
| `wayback_cdx` | Wayback Machine CDX API (free, official) | Competitor history without having watched it: every snapshot of a rival's pricing page since 2015 → price evolution, repositioning. Pairs with `diff` |
| `wikimedia_pageviews` | Wikimedia REST API (free, official) | Demand proxy: interest in a product/topic/company over time |
| `app_store` | iTunes Search/Lookup API (free, official, no key) | App market maps: ratings, rankings, version cadence per category. (Google Play side is scraping-gray — skip) |
| `tech_stack` | Native — detectable from magpie's own fetch (generator meta, script srcs, headers) | "62% of these 100 competitor sites run Shopify" — build-vs-buy intel, zero external dependency. Pure differentiator |
| `sec_form_d` | EDGAR full-text (extends the finance family) | Every US startup funding round — venture radar, service-provider lead-gen |
| `gdelt` | GDELT Project API (free, no key) | Global news coverage analytics: who's getting written about, where |

### Tier B — real but heavier

- **`rdap`** — official free whois-replacement: domain portfolios, expiry, infra footprint
- **`gleif`** — free legal-entity registry: firmographic spine for B2B market maps
- **`comtrade`** — UN trade flows by commodity code: import/export volumes per category

### Skip (gray/paywalled)

Google Trends (unofficial endpoint, blocks), SimilarWeb/Crunchbase (paywalled),
Amazon reviews, Meta/Google ad libraries (gated or hostile).

### The combos that sell it

- **Competitor teardown** = `tech_stack` + `wayback_cdx` + `ecommerce_product` +
  `trustpilot` → one report: what they run, how pricing evolved, what they
  charge, how customers complain
- **Startup radar** = `sec_form_d` + `job_posting` + `insider_13f` → who raised,
  who's hiring, what funds hold — a paid product on its own

## Public-sector, science & niche markets (2026-09-19)

Third theme batch — same filter: zero-key, structured, legal.

### Public-sector money & records (underrated)

| Vertical | Source | Angle |
| :-- | :-- | :-- |
| `usaspending` | USAspending.gov API (free, no key) | Every US federal contract/grant award — who wins public money. B2B lead-gen gold |
| `nonprofit_990` | ProPublica Nonprofit Explorer (free, no key) | Every US nonprofit's revenue/expenses/donors — prospect research, nonprofit market maps |
| `grants_tenders` | Grants.gov / SAM.gov / EU TED | Tenders & grants before competitors see them |

### Science & health

- **`openalex`** — free, no key: every scholarly paper/author/institution + citation networks; research-trend analysis. Pairs with `arxiv`
- **`clinical_trials`** — ClinicalTrials.gov API v2 (free): pharma pipeline by sponsor/condition/drug
- **`openfda`** — drug adverse events, recalls, approvals (free): pharma risk monitoring

### Infrastructure & mobility

- **`ev_charging`** — OpenChargeMap (free, no key): EV-charging market maps by region
- **`gtfs`** — public transit feeds (free, heavier): mobility-access scoring
- **`onchain`** — public blockchain JSON-RPC (zero-key by design): wallet/token flow analysis

### Meta / fun

- **`llm_catalog`** — OpenRouter/models.dev public JSON: every LLM + price. AI-market tracker inside a BYOK product
- **`steam`** — semi-official `appdetails` JSON: game market scans (gray-ish)
- **`courtlistener`** — free legal-docket API: litigation analytics

Skip: hotel/flight pricing (hostile), Spotify/Netflix/IMDb (keyed or no-API),
patent analytics (paywalled).

## Geo, registries, culture, compliance (2026-09-19, final sweep)

### Geo & demographics — completes the OSM story

| Vertical | Source | Angle |
| :-- | :-- | :-- |
| `census_demographics` | US Census Bureau APIs (free, no key) | The missing half of local market analysis: population/income/age per tract — OSM says where the gyms are, Census says who lives there |
| `nominatim_geocode` | OSM Nominatim (free, 1 rps) | Utility vertical: normalize scraped CSV addresses → lat/lon |
| `geonames` | GeoNames (free, attribution) | Global place gazetteer for international lists |

### Dev-registry matrix completion (1-day clones each)

`nuget` · `packagist` · `rubygems` · `cran` — plus **`homebrew_analytics`**
(install counts = dev-tool adoption, official data) and **`jetbrains_plugins`**
(public JSON). Takes the registry family from 5 to 11.

### Culture & media (GLAM)

- **`archive_org`** — Internet Archive metadata API (free, no key): books/audio/video/software; public-domain discovery
- **`met_museum`** — Met open-access API (free, no key): 470k artworks with images — print-on-demand niche
- **`openlibrary`** + **`musicbrainz`** (free, UA-required) — publishing and music catalogs

### Consumer safety & food

- **`nhtsa_recalls`** — NHTSA API (free, no key): vehicle recalls/complaints — automotive quality signals
- **`cpsc_recalls`** — CPSC API (free): consumer-product recalls — brand-risk monitoring
- **`usda_food`** — FoodData Central + Farmers Market Directory (free): nutrition + local food markets

### Compliance

- **`open_sanctions`** — free sanctions/PEP database+API: name-screening is an
  entire paid-product category and the data is public

## Pick for the next verticals phase

**`job_posting` + `event` + `rss`** — all three are the proven JSON-LD/stdlib
patterns, no new architecture, roughly a day together. `osm` deserves its own
phase because of the query-input design question. Finance runs in parallel:
`sec_filing` + `sec_financials` first, crypto+FX after.

## Recommended build order (freeze — brainstorming closed 2026-09-19)

The doc crossed from roadmap to collection: ~50 candidates, every one a variant
of the two proven patterns (public JSON API / JSON-LD harvest). Marginal value
of more ideas ≈ 0; ship instead.

1. **Phase K** — `job_posting` + `event` + `rss` (1 day, proven patterns)
2. **Phase L** — `sec_filing` + `sec_financials` (the wow demo: every US
   company's financials, no key, one command)
3. **Phase M** — `osm` + `census_demographics` + `nominatim_geocode` — the
   complete local lead-gen product: who lives there, what businesses are there,
   where exactly they are
4. **After that** — everything else in this doc, driven by real user demand.
   If a user asks for a vertical that's not here, that beats ten ideas from us.

Strongest pulls if anything jumps the queue: `usaspending` and `openalex`
(least-known, most defensible); `tech_stack` (native differentiator, no
external dependency).
