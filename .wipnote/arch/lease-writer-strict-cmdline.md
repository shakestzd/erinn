---
name: lease-writer-strict-cmdline
kind: hazard
paths:
    - core/daemon/lease_linux.go
    - core/daemon/daemon_test.go
    - core/daemon/lifecycle_test.go
verified_at: ""
links: []
created_by: architect-coder
created_at: 2026-06-11T13:05:50.909734049Z
updated_at: 2026-06-11T13:05:50.909734049Z
---

isWriterProcessImpl in core/daemon/lease_linux.go MUST verify writers strictly by /proc/<pid>/cmdline containing 'wipnote'. The project-root-name fallback (project basename contains 'wipnote') is a TEST-ONLY relaxation gated behind WIPNOTE_LEASE_TEST_FALLBACK env. Never enable it in production: wipnote commonly lives at /workspaces/wipnote, so the fallback would match EVERY live PID and defeat the PID-reuse guard, letting an unrelated process inheriting a recycled PID masquerade as the live writer. Tests opt in via t.Setenv.
