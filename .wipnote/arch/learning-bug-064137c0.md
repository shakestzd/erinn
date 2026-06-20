---
name: learning-bug-064137c0
kind: decision
paths:
    - cmd/wipnote/claude.go
    - cmd/wipnote/claude_launch_intent.go
    - cmd/wipnote/plugin_ensure_test.go
    - cmd/wipnote/yolo.go
verified_at: 8f12f4935c9fe694b09d8838daccd3956f9e9336
links:
    - bug-064137c0
created_by: wipnote-completion
created_at: 2026-06-20T18:33:11.122048228Z
updated_at: 2026-06-20T18:33:11.122048228Z
---

wipnote distribution moved from Claude marketplace install to bundled --plugin-dir tree (deploy-all.sh Phase B); dev/yolo launchers no longer need removeMarketplaceWipnote — it was legacy-only cleanup. Removed it and marketplaceWipnotePresent entirely. If a stale ~/.claude/plugins/marketplaces/wipnote from a pre-migration install ever shadows dev source, that is now a one-time manual cleanup, not a per-launch cost.
