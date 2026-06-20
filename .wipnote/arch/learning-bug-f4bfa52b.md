---
name: learning-bug-f4bfa52b
kind: decision
paths:
    - core/hooks/user_prompt_test.go
    - core/hooks/yolo_guard.go
    - core/hooks/yolo_guard_test.go
verified_at: 1672f0b5c541ec10d27a63dae7f59aa23fa98362
links:
    - bug-f4bfa52b
created_by: wipnote-completion
created_at: 2026-06-20T17:39:02.764563342Z
updated_at: 2026-06-20T17:39:02.764563342Z
---

checkYoloWorkItemGuard now inherits work-item attribution one hop up the session parent chain (ancestorHasActiveWorkItem, JOIN-gated to in-progress) so nested/subagent edits are no longer false-blocked, without the global any-in-progress false-pass. Fix lives in core/hooks/yolo_guard.go.
