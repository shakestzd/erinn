---
name: learning-feat-a7806bf5
kind: decision
paths:
    - cmd/wipnote/antigravity.go
verified_at: 1f6f9a2aa0af1b90f9b43bd00a94c6e1ead78ca4
links:
    - feat-a7806bf5
created_by: wipnote-completion
created_at: 2026-06-17T05:57:19.594797286Z
updated_at: 2026-06-17T05:57:19.594797286Z
---

applyAntigravityLaunchIntent: continue_/resumeID must be gated behind SessionHarness=='antigravity'. Cross-harness chooser rows (codex/gemini) have ResumeForHarness('antigravity')=='' so they would trigger 'agy --continue' with no ID, resuming the most-recent agy session. Work-item/worktree context is safe to carry for all harnesses.
