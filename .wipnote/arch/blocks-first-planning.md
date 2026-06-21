---
name: blocks-first-planning
kind: decision
paths:
    - plugin/skills/plan/**
    - plan/interview/**
    - plan/plantmpl/**
verified_at: e82755b9d
links:
    - feat-918c439d
    - plan-93b8eba0
created_by: claude-code/opus-4-8
created_at: 2026-06-21T08:05:38.482588226Z
updated_at: 2026-06-21T08:05:38.482588226Z
---

Planning is BLOCKS-FIRST, not prose-first. Each wipnote:plan interview stage authors a visual block inline as the slice YAML is built: Scope -> file-tree, API/contract -> api-endpoint + data-model, UI/flow -> wireframe/diagram; what/why/done_when DERIVE from the blocks. The old separate Step 2b /wipnote:visual-plan round-trip is removed; wipnote:visual-plan is now enrichment-only for existing/legacy plans. StageBlockPlan (plan/interview/model.go) defines the stage->type mapping; attachBlockPrompts reads BlockCatalog() so the live catalog stays the source of truth for which types exist. Plan HTML leads with VisualOverviewZone (all slice blocks grouped by type, expanded, above the dependency graph). PlanSlice.Blocks was already first-class — no schema change.
