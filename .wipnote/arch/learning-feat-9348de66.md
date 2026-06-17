---
name: learning-feat-9348de66
kind: subsystem-map
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/db/migrations_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/db/session_exec_context_migration_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/hooks/session_start_exec_context_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/cmd/wipnote/claude.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/hooks/session_start.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/db/session_repo.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/models/session.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/db/migrations.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a1d67c0d5e7e582b5/core/db/schema.go
verified_at: f77b0792ecdffac34c9dbc6ad8efe742af3888eb
links:
    - feat-9348de66
created_by: wipnote-completion
created_at: 2026-06-16T23:47:55.469696431Z
updated_at: 2026-06-16T23:47:55.469696431Z
---

sessions read-index has TWO independent harness-bearing columns: gate_records.harness (per-gate-record, migration step 6) and sessions.harness (per-session, migration step 014_session_exec_context, feat-9348de66). They live on different tables and are NOT duplicates. sessions.harness is written at SessionStart from WIPNOTE_HARNESS env, constrained to the closed enum {claude,codex,gemini,antigravity}; values outside the enum are stored empty. exec_worktree_path is repo-relative to the MAIN repo root (projectDir), never absolute; main-worktree launches store empty. project_dir stays canonical ('.'). Later slices source harness from WIPNOTE_HARNESS (set by each launcher), NOT from .launch-mode Mode (which holds yolo/dev/auto).
