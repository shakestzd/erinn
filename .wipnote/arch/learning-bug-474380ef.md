---
name: learning-bug-474380ef
kind: decision
paths:
    - .wipnote/bugs/bug-474380ef.html
verified_at: af71f6644668b36112256aa4258194b0a8af7038
links:
    - bug-474380ef
created_by: wipnote-completion
created_at: 2026-06-11T10:15:10.069727526Z
updated_at: 2026-06-11T10:15:10.069727526Z
---

Claude Code WorktreeCreate payloads now carry only a top-level 'name' field; worktree_name/worktree_base_path are gone. Hook falls back to name + <cwd>/.claude/worktrees default.
