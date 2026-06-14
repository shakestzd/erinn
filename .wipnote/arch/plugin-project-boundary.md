---
name: plugin-project-boundary
kind: invariant
paths:
    - plugin/**
    - packages/plugin-core/**
verified_at: ""
links:
    - feat-9e03e3f7
created_by: claude-code/feat-9e03e3f7
created_at: 2026-06-13T10:00:49.458648056Z
updated_at: 2026-06-13T10:00:49.458648056Z
---

wipnote is a plugin installed across many projects; it must never author, generate, or overwrite a project own instruction files (AGENTS.md, CLAUDE.md, GEMINI.md). Those are user-owned, repo-committed, and describe the project — they configure nothing for the plugin. wipnote agents READ and respect whatever project-instruction file the host harness exposes, and at most OFFER an opt-in, user-reviewed snippet (init-style); never silent ownership. Cross-harness portability of wipnote BEHAVIOR comes from the single-source manifest to generated per-harness trees, NOT from AGENTS.md. Portability of PROJECT intent is the AGENTS.md ecosystem standard job. Corollary already in CLAUDE.md: never land fixes in AGENTS.md/CLAUDE.md.
