---
name: learning-feat-254cfda3
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/port/pluginbuild/parity_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/packages/plugin-core/manifest.json
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/port/pluginbuild/antigravity.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/port/pluginbuild/codex.go
verified_at: 886b277b99d2ba16d541b5030157bee39d9b8e97
links:
    - feat-254cfda3
created_by: wipnote-completion
created_at: 2026-06-16T05:40:06.869068859Z
updated_at: 2026-06-16T05:40:06.869068859Z
---

agy reads plugin MCP from mcp_config.json (root) only — not .mcp.json or plugin.json mcpServers key (validate-confirmed). antigravity target now emits {mcpServers:{}} scaffold.
