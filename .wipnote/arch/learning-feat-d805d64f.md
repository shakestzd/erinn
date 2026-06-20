---
name: learning-feat-d805d64f
kind: decision
paths:
    - cmd/wipnote/launch_isolation_config.go
    - cmd/wipnote/launch_isolation_config_test.go
    - cmd/wipnote/launcher_mode.go
    - core/worktree/worktree.go
    - internal/launcher/plan/plan.go
    - internal/launcher/plan/plan_test.go
verified_at: cb1d98b180ca75ea98bbbd3fde18ab0fe9957559
links:
    - feat-d805d64f
created_by: wipnote-completion
created_at: 2026-06-16T20:10:13.324866035Z
updated_at: 2026-06-16T20:10:13.324866035Z
---

Launcher isolation is config-driven: .wipnote/config.json launch_isolation = warn-only|enforce|auto. 'auto' isolates even bare sessions via ad-hoc worktree (adhoc-<UTC ts>); plan.PlanLaunch stays pure (caller injects AdhocBranchName); env WIPNOTE_ENFORCE_ISOLATION still ORs in; absent config = unchanged.
