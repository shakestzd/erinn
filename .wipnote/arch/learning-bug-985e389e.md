---
name: learning-bug-985e389e
kind: decision
paths:
    - .gitignore
verified_at: 4ae64814d7346ff9562ff00f2a5d50eea296bbe0
links:
    - bug-985e389e
created_by: wipnote-completion
created_at: 2026-06-18T05:14:02.63460121Z
updated_at: 2026-06-18T05:14:02.63460121Z
---

Compiled Go test binaries (db.test, hooks.test, tmp/hooks.test) were committed in 37957a6e9; untracked via git rm --cached and .gitignore broadened from narrow /wipnote.test to *.test (safe once nothing tracked matches). .claude/settings.local.json references .gotmp/hooks.test, a different sandbox path — not a dependency.
