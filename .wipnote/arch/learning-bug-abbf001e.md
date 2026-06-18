---
name: learning-bug-abbf001e
kind: decision
paths:
    - .gitignore
    - core/worktree/worktree.go
verified_at: 97a8a658a5a0daee210df4f7eaf57317d2e0a50b
links:
    - bug-abbf001e
created_by: wipnote-completion
created_at: 2026-06-18T05:01:51.764049063Z
updated_at: 2026-06-18T05:10:26.767532216Z
---

Launcher worktree reindex was killed by a 30s context.WithTimeout SIGKILL on large repos in devcontainer; fix = fire-and-forget exec.Command + Start() + Setpgid:true + async Wait(), no timeout. reindex is best-effort so silent background failure is acceptable (HTML/NDJSON canonical).
