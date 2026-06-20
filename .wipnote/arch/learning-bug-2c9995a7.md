---
name: learning-bug-2c9995a7
kind: decision
paths:
    - .wipnote/bugs/bug-2c9995a7.html
    - .gitignore
verified_at: 31881599f96c27aec334276b2f33ea2c1fe142da
links:
    - bug-2c9995a7
created_by: wipnote-completion
created_at: 2026-06-18T04:59:43.246328398Z
updated_at: 2026-06-18T04:59:43.246328398Z
---

Repo has intentionally-tracked Go test binaries (db.test, hooks.test, tmp/hooks.test), so a broad '*.test' gitignore glob is unsafe — use explicit rules like '/wipnote.test' for stray root-level compiled test binaries.
