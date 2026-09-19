# devsite — offline playground

Static pages for exercising magpie end-to-end without network. The production
binary's SSRF guard blocks loopback/private targets by design; `file://` URLs
work when `MAGPIE_ALLOW_FILE=1` (implemented in `fetch`, documented in the
scrape/crawl MCP tool descriptions; the local `.mcp.json` sets it for the MCP
server):

```sh
export MAGPIE_ALLOW_FILE=1
./magpie scrape  file://$PWD/devsite/article.html
./magpie brand   file://$PWD/devsite/article.html
./magpie crawl   file://$PWD/devsite/index.html --corpus --max-pages 6
./magpie extract devsite/products.html --schema products.schema.json  # needs LLM key
```

Pages: index (hub/crawl seed), article (clean/metadata/brand), products
(selector/extract), nested/deep (crawl depth). Schemas follow the house
convention — flat top-level fields with `x-magpie` hints (see
`testdata/extract/price.yaml`); extract returns one record per page, so
`products.html` demonstrates first-product extraction. Known quirk: absolute
`/href` paths resolve against the filesystem root under `file://` — always use
relative links here.
