---
name: learning-bug-a199c652
kind: decision
paths:
    - .claude/worktrees/agent-ad522125e8f4a09d5/cmd/wipnote/codex.go
    - .claude/worktrees/agent-a38d789c8b320ea9a/cmd/wipnote/upgrade_extract_test.go
    - .claude/worktrees/agent-a38d789c8b320ea9a/cmd/wipnote/upgrade_cmd.go
verified_at: c27dcc87005291939ceff437ed96fba0e561f9b4
links:
    - bug-a199c652
created_by: wipnote-completion
created_at: 2026-06-23T18:37:32.908768388Z
updated_at: 2026-06-23T19:39:44.770805315Z
---

Installing over a running executable: write a sibling temp in the destination dir (same filesystem) + chmod + os.Rename. rename() swaps the directory entry atomically and avoids ETXTBSY because the running process keeps its old inode until exit. A cross-device os.Rename (EXDEV) must NOT fall back to O_TRUNC over the live binary.
