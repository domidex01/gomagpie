# Project memory

- User's long-term plan: eventually wrap magpie in a Wails v3 desktop app (new cmd/magpie-desktop binary, per-OS CI builds, Wails is the sanctioned CGO exception) and monetize as BYOK subscription (~$15-25/mo or $149-199/yr, lifetime early-bird) via merchant-of-record (Paddle/LS/Polar) with offline Ed25519 license keys; CLI stays OSS, GUI is the paid product. Positioning: no credits, local-first, zero-LLM verticals as default, MCP-server-as-agent-tool angle. Implication: keep scrape.Deps-style seams and core packages free of terminal coupling so the GUI port stays thin.
