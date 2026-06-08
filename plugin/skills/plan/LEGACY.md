# Plan Legacy Compatibility

This file is referenced by validator errors when an older plan is loaded against the current schema. It documents how legacy plans interact with the triage-gated v2+ schema.

## v1 plans (pre slice-card)

Plans created before v2 lacked `approval_status`, `execution_status`, `questions`, and `critic_revisions` on slices. The schema treats these as optional (`omitempty`). The dashboard renders the global Questions/Critique sections as a fallback when the v2 slice-card fields are absent.

Legacy plans can be migrated incrementally: add v2 fields to individual slices as they come up for review, without touching the rest of the YAML.

## v2 plans (pre-triage redesign)

Plans created after v2 but before the triage-gated interview redesign do not carry a `complexity` field. The validator treats an unset `complexity` as `standard` via `effectiveComplexity` — see `plan/planyaml/validate.go`. Such plans validate identically to how they did before the field existed, except:

- `decisions_notes` (>=50 chars after `TrimSpace`) is required for non-finalized plans. Finalized plans (`meta.status: finalized`) are exempted as a back-compat carve-out for historical plans that pre-date the requirement.

If a draft/active v2 plan fails `validate-yaml` solely on `decisions_notes`, run `wipnote plan elicit-decisions <plan-id> <num>` per slice to populate it. Alternatively, mark the slice `complexity: trivial` if the work truly does not warrant rationale capture (one-shot patches, typo fixes, documentation-only).

## Migration cheatsheet

| Symptom | Fix |
|---------|-----|
| `slices[N].decisions_notes is required` on a draft plan | Run `wipnote plan elicit-decisions <plan-id> <N>` or set `complexity: trivial` |
| `slices[N].done_when must have at least 2 entries for complex slices` | Either downgrade `complexity` to `standard`, or add a second testable acceptance criterion |
| `slices[N].questions must include at least 1 question with a non-empty answer for complex slices` | Add `questions: [{id: q-..., text: ..., answer: ...}]` to the slice |
| `slices[N].complexity "<value>" must be trivial\|standard\|complex` | Fix the typo; valid values are exactly `trivial`, `standard`, `complex` (or empty for the default) |
