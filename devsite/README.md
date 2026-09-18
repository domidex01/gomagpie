# devsite — offline playground

Static pages for exercising magpie end-to-end without network. The production
binary's SSRF guard blocks 127.0.0.1 by design, so drive it via `file://` with
the spec-sanctioned hatch (`MAGPIE_ALLOW_FILE=1`, also set in the project
`.mcp.json` for the MCP server):

```sh
export MAGPIE_ALLOW_FILE=1
./magpie scrape  file://$PWD/devsite/article.html
./magpie brand   file://$PWD/devsite/article.html
./magpie crawl   file://$PWD/devsite/index.html --corpus --max-pages 6
./magpie extract devsite/products.html --schema products.schema.json  # needs LLM key
```

Pages: index (hub/crawl seed), article (clean/metadata/brand), products
(selector/extract), nested/deep (crawl depth). Known quirk: absolute `/href`
paths resolve against the filesystem root under `file://` — always use
relative links here.
