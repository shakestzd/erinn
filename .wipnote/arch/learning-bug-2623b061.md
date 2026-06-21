---
name: learning-bug-2623b061
kind: decision
paths:
    - internal/recap/collect_test.go
    - cmd/wipnote/recap_test.go
    - cmd/wipnote/recap.go
    - internal/recap/git.go
    - internal/recap/collect.go
    - cmd/wipnote/plugin_ensure_test.go
    - cmd/wipnote/claude.go
    - cmd/wipnote/plugin_ensure.go
verified_at: 78391255652046ef6e7a14d11edd3477e4580046
links:
    - bug-2623b061
created_by: wipnote-completion
created_at: 2026-06-21T06:11:17.343380198Z
updated_at: 2026-06-21T06:11:17.343380198Z
---

Roborev job 431 for the plan rollup recap root-commit range is closed: collectRange detects empty-tree ranges and uses HEAD for commit listing while preserving empty-tree..HEAD for diffing.
