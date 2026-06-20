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
updated_at: 2026-06-15T23:28:21.716674197Z
---

Codex Linux sandbox needs unprivileged user-namespace creation (bwrap --unshare-user), which Codespaces/restricted containers withhold; probe once with 'bwrap --unshare-user --ro-bind / / true', degrade to --sandbox danger-full-access (devcontainer is the boundary), and set WIPNOTE_CODEX_SANDBOX=degraded so agents stop retrying nested codex exec. Match EPERM only with namespace context to avoid false positives.
