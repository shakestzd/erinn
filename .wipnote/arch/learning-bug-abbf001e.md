---
name: learning-bug-abbf001e
kind: decision
paths:
    - .gitignore
verified_at: 97a8a658a5a0daee210df4f7eaf57317d2e0a50b
links:
    - bug-abbf001e
created_by: wipnote-completion
created_at: 2026-06-18T05:01:51.764049063Z
updated_at: 2026-06-18T05:01:51.764049063Z
---

reindexWorktree uses exec.CommandContext with a 30s timeout; in devcontainer (overlayfs) the full reindex (sessions, arch cards, plan edges, commit trailers) exceeds 30s and gets SIGKILLed. Fix: run reindex async (fire-and-forget with Setpgid) so it does not block the launcher and cannot be killed by context expiry.
