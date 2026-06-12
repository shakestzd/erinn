---
name: learning-bug-5ff181f3
kind: decision
paths:
    - .wipnote/bugs/bug-5ff181f3.html
    - plugin/skills/orchestrator-directives-skill/SKILL.md
    - cmd/wipnote/prompts/system-prompt.md
verified_at: ac57e33d16003f044e288eaa3486a202b503e637
links:
    - bug-5ff181f3
created_by: wipnote-completion
created_at: 2026-06-12T16:07:38.024854355Z
updated_at: 2026-06-12T17:48:32.37799699Z
---

No runtime detection of bwrap/sandbox failures exists — bwrap errors from nested codex exec in devcontainers are permanent env incompatibility, not transient; guidance now says skip codex exec for the session and use in-harness agents
