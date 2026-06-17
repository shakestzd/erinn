---
name: learning-feat-e3005c7f
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/core/hooks/session_end_gc.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/cmd/wipnote/sessions_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/core/hooks/session_end.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/cmd/wipnote/config.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/core/worktree/cleanup.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/cmd/wipnote/serve.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/cmd/wipnote/api_sessions.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/cmd/wipnote/main.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/cmd/wipnote/sessions.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/cmd/wipnote/config_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/core/hooks/session_end_gc_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/core/db/session_repo.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a10a3f0d5032e285e/core/worktree/cleanup_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/.claude/worktrees/agent-a9f5a4c973933e188/core/db/resumable_session_test.go
verified_at: 64de1107b4a409b201de6c9e2da21a8778ad4461
links:
    - feat-e3005c7f
created_by: wipnote-completion
created_at: 2026-06-17T04:13:20.79135317Z
updated_at: 2026-06-17T04:13:20.79135317Z
---

Legacy sessions need a dedicated handoff-field migration because fresh DB schema already includes those SessionEnd columns.
