---
name: durable-architecture-bets-2026-06-13
kind: decision
paths:
    - core/**
    - plugin/**
verified_at: ""
links:
    - feat-9e03e3f7
created_by: claude-code/feat-9e03e3f7
created_at: 2026-06-13T10:00:49.499442832Z
updated_at: 2026-06-13T10:00:49.499442832Z
---

Durable design validated by research (local-first; event-sourcing/CQRS; SQLite single-writer). KEEP: HTML+NDJSON canonical store, SQLite disposable projection, git as durability/sync, daemon single-writer. Reinforce: merge=union on NDJSON logs, idempotent reindex, fsck integrity check. AVOID (single-machine YAGNI): CRDTs, sync engines, event-store servers. Cross-harness portability = generated per-harness plugin trees plus harness-neutral session/lineage model (the moat), NOT AGENTS.md. MCP = external interop only with deferred loading. Capability delivery tiers: see capability-delivery-tiers card. Boundary: see plugin-project-boundary card. Align-later (do not block): OTel GenAI / OpenInference export. External deadline: Gemini CLI to Antigravity CLI 2026-06-18 (verify port mechanics first). Supersedes the looser bet-on-AGENTS.md-and-MCP framing.
