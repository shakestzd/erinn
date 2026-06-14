---
name: learning-bug-91e8aa4c
kind: decision
paths:
    - core/workitem/htmlwriter_edgehref_test.go
    - .wipnote/features/feat-a1e427d6.html
    - core/workitem/htmlwriter.go
    - plugin/agents/architect-coder.md
    - plugin/agents/feature-coder.md
verified_at: c587d5e60f3b9cf133d775b5facc4960862bb852
links:
    - bug-91e8aa4c
created_by: wipnote-completion
created_at: 2026-06-12T23:43:34.946875954Z
updated_at: 2026-06-12T23:43:34.946875954Z
---

HTML writer edge-href mapping: unprefixed IDs (sessions) fell through to same-directory hrefs on rewrite — any new ID-to-collection mapping must handle prefixless session IDs explicitly
