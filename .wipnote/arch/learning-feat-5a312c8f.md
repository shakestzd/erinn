---
name: learning-feat-5a312c8f
kind: invariant
paths:
    - .wipnote/features/feat-5a312c8f.html
verified_at: 03394d27ce54a20fd8da8761d89866bccf89add3
links:
    - feat-5a312c8f
created_by: wipnote-completion
created_at: 2026-06-10T21:54:04.121250945Z
updated_at: 2026-06-11T08:55:01.508510964Z
---

The --learning validation gate must run BEFORE all other completion side effects. If body validation ran after col.Complete, the item could end up marked done with no card created on validation failure.
