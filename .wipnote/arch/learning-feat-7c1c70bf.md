---
name: learning-feat-7c1c70bf
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/launchtui/theme_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/launchtui/theme.go
verified_at: 288448b39c0bfaebe02c60d1cf093dafec9f1a53
links:
    - feat-7c1c70bf
created_by: wipnote-completion
created_at: 2026-06-18T22:43:21.973966525Z
updated_at: 2026-06-18T22:43:21.973966525Z
---

lipgloss.Style.GetForeground()/GetBackground() return TerminalColor interface; type-assert to lipgloss.Color (a string) to extract the hex value in tests — avoids needing a TTY for ANSI rendering checks
