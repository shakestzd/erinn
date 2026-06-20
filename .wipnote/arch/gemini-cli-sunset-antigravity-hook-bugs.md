---
name: gemini-cli-sunset-antigravity-hook-bugs
kind: decision
paths:
    - port/pluginbuild/**
    - packages/plugin-core/**
verified_at: ""
links: []
created_by: claude-orchestrator
created_at: 2026-06-16T14:22:35.193375287Z
updated_at: 2026-06-16T14:22:35.193375287Z
---

Google retires Gemini CLI for consumer/Pro/Ultra/free on 2026-06-18 (I/O 2026-05-19); replaced by Antigravity CLI (agy). Enterprise Code Assist keeps legacy temporarily. UNVERIFIED-IN-SANDBOX (hooks experiment-gated behind enable_json_hooks; needs authenticated agy session): wipnote's generated Antigravity hooks.json has two proven-fail bugs — (1) wrong top-level schema (emits Claude {event:[specs]}; agy wants named-hook-at-top with enabled key; parse error 'cannot unmarshal array into JSONHookSpec'); (2) wrong event names (emits BeforeTool/AfterTool/SessionStart; agy wants PreToolUse/PostToolUse/PreInvocation/PostInvocation/Stop; tool rename run_shell_command->run_command). All harnesses shipped worktree isolation; best practice = worktree for edits + canonical root for observability. Codex sandbox_mode superseded by permission profiles. Default Gemini model now gemini-3.5-flash. See antigravity-hook-naming-install-risks.md.
