---
name: antigravity-hook-naming-install-risks
kind: hazard
paths:
    - port/pluginbuild/antigravity.go
    - cmd/wipnote/antigravity.go
verified_at: bbf88c119bfec97eebffee6dabde3c1f2d09c7bf
links:
    - spk-c928d70a
created_by: claude-opus-4-8
created_at: 2026-06-15T17:04:19.258710095Z
updated_at: 2026-06-16T20:13:28.237709024Z
---

VERIFIED LIVE vs agy v1.0.8 (spk-0698d585, feat-c08b20a6, feat-eee1e9e2).
HOOKS.JSON (FIXED): named-hook map {"wipnote":{"enabled":true,"<Event>":[{"matcher","hooks":[{"type":"command","command"}]}]}}; STRICT decode. Handler events: PreToolUse/PostToolUse/PreInvocation/PostInvocation/Stop. Loads "5 total handlers". Map: UserPromptSubmit->PreInvocation, AfterAgent->PostInvocation; tool run_shell_command->run_command.
HOOK RESPONSE (FIXED): agy decodes hook stdout as protojson, STRICT-rejects unknowns — Gemini {"continue":true} fails "unknown field continue", discarding all output. Emit "{}" for allow; PreInvocation injects {"injectSteps":[{"systemMessage":"..."}]}.
INJECTION: only channel = PreInvocation injectSteps[].systemMessage (GEMINI_SYSTEM_MD/additionalContext/plugin GEMINI.md DEAD). Launcher stages prompt to WIPNOTE_ANTIGRAVITY_SYSTEM_MD; hook injects it.
RUNTIME GAP: hooks experiment-gated (enable_json_hooks); model consumption needs authed agy.
