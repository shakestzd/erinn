---
name: learning-bug-b262d303
kind: decision
paths:
    - cmd/wipnote/launcher_continue_test.go
    - cmd/wipnote/launcher_continue.go
    - cmd/wipnote/claude_env_test.go
    - core/hooks/session_start.go
    - cmd/wipnote/claude_env.go
verified_at: e3c68699ccd28016641be100720761932901cb86
links:
    - bug-b262d303
created_by: wipnote-completion
created_at: 2026-06-20T09:09:38.598841592Z
updated_at: 2026-06-20T09:09:38.598841592Z
---

OTel collector session ID must travel under WIPNOTE_OTEL_SESSION_ID, never WIPNOTE_SESSION_ID — the latter is wipnote canonical current-session identity used for work-item attribution; conflating them made claude --resume receive a phantom 28-char hex ID with no Claude transcript.
