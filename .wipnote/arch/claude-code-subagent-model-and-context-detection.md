---
name: claude-code-subagent-model-and-context-detection
kind: decision
paths:
    - port/pluginbuild/**
    - cmd/wipnote/statusline.go
verified_at: ""
links:
    - plan-6d49f2df
created_by: claude-code
created_at: 2026-06-17T04:21:01.641078798Z
updated_at: 2026-06-17T04:21:01.641078798Z
---

Doc-verified 2026-06-17. SUBAGENT MODEL: write model: into GENERATED agent frontmatter at 'wipnote plugin build-ports' time. sub-agents docs accept sonnet/opus/haiku/fable/full-ID/inherit. Resolution order: CLAUDE_CODE_SUBAGENT_MODEL env > Agent-tool param > frontmatter > parent. AVOID the env var for role routing (blunt global override; gh #9573 404 with plugin agents). WATCH gh #44385: frontmatter model may be ignored at runtime. CONTEXT SIZE: hook payloads (PostToolUse/PreCompact/PostCompact) carry NO token counts. Use a statusLine script reading context_window.used_percentage / exceeds_200k_tokens from statusLine JSON (statusline docs) - the only documented machine-readable live-context surface for plugins.
