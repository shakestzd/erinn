---
name: learning-bug-8251b639
kind: decision
paths:
    - cmd/wipnote/claude_dev_test.go
    - cmd/wipnote/claude_chooser.go
    - cmd/wipnote/dev.go
    - cmd/wipnote/claude.go
    - cmd/wipnote/claude_launch_intent.go
    - cmd/wipnote/serve.go
verified_at: 0a8d1a0d2e4ed51d6218cf9e9e31948c20e33369
links:
    - bug-8251b639
created_by: wipnote-completion
created_at: 2026-06-17T12:25:39.573529422Z
updated_at: 2026-06-17T12:25:39.573529422Z
---

Intentionality/chooser gates must be wired per launch path, not assumed global: wipnote claude --dev short-circuited the RunE switch to launchClaudeDev BEFORE the default arm, so it silently skipped resolveLaunchIntentForDefaultLaunch. Any new launch mode (dev/yolo/codex/gemini/antigravity) needs explicit intent-resolution wiring or it bypasses plan-311440bf's separation of planning vs code-change sessions.
