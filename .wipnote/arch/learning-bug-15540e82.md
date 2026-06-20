---
name: learning-bug-15540e82
kind: decision
paths:
    - .wipnote/plans/plan-93b8eba0.html
verified_at: 35d5b37e89da6d9fd6eaa48ecb4271f2f49d89f3
links:
    - bug-15540e82
created_by: wipnote-completion
created_at: 2026-06-19T03:43:15.137765255Z
updated_at: 2026-06-19T03:43:15.137765255Z
---

Plan HTML can go stale vs its YAML: approved-slice radios render unchecked if the HTML predates the renderer's approval-radio logic. wipnote plan render regenerates correctly (emits checked value=approved radios for approval_status:approved slices). Re-render after renderer changes, not just YAML edits.
