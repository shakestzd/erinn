---
name: learning-feat-71704981
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a95af5b4e95a03287/cmd/wipnote/claude_chooser_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a95af5b4e95a03287/cmd/wipnote/claude_chooser.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a77df1828473e9b8c/cmd/wipnote/launcher_continue.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a77df1828473e9b8c/cmd/wipnote/launcher_mode.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a77df1828473e9b8c/cmd/wipnote/claude.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a77df1828473e9b8c/cmd/wipnote/launchtui/banner.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/.claude/worktrees/agent-a77df1828473e9b8c/cmd/wipnote/launchtui/banner_test.go
verified_at: 432515b2db2c0644a17034a88ed7f633027ba355
links:
    - feat-71704981
created_by: wipnote-completion
created_at: 2026-06-19T02:49:23.043377296Z
updated_at: 2026-06-19T02:49:23.043377296Z
---

RenderLaunchBanner(r *lipgloss.Renderer, in BannerInput) string — use MakeRendererForProfile(profile) + r.SetColorProfile(profile) for deterministic test profiles; NewRenderer alone does not set explicitColorProfile so EnvColorProfile() ignores WithProfile.
