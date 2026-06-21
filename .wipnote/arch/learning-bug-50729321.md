---
name: learning-bug-50729321
kind: decision
paths:
    - cmd/wipnote/plugin_ensure_test.go
    - cmd/wipnote/claude.go
verified_at: 6e6fd6e565ad844efd24d6f9985217ade206f5a3
links:
    - bug-50729321
created_by: wipnote-completion
created_at: 2026-06-20T21:24:33.553450618Z
updated_at: 2026-06-20T21:24:33.553450618Z
---

Dev-mode fast-path 'is X installed?' checks must enumerate the SAME artifact set as the corresponding remover; share one source list (e.g. marketplaceArtifactDirs helper) so detection and removal can't drift — a partial presence check silently skips cleanup and lets stale plugins shadow --plugin-dir.
