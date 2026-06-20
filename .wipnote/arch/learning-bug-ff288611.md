---
name: learning-bug-ff288611
kind: hazard
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/hooks/harness_antigravity_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/hooks/harness.go
verified_at: ee6abd4cd4be8dd802c032f10464043912520283
links:
    - bug-ff288611
created_by: wipnote-completion
created_at: 2026-06-16T20:27:55.63064032Z
updated_at: 2026-06-16T20:27:55.63064032Z
---

agy PreToolHookResult.allowTool defaults false: emitting {} for PreToolUse DENIES all tools. Must emit {allowTool:true} to allow, {allowTool:false,denyReason} to block. Only PreToolUse is gated this way; PostToolUse/PostInvocation/Stop accept {}. Verified via live agy ('Tool call denied by jsonhook__wipnote_PreToolUse_0_0').
