---
name: learning-bug-f0eff9b7
kind: decision
paths:
    - .claude/worktrees/agent-a21c73b70cc4c0ca3/cmd/wipnote/main.go
    - .claude/worktrees/agent-a21c73b70cc4c0ca3/cmd/wipnote/registry_cmd.go
    - .claude/worktrees/agent-a21c73b70cc4c0ca3/cmd/wipnote/registry_cmd_test.go
    - .claude/worktrees/agent-a21c73b70cc4c0ca3/internal/registry/registry_test.go
    - .claude/worktrees/agent-a21c73b70cc4c0ca3/internal/registry/registry.go
    - core/workitem/htmlwriter.go
    - core/workitem/htmlwriter_edgehref_test.go
    - plugin/agents/researcher.md
    - observe/otel/retention/disk_retention_test.go
    - cmd/wipnote/serve_child.go
    - cmd/wipnote/claude_serve_autostart.go
    - observe/otel/retention/logrotate.go
    - observe/otel/retention/config.go
verified_at: 3452b2beeb07eb18177cf4a574c28c3adc76c280
links:
    - bug-f0eff9b7
created_by: wipnote-completion
created_at: 2026-06-12T00:02:16.285739307Z
updated_at: 2026-06-12T00:02:16.285739307Z
---

GOTMPDIR env var (devcontainer/CI redirect for Go test temp dirs) creates t.TempDir() subtrees outside os.TempDir(). Fix: effectiveTempDirs() checks both os.TempDir() and GOTMPDIR so isGoTestTempDirPath rejects paths under either root. Added wipnote registry prune (dry-run default, --force to apply) as the spec-mandated cleanup surface for dangling entries.
