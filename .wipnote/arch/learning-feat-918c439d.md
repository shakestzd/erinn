---
name: learning-feat-918c439d
kind: decision
paths:
    - plan/plantmpl/visual_overview_zone.go
    - plugin/skills/visual-plan/SKILL.md
    - plugin/skills/plan/SKILL.md
    - plan/plantmpl/visual_overview_zone_test.go
    - plan/interview/model_test.go
    - cmd/wipnote/plan_interview_test.go
    - plan/planyaml/validate.go
    - cmd/wipnote/plan_create.go
    - cmd/wipnote/plan_interview.go
    - plan/plantmpl/templates/plan_page.gohtml
    - plan/interview/model.go
    - plan/plantmpl/plantmpl.go
    - plan/plantmpl/templates/visual_overview_zone.gohtml
    - cmd/wipnote/sqlite_write_boundary_test.go
verified_at: 6e44e75c589d5d17cd892cf8398ed1779c5e2481
links:
    - feat-918c439d
created_by: wipnote-completion
created_at: 2026-06-21T07:51:16.618871713Z
updated_at: 2026-06-21T08:56:38.011458132Z
---

Planning is now blocks-first: each interview stage authors the visual block (file-tree at Scope, api-endpoint+data-model at API/contract, wireframe/diagram for UI) BEFORE deriving prose fields; the separate visual-plan Step 2b round-trip is removed; wipnote:visual-plan is demoted to enrichment-only for legacy/existing plans. Plan HTML leads with a plan-level visual overview zone (blocks grouped by type) above the dependency graph.
