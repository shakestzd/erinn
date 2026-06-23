---
name: learning-bug-2a6a8076
kind: decision
paths:
    - .claude/worktrees/agent-ad522125e8f4a09d5/cmd/wipnote/codex_test.go
verified_at: 8d27eae4670af1f968616eb6633cc0ed6a667643
links:
    - bug-2a6a8076
created_by: wipnote-completion
created_at: 2026-06-23T18:42:54.825600612Z
updated_at: 2026-06-23T19:39:52.968816547Z
---

Unify launcher banners: setup/prepare/launch helpers return []launchtui.BannerDetail and one combined RenderLaunchBanner renders them at a single width (mirror claude.go); keep only post-launch notices (e.g. sandbox-degradation) as separate banners since they fire after the launch banner prints.
