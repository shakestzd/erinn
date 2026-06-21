---
name: learning-bug-9a1154d9
kind: decision
paths:
    - .wipnote/bugs/bug-9a1154d9.html
verified_at: b5b54ca3bf12ba0eac82dd8a0643a438e828dcec
links:
    - bug-9a1154d9
created_by: wipnote-completion
created_at: 2026-06-21T06:03:52.715401009Z
updated_at: 2026-06-21T06:03:52.715401009Z
---

The launcher current-session chooser row must resolve work item labels from fresh claims first, then same-family claims, before root active_work_items or legacy active_feature_id. This shared GetCurrentSessionResumable path covers Claude, Codex, Gemini, and Antigravity.
