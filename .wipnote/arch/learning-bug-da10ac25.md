---
name: learning-bug-da10ac25
kind: decision
paths:
    - cmd/wipnote/antigravity.go
    - cmd/wipnote/gemini.go
    - cmd/wipnote/codex.go
    - core/hooks/session_start_exec_context_test.go
    - core/hooks/session_end_slim_test.go
    - core/hooks/session_end.go
    - core/db/session_repo.go
    - core/hooks/session_start.go
    - cmd/wipnote/dev.go
    - cmd/wipnote/antigravity_launch_test.go
    - plan/plantmpl/plantmpl.go
    - cmd/wipnote/claude_dev_test.go
    - cmd/wipnote/yolo.go
    - cmd/wipnote/claude_launch_intent.go
    - plan/plantmpl/plantmpl_test.go
    - cmd/wipnote/claude.go
    - cmd/wipnote/launcher_mode.go
verified_at: d317a8987e977491ddbf6ce7a4a69717e475ae59
links:
    - bug-da10ac25
created_by: wipnote-completion
created_at: 2026-06-18T04:32:25.581112544Z
updated_at: 2026-06-18T04:32:25.581112544Z
---

core/hooks session tests false-fail when run inside a nested Claude Code session: leaked CLAUDE_CODE_SESSION_ID (overrides event session ID via resolveSessionIDWithHarness) and WIPNOTE_PARENT_SESSION+WIPNOTE_NESTING_DEPTH (isSubagent) make SessionStart write under the real session ID, so GetSession returns no rows. Unset these vars (or run from a non-nested shell) before go test / wipnote bug complete. roborev-fix sweep: triage findings LIVE/STALE/UNCERTAIN against current main; respect developer deferrals (322/329/330); gate each Go module separately (root/core/plan).
