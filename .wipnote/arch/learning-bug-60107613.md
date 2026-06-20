---
name: learning-bug-60107613
kind: decision
paths:
    - core/hooks/pretooluse_test.go
    - core/hooks/pretooluse.go
    - cmd/wipnote/hook.go
    - core/hooks/harness.go
    - core/hooks/runner.go
    - cmd/wipnote/hook_event_name_test.go
    - core/hooks/pretooluse_delegation_test.go
verified_at: 9a89d06ac79b26e52dec76ad8000fa18078e9cb2
links:
    - bug-60107613
created_by: wipnote-completion
created_at: 2026-06-15T18:40:48.711991325Z
updated_at: 2026-06-15T19:43:18.794644232Z
---

Gemini/Antigravity share Gemini-native hook event names (BeforeAgent/BeforeTool/AfterTool); hook responses must echo the incoming hook_event_name, not Claude canonical names, or structured hookSpecificOutput is ignored. TaskStarted is a Codex generic checkpoint, not delegation evidence.
