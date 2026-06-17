---
name: learning-feat-516a2ee9
kind: decision
paths:
    - .wipnote/plans/plan-6d49f2df.yaml
verified_at: e1e4c6e8a88fc1f5aebbdb30db68a8ba2de62493
links:
    - feat-516a2ee9
created_by: wipnote-completion
created_at: 2026-06-17T04:08:39.792593147Z
updated_at: 2026-06-17T04:08:39.792593147Z
---

plan-critique writeback to .wipnote/plans/*.yaml is gated by the active-work-item guard (Write/Edit under .wipnote/ require a started feature); create+start a feature on the plan's track BEFORE dispatching the YAML-rewrite agent. Editing a plan YAML counts as code-bearing, so complete with --accepted-advisory (no Go commit). Use 'wipnote plan rewrite-yaml <id> --file <path>' to regen HTML+reindex.
