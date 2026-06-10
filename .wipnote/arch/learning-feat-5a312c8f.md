---
name: learning-feat-5a312c8f
kind: invariant
paths:
    - /home/vscode/.claude/projects/-workspaces-wipnote/memory/wipnote-go-test-tmpdir.md
    - /tmp/claude-1000/-workspaces-wipnote--claude-worktrees-architectural-memory-v1-trk-53715165/873ecf53-d8ef-4743-8599-064e2c00279e/tasks/b8xipxr56.output
    - /workspaces/wipnote/.claude/worktrees/slice4-feat-5a312c8f/plugin/skills/orchestrator-directives-skill/SKILL.md
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/core/arch/card_test.go
    - /workspaces/wipnote/.claude/worktrees/slice4-feat-5a312c8f/cmd/wipnote/arch_cmds_test.go
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/plugin/skills/arch-bootstrap/SKILL.md
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/cmd/wipnote/arch_cmds.go
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/cmd/wipnote/arch_bootstrap.go
    - /workspaces/wipnote/.claude/worktrees/slice4-feat-5a312c8f/cmd/wipnote/workitem.go
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/core/arch/card.go
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/core/arch/store.go
    - /workspaces/wipnote/.claude/worktrees/slice4-feat-5a312c8f/cmd/wipnote/arch_cmds.go
    - /workspaces/wipnote/.claude/worktrees/slice3-feat-5587d29b/cmd/wipnote/arch_bootstrap_test.go
verified_at: 03394d27ce54a20fd8da8761d89866bccf89add3
links:
    - feat-5a312c8f
created_by: wipnote-completion
created_at: 2026-06-10T21:54:04.121250945Z
updated_at: 2026-06-10T21:54:04.121250945Z
---

The --learning validation gate must run BEFORE all other completion side effects. If body validation ran after col.Complete, the item could end up marked done with no card created on validation failure.
