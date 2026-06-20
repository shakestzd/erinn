---
name: learning-bug-3e20ffcf
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/family_attribution_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/current_session_resumable_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/family_attribution.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/current_session_resumable.go
verified_at: 433ca649d7fb6a0fc0d436326bb619d5377ebdbe
links:
    - bug-3e20ffcf
created_by: wipnote-completion
created_at: 2026-06-19T06:35:30.035213981Z
updated_at: 2026-06-19T06:35:30.035213981Z
---

Attribution lives in TWO stores with a precedence rule: active_work_items (root agent) wins over sessions.active_feature_id. Any new query or write touching attribution must honor BOTH the agent scoping (__root__) and that precedence — a session_id-only awi join duplicates multi-agent sessions, and a write guarded only on active_feature_id can shadow a concurrent awi claim. Mirror GetActiveWorkItemWithFallback semantics everywhere.
