---
name: learning-feat-eee1e9e2
kind: invariant
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/hook.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/antigravity_launch_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/hooks/harness_antigravity_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/antigravity_launch.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/hooks/harness.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/statusline_cache_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/statusline.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/antigravity_statusline_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/antigravity_statusline.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/antigravity.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/tmux_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/tmux.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/port/pluginbuild/antigravity.go
verified_at: 1095426319b0cb0958b88c24c7df11fdb4a17f9b
links:
    - feat-eee1e9e2
created_by: wipnote-completion
created_at: 2026-06-16T20:13:08.687803074Z
updated_at: 2026-06-16T20:13:08.687803074Z
---

agy hook RESPONSE contract (verified live, feat-eee1e9e2): agy decodes a hook's stdout as a protojson hook-result and STRICT-rejects unknown fields — the Gemini {"continue":true} shape fails with 'unknown field "continue"', discarding ALL hook output. Emit "{}" for allow; on PreInvocation inject via {"injectSteps":[{"systemMessage":"..."}]}. This injectSteps[].systemMessage is the ONLY channel that conveys the wipnote orchestrator prompt to agy (GEMINI_SYSTEM_MD/additionalContext/plugin GEMINI.md all ignored) — without it agy runs as a vanilla agent and never delegates. Launcher stages the prompt to WIPNOTE_ANTIGRAVITY_SYSTEM_MD; PreInvocation hook reads+injects it. agy merges duplicate system messages so re-injecting each turn is cheap. RUNTIME GAP: model consumption needs an authed agy.
