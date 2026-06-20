---
name: learning-feat-74f51760
kind: decision
paths:
    - .wipnote/plans/plan-92a31aea.yaml
verified_at: 238dbaa8141b0c2781c8c9d2598e01d0bf08e4f2
links:
    - feat-74f51760
created_by: wipnote-completion
created_at: 2026-06-19T03:09:59.088761841Z
updated_at: 2026-06-19T03:09:59.088761841Z
---

Plans from the normal create/interview flow carry visual-block CSS (baked into plantmpl) but ZERO authored blocks; /visual-plan enrichment must be run explicitly per-plan. plan-92a31aea was first real consumer of authored blocks (5 of 6 types; api-endpoint omitted, no HTTP routes). Schema: SliceBlock{type,title,fields,rows,entries}; wireframe.fields.html accepts only var(--wf-bg/surface/fg/muted/border/accent/radius), raw hex/rgb rejected by validateWireframeBlock.
