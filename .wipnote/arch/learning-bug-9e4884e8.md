---
name: learning-bug-9e4884e8
kind: decision
paths:
    - plugin/skills/orchestrator-directives-skill/SKILL.md
    - plugin/agents/test-runner.md
    - plugin/skills/code-quality-skill/SKILL.md
    - cmd/wipnote/prompts/system-prompt.md
verified_at: 1da1ab628c2caa728c791f4a7d906a8a3d5f2cf7
links:
    - bug-9e4884e8
created_by: wipnote-completion
created_at: 2026-06-12T19:09:24.961409079Z
updated_at: 2026-06-12T19:09:24.961409079Z
---

From cold, 'go test ./...' is silent ~5-6 min: output buffers per package and cmd/wipnote (~320s, slowest) prints first — silence is not a stall; budget >=10 min, use 'go test -json' or internal/cmd split for streaming progress
