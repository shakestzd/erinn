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
updated_at: 2026-06-20T09:59:37.701944031Z
---

checkYoloWorkItemGuard (core/hooks/yolo_guard.go) blocks subagent Edit/Write unless THAT subagent's session row has active_feature_id; in nested sessions 'wipnote bug start' writes to a stale/dead session row so fresh subagents get false-blocked. The hasAnyActiveWorkItem fallback that prevented this is now unused (dead code) — likely a regression.
