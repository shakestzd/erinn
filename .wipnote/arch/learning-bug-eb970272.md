---
name: learning-bug-eb970272
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/claude_chooser_current_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/current_session_resumable_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/hooks/session_start.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/claude_chooser.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/claude_chooser_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/current_session_resumable.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/session_repo.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/family_attribution.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/family_attribution_test.go
verified_at: a1476bf6e73dabe36e3773cd291eddf32043b529
links:
    - bug-eb970272
created_by: wipnote-completion
created_at: 2026-06-19T06:09:58.22182637Z
updated_at: 2026-06-19T06:11:48.863581934Z
---

Claude Code splits a logical session: SessionStart fires only on a short parent stub (which gets active_feature_id), while the long-running child session IDs (019ede…) never get a SessionStart the hook sees — so they carry no work-item attribution and are dropped by the resumable query's work_item_id <> '' gate (session_repo.go:705) before any other filter. Fix is twofold: (1) a current-session chooser slot resolved from harness/launcher env + session-family members that bypasses BOTH the work_item_id and item_status('done','completed') gates, and (2) PropagateFamilyAttribution wired into SessionStart copying the family's work item onto siblings lacking one. Lean on family membership (session_family_id / session-families.json) as the durable parent-child link — never assume the hook fires per child.
