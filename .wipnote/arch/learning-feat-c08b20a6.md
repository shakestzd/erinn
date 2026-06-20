---
name: learning-feat-c08b20a6
kind: invariant
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/packages/plugin-core/manifest.json
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/port/pluginbuild/parity_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/port/pluginbuild/antigravity.go
verified_at: bbed2e158445a18f4d159a898559a845f4a17297
links:
    - feat-c08b20a6
created_by: wipnote-completion
created_at: 2026-06-16T14:42:54.829731024Z
updated_at: 2026-06-16T14:42:54.829731024Z
---

agy v1.0.8 hooks.json schema (verified live, feat-c08b20a6): top-level map of named hooks; each = {"enabled":true, "<Event>":[{"matcher","hooks":[{"type":"command","command"}]}]}. STRICT decode (unknown keys rejected). Events that register command handlers: PreToolUse, PostToolUse, PreInvocation, PostInvocation, Stop. SessionStart/SessionEnd/UserPromptSubmit are accepted fields but register 0 handlers (no-op in v1.0.8). Map canonical->agy: UserPromptSubmit->PreInvocation, AfterAgent->PostInvocation. Tool rename: run_shell_command->run_command. Generated file now loads as "5 total handlers" (was 0). Supersedes the earlier flat-spec guess in antigravity-hook-naming-install-risks. RUNTIME GAP: hooks experiment-gated (enable_json_hooks) — firing needs an authenticated agy.
