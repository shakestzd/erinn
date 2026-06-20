---
name: learning-feat-4f98f706
kind: decision
paths:
    - cmd/wipnote/antigravity.go
    - cmd/wipnote/antigravity_statusline.go
    - cmd/wipnote/antigravity_statusline_test.go
    - cmd/wipnote/statusline.go
verified_at: fa7401200d93822bc70444ff8ef89555b1cc2ba8
links:
    - feat-4f98f706
created_by: wipnote-completion
created_at: 2026-06-16T06:27:04.170698331Z
updated_at: 2026-06-16T06:27:04.170698331Z
---

agy statusLine: settings key 'statusLine'={type:command,command,padding} in per-user agy settings.json; agy parses merged file OK & auto-disables failing statusline. wipnote renders via 'statusline --cache' (project cache, session-independent — agy hooks don't key wipnote sessions). Launcher merge is non-clobbering/idempotent/opt-out. TUI invocation unverified (no auth).
