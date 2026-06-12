---
name: git-commit-gpg-sandbox
kind: hazard
paths:
    - .git/**
    - cmd/wipnote/**
verified_at: f0f34febadd9ebbf29ab8105679df87d7dc40110
links: []
created_by: claude-code/feat-c96e069b
created_at: 2026-06-11T09:29:51.167637647Z
updated_at: 2026-06-11T09:29:51.167637647Z
---

git commits and wipnote autocommits fail with 401/signing error when the Bash tool runs in sandboxed mode. Fix: retry the commit in an unsandboxed Bash call. If it still fails, leave the changes staged and report — `wipnote sync` can recover orphaned autocommits later.
