---
name: learning-feat-359312ab
kind: decision
paths:
    - .wipnote/features/feat-359312ab.html
verified_at: 3107621a048baa2bc81bde2503bb2a928483e9f4
links:
    - feat-359312ab
created_by: wipnote-completion
created_at: 2026-06-10T22:22:19.476368791Z
updated_at: 2026-06-11T08:55:01.398149648Z
---

Skills are static markdown — 'dynamic injection' means writing imperative startup instructions (run X, paste output) rather than build-time preprocessing. The agent executes those instructions literally. This makes the mechanism harness-agnostic and eliminates hook-surface complexity entirely; the tradeoff is the agent must spend one tool call per session to run the resolve, but that's cheaper than 15-25 min of re-deriving the same facts.
