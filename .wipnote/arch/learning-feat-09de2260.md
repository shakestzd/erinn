---
name: learning-feat-09de2260
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/yolo.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/claude_chooser_test.go
verified_at: 364b5544cdde5f683425f80f5b4e9746284a2044
links:
    - feat-09de2260
created_by: wipnote-completion
created_at: 2026-06-19T02:56:36.632190979Z
updated_at: 2026-06-19T02:56:36.632190979Z
---

yolo intentionally skips interactive chooser (autonomous mode must not block); adopts framed RenderLaunchBanner instead. Seam: yoloEmitBannerFn package-var. Antigravity/codex/gemini all route through resolveLaunchIntentForDefaultLaunch at antigravity.go:102, codex.go:1028, gemini.go:256.
