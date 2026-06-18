---
name: learning-bug-983e9130
kind: decision
paths:
    - cmd/wipnote/claude_session_name_test.go
    - cmd/wipnote/claude.go
    - .wipnote/plans/plan-6d49f2df.yaml
verified_at: cc204be996714aa0eecb44019c6f3c745c43155e
links:
    - bug-983e9130
created_by: wipnote-completion
created_at: 2026-06-18T05:55:09.476975431Z
updated_at: 2026-06-18T05:56:51.169058198Z
---

Auto-review of a roborev fix commit (job 337) caught that the prior fix updated slice-5 what but not its decisions_notes — fix-commit auto-reviews catch partial edits. Resolution also extracted resolveSessionName(sessionName, resumeID, continue_, projectRoot) helper so launchClaudeDev AND launchClaudeDefault share one tested guard instead of duplicated inline logic.
