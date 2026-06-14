---
name: capability-delivery-tiers
kind: decision
paths:
    - plugin/**
    - cmd/wipnote/prompts/**
verified_at: ""
links:
    - feat-9e03e3f7
created_by: claude-code/feat-9e03e3f7
created_at: 2026-06-13T10:00:49.417894104Z
updated_at: 2026-06-13T10:00:49.417894104Z
---

When delivering a wipnote capability, pick the cheapest context tier that works: CLI-via-Bash (near-zero resident cost) > Skill (progressive disclosure, body loads on invoke) > deferred MCP tool (names only until used) > eager MCP tool (full schema always resident; avoid). Never expose wipnote own command surface as eager MCP tools — that recreates MCP context bloat (a 3-server/40-tool setup burned ~72 percent of context on schemas before the first prompt). MCP is for external user-chosen services only, with deferred/tool-search loading. The CLI tier is also the most cross-harness-portable: deferred MCP loading is default on Claude Code 2.1.x but unconfirmed on Codex/Antigravity. Verified Feb 2026 (Anthropic Tool Search / defer_loading).
