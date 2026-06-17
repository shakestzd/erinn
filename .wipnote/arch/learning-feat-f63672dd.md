---
name: learning-feat-f63672dd
kind: decision
paths:
    - .wipnote/features/feat-f63672dd.html
    - cmd/wipnote/antigravity.go
    - cmd/wipnote/antigravity_launch_test.go
    - cmd/wipnote/claude.go
    - cmd/wipnote/claude_chooser.go
    - cmd/wipnote/claude_chooser_test.go
    - cmd/wipnote/codex.go
    - cmd/wipnote/codex_launch_test.go
    - cmd/wipnote/gemini.go
    - cmd/wipnote/gemini_test.go
verified_at: ec019430aad6a16f26b203ad3023e8ef92d8e82a
links:
    - feat-f63672dd
created_by: wipnote-completion
created_at: 2026-06-17T03:48:10.209560598Z
updated_at: 2026-06-17T03:48:10.209560598Z
---

Chooser-driven continue can share work-item/worktree context across launchers, but only Codex and Antigravity can safely reuse stored session IDs; Gemini must stay transcript-flag-neutral because it resumes by latest/index.
