---
name: learning-feat-2c6d04d1
kind: decision
paths:
    - .wipnote/features/feat-2c6d04d1.html
    - cmd/wipnote/codex_launch_test.go
    - cmd/wipnote/codex.go
    - cmd/wipnote/codex_launch.go
verified_at: caa11697fa76dcc0cbaeae6c46083098b215151f
links:
    - feat-2c6d04d1
created_by: wipnote-completion
created_at: 2026-06-15T22:51:08.072708049Z
updated_at: 2026-06-15T23:24:39.507650073Z
---

Codex CLI Linux sandbox requires user-namespace creation via bwrap --unshare-user. In Codespaces/devcontainers the required kernel caps are absent, so probing 'codex --help' never fires. Probe bwrap directly: 'bwrap --unshare-user --ro-bind / / true'. Set WIPNOTE_CODEX_SANDBOX=degraded in the launched process env (auto-degraded case only) so agents stop retrying sandbox paths. 'operation not permitted' (EPERM) must only match as a bwrap failure when co-occurring with namespace/bwrap context words to avoid false positives.
