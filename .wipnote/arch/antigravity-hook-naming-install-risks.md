---
name: antigravity-hook-naming-install-risks
kind: hazard
paths:
    - port/pluginbuild/antigravity.go
    - cmd/wipnote/antigravity.go
verified_at: ""
links:
    - spk-c928d70a
created_by: claude-opus-4-8
created_at: 2026-06-15T17:04:19.258710095Z
updated_at: 2026-06-15T17:04:19.258710095Z
---

ANTIGRAVITY HOOK SILENT-FAILURE RISKS (UNVERIFIED, June 2026). (1) wipnote emits hooks.json with Gemini event names (BeforeTool/BeforeAgent/AfterTool/AfterModel/AfterAgent/SessionEnd) via e.GeminiEventName; web docs say Antigravity events are PreToolUse/PostToolUse/PreInvocation/PostInvocation/Stop. If agy does not alias Gemini names, all Antigravity hooks are dead. (2) hooks.json emitted at bundle root + installed via 'agy plugin install'; docs locate hooks at .agents/hooks.json or ~/.gemini/config/hooks.json — remap unverified. (3) hook.go:322 returns Claude canonical names for Antigravity (open roborev job 267). (4) install dir and GEMINI_TELEMETRY_* env are Gemini-derived guesses. Verify against live agy before release.
