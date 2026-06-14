---
name: plan-supersession-2026-06-12
kind: decision
paths:
    - .wipnote/plans/**
verified_at: ""
links:
    - feat-f8d803fe
created_by: claude-code/feat-f8d803fe
created_at: 2026-06-13T02:44:33.218407047Z
updated_at: 2026-06-13T02:44:33.218407047Z
---

Architecture review 2026-06-12 resolved the plan-portfolio contradiction. KILLED as superseded (recorded, never executed): plan-149eeb44 (delete SQLite) — the tree chose the opposite, hardening SQLite as the disposable derived index via daemon spine plan-bb91616a; plan-251676c5 (distribution de-bundle) — superseded by shipped v0.59 lockstep single-tarball distribution. plan-bb91616a is the live spine but STALE: slices 1-2 plus parts of 4-5 already landed via feat-075c110d; needs re-baseline; cut slices 3,8,10-13; add commit-outbox daemon driver, canonical-HTML fsck, sandbox fail-fast spawn. feat-d2754049 closed as duplicate of feat-075c110d. plan-3bbc313f is done-but-never-executed (16/16 slices not_started) anomaly.
