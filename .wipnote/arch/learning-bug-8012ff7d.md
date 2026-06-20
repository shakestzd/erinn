---
name: learning-bug-8012ff7d
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/family_attribution_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/core/db/family_attribution.go
verified_at: 7bf0930ba21b44c6640bdd5d4991a98777e588ef
links:
    - bug-8012ff7d
created_by: wipnote-completion
created_at: 2026-06-19T06:26:19.231436386Z
updated_at: 2026-06-19T06:26:19.231436386Z
---

DB read-then-write attribution helpers need the guard ON the write, not just a pre-check: GetActiveWorkItemWithFallback()!='' followed by an unconditional UPDATE is a TOCTOU window under concurrent SessionStart/claims. Push the predicate into the WHERE clause (active_feature_id IS NULL OR '') and count RowsAffected. This also makes the op idempotent for free.
