---
name: web-search-mandated
kind: decision
paths:
    - plugin/skills/**
    - plugin/agents/**
    - cmd/wipnote/prompts/**
verified_at: 8106ffacf
links:
    - feat-a7b41559
created_by: claude-code/feat-a7b41559
created_at: 2026-06-12T19:41:44.009599556Z
updated_at: 2026-06-12T19:41:44.009599556Z
---

Web search is mandated, not opportunistic (feat-a7b41559): planning skills require pre-slice web research — latest docs/standards, existing OSS packages before custom builds, harness provider plugin/skill/hook ecosystems. FEASIBILITY CRITIC must web-verify external library/API claims and flag reinvented wheels. feature-coder and architect-coder carry WebSearch/WebFetch (auto-translated for Gemini; Codex TOML has no tools field, so Codex relies on prompt guidance). Keep guidance harness-neutral — tool names differ per harness.
